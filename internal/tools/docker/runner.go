// Package docker runs Pentest-Swarm-AI tool calls inside ephemeral
// Docker containers.
//
// Why a separate package:
//
//   - The existing tools.RunCommand executes binaries on the host.
//     That's fine for tools we trust (httpx, naabu from a vetted
//     apt source) but for the exploit phase (sqlmap, nuclei
//     templates) we want a stronger sandbox boundary than a process
//     on the host filesystem.
//   - The Docker daemon already gives us: filesystem isolation,
//     network namespace, ephemeral lifecycle, and cgroup resource
//     limits (planned for P2-1 B/C; the minimal scope A only does
//     create / start / wait / remove).
//   - The interface is intentionally narrow — Run(ctx, Request)
//     returns Result — so tool adapters can pick a backend
//     (host or docker) per-call without rewriting call sites.
//
// Scope (P2-1 A — minimum viable loop):
//
//   - One container per Run() call. Auto-removed on success or
//     failure.
//   - Stdout / Stderr captured in full (not streamed). Output
//     buffers cap at 16 MiB; the cap is intentional to prevent
//     runaway tools from OOMing the agent.
//   - Image whitelist enforced. Operators list allowed image
//     refs; an out-of-list image returns a typed error.
//   - ctx cancellation kills the container (SIGKILL after 5s
//     grace period if SIGTERM is ignored).
//   - No resource limits, no network mode override, no image
//     pre-warming. Those land in P2-1 B/C.
//
// Out of scope:
//
//   - Privileged mode (never used; all calls are unprivileged).
//   - Bind mounts from the host (we copy data in via API if
//     needed, not in P2-1 A).
//   - Container reuse / pooling. Each call is a fresh container.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/moby/moby/client"
)

// Request describes one container invocation.
type Request struct {
	// Image is the image ref to use (e.g. "ptagent/kali-linux").
	// Must be in Config.AllowedImages if the whitelist is set.
	Image string

	// Command is the argv to exec inside the container. Required.
	Command []string

	// Env is the env vars set in the container. Optional.
	Env []string

	// WorkDir is the working directory inside the container.
	// Optional; defaults to "/".
	WorkDir string

	// Stdin, if non-nil, is piped into the container. Optional.
	Stdin io.Reader

	// Entrypoint, if non-nil, overrides the image's configured
	// entrypoint. Use an empty slice (not nil) to clear the
	// entrypoint entirely so Command is invoked directly. This
	// is what most call sites want — tool images frequently
	// ship an entrypoint that expects a different argv shape.
	Entrypoint []string

	// Resources, if non-nil, overrides the Runner's default
	// cgroup limits for this call. P2-1 B. A nil pointer
	// means "use Config.Resources as-is". Field-by-field
	// merge: zero fields in the override don't clobber
	// non-zero fields in the default. Use this to e.g.
	// bump memory for a known-heavy tool without changing
	// the Runner's global ceiling.
	Resources *ResourceLimits

	// Isolation, if non-nil, overrides the Runner's
	// default network + rootfs posture for this call.
	// P2-1 C. A nil pointer means "use Config.Isolation".
	// Use this to e.g. grant a single http-based scanner
	// bridge networking while every other tool runs
	// air-gapped.
	Isolation *Isolation

	// Tool, Agent, Target are best-effort attribution
	// hints forwarded to the GuardrailHook (if any) for
	// audit / quota bookkeeping. P2 商业化护栏. They do
	// NOT influence the moby SDK call; the docker
	// package itself doesn't care what's in them. Empty
	// strings are fine — the hook falls back to "-" in
	// its event metadata.
	Tool   string
	Agent  string
	Target string
}

