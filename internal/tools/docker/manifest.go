package docker

// Image manifest verification. P3-1.
//
// Each P3-1 Kali image bakes a small JSON document at
// /etc/psa/manifest.json describing the installed tool
// versions. The Runner, when configured to verify
// (Config.VerifyManifest = true), runs `cat /etc/psa/manifest.json`
// inside the container BEFORE the user's command and
// records the parsed manifest on Result.Manifest.
//
// Why a separate file rather than env vars / labels:
//
//   - Labels are set at image build time and are
//     coarse-grained ("here are 30 tool names"). They
//     don't carry per-tool version strings, and they
//     can't be updated by an operator who re-installs a
//     tool inside a running container.
//   - Env vars are lost on container restart, and
//     setting them at build time means another rebuild
//     to bump a version.
//   - A JSON file in /etc/psa/ is a stable, versionable
//     artifact that the operator can `cat` / `jq` /
//     diff across builds without touching the image
//     recipe.
//
// The manifest format is deliberately tiny — we are NOT
// modeling the full SBOM/SPDX shape, just the tools the
// agents invoke. Operators who need SBOM can attach a
// second tool (e.g. syft) that consumes the image as
// input.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Manifest is the parsed contents of /etc/psa/manifest.json
// inside a P3-1 image.
//
// The shape is intentionally minimal: a list of tools
// with their version strings, a build timestamp, and the
// image's overall identity. The Runner does not validate
// the tool list against any policy — that is the caller's
// job (a tools agent can compare the manifest against
// expected versions and fail the run if a downgrade is
// detected).
type Manifest struct {
	// Image is the canonical ref of the image the
	// manifest was baked into (e.g. "psa/kali:dev").
	// Set at build time by the Dockerfile's stamp
	// step.
	Image string `json:"image"`

	// BuiltAt is the ISO-8601 timestamp of when the
	// manifest was written. RFC3339. Set at build
	// time.
	BuiltAt string `json:"built_at"`

	// User is the unprivileged UID/GID the image is
	// configured to run as (e.g. "pentest:1000"). The
	// Runner cross-checks this against the actual
	// container's UID at run time.
	User string `json:"user"`

	// Workdir is the image's configured WORKDIR.
	// Cross-checked against Request.WorkDir.
	Workdir string `json:"workdir"`

	// Tools is the list of installed pentest tools and
	// their version strings, as resolved by the build
	// (`apt-cache policy <pkg> | awk …`). The list is
	// not ordered; callers should sort if they need
	// deterministic output.
	Tools []Tool `json:"tools"`
}

// Tool is one entry in the manifest's Tools list. Name
// matches the apt package name (or the upstream binary's
// name for tools installed outside apt). Version is the
// resolved version string at build time — for apt
// packages, this is `apt-cache policy`'s "Candidate"
// value; for binary tools (e.g. ProjectDiscovery
// releases), it's the version reported by the binary
// itself.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DefaultManifestPath is the in-container path the
// Runner probes for the manifest. The Dockerfile bakes
// the manifest here; operators who want a custom path
// can change it in their own image and override this
// constant via Config.ManifestPath.
const DefaultManifestPath = "/etc/psa/manifest.json"

// ParseManifest decodes a manifest JSON document. The
// function is pure (no I/O) so it's trivially unit-
// testable with a bytes.Reader.
//
// We use DisallowUnknownFields so a typo in the JSON
// field name (e.g. "tools" vs "toolss") surfaces as a
// parse error rather than silently producing a Manifest
// with empty fields.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("docker: manifest is empty")
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("docker: parse manifest: %w", err)
	}
	return &m, nil
}

