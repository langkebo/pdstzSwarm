// Package guardrails — quota tracker with auto-downgrade.
//
// Why a quota layer on top of swarm.Monitor: monitor.go enforces
// hard caps (refuse the call when budget is gone). That's the
// right behavior for "burning tokens in a loop" but the wrong
// behavior for "tool misbehaving" — a sqlmap that's flaky might
// produce 0 findings on calls 1..N, then suddenly produce
// real data on call N+1. Killing it at N is wasteful. The
// right reaction is **degrade the blast radius**: keep calling
// the tool, but force it into a stricter sandbox so a malicious
// payload in the late-stage tool output can't escape.

package guardrails

import (
	"sync"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools/docker"
)

// QuotaConfig configures the QuotaGuard. Zero values disable the
// corresponding check (preserve historical behavior — the quota
// layer is opt-in, not a blanket throttle).
type QuotaConfig struct {
	// MaxCallsPerTool caps how many times any single tool may
	// be called within a campaign. After this, every
	// subsequent call gets its Isolation posture downgraded
	// to QuotaDowngrade. Zero = no cap.
	MaxCallsPerTool int

	// MaxCallsPerAgent caps how many tool calls any single
	// agent may make. Useful to detect "exploit agent in a
	// loop" before it spirals. Zero = no cap.
	MaxCallsPerAgent int

	// QuotaDowngrade is the Isolation posture applied to
	// every call that exceeds a cap. We default to "no
	// network + read-only rootfs" — the strictest posture
	// the docker package supports. Operators can choose a
	// milder posture (e.g. just NetworkNone) by setting
	// this explicitly.
	QuotaDowngrade docker.Isolation
}

// QuotaGuard counts tool calls and downgrades Isolation posture
// when a cap is exceeded. A single instance is shared across a
// campaign; construction is cheap (no goroutines, no network).
type QuotaGuard struct {
	cfg QuotaConfig

	mu sync.Mutex

	// perTool counts how many times each tool has been called.
	// Keyed by tool name; the dashboard uses this to show
	// "top-N talkers".
	perTool map[string]int

	// perAgent counts how many calls each agent has made.
	// Keyed by agent name.
	perAgent map[string]int

	// downgradesTotal counts how many times the policy
	// downgraded a call. Used for /stats; not used
	// internally.
	downgradesTotal int
}

// NewQuotaGuard returns a QuotaGuard. cfg.MaxCallsPerTool or
// cfg.MaxCallsPerAgent set to 0 disables that cap.
func NewQuotaGuard(cfg QuotaConfig) *QuotaGuard {
	if cfg.QuotaDowngrade.Network == "" && cfg.QuotaDowngrade.ReadonlyRootfs == 0 {
		// Sensible default: air-gap + read-only rootfs.
		// This is the strictest posture; operators who
		// want to allow more (e.g. keep network) must
		// say so explicitly.
		cfg.QuotaDowngrade = docker.Isolation{
			Network:        docker.NetworkNone,
			ReadonlyRootfs: docker.ReadonlyRootfsReadOnly,
		}
	}
	return &QuotaGuard{
		cfg:      cfg,
		perTool:  make(map[string]int),
		perAgent: make(map[string]int),
	}
}

// Enabled reports whether any cap is configured. Callers may
// skip the Observe fast-path entirely when this returns false.
func (q *QuotaGuard) Enabled() bool {
	if q == nil {
		return false
	}
	return q.cfg.MaxCallsPerTool > 0 || q.cfg.MaxCallsPerAgent > 0
}

// Observe is the per-call entry point. It increments counters
// and, if any cap is exceeded, returns a downgraded Isolation
// posture (or nil when no downgrade is needed) and a non-nil
// Event describing the policy violation.
//
// Returning a *docker.Isolation rather than mutating the
// caller's struct in place is intentional: the docker.Runner
// pipeline already does its own Merge dance, and we want the
// mutation to be explicit at the call site (so reviewers can
// see "yes, the quota layer rewrote the posture here").
//
// Returning a *Event rather than just emitting it is also
// intentional: the caller (Guardrails.AuditAndDowngrade)
// owns the fan-out to the Reporter, which lets us batch
// "audit + quota" events into a single Report loop and gives
// tests a single, ordered list to assert on.
func (q *QuotaGuard) Observe(iso *docker.Isolation, tool, agent, target string, now time.Time) (*docker.Isolation, *Event) {
	if q == nil || !q.Enabled() {
		return nil, nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// Increment first, then check. The reasoning: a cap of
	// "10 calls" means "the 11th call is over budget", so we
	// compare against the post-increment value.
	q.perTool[tool]++
	q.perAgent[agent]++

	// Determine which cap (if any) fired. We fire on the
	// first match; tool-cap is checked before agent-cap
	// because per-tool is the more specific signal (an
	// agent may run many tools; "this tool" is what
	// operators want to know about).
	var reason string
	var count, cap int
	switch {
	case q.cfg.MaxCallsPerTool > 0 && q.perTool[tool] > q.cfg.MaxCallsPerTool:
		reason = "tool call budget exceeded"
		count = q.perTool[tool]
		cap = q.cfg.MaxCallsPerTool
	case q.cfg.MaxCallsPerAgent > 0 && q.perAgent[agent] > q.cfg.MaxCallsPerAgent:
		reason = "agent call budget exceeded"
		count = q.perAgent[agent]
		cap = q.cfg.MaxCallsPerAgent
	default:
		// Within budget. No event, no downgrade.
		return nil, nil
	}

	q.downgradesTotal++

	// Build the downgraded Isolation. We always return a
	// fresh pointer (not the caller's iso) so the caller
	// has a clear "this is a NEW value, distinct from
	// what you passed in" signal.
	downgrade := q.cfg.QuotaDowngrade
	ev := &Event{
		Kind:   EventQuotaExceeded,
		Tool:   tool,
		Agent:  agent,
		Target: target,
		Reason: reason,
		Metadata: map[string]any{
			"count":              count,
			"cap":                cap,
			"downgrade_network":  string(downgrade.Network),
			"downgrade_rootfs":   downgrade.ReadonlyRootfs,
			"downgrades_total":   q.downgradesTotal,
		},
		Time: now,
	}

	// If the auditor already saw a downgraded posture
	// (e.g. host-network request was retained for audit but
	// quota is now also downgrading), the quota layer
	// wins. Rationale: a call that hits quota is a
	// stronger signal than one that just asked for host
	// network, and we want the most restrictive
	// posture.
	return &downgrade, ev
}

// Stats returns a snapshot of current usage. Safe for
// concurrent use; intended for tests and operator
// dashboards. The returned maps are copies.
func (q *QuotaGuard) Stats() (perTool, perAgent map[string]int, downgrades int) {
	if q == nil {
		return nil, nil, 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	perTool = make(map[string]int, len(q.perTool))
	for k, v := range q.perTool {
		perTool[k] = v
	}
	perAgent = make(map[string]int, len(q.perAgent))
	for k, v := range q.perAgent {
		perAgent[k] = v
	}
	return perTool, perAgent, q.downgradesTotal
}

// Reset clears all counters. Test-only.
func (q *QuotaGuard) Reset() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.perTool = make(map[string]int)
	q.perAgent = make(map[string]int)
	q.downgradesTotal = 0
	q.mu.Unlock()
}
