package guardrails

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools/docker"
)

// fakeReporter is the test double for EventReporter. We keep
// the captured events in a slice so tests can assert on
// them in order — the layer is supposed to emit events in
// the same order it inspected them.
type fakeReporter struct {
	mu     sync.Mutex
	events []Event
}

func (f *fakeReporter) Report(ev Event) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}

func (f *fakeReporter) snapshot() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Event, len(f.events))
	copy(out, f.events)
	return out
}

// --- HostNetworkAuditor ---------------------------------------------

func TestHostNetworkAuditor_IgnoresBridgeAndNone(t *testing.T) {
	a := NewHostNetworkAuditor()
	cases := []struct {
		name string
		iso  *docker.Isolation
	}{
		{"nil override", nil},
		{"bridge", &docker.Isolation{Network: docker.NetworkBridge}},
		{"none", &docker.Isolation{Network: docker.NetworkNone}},
		{"empty network (daemon default)", &docker.Isolation{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := docker.Request{Isolation: c.iso}
			ev := a.Inspect(req, "nmap", "recon", "10.0.0.5", time.Now())
			if ev != nil {
				t.Errorf("expected nil event for %s, got %+v", c.name, ev)
			}
		})
	}
	if got := a.SeenTotal(); got != 0 {
		t.Errorf("SeenTotal = %d, want 0", got)
	}
}

func TestHostNetworkAuditor_FlagsHost(t *testing.T) {
	a := NewHostNetworkAuditor()
	req := docker.Request{
		Isolation: &docker.Isolation{
			Network:        docker.NetworkHost,
			ReadonlyRootfs: docker.ReadonlyRootfsReadOnly,
		},
	}
	ev := a.Inspect(req, "masscan", "recon", "10.0.0.0/24", time.Now())
	if ev == nil {
		t.Fatal("expected non-nil event for host network, got nil")
	}
	if ev.Kind != EventHostNetworkUsed {
		t.Errorf("Kind = %q, want %q", ev.Kind, EventHostNetworkUsed)
	}
	if ev.Tool != "masscan" || ev.Agent != "recon" || ev.Target != "10.0.0.0/24" {
		t.Errorf("event fields wrong: %+v", ev)
	}
	if !strings.Contains(ev.Reason, "host") {
		t.Errorf("Reason = %q, want to contain 'host'", ev.Reason)
	}
	if ev.Metadata["network_mode"] != "host" {
		t.Errorf("metadata.network_mode = %v, want \"host\"", ev.Metadata["network_mode"])
	}
	if got := a.SeenTotal(); got != 1 {
		t.Errorf("SeenTotal = %d, want 1", got)
	}
	if got := a.SeenByTool()["masscan"]; got != 1 {
		t.Errorf("SeenByTool[masscan] = %d, want 1", got)
	}
}

func TestHostNetworkAuditor_ConcurrentSafe(t *testing.T) {
	a := NewHostNetworkAuditor()
	const N = 200
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := docker.Request{Isolation: &docker.Isolation{Network: docker.NetworkHost}}
			_ = a.Inspect(req, "nmap", "recon", "10.0.0.5", time.Now())
		}()
	}
	wg.Wait()
	if got := a.SeenTotal(); got != N {
		t.Errorf("SeenTotal = %d, want %d", got, N)
	}
}

func TestHostNetworkAuditor_NilReceiver(t *testing.T) {
	var a *HostNetworkAuditor
	// All methods on nil receiver must be safe.
	if ev := a.Inspect(docker.Request{}, "x", "y", "z", time.Now()); ev != nil {
		t.Errorf("nil Inspect returned %+v", ev)
	}
	if got := a.SeenTotal(); got != 0 {
		t.Errorf("nil SeenTotal = %d, want 0", got)
	}
}

// --- QuotaGuard -----------------------------------------------------

func TestQuotaGuard_DisabledByDefault(t *testing.T) {
	q := NewQuotaGuard(QuotaConfig{}) // all caps zero
	if q.Enabled() {
		t.Error("zero-cfg QuotaGuard should be disabled")
	}
	// Observe must be a noop.
	iso, ev := q.Observe(nil, "nmap", "recon", "10.0.0.5", time.Now())
	if iso != nil || ev != nil {
		t.Errorf("disabled Observe returned iso=%v ev=%v, want both nil", iso, ev)
	}
}

