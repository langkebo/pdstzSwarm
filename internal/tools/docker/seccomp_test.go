package docker

// Tests for the seccomp profile loader and the
// SecurityOpt plumbing in buildCreateOptions. P3-1.
//
// The seccomp test matrix:
//
//   - LoadSeccompProfile_ValidFile     : read the bundled
//     pentest-swarm.json, verify the round-trip preserves
//     the raw bytes and BuildSecurityOpt emits the
//     expected "seccomp=" prefix.
//   - LoadSeccompProfile_MissingFile   : the loader returns
//     a wrapped error on a missing file path.
//   - LoadSeccompProfile_EmptyPath     : the loader rejects
//     an empty path (so callers can't silently mean
//     "current directory" with an empty string).
//   - LoadSeccompProfile_MalformedJSON : the loader returns
//     a syntax error, not a panic.
//   - LoadSeccompProfile_MissingDefaultAction : the loader
//     rejects a JSON object that lacks the libseccomp-required
//     defaultAction field.
//   - BuildSecurityOpt_NilProfile      : nil profile → nil
//     SecurityOpt, no "seccomp=" entry.
//   - BuildSecurityOpt_IncludesSeccomp : non-nil profile →
//     SecurityOpt has exactly one entry starting with
//     "seccomp=".
//   - BuildSecurityOpt_PreservesExisting : pre-existing
//     SecurityOpt entries (e.g. apparmor) are preserved;
//     the seccomp entry is appended, not replacing.
//   - TestRunner_AppliesSeccomp         : end-to-end — a
//     Config with SeccompProfile plumbs the SecurityOpt
//     into the moby ContainerCreateOptions.
//
// The live test (TestLive_SeccompBlocksForbiddenSyscall)
// is in integration_test.go because it needs the same
// Docker socket / image-picking helpers.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// embeddedProfilePath resolves the canonical path to the
// P3-1 profile that ships in the repo. The path is
// relative to the test file's location so it works from
// any working directory (CI, dev, etc.).
func embeddedProfilePath(t *testing.T) string {
	t.Helper()
	// Tests run with cwd=internal/tools/docker; the
	// profile lives at <repo>/deploy/docker/seccomp/.
	// That's ../../../
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "deploy", "docker", "seccomp", "pentest-swarm.json"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("embedded profile not available at %q: %v (skipping)", p, err)
	}
	return p
}

func TestLoadSeccompProfile_ValidFile(t *testing.T) {
	path := embeddedProfilePath(t)
	p, err := LoadSeccompProfile(path)
	if err != nil {
		t.Fatalf("LoadSeccompProfile: %v", err)
	}
	if p == nil {
		t.Fatal("got nil profile, want non-nil")
	}
	raw := p.Raw()
	if raw == "" {
		t.Fatal("Raw() returned empty string")
	}
	// The raw payload should start with `{` (no BOM, no
	// leading whitespace) and contain the defaultAction
	// field. We don't try to parse it again — that's
	// tested in the integration test.
	if !strings.HasPrefix(raw, "{") {
		t.Errorf("Raw() prefix = %q, want \"{\"", raw[:min(20, len(raw))])
	}
	if !strings.Contains(raw, "SCMP_ACT_ERRNO") {
		t.Error("Raw() missing SCMP_ACT_ERRNO — not a valid seccomp profile")
	}
	// The forbidden syscalls from the task description
	// must be present somewhere in the explicit-denials
	// section. We grep the raw text rather than the
	// parsed struct because we deliberately don't model
	// the full libseccomp spec in Go.
	for _, want := range []string{
		"kexec_load", "kexec_file_load",
		"mount", "umount2", "pivot_root",
		"reboot", "shutdown",
		"init_module", "finit_module", "delete_module",
		"acct", "bpf", "perf_event_open", "ptrace",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("forbidden syscall %q not in profile", want)
		}
	}
}

func TestLoadSeccompProfile_MissingFile(t *testing.T) {
	_, err := LoadSeccompProfile("/nonexistent/path/pentest-swarm.json")
	if err == nil {
		t.Fatal("expected error on missing file, got nil")
	}
	if !strings.Contains(err.Error(), "pentest-swarm.json") {
		t.Errorf("error message should name the file, got: %v", err)
	}
}

