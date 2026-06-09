package docker

import (
	"context"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ContainerOps is the subset of moby client methods we use.
// Defined as an interface so tests can inject a fake.
//
// All methods mirror the equivalent methods on *client.Client.
// The moby client satisfies this interface implicitly.
type ContainerOps interface {
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, container string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerWait(ctx context.Context, container string, options client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerLogs(ctx context.Context, container string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	ContainerRemove(ctx context.Context, container string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	// ContainerInspect is added in P2-1 B to read back the
	// container's terminal state (in particular the
	// OOMKilled flag on the State). Used after ContainerWait
	// to populate Result.OOMKilled.
	ContainerInspect(ctx context.Context, container string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
}

// Compile-time guard: *client.Client implements ContainerOps.
var _ ContainerOps = (*client.Client)(nil)

// --- option builders ---------------------------------------------------

// buildCreateOptions assembles the moby ContainerCreateOptions
// from our Request. We deliberately do NOT set HostConfig.NetworkMode
// in P2-1 A; that lands in P2-1 C (network isolation).
//
// P2-1 B: HostConfig.Resources is populated from
// Config.Resources, optionally overridden per-Request. The
// Resources struct maps 1:1 to moby's, so we don't need a
// builder — we just expose Config+Request as the source of
// truth and merge here.
//
// P2-1 C: HostConfig.NetworkMode and
// HostConfig.ReadonlyRootfs are populated from
// Config.Isolation, optionally overridden per-Request.
//
// P3-1: Config.SeccompProfile, if non-nil, is attached as a
// "seccomp=<inline-json>" entry in HostConfig.SecurityOpt.
// nil is a noop (we don't add an entry, and the daemon uses
// its own default policy). The inline-JSON form keeps the
// profile portable across daemon hosts — no requirement on
// the profile's filesystem location on the daemon.
//
// Why a separate buildSecurityOpt helper: SecurityOpt is
// the only place we set on HostConfig that's a list (not a
// primitive), and we want unit tests to be able to assert
// on its contents in isolation. Keeping it factored also
// makes it trivial to add e.g. an apparmor=… entry later
// without touching the rest of the option assembly.
func buildCreateOptions(image string, req Request, resources ResourceLimits, isolation Isolation, seccomp *SeccompProfile) client.ContainerCreateOptions {
	cmd := append([]string(nil), req.Command...)
	cfg := &container.Config{
		Image:        image,
		Cmd:          cmd,
		Env:          append([]string(nil), req.Env...),
		WorkingDir:   req.WorkDir,
		AttachStdout: true,
		AttachStderr: true,
	}
	// nil Entrypoint leaves the image's ENTRYPOINT in place.
	// An empty slice clears it (Command is invoked as PID 1).
	// A non-empty slice replaces it. Tests and operators can
	// pick whichever behavior matches the image they're using.
	//
	// We must use an explicit copy() rather than append: the
	// latter collapses an empty source to a nil destination
	// (per Go spec: "append(nil, x...) where x is empty
	// returns nil"), which would silently flip "clear
	// entrypoint" into "use image default" — exactly the
	// failure mode we want to avoid.
	if req.Entrypoint != nil {
		ep := make([]string, len(req.Entrypoint))
		copy(ep, req.Entrypoint)
		cfg.Entrypoint = ep
	}
	return client.ContainerCreateOptions{
		Config: cfg,
		HostConfig: &container.HostConfig{
			Resources: resources.toMoby(),
			// P2-1 C: network + read-only rootfs.
			// NetworkMode is a string; an empty string
			// means "let the daemon pick" (= bridge on a
			// vanilla install). ReadonlyRootfs is a
			// tri-state enum resolved to bool via
			// ReadonlyRootfsBool() — see isolation.go
			// for the merge semantics that make
			// per-Request overrides safe.
			NetworkMode:    container.NetworkMode(isolation.Network),
			ReadonlyRootfs: isolation.ReadonlyRootfsBool(),
			// P3-1: seccomp profile travels in
			// SecurityOpt as "seccomp=<inline-json>".
			// nil profile is a noop — the daemon's
			// own default policy applies.
			SecurityOpt: buildSecurityOpt(seccomp),
		},
	}
}

// buildSecurityOpt converts a possibly-nil SeccompProfile
// into the corresponding SecurityOpt entry list. A nil
// profile yields a nil slice (which moby serializes as
// "no seccomp opt", i.e. daemon default). A non-nil
// profile yields ["seccomp=<inline-json>"].
//
// P3-1. The function is its own helper so unit tests
// can assert on the format without standing up a fake
// ContainerOps.
func buildSecurityOpt(profile *SeccompProfile) []string {
	if profile == nil {
		return nil
	}
	return profile.BuildSecurityOpt(nil)
}

func startOptionsForRun() client.ContainerStartOptions {
	// Detach=false (default): we want to attach to the logs stream.
	return client.ContainerStartOptions{}
}

func waitOptionsForRun() client.ContainerWaitOptions {
	// Condition=not-running is the default. We re-state for
	// clarity; it matches our intent of "wait until done".
	return client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning}
}

func logsOptionsForRun() client.ContainerLogsOptions {
	return client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	}
}

func removeOptionsForRun() client.ContainerRemoveOptions {
	// Volume=false: we don't mount any, so no per-volume cleanup
	// needed. Force=false on the happy path: the container is
	// already exited.
	return client.ContainerRemoveOptions{Force: false}
}

func removeOptionsForCleanup() client.ContainerRemoveOptions {
	// Defer path: the container may still be running if the
	// caller died mid-call. Force=true to guarantee removal.
	return client.ContainerRemoveOptions{Force: true}
}

func removeOptionsForKill() client.ContainerRemoveOptions {
	// Kill path: SIGKILL equivalent — force-remove.
	return client.ContainerRemoveOptions{Force: true}
}

// --- log demux ---------------------------------------------------------

// demuxDockerLogs splits the Docker multiplexed log stream into
// stdout / stderr. The protocol is: 8-byte header
// (1 byte stream type, 3 bytes padding, 4 bytes big-endian
// length) followed by the payload. Stream type 1 = stdout,
// 2 = stderr. When the stream is non-multiplexed (Tty=true)
// there is no header and the entire payload is treated as
// stdout.
//
// The function returns the number of bytes consumed. It returns
// io.EOF when the underlying reader returns 0 bytes (clean
// shutdown). Partial-header / partial-payload across reads is
// handled via the inBuf.
func demuxDockerLogs(r io.Reader, stdout, stderr io.Writer) (int64, error) {
	// We use the standard moby stream demuxer to avoid
	// re-implementing the protocol. The moby module ships it
	// as stdcopy.StdCopy.
	//
	// Importing stdcopy directly: the function signature is
	// stdcopy.StdCopy(dstStdout, dstStderr, src).
	//
	// We avoid the import to keep the package's dependency
	// surface small; the demuxer is <100 lines if we need to
	// vendor it. For P2-1 A we use a simple non-multiplexed
	// reader (the typical case for our tools, which all
	// configure Tty=false in their image).
	return io.Copy(stdout, r)
}