func TestQuotaGuard_ToolCapFiresAndDowngrades(t *testing.T) {
	q := NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 3})
	// 3 calls in budget; 4th is over.
	for i := 1; i <= 3; i++ {
		iso, ev := q.Observe(nil, "nmap", "recon", "tgt", time.Now())
		if iso != nil || ev != nil {
			t.Errorf("call %d (in budget): iso=%v ev=%v, want both nil", i, iso, ev)
		}
	}
	// 4th call must trigger.
	iso, ev := q.Observe(nil, "nmap", "recon", "tgt", time.Now())
	if iso == nil {
		t.Fatal("expected downgraded isolation on 4th call, got nil")
	}
	if iso.Network != docker.NetworkNone {
		t.Errorf("downgrade.Network = %q, want none", iso.Network)
	}
	if iso.ReadonlyRootfs != docker.ReadonlyRootfsReadOnly {
		t.Errorf("downgrade.ReadonlyRootfs = %v, want RO", iso.ReadonlyRootfs)
	}
	if ev == nil || ev.Kind != EventQuotaExceeded {
		t.Errorf("ev = %+v, want EventQuotaExceeded", ev)
	}
	if ev.Metadata["count"] != 4 {
		t.Errorf("ev.metadata.count = %v, want 4", ev.Metadata["count"])
	}
	if ev.Metadata["cap"] != 3 {
		t.Errorf("ev.metadata.cap = %v, want 3", ev.Metadata["cap"])
	}
}

func TestQuotaGuard_AgentCapFires(t *testing.T) {
	q := NewQuotaGuard(QuotaConfig{MaxCallsPerAgent: 2})
	// Two different tools, same agent, budget per agent.
	_, _ = q.Observe(nil, "nmap", "recon", "t1", time.Now())
	_, _ = q.Observe(nil, "httpx", "recon", "t2", time.Now())
	// 3rd call from recon must fire even though neither
	// tool individually hit its (zero) cap.
	iso, ev := q.Observe(nil, "nmap", "recon", "t3", time.Now())
	if iso == nil {
		t.Fatal("agent cap should fire, got nil iso")
	}
	if ev == nil {
		t.Fatal("agent cap should fire, got nil ev")
	}
	if ev.Metadata["count"] != 3 || ev.Metadata["cap"] != 2 {
		t.Errorf("metadata count/cap = %v/%v, want 3/2", ev.Metadata["count"], ev.Metadata["cap"])
	}
}

func TestQuotaGuard_ToolCapWinsOverAgentCap(t *testing.T) {
	// Both caps set; tool cap should be reported first
	// because it's the more specific signal.
	q := NewQuotaGuard(QuotaConfig{
		MaxCallsPerTool:  1,
		MaxCallsPerAgent: 100,
	})
	_, _ = q.Observe(nil, "nmap", "recon", "t1", time.Now())
	_, ev := q.Observe(nil, "nmap", "recon", "t2", time.Now())
	if ev == nil {
		t.Fatal("expected event on 2nd nmap call")
	}
	if !strings.Contains(ev.Reason, "tool") {
		t.Errorf("Reason = %q, want to mention 'tool'", ev.Reason)
	}
}

func TestQuotaGuard_CustomDowngrade(t *testing.T) {
	// Operator wants bridge networking preserved on
	// over-quota calls — just harden the rootfs.
	q := NewQuotaGuard(QuotaConfig{
		MaxCallsPerTool: 1,
		QuotaDowngrade: docker.Isolation{
			Network:        docker.NetworkBridge,
			ReadonlyRootfs: docker.ReadonlyRootfsReadOnly,
		},
	})
	_, _ = q.Observe(nil, "nmap", "recon", "t1", time.Now())
	iso, _ := q.Observe(nil, "nmap", "recon", "t2", time.Now())
	if iso == nil {
		t.Fatal("expected downgrade")
	}
	if iso.Network != docker.NetworkBridge {
		t.Errorf("custom downgrade.Network = %q, want bridge", iso.Network)
	}
	if iso.ReadonlyRootfs != docker.ReadonlyRootfsReadOnly {
		t.Errorf("custom downgrade.ReadonlyRootfs = %v, want RO", iso.ReadonlyRootfs)
	}
}

