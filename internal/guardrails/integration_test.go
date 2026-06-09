package guardrails

// Live integration tests for the guardrails package. These
// require a real Docker daemon (the same one the docker
// package's integration tests use) and exercise the end-to-end
// flow: Guardrails.BeforeRun → docker.Runner.Run() → real
// container with the downgraded posture.

import (
	"context"
	"sync"
	"testing"
	"time"

	dockerpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools/docker"
	"github.com/moby/moby/client"
)

// memoryReporter is a goroutine-safe in-memory reporter used
// by the live tests. We don't want to depend on a real
// LangFuse endpoint during integration tests.
type memoryReporter struct {
	mu     sync.Mutex
	events []Event
}

func (m *memoryReporter) Report(ev Event) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
}

func (m *memoryReporter) snapshot() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// liveRunner constructs a docker.Runner backed by a real
// daemon. The cfg is used as-is (no defaults). Skips the
// test if no Docker socket / no small image is available,
// matching the docker package's behavior.
func liveRunner(t *testing.T, cfg dockerpkg.Config) *dockerpkg.Runner {
	t.Helper()
	cli, err := client.New(client.WithHostFromEnv(), client.WithTimeout(30*time.Second))
	if err != nil {
		t.Skipf("docker client init failed: %v", err)
	}
	// Resolve a small image. We try the same fallbacks
	// the docker package's tests use, then bail if
	// nothing is available.
	for _, ref := range []string{
		"alpine:3", "alpine:latest", "busybox:latest",
		"ptagent/server:latest", "ptagent/scraper:latest", "ptagent/db:latest",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, ierr := cli.ImageInspect(ctx, ref)
		cancel()
		if ierr == nil {
			if cfg.DefaultImage == "" {
				cfg.DefaultImage = ref
			}
			cfg.AllowedImages = nil
			return dockerpkg.NewRunner(cli, cfg)
		}
	}
	t.Skip("no small base image cached locally; skipping live guardrail test")
	return nil
}

