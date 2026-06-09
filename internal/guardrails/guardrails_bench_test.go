package guardrails

// Micro-benchmarks for the guardrails layer. We need to
// know: how much does an opt-in GuardrailHook add to the
// hot path of a docker.Runner.Run() call? The target is
// "essentially free" (sub-microsecond when no event
// fires), matching the ResourceLimits / Isolation merge
// benchmarks in the docker package.

import (
	"testing"
	"time"

	dockerpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools/docker"
)

// BenchmarkGuardrails_AuditAndDowngrade_NoFire measures
// the happy path: a non-host, in-budget request. No
// events are emitted, no downgrades applied. We expect
// this to be a few nanoseconds and zero allocations.
func BenchmarkGuardrails_AuditAndDowngrade_NoFire(b *testing.B) {
	g := &Guardrails{
		Auditor:  NewHostNetworkAuditor(),
		Quota:    NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 1000}),
		Reporter: noopReporter{},
	}
	req := dockerpkg.Request{
		Image:     "alpine:3",
		Command:   []string{"true"},
		Isolation: &dockerpkg.Isolation{Network: dockerpkg.NetworkBridge},
		Tool:      "httpx",
		Agent:     "recon",
		Target:    "https://example.com",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.AuditAndDowngrade("httpx", "recon", "https://example.com", req)
	}
}

// BenchmarkGuardrails_AuditAndDowngrade_HostFire measures
// the auditor's host-network path. We expect a small
// constant overhead for the Event allocation and the
// auditor's seen/byTool map updates.
func BenchmarkGuardrails_AuditAndDowngrade_HostFire(b *testing.B) {
	g := &Guardrails{
		Auditor:  NewHostNetworkAuditor(),
		Reporter: noopReporter{},
	}
	req := dockerpkg.Request{
		Image:     "alpine:3",
		Command:   []string{"true"},
		Isolation: &dockerpkg.Isolation{Network: dockerpkg.NetworkHost},
		Tool:      "nmap",
		Agent:     "recon",
		Target:    "10.0.0.0/24",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.AuditAndDowngrade("nmap", "recon", "10.0.0.0/24", req)
	}
}

// BenchmarkGuardrails_AuditAndDowngrade_QuotaFire
// measures the quota-exceeded path. This allocates an
// Event and a fresh Isolation pointer. We expect
// allocation count to be small and bounded.
func BenchmarkGuardrails_AuditAndDowngrade_QuotaFire(b *testing.B) {
	g := &Guardrails{
		Quota:    NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 0}), // every call is over
		Reporter: noopReporter{},
	}
	req := dockerpkg.Request{
		Image:     "alpine:3",
		Command:   []string{"true"},
		Isolation: &dockerpkg.Isolation{Network: dockerpkg.NetworkBridge},
		Tool:      "nmap",
		Agent:     "recon",
		Target:    "10.0.0.5",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.AuditAndDowngrade("nmap", "recon", "10.0.0.5", req)
	}
}

// BenchmarkHostNetworkAuditor_Inspect is the per-call cost
// of the auditor alone, in isolation. Lets us see how much
// of AuditAndDowngrade is the auditor vs. the quota layer.
func BenchmarkHostNetworkAuditor_Inspect(b *testing.B) {
	a := NewHostNetworkAuditor()
	req := dockerpkg.Request{
		Isolation: &dockerpkg.Isolation{Network: dockerpkg.NetworkHost},
	}
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Inspect(req, "nmap", "recon", "10.0.0.5", now)
	}
}

// BenchmarkQuotaGuard_Observe is the per-call cost of the
// quota layer alone, in the over-budget path (which is
// the one that allocates).
func BenchmarkQuotaGuard_Observe(b *testing.B) {
	q := NewQuotaGuard(QuotaConfig{MaxCallsPerTool: 0})
	iso := &dockerpkg.Isolation{Network: dockerpkg.NetworkBridge}
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = q.Observe(iso, "nmap", "recon", "10.0.0.5", now)
	}
}

// noopReporter is a reporter that drops everything.
// Using it in the benchmarks keeps the cost of the
// Report call itself out of the measurement.
type noopReporter struct{}

func (noopReporter) Report(Event) {}