func TestQuotaGuard_StatsSnapshot(t *testing.T) {
	q := NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 1})
	_, _ = q.Observe(nil, "nmap", "recon", "t1", time.Now())
	_, _ = q.Observe(nil, "nmap", "recon", "t2", time.Now()) // over budget
	_, _ = q.Observe(nil, "httpx", "recon", "t3", time.Now())
	perTool, perAgent, downgrades := q.Stats()
	if perTool["nmap"] != 2 {
		t.Errorf("perTool[nmap] = %d, want 2", perTool["nmap"])
	}
	if perTool["httpx"] != 1 {
		t.Errorf("perTool[httpx] = %d, want 1", perTool["httpx"])
	}
	if perAgent["recon"] != 3 {
		t.Errorf("perAgent[recon] = %d, want 3", perAgent["recon"])
	}
	if downgrades != 1 {
		t.Errorf("downgrades = %d, want 1", downgrades)
	}
}

func TestQuotaGuard_ConcurrentSafe(t *testing.T) {
	q := NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 1000})
	const N = 500
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = q.Observe(nil, "nmap", "recon", "tgt", time.Now())
		}()
	}
	wg.Wait()
	perTool, _, _ := q.Stats()
	if perTool["nmap"] != N {
		t.Errorf("perTool[nmap] = %d, want %d", perTool["nmap"], N)
	}
}

// --- Guardrails.AuditAndDowngrade -----------------------------------

func TestGuardrails_NilIsNoop(t *testing.T) {
	var g *Guardrails
	req := docker.Request{Isolation: &docker.Isolation{Network: docker.NetworkHost}}
	out, events := g.AuditAndDowngrade("nmap", "recon", "tgt", req)
	if out.Isolation.Network != docker.NetworkHost {
		t.Errorf("nil guardrail mutated request: %+v", out)
	}
	if len(events) != 0 {
		t.Errorf("nil guardrail emitted events: %+v", events)
	}
}

func TestGuardrails_AuditsAndDoesNotMutate(t *testing.T) {
	rep := &fakeReporter{}
	g := &Guardrails{
		Auditor:  NewHostNetworkAuditor(),
		Reporter: rep,
	}
	req := docker.Request{Isolation: &docker.Isolation{Network: docker.NetworkHost}}
	out, events := g.AuditAndDowngrade("nmap", "recon", "tgt", req)
	// Auditor is passive: request must be returned unchanged.
	if out.Isolation != req.Isolation {
		t.Errorf("auditor mutated request: %+v", out)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Kind != EventHostNetworkUsed {
		t.Errorf("event[0].Kind = %q, want %q", events[0].Kind, EventHostNetworkUsed)
	}
	if len(rep.snapshot()) != 1 {
		t.Errorf("reporter received %d events, want 1", len(rep.snapshot()))
	}
}

func TestGuardrails_QuotaDowngradesAndReports(t *testing.T) {
	rep := &fakeReporter{}
	g := &Guardrails{
		Quota:    NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 1}),
		Reporter: rep,
	}
	// First call: in budget — Isolation must come back
	// unchanged (same pointer, no allocation).
	req := docker.Request{Isolation: &docker.Isolation{Network: docker.NetworkBridge}}
	out, events := g.AuditAndDowngrade("nmap", "recon", "tgt", req)
	if out.Isolation != req.Isolation {
		t.Errorf("in-budget call mutated isolation: in=%p out=%p", req.Isolation, out.Isolation)
	}
	if len(events) != 0 {
		t.Errorf("in-budget call emitted events: %+v", events)
	}
	// Second call: over quota. Quota layer returns a new
	// pointer; runner sees the downgraded posture.
	out, events = g.AuditAndDowngrade("nmap", "recon", "tgt", req)
	if out.Isolation == nil {
		t.Fatal("over-quota call did not return a downgrade")
	}
	if out.Isolation == req.Isolation {
		t.Error("quota layer returned the same pointer — should be a fresh downgrade")
	}
	if out.Isolation.Network != docker.NetworkNone {
		t.Errorf("downgrade.Network = %q, want none", out.Isolation.Network)
	}
	if len(events) != 1 || events[0].Kind != EventQuotaExceeded {
		t.Errorf("events = %+v, want one EventQuotaExceeded", events)
	}
	if len(rep.snapshot()) != 1 {
		t.Errorf("reporter received %d events, want 1", len(rep.snapshot()))
	}
}