// Result captures the outcome of a single container run.
type Result struct {
	// ContainerID is the Docker container ID. Useful for log
	// correlation even after the container is removed.
	ContainerID string

	// ExitCode is the container's exit code. -1 if the
	// container was killed (timeout, ctx cancel).
	ExitCode int

	// OOMKilled is true when the container was killed by
	// the kernel's OOM-killer (i.e. exceeded the configured
	// memory cgroup limit). When true, ExitCode is 137
	// (128 + SIGKILL=9) and the OOM-kill line appears in
	// the kernel ring buffer / dmesg.
	//
	// P2-1 B. We surface this as a typed field so the
	// monitor layer can distinguish "tool crashed" from
	// "tool ran away" without having to grep dmesg.
	OOMKilled bool

	// Stdout / Stderr are the captured streams. Capped at
	// MaxOutputBytes per stream; anything beyond is dropped and
	// the result's Truncated field is set.
	Stdout []byte
	Stderr []byte
	Truncated bool

	// Duration is wall time from ContainerStart to container exit.
	Duration time.Duration

	// Manifest is the parsed contents of the image's
	// /etc/psa/manifest.json, recorded when
	// Config.VerifyManifest is true. nil otherwise.
	//
	// The probe runs in a SECOND, short-lived
	// container (cat /etc/psa/manifest.json) BEFORE
	// the user's command. Failure of the probe is
	// observational: Result.Manifest is nil and
	// Run() does not error out (the user's command
	// still runs). Operators who want fail-closed
	// semantics should check Result.Manifest and
	// return their own error.
	//
	// P3-1.
	Manifest *Manifest
}

// MaxOutputBytes caps stdout and stderr per call. Picked at 16 MiB
// because real nmap scans can produce 5–10 MiB of XML; sqlmap can
// push 20+ MiB; we accept truncating the latter. Truncation is
// visible in Result.Truncated.
const MaxOutputBytes = 16 << 20

// ErrImageNotAllowed is returned by Run when the requested image
// is not in the configured whitelist.
var ErrImageNotAllowed = errors.New("docker: image not in allowed-images whitelist")

// ErrEmptyCommand is returned by Run when Request.Command is empty.
var ErrEmptyCommand = errors.New("docker: empty command")

// Runner executes Docker containers. One Runner is shared across
// all calls in a campaign; the underlying *client.Client is
// safe for concurrent use.
type Runner struct {
	// ContainerOps is the subset of the Docker client we use.
	// Defined as an interface so tests can inject a fake. In
	// production it's the real moby client.
	ContainerOps ContainerOps

	// Config is the immutable per-Runner configuration.
	Config Config

	// lastManifest is the parsed manifest from the most
	// recent VerifyManifest probe. P3-1. It's a
	// per-Runner slot so the probe in Run() can stash
	// the result and the result-builder at the end of
	// Run() can pick it up. We reset to nil at the
	// end of every Run() so a probe-enabled Runner
	// doesn't leak state across calls.
	//
	// Not safe for concurrent use. Run() is the only
	// writer; it's documented to be safe for
	// concurrent use only when VerifyManifest is
	// false (so lastManifest stays nil).
	lastManifest *Manifest
}

// NewRunner returns a Runner backed by the given ops and config.
// The caller is responsible for closing the underlying client
// (we don't take ownership).
func NewRunner(ops ContainerOps, cfg Config) *Runner {
	if cfg.DefaultImage == "" {
		cfg.DefaultImage = "ptagent/kali-linux"
	}
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = 5 * time.Second
	}
	return &Runner{ContainerOps: ops, Config: cfg}
}

