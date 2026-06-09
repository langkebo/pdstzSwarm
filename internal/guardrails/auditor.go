// Package guardrails — host network auditor.
//
// ptagent-comparison note: ptagent's network mode is fixed at the
// image level (their kali image already shares host's netns by
// default). We don't have that option in the docker SDK — host
// network is a per-ContainerCreate opt-in, not an image attribute.
// So instead of "block host network at image build time", we
// audit every Request that asks for it. The audit is non-blocking
// by design: an operator might legitimately need host network
// (raw socket scans, masscan's --send-eth, port-knock detection).
// What we want is a paper trail, not a refusal.

package guardrails

import (
	"sync"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools/docker"
)

// HostNetworkAuditor detects Request.Isolation values that ask
// for the host's network namespace. A single instance is safe
// to share across goroutines; the only mutable state is the
// in-memory counter, used by tests and the operator dashboard.
type HostNetworkAuditor struct {
	mu sync.Mutex

	// seen counts how many host-network requests we've
	// observed since construction (or last Reset). Operators
	// read this via the /stats endpoint; tests use it to
	// assert on call volume.
	seen int

	// byTool partitions the count by tool name. This is the
	// pivot the operator uses to find "which tool keeps
	// asking for host network".
	byTool map[string]int
}

// NewHostNetworkAuditor returns a fresh auditor.
func NewHostNetworkAuditor() *HostNetworkAuditor {
	return &HostNetworkAuditor{byTool: make(map[string]int)}
}

// Inspect is the auditor's per-call entry point. It examines
// the Request's resolved Isolation posture; if the Network
// field is host, it returns a non-nil Event describing the
// observation. The caller (Guardrails.AuditAndDowngrade) is
// responsible for forwarding the event to the reporter.
//
// Inspect does NOT modify the Request. The auditor is
// passive by design: it never blocks a call, it just records.
// A future iteration could couple this with a "requires
// explicit approval" policy, but that's product-tier scope
// beyond P2.
func (a *HostNetworkAuditor) Inspect(req docker.Request, tool, agent, target string, now time.Time) *Event {
	if a == nil {
		return nil
	}
	// Empty Network = "let the daemon pick" = bridge on a
	// vanilla install. Bridge is fine, no audit needed.
	// Anything other than host is also fine: bridge is the
	// common case; none is the strictest.
	if req.Isolation == nil || req.Isolation.Network != docker.NetworkHost {
		return nil
	}

	a.mu.Lock()
	a.seen++
	a.byTool[tool]++
	a.mu.Unlock()

	return &Event{
		Kind:   EventHostNetworkUsed,
		Tool:   tool,
		Agent:  agent,
		Target: target,
		Reason: "request asked for NetworkMode=host (bypasses netns isolation)",
		Metadata: map[string]any{
			"network_mode":  string(req.Isolation.Network),
			"readonly_rootfs": req.Isolation.ReadonlyRootfs,
			"audit_count":   a.SeenTotal(),
		},
		Time: now,
	}
}

// SeenTotal returns the cumulative count of host-network
// requests observed. Safe to call concurrently.
func (a *HostNetworkAuditor) SeenTotal() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen
}

// SeenByTool returns a copy of the per-tool count map. The
// copy is intentional: callers iterating the map don't need
// to worry about the auditor mutating it under them.
func (a *HostNetworkAuditor) SeenByTool() map[string]int {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]int, len(a.byTool))
	for k, v := range a.byTool {
		out[k] = v
	}
	return out
}

// Reset clears all counters. Test-only.
func (a *HostNetworkAuditor) Reset() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.seen = 0
	a.byTool = make(map[string]int)
	a.mu.Unlock()
}
