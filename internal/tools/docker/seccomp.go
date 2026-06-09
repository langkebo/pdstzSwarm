package docker

import (
	"encoding/json"
	"fmt"
	"os"
)

// SeccompProfile is the in-memory representation of a seccomp
// profile that the Runner will attach to every container it
// creates. P3-1.
//
// The moby public SDK (v28.5.2 / api v1.54.2) does not expose
// a typed *container.SeccompProfile struct — seccomp is plumbed
// through HostConfig.SecurityOpt as a string of the form
//   seccomp=<profile-name>
//   seccomp=<file-path-on-daemon-host>
//   seccomp=<inline-json>
// The daemon then re-loads the profile from the path or parses
// the inline JSON. We do NOT want a deployment where the
// profile's filesystem location on the daemon host matters
// (that's a portability and config-management footgun), so we
// always use the inline-JSON form. This is why SeccompProfile
// is defined here in our package, not re-exported from moby.
//
// The raw field is exposed (not a typed Spec) because libseccomp
// is outside our Go dependency surface; the daemon does its own
// validation against the libseccomp spec. We just need to keep
// the JSON valid + structurally complete (defaultAction +
// archMap + syscalls).
type SeccompProfile struct {
	// raw is the original JSON text we read off disk. We
	// keep it verbatim (no re-marshal) so round-tripping
	// doesn't lose comments or field order — and so the
	// daemon sees exactly what the operator reviewed.
	raw string
}

// Raw returns the JSON text that will be appended to
// HostConfig.SecurityOpt via BuildSecurityOpt. Callers should
// not modify the returned slice.
func (p *SeccompProfile) Raw() string {
	if p == nil {
		return ""
	}
	return p.raw
}

// LoadSeccompProfile reads a seccomp profile from a JSON file
// on disk and returns a *SeccompProfile. The file is parsed
// with encoding/json to catch syntax errors early — the daemon
// would also reject malformed JSON, but its error message is
// harder to attribute back to "the seccomp profile I just
// loaded" (the daemon just reports a generic create error).
//
// The validation is intentionally minimal: we only check
// (a) the file is readable, (b) it's valid JSON, and
// (c) the top-level object has the libseccomp-required
// defaultAction field. The daemon does the rest (syscall
// name resolution, arch compatibility, kernel version
// gating via the includes/excludes clauses).
//
// Why not a stronger validator: any deeper check (e.g. "every
// entry in syscalls[].names must be a known syscall on this
// arch") duplicates libseccomp's job and would need to track
// the kernel's syscall tables. The daemon is the source of
// truth here.
func LoadSeccompProfile(path string) (*SeccompProfile, error) {
	if path == "" {
		return nil, fmt.Errorf("docker: seccomp profile path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("docker: read seccomp profile %q: %w", path, err)
	}
	// Decode into a generic map to surface JSON syntax
	// errors and let us sanity-check the top-level shape.
	// We do NOT keep the decoded value; we ship the raw
	// bytes to the daemon. This avoids drift if a future
	// moby version adds new top-level fields we don't know
	// about.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("docker: parse seccomp profile %q: %w", path, err)
	}
	if _, ok := probe["defaultAction"]; !ok {
		return nil, fmt.Errorf("docker: seccomp profile %q missing required defaultAction field", path)
	}
	if _, ok := probe["syscalls"]; !ok {
		return nil, fmt.Errorf("docker: seccomp profile %q missing required syscalls field", path)
	}
	return &SeccompProfile{raw: string(data)}, nil
}

// BuildSecurityOpt formats the profile as a SecurityOpt entry
// suitable for HostConfig.SecurityOpt. The result is a single
// string of the form "seccomp=<inline-json>".
//
// Why inline JSON rather than a path: the daemon's
// parseSecurityOpt treats the value of "seccomp=" as either a
// known name (default / builtin / unconfined), a file path on
// the daemon host, or inline JSON. We pick inline JSON so the
// deployment is self-contained — no requirement on the
// profile's filesystem location on whatever host the daemon
// happens to be running on. Trade-off: a large profile makes
// every create-call RPC a few KB bigger. The moby default
// profile is ~22 KB; ours is ~30 KB after the explicit-denials
// section. Negligible against a typical container create
// payload of 5-50 KB.
//
// Pre-existing SecurityOpt entries (e.g. an AppArmor profile
// that the operator set elsewhere) are preserved — we only
// append the seccomp entry if a profile is configured.
func (p *SeccompProfile) BuildSecurityOpt(existing []string) []string {
	if p == nil || p.raw == "" {
		return existing
	}
	// A single append is allocation-free when existing has
	// spare capacity. We always return a copy-like slice
	// for the caller because the daemon reads from the
	// same backing array across calls if we're not careful.
	out := make([]string, 0, len(existing)+1)
	out = append(out, existing...)
	out = append(out, "seccomp="+p.raw)
	return out
}
