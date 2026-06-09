package docker

// This is a one-shot integration test for P3-1: spin up
// the locally-built psa-kali:dev image with the bundled
// seccomp profile and verify the seccomp blocking works
// end-to-end. Skipped automatically if psa-kali:dev isn't
// cached.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestLive_PsaKaliImageWithSeccomp(t *testing.T) {
	if !socketAvailable(t) {
		t.Skip("no Docker socket reachable; skipping live test")
	}
	cli, err := client.New(client.WithHostFromEnv(), client.WithTimeout(30*time.Second))
	if err != nil {
		t.Skipf("docker client init failed: %v", err)
	}
	if !imageAvailable(t, cli, "psa-kali:dev") {
		t.Skip("psa-kali:dev not built; run 'docker build -t psa-kali:dev deploy/docker/images/kali' first")
	}
	profile, err := LoadSeccompProfile(embeddedProfilePath(t))
	if err != nil {
		t.Fatalf("LoadSeccompProfile: %v", err)
	}
	r := NewRunner(cli, Config{
		DefaultImage:   "psa-kali:dev",
		SeccompProfile: profile,
	})
	const probe = `perl -e 'use POSIX; my $rc = syscall(48, 99); printf("rc=%d\nerrno_str=%s\n", $rc, "$!");'`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := r.Run(ctx, Request{
		Image:      "psa-kali:dev",
		Command:    []string{"/bin/sh", "-c", probe},
		Entrypoint: []string{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("probe exit=%d, stderr=%q", res.ExitCode, string(res.Stderr))
	}
	out := string(res.Stdout)
	t.Logf("psa-kali:dev + seccomp probe:\n%s", out)
	if !strings.Contains(out, "errno_str=Operation not permitted") {
		t.Errorf("expected 'Operation not permitted' (seccomp denied), got:\n%s", out)
	}
}