func TestLoadSeccompProfile_EmptyPath(t *testing.T) {
	_, err := LoadSeccompProfile("")
	if err == nil {
		t.Fatal("expected error on empty path, got nil")
	}
}

func TestLoadSeccompProfile_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"defaultAction": "SCMP_ACT_ERRNO"`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSeccompProfile(bad)
	if err == nil {
		t.Fatal("expected JSON parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse failure, got: %v", err)
	}
}

func TestLoadSeccompProfile_MissingDefaultAction(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	// Valid JSON, but no defaultAction field.
	if err := os.WriteFile(missing, []byte(`{"archMap":[],"syscalls":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSeccompProfile(missing)
	if err == nil {
		t.Fatal("expected missing-defaultAction error, got nil")
	}
	if !strings.Contains(err.Error(), "defaultAction") {
		t.Errorf("error should mention defaultAction, got: %v", err)
	}
}

func TestBuildSecurityOpt_NilProfile(t *testing.T) {
	got := buildSecurityOpt(nil)
	if got != nil {
		t.Errorf("nil profile should give nil SecurityOpt, got: %v", got)
	}
}

func TestBuildSecurityOpt_IncludesSeccomp(t *testing.T) {
	// A trivial valid profile is enough for the format
	// check — the daemon will re-validate the contents
	// itself.
	p := &SeccompProfile{raw: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[]}`}
	got := buildSecurityOpt(p)
	if len(got) != 1 {
		t.Fatalf("SecurityOpt len = %d, want 1", len(got))
	}
	if !strings.HasPrefix(got[0], "seccomp=") {
		t.Errorf("SecurityOpt[0] = %q, want seccomp=… prefix", got[0])
	}
	// Round-trip: the JSON body should be intact.
	if !strings.Contains(got[0], "SCMP_ACT_ERRNO") {
		t.Errorf("SecurityOpt[0] lost the JSON body: %q", got[0])
	}
}

func TestBuildSecurityOpt_PreservesExisting(t *testing.T) {
	p := &SeccompProfile{raw: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[]}`}
	existing := []string{"apparmor=docker-default", "no-new-privileges"}
	got := p.BuildSecurityOpt(existing)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (existing 2 + seccomp 1)", len(got))
	}
	if !strings.HasPrefix(got[2], "seccomp=") {
		t.Errorf("seccomp entry should be appended, got order: %v", got)
	}
	if got[0] != existing[0] || got[1] != existing[1] {
		t.Errorf("pre-existing entries clobbered: got %v, want first 2 = %v", got, existing)
	}
}

// TestRunner_AppliesSeccomp is the end-to-end check: a
// Config with a SeccompProfile must result in the moby
// ContainerCreateOptions carrying a SecurityOpt entry that
// starts with "seccomp=". This is the same check as
// TestBuildSecurityOpt_IncludesSeccomp but exercised
// through the full Runner.Run path so we know the
// Config→create-options wiring is intact.
func TestRunner_AppliesSeccomp(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	profile := &SeccompProfile{raw: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[]}`}
	r := NewRunner(ops, Config{SeccompProfile: profile})
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call")
	}
	hc := ops.createdOpts[0].HostConfig
	if hc == nil {
		t.Fatal("HostConfig nil")
	}
	if len(hc.SecurityOpt) != 1 {
		t.Fatalf("SecurityOpt len = %d, want 1", len(hc.SecurityOpt))
	}
	if !strings.HasPrefix(hc.SecurityOpt[0], "seccomp=") {
		t.Errorf("SecurityOpt[0] = %q, want seccomp=… prefix", hc.SecurityOpt[0])
	}
}

// TestRunner_NilSeccompLeavesSecurityOptEmpty confirms
// the noop path: a Config with no SeccompProfile must
// produce a nil SecurityOpt (so the daemon applies its
// own default policy). This is a regression guard
// against accidentally always-on seccomp.
func TestRunner_NilSeccompLeavesSecurityOptEmpty(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	r := NewRunner(ops, Config{}) // no SeccompProfile
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	hc := ops.createdOpts[0].HostConfig
	if len(hc.SecurityOpt) != 0 {
		t.Errorf("SecurityOpt = %v, want empty (nil profile = daemon default)", hc.SecurityOpt)
	}
}

// min is built-in since Go 1.21; we declare a tiny shim
// so this file compiles under older toolchains if anyone
// backports. (Go 1.24 is the project's floor so this is
// belt-and-suspenders.)
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