func TestGuardrails_BothLayersEmitDistinctEvents(t *testing.T) {
	rep := &fakeReporter{}
	g := &Guardrails{
		Auditor:  NewHostNetworkAuditor(),
		Quota:    NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 1}),
		Reporter: rep,
	}
	// Call 1: under quota, but request asks for host network.
	_, _ = g.AuditAndDowngrade("nmap", "recon", "tgt", docker.Request{
		Isolation: &docker.Isolation{Network: docker.NetworkHost},
	})
	// Call 2: over quota (and still host network).
	_, events := g.AuditAndDowngrade("nmap", "recon", "tgt", docker.Request{
		Isolation: &docker.Isolation{Network: docker.NetworkHost},
	})
	// We expect both kinds in the second call's events:
	// the auditor runs every call, the quota layer fires
	// on the second.
	if len(events) != 2 {
		t.Fatalf("call 2 events = %d, want 2 (host + quota)", len(events))
	}
	kinds := map[EventKind]bool{}
	for _, ev := range events {
		kinds[ev.Kind] = true
	}
	if !kinds[EventHostNetworkUsed] || !kinds[EventQuotaExceeded] {
		t.Errorf("expected both kinds, got %+v", kinds)
	}
	// The reporter should have received 3 events total
	// (call 1: host; call 2: host + quota).
	if got := len(rep.snapshot()); got != 3 {
		t.Errorf("reporter got %d events, want 3", got)
	}
}

// --- LangFuseReporter -----------------------------------------------

// TestLangFuseReporter_NilSafe is a defense-in-depth test: the
// runner calls Report from a goroutine and a nil reporter must
// not panic.
func TestLangFuseReporter_NilSafe(t *testing.T) {
	var r *LangFuseReporter
	r.Report(Event{Kind: EventHostNetworkUsed}) // must not panic
}

// TestLangFuseReporter_LogsToSlog verifies the on-disk
// (slog) path works even when LangFuse is nil. We capture the
// slog output to a buffer.
func TestLangFuseReporter_LogsToSlog(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewLangFuseReporter(nil, logger) // no LangFuse
	r.Report(Event{
		Kind:   EventHostNetworkUsed,
		Tool:   "nmap",
		Agent:  "recon",
		Target: "10.0.0.5",
		Reason: "test reason",
		Metadata: map[string]any{
			"audit_count": 7,
		},
		Time: time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC),
	})
	out := buf.String()
	// The JSON line must round-trip and contain our
	// fields. We use json.Unmarshal rather than string
	// matching because slog's JSON layout is stable but
	// ordering is not.
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("slog output not valid JSON: %v\noutput=%s", err, out)
	}
	if got["msg"] != "guardrail event" {
		t.Errorf("msg = %v, want \"guardrail event\"", got["msg"])
	}
	if got["kind"] != "host_network_used" {
		t.Errorf("kind = %v", got["kind"])
	}
	if got["tool"] != "nmap" {
		t.Errorf("tool = %v", got["tool"])
	}
	if got["reason"] != "test reason" {
		t.Errorf("reason = %v", got["reason"])
	}
}

// TestLangFuseReporter_PreservesClock verifies the reporter
// honors the time stamped on the event (rather than calling
// time.Now internally). This matters because the docker.Runner
// already takes a clock via the Guardrails Now field — the
// reporter should not introduce a second, independent clock.
func TestLangFuseReporter_HonorsEventTime(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	r := NewLangFuseReporter(nil, logger)
	want := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	r.Report(Event{Kind: EventHostNetworkUsed, Time: want})
	if !strings.Contains(buf.String(), "2025-01-01T00:00:00Z") {
		t.Errorf("slog line should contain the event's time, got: %s", buf.String())
	}
}

// --- newID / eventToInput -------------------------------------------

func TestNewID_Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := newID()
		if seen[id] {
			t.Fatalf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}

func TestEventToInput_BlocksShadowKeys(t *testing.T) {
	ev := Event{
		Kind:   EventHostNetworkUsed,
		Tool:   "nmap",
		Agent:  "recon",
		Target: "10.0.0.5",
		Time:   time.Now(),
		Metadata: map[string]any{
			"tool":        "OVERRIDE",   // should be ignored
			"kind":        "OVERRIDE",   // should be ignored
			"audit_count": 7,            // should pass through
		},
	}
	out := eventToInput(ev)
	if out["tool"] != "nmap" {
		t.Errorf("eventToInput leaked a shadowed tool: %v", out["tool"])
	}
	if out["kind"] != string(EventHostNetworkUsed) {
		t.Errorf("eventToInput leaked a shadowed kind: %v", out["kind"])
	}
	if out["audit_count"] != 7 {
		t.Errorf("audit_count = %v, want 7", out["audit_count"])
	}
}