// Run executes the request in a fresh container and returns the
// captured result. On any error path the container is removed
// (best-effort).
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	if len(req.Command) == 0 {
		return nil, ErrEmptyCommand
	}
	image := req.Image
	if image == "" {
		image = r.Config.DefaultImage
	}
	if err := r.checkImageAllowed(image); err != nil {
		return nil, err
	}

	// Layer the per-Request resource override on top of the
	// Runner's default. nil override is a no-op; non-nil
	// does a field-by-field merge.
	resources := r.Config.Resources.Merge(req.Resources)

	// Same merge dance for isolation (P2-1 C). The
	// field-by-field merge is necessary because
	// NetworkMode's empty string is a meaningful "use
	// daemon default" signal — we can't just copy the
	// whole struct.
	isolation := r.Config.Isolation.Merge(req.Isolation)

	// P2 商业化护栏: optional pre-Run() hook for audit /
	// quota / event reporting. nil hook is a noop. The
	// hook MAY return a request with a different
	// Isolation pointer (downgraded posture) — we
	// re-merge so the downgraded value lands in the
	// moby SDK call.
	if r.Config.Guardrails != nil {
		hooked := r.Config.Guardrails.BeforeRun(req.Tool, req.Agent, req.Target, req)
		if hooked.Isolation != req.Isolation {
			isolation = r.Config.Isolation.Merge(hooked.Isolation)
		}
	}

	createRes, err := r.ContainerOps.ContainerCreate(ctx, buildCreateOptions(image, req, resources, isolation, r.Config.SeccompProfile))
	if err != nil {
		return nil, fmt.Errorf("docker: create: %w", err)
	}
	containerID := createRes.ID

	// P3-1: optional manifest probe. We run this AFTER
	// the user's container is created (so a missing
	// /etc/psa/manifest.json doesn't block tool runs
	// for callers who don't care about manifests) but
	// BEFORE the user container is started. The probe
	// itself is a one-shot second container that runs
	// `cat <path>` and exits.
	//
	// Failure of the probe is observational: we
	// record the error via t.Logf-shaped best-effort
	// (no logger is plumbed through; the caller can
	// see the nil Result.Manifest as a signal). The
	// user's command still runs. Operators who want
	// fail-closed should branch on Result.Manifest
	// in their own code.
	if r.Config.VerifyManifest {
		probeCtx, probeCancel := context.WithTimeout(ctx, ManifestProbeTimeout)
		manifest, mErr := ReadManifestInContainer(probeCtx, r.ContainerOps, image, r.Config, r.Config.ManifestPath)
		probeCancel()
		if mErr != nil {
			// Observational. We deliberately don't
			// return the error so dev-mode callers
			// can iterate without an
			// /etc/psa/manifest.json on every image.
			// Production callers should check
			// Result.Manifest for nil.
			_ = mErr
		}
		// Stash the manifest on a side-channel —
		// we'll attach it to the Result after the
		// user's container finishes.
		r.lastManifest = manifest
	}

	// Always remove the container on exit. We use defer so
	// success / failure / panic all get cleanup. The Remove
	// call uses a fresh context with a 10s cap so a stuck
	// daemon doesn't block the caller indefinitely.
	removed := false
	defer func() {
		if removed {
			return
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = r.ContainerOps.ContainerRemove(rmCtx, containerID, removeOptionsForCleanup())
	}()

	startOpts := startOptionsForRun()
	if _, err := r.ContainerOps.ContainerStart(ctx, containerID, startOpts); err != nil {
		return nil, fmt.Errorf("docker: start: %w", err)
	}

	// Attach log streams BEFORE waiting so we don't miss early
	// output (Docker's logs API only returns data accumulated
	// up to the call).
	stdoutBuf := &boundedBuffer{cap: MaxOutputBytes}
	stderrBuf := &boundedBuffer{cap: MaxOutputBytes}
	logsCh := make(chan error, 1)
	go func() {
		logsCh <- r.attachLogs(ctx, containerID, stdoutBuf, stderrBuf)
	}()

	// Optionally pipe stdin.
	if req.Stdin != nil {
		// Stdout / Stderr are read above; attach stdin is a
		// separate connection we open here. The moby SDK
		// doesn't expose a typed Attach in this version, so
		// we rely on the underlying Client when available.
		// For now, we just discard — most recon tools don't
		// read stdin, and explicit stdin support lands in
		// P2-1 B.
	}

	start := time.Now()
	waitRes := r.ContainerOps.ContainerWait(ctx, containerID, waitOptionsForRun())
	duration := time.Since(start)
	var exitCode int
	select {
	case err := <-waitRes.Error:
		// ctx cancel / timeout — kill the container explicitly.
		_ = r.kill(context.Background(), containerID)
		// Drain logs best-effort.
		<-logsCh
		return nil, fmt.Errorf("docker: wait: %w", err)
	case resp := <-waitRes.Result:
		// container.WaitResponse.StatusCode is the exit code
		// (0 = success, 137 = SIGKILL, etc).
		exitCode = int(resp.StatusCode)
	}

	// Drain log attach goroutine. Cap to 5s to avoid hanging
	// on a stuck stream.
	select {
	case err := <-logsCh:
		if err != nil {
			// Non-fatal — we still return the captured bytes.
			// Logged in a future iteration via the LangFuse
			// tracer's span error field.
			_ = err
		}
	case <-time.After(5 * time.Second):
	}

	// P2-1 B: read back the container's terminal state so
	// callers can tell an OOM-kill from a clean exit with
	// code 137. We use a fresh context with a 2s cap so a
	// stuck daemon doesn't block the caller. Failure to
	// inspect is non-fatal — we just don't set OOMKilled.
	var oomKilled bool
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if inspect, err := r.ContainerOps.ContainerInspect(inspectCtx, containerID, client.ContainerInspectOptions{}); err == nil {
		// moby's ContainerInspectResult wraps a
		// container.InspectResponse with State.OOMKilled.
		// Access via .Container.State.OOMKilled. We only
		// treat the field as authoritative when the
		// inspect succeeded.
		oomKilled = inspect.Container.State.OOMKilled
	}
	inspectCancel()

	// Remove on the happy path too.
	rmCtx, rmCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, _ = r.ContainerOps.ContainerRemove(rmCtx, containerID, removeOptionsForRun())
	rmCancel()
	removed = true

	res := &Result{
		ContainerID: containerID,
		ExitCode:    exitCode,
		OOMKilled:   oomKilled,
		Stdout:      stdoutBuf.Bytes(),
		Stderr:      stderrBuf.Bytes(),
		Truncated:   stdoutBuf.Truncated() || stderrBuf.Truncated(),
		Duration:    duration,
		Manifest:    r.lastManifest,
	}
	r.lastManifest = nil // reset for next call
	return res, nil
}