// String marshals the manifest back to JSON for the
// Result.Manifest field. Returns "" on a nil receiver
// so callers can use it as a one-liner.
func (m *Manifest) String() string {
	if m == nil {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// AsReader returns the manifest as an io.Reader. Useful
// for callers that want to feed it into a JSON streaming
// decoder.
func (m *Manifest) AsReader() io.Reader {
	if m == nil {
		return bytes.NewReader(nil)
	}
	b, _ := json.Marshal(m)
	return bytes.NewReader(b)
}

// trimLeadingStreamHeader strips any non-printable
// prefix bytes that the Docker logs demuxer sometimes
// leaves in the captured stream. The 8-byte header
// (1 byte stream type + 3 padding + 4 big-endian
// length) is the typical offender when Tty=false. We
// trim until we hit a JSON open brace '{' or another
// non-control byte. The live seccomp test exercises
// this path (see integration_test.go: TestLive_SeccompAllowsNormalSyscalls,
// which logs "\x01\x00\x00\x00\x00\x00\x00\x05rc=0\n"
// and has to recover the actual output).
//
// If the input contains ONLY control bytes (≤ 0x20)
// and no '{' delimiter, the function returns nil — the
// caller will then see an empty manifest and treat
// that as a parse error. This avoids the alternative
// "return the input unchanged" semantic, which would
// feed control bytes into the JSON decoder and
// produce an obscure parse error.
func trimLeadingStreamHeader(b []byte) []byte {
	for i, c := range b {
		if c == '{' {
			return b[i:]
		}
		if c > 0x20 {
			return b[i:]
		}
	}
	// All bytes were ≤ 0x20 and no '{' was found.
	// Return nil so the caller's ParseManifest
	// returns "manifest is empty".
	return nil
}

// ManifestProbeCommand is the argv the Runner uses to
// read the manifest out of the container. Exported so
// tests can assert on it. We use /bin/sh -c cat <path>
// rather than `cat <path>` directly so the entrypoint
// can be cleared with an empty Entrypoint slice (the
// psa/kali entrypoint expects argv[0] to be a tool
// name, not "cat").
var ManifestProbeCommand = []string{"/bin/sh", "-c", "cat " + DefaultManifestPath}

// ReadManifestInContainer executes the manifest probe
// in a fresh container and returns the parsed result.
// Used by Runner.Run() when Config.VerifyManifest is
// true.
//
// The probe is a one-shot: ContainerCreate →
// ContainerStart → ContainerWait → parse stdout. The
// caller passes its own ContainerOps (typically the same
// moby client) and a stripped Config that has
// VerifyManifest=false (otherwise we'd recurse).
//
// Why not use Runner.Run(): the probe is a fixed
// shell-utility, not a user-controlled tool, so it
// doesn't need the full demux / truncated-stdout /
// resource-limit / OOM-flag machinery. Implementing
// the four ops calls directly keeps the probe
// allocation-light (one container-create RPC instead of
// two — one for the probe and one for the user's
// tool).
//
// Returns the parsed *Manifest on success. On any
// failure (create error, non-zero exit, parse error)
// returns a wrapped error AND a nil manifest so the
// caller can decide whether to fail-closed or log+skip.
func ReadManifestInContainer(ctx context.Context, ops ContainerOps, image string, cfg Config, path string) (*Manifest, error) {
	if path == "" {
		path = DefaultManifestPath
	}
	if ops == nil {
		return nil, fmt.Errorf("docker: nil ContainerOps in manifest probe")
	}
	cmd := []string{"/bin/sh", "-c", "cat " + path}
	createRes, err := ops.ContainerCreate(ctx, buildCreateOptions(image, Request{
		Image:      image,
		Command:    cmd,
		Entrypoint: []string{},
	}, cfg.Resources.Merge(nil), cfg.Isolation.Merge(nil), cfg.SeccompProfile))
	if err != nil {
		return nil, fmt.Errorf("docker: manifest probe create: %w", err)
	}
	probeID := createRes.ID
	// Cleanup regardless of outcome.
	defer func() {
		// Bounded context for cleanup so a stuck
		// daemon doesn't leak containers forever.
		rmCtx, cancel := context.WithTimeout(context.Background(), _manifestProbeCleanupTimeout)
		defer cancel()
		_, _ = ops.ContainerRemove(rmCtx, probeID, removeOptionsForCleanup())
	}()
	if _, err := ops.ContainerStart(ctx, probeID, startOptionsForRun()); err != nil {
		return nil, fmt.Errorf("docker: manifest probe start: %w", err)
	}
	waitRes := ops.ContainerWait(ctx, probeID, waitOptionsForRun())
	var exitCode int
	select {
	case err := <-waitRes.Error:
		return nil, fmt.Errorf("docker: manifest probe wait: %w", err)
	case resp := <-waitRes.Result:
		exitCode = int(resp.StatusCode)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("docker: manifest probe exit=%d (image %q may not have /etc/psa/manifest.json)", exitCode, image)
	}
	logsRes, err := ops.ContainerLogs(ctx, probeID, logsOptionsForRun())
	if err != nil {
		return nil, fmt.Errorf("docker: manifest probe logs: %w", err)
	}
	defer logsRes.Close()
	stdout, _ := io.ReadAll(logsRes)
	out := trimLeadingStreamHeader(stdout)
	return ParseManifest(out)
}

// _manifestProbeCleanupTimeout caps the time the probe
// container's defer-cleanup will wait on
// ContainerRemove. Set to 10s to match the post-Run
// cleanup timeout in runner.go (consistent
// UX — a slow daemon in one path is a slow daemon in
// all paths).
const _manifestProbeCleanupTimeout = 10 * 1e9 // 10s

// ManifestProbeTimeout caps the time we'll wait for the
// cat probe to finish. Set generously (15s) because the
// first pull of a psa/kali image can take 30s on a slow
// network, but a manifest read on a cached image is
// sub-second. 15s catches "image not cached" without
// hanging the agent.
//
// Exported so the Runner can derive a context.WithTimeout
// in Run() (see runner.go: probeCtx).
const ManifestProbeTimeout = 15 * 1e9 // 15s