// TestLive_HostNetworkRequest_ReachesMoby verifies that a
// Request asking for NetworkHost is honored by the docker
// daemon end-to-end. The auditor's job is to *record*, not
// to *block* — so we expect the run to succeed and the
// reporter to see exactly one EventHostNetworkUsed.
func TestLive_HostNetworkRequest_ReachesMoby(t *testing.T) {
	rep := &memoryReporter{}
	g := &Guardrails{
		Auditor:  NewHostNetworkAuditor(),
		Reporter: rep,
	}
	r := liveRunner(t, dockerpkg.Config{Guardrails: g})
	res, err := r.Run(context.Background(), dockerpkg.Request{
		Image:      r.Config.DefaultImage,
		Command:    []string{"/bin/sh", "-c", "ip -4 addr show 2>/dev/null | grep -c '^' || echo no-ip"},
		Entrypoint: []string{},
		Isolation:  &dockerpkg.Isolation{Network: dockerpkg.NetworkHost},
		Tool:       "masscan",
		Agent:      "recon",
		Target:     "10.0.0.0/24",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Logf("stdout: %s", string(res.Stdout))
		t.Logf("stderr: %s", string(res.Stderr))
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	events := rep.snapshot()
	if len(events) != 1 {
		t.Fatalf("reporter saw %d events, want 1", len(events))
	}
	if events[0].Kind != EventHostNetworkUsed {
		t.Errorf("event kind = %q, want %q", events[0].Kind, EventHostNetworkUsed)
	}
	if events[0].Tool != "masscan" || events[0].Agent != "recon" {
		t.Errorf("event attribution: tool=%q agent=%q", events[0].Tool, events[0].Agent)
	}
	if g.Auditor.SeenTotal() != 1 {
		t.Errorf("auditor count = %d, want 1", g.Auditor.SeenTotal())
	}
}

// TestLive_QuotaDowngrade_ReachesMoby is the key end-to-end
// test: trigger a quota exceedance, and verify the
// downgraded Isolation actually arrives at the docker
// daemon. We use a 2-call budget; the second call must run
// in a network=none, read-only rootfs container — which we
// can observe by the /tmp write failing and the nc probe
// failing.
func TestLive_QuotaDowngrade_ReachesMoby(t *testing.T) {
	rep := &memoryReporter{}
	g := &Guardrails{
		Quota:    NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 1}),
		Reporter: rep,
	}
	r := liveRunner(t, dockerpkg.Config{
		// Config default: bridge + rw. Quota layer
		// downgrades to none + RO.
		Isolation: dockerpkg.Isolation{
			Network:        dockerpkg.NetworkBridge,
			ReadonlyRootfs: dockerpkg.ReadonlyRootfsReadWrite,
		},
		Guardrails: g,
	})
	// First call: in budget. No event.
	_, _ = r.Run(context.Background(), dockerpkg.Request{
		Image:      r.Config.DefaultImage,
		Command:    []string{"/bin/sh", "-c", "true"},
		Entrypoint: []string{},
		Tool:       "nmap",
		Agent:      "recon",
		Target:     "10.0.0.5",
	})
	// Second call: over budget. Quota layer downgrades.
	// We probe both axes:
	//   1) network=none: nc -z 127.0.0.1 1 must fail
	//   2) read-only rootfs: write to /tmp must EROFS
	// The shell command does both in one shot.
	res, err := r.Run(context.Background(), dockerpkg.Request{
		Image:   r.Config.DefaultImage,
		Command: []string{"/bin/sh", "-c", "(echo x > /tmp/probe 2>&1; echo ro=$?); timeout 2 nc -z 127.0.0.1 1; echo nc=$?"},
		Entrypoint: []string{},
		Tool:       "nmap",
		Agent:      "recon",
		Target:     "10.0.0.5",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("over-quota run exit=%d stdout=%q", res.ExitCode, string(res.Stdout))
	// The container's overall ExitCode is 0 (final echo
	// succeeds). The signals are in stdout.
	out := string(res.Stdout)
	if !containsAny(out, []string{"Read-only", "read-only", "EROFS", "Read only"}) {
		t.Errorf("expected RO error in stdout, got: %q", out)
	}
	// nc exit is in stdout: we expect "nc=1" (refused)
	// or "nc=124" (timeout). "nc=0" would mean network
	// leaked.
	if !containsAny(out, []string{"nc=1", "nc=124"}) {
		t.Errorf("expected nc=1 or nc=124, got: %q", out)
	}
	if containsAny(out, []string{"nc=0"}) {
		t.Errorf("nc=0 means network leaked through quota downgrade! stdout: %q", out)
	}
	// Reporter must have seen exactly one EventQuotaExceeded.
	events := rep.snapshot()
	if len(events) != 1 {
		t.Fatalf("reporter saw %d events, want 1", len(events))
	}
	if events[0].Kind != EventQuotaExceeded {
		t.Errorf("event kind = %q, want %q", events[0].Kind, EventQuotaExceeded)
	}
}

// TestLive_BothLayers_QuotasWinOverHost verifies the
// priority rule: when a Request asks for host network AND
// the quota is exceeded, the quota's downgrade wins. We
// observe this by the nc probe failing (network=none from
// quota) rather than succeeding (host network from
// request).
func TestLive_BothLayers_QuotasWinOverHost(t *testing.T) {
	rep := &memoryReporter{}
	g := &Guardrails{
		Auditor:  NewHostNetworkAuditor(),
		Quota:    NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 1}),
		Reporter: rep,
	}
	r := liveRunner(t, dockerpkg.Config{Guardrails: g})
	// First call: under quota, host network.
	_, _ = r.Run(context.Background(), dockerpkg.Request{
		Image:      r.Config.DefaultImage,
		Command:    []string{"/bin/sh", "-c", "true"},
		Entrypoint: []string{},
		Isolation:  &dockerpkg.Isolation{Network: dockerpkg.NetworkHost},
		Tool:       "nmap",
		Agent:      "recon",
		Target:     "10.0.0.0/24",
	})
	// Second call: over quota AND host network.
	res, err := r.Run(context.Background(), dockerpkg.Request{
		Image:      r.Config.DefaultImage,
		Command:    []string{"/bin/sh", "-c", "timeout 2 nc -z 127.0.0.1 1; echo nc=$?"},
		Entrypoint: []string{},
		Isolation:  &dockerpkg.Isolation{Network: dockerpkg.NetworkHost},
		Tool:       "nmap",
		Agent:      "recon",
		Target:     "10.0.0.0/24",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := string(res.Stdout)
	// Quota won: network should be none, not host. nc
	// cannot reach 127.0.0.1.
	if containsAny(out, []string{"nc=0"}) {
		t.Errorf("quota did NOT win over host network; nc=0 leaked. stdout: %q", out)
	}
	// Both events must have been emitted.
	events := rep.snapshot()
	// First call: 1 host event. Second call: 1 host
	// event + 1 quota event = 2. Total: 3.
	if len(events) != 3 {
		t.Fatalf("reporter saw %d events, want 3 (1+2)", len(events))
	}
	kinds := map[EventKind]int{}
	for _, ev := range events {
		kinds[ev.Kind]++
	}
	if kinds[EventHostNetworkUsed] != 2 {
		t.Errorf("EventHostNetworkUsed count = %d, want 2", kinds[EventHostNetworkUsed])
	}
	if kinds[EventQuotaExceeded] != 1 {
		t.Errorf("EventQuotaExceeded count = %d, want 1", kinds[EventQuotaExceeded])
	}
}

// containsAny is a local helper — we deliberately do not
// import the docker package's regexp / strings helpers to
// keep the test self-contained.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf is strings.Index without the import. Keeps the
// live test's import list short.
func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}