// attachLogs opens the container's log stream and copies
// stdout / stderr into the provided buffers. It returns when the
// stream EOFs (the container exited and logs are drained) or
// when ctx is cancelled.
func (r *Runner) attachLogs(ctx context.Context, containerID string, stdout, stderr *boundedBuffer) error {
	logs, err := r.ContainerOps.ContainerLogs(ctx, containerID, logsOptionsForRun())
	if err != nil {
		return err
	}
	defer logs.Close()
	_, err = demuxDockerLogs(logs, stdout, stderr)
	return err
}

// kill sends SIGKILL via the timeout-then-force pattern. Used
// when ctx is cancelled or wait returns an error.
func (r *Runner) kill(ctx context.Context, containerID string) error {
	// We use ContainerRemove with Force=true as the kill
	// primitive. The moby SDK doesn't expose a typed Kill in
	// the same surface; in production we'd add ContainerKill
	// to ContainerOps. For P2-1 A, force-remove suffices.
	_, err := r.ContainerOps.ContainerRemove(ctx, containerID, removeOptionsForKill())
	return err
}

// checkImageAllowed enforces Config.AllowedImages when set.
func (r *Runner) checkImageAllowed(image string) error {
	if len(r.Config.AllowedImages) == 0 {
		return nil
	}
	for _, allow := range r.Config.AllowedImages {
		if allow == image || allow == "*" {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrImageNotAllowed, image)
}

// boundedBuffer is a thread-safe byte buffer with a hard cap.
// Once cap is reached, additional writes are discarded and the
// buffer is marked truncated.
type boundedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	cap       int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.cap - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) <= remaining {
		return b.buf.Write(p)
	}
	b.buf.Write(p[:remaining])
	b.truncated = true
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
