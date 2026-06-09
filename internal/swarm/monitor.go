// Package swarm — execution monitor (3-layer tool-call guard).
//
// ptagent inspired: when an LLM agent is stuck in a feedback loop
// (e.g. "scan → no findings → scan the same thing again") it can burn
// through the campaign token budget before anyone notices. The
// execution monitor provides three independent trip-wires:
//
//  1. Per-agent total tool-call cap
//  2. Per-tool continuous streak cap (same tool N times in a row)
//  3. Per-agent time window cap (calls/minute)
//
// When any cap trips, the monitor returns ErrToolBudgetExhausted.
// The scheduler converts that into:
//   - a skipped dispatch (cursor not advanced so the finding is
//     retriable on next campaign if the operator raises the cap)
//   - an "agent_error" event
//   - a TypeAgentError finding on the blackboard (already declared
//     in internal/swarm/blackboard/types.go).
//
// The monitor is concurrency-safe — multiple goroutines within one
// agent may call Observe concurrently. A new monitor is created per
// campaign (via NewMonitor) so a fresh campaign starts with a clean
// slate.
package swarm

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrToolBudgetExhausted is returned by Monitor.Allow when any of the
// three caps has been tripped. The wrapped error string identifies
// which cap fired and what the current usage looks like.
var ErrToolBudgetExhausted = errors.New("tool budget exhausted")

// MonitorConfig configures the execution monitor. Zero-value disables
// the corresponding cap (preserve historical behavior — the monitor
// is opt-in hardening, not a blanket throttle).
type MonitorConfig struct {
	// MaxTotalPerAgent caps the total number of tool calls any single
	// agent may issue during a campaign. Zero = no cap.
	MaxTotalPerAgent int

	// MaxSameToolStreak caps the number of times the same tool may be
	// called in a row by the same agent. Catches "scan same host
	// forever" feedback loops. Zero = no cap.
	MaxSameToolStreak int

	// MaxPerWindow caps the number of tool calls any single agent may
	// issue within a rolling Window. Zero = no cap.
	MaxPerWindow int

	// Window is the rolling time window for MaxPerWindow. Default 1m.
	Window time.Duration
}

// Monitor is the per-campaign tool-call budget tracker.
type Monitor struct {
	cfg MonitorConfig

	mu sync.Mutex

	// total counts tool calls per agent.
	total map[string]int

	// lastTool / streak tracks the most recent tool name and the
	// current consecutive-same-tool count for each agent.
	lastTool map[string]string
	streak   map[string]int

	// window is a ring of timestamps per agent; we trim the front
	// when checking the cap so memory stays bounded.
	window     map[string][]time.Time
	windowSize int // cap on slice length so memory can't blow up
}

// NewMonitor returns a Monitor with the given config. The monitor is
// disabled (all calls allowed) when cfg has every cap set to zero.
func NewMonitor(cfg MonitorConfig) *Monitor {
	if cfg.Window <= 0 {
		cfg.Window = time.Minute
	}
	return &Monitor{
		cfg:        cfg,
		total:      make(map[string]int),
		lastTool:   make(map[string]string),
		streak:     make(map[string]int),
		window:     make(map[string][]time.Time),
		windowSize: 1024, // hard cap so a runaway campaign can't OOM us
	}
}

// Enabled reports whether any cap is configured. Callers can skip the
// Allow fast-path entirely when this returns false.
func (m *Monitor) Enabled() bool {
	return m.cfg.MaxTotalPerAgent > 0 ||
		m.cfg.MaxSameToolStreak > 0 ||
		m.cfg.MaxPerWindow > 0
}

// Allow checks whether (agent, tool) is permitted by every configured
// cap. On success it records the call and returns nil. On failure it
// returns ErrToolBudgetExhausted wrapping a descriptive detail.
//
// The three checks happen in O(1) for total/streak and O(N) for the
// rolling window where N is the number of calls in the last Window.
// N is bounded by MaxPerWindow * 60 / Window + 1 worst case, which we
// further clamp to windowSize.
//
// Stats are tracked unconditionally so disabled monitors still expose
// usage to dashboards and tests; the per-cap checks are simply skipped
// when the corresponding cap is zero.
func (m *Monitor) Allow(agent, tool string) error {
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Total cap.
	if m.cfg.MaxTotalPerAgent > 0 {
		if m.total[agent] >= m.cfg.MaxTotalPerAgent {
			return fmt.Errorf("%w: total %d/%d for agent %q",
				ErrToolBudgetExhausted, m.total[agent], m.cfg.MaxTotalPerAgent, agent)
		}
	}

	// 2. Same-tool streak cap.
	if m.cfg.MaxSameToolStreak > 0 {
		if m.lastTool[agent] == tool {
			if m.streak[agent] >= m.cfg.MaxSameToolStreak {
				return fmt.Errorf("%w: %q called %d times in a row by %q",
					ErrToolBudgetExhausted, tool, m.streak[agent], agent)
			}
		}
	}

	// 3. Per-window cap. Drop expired timestamps, then compare.
	if m.cfg.MaxPerWindow > 0 {
		cutoff := now.Add(-m.cfg.Window)
		bucket := m.window[agent]
		// Find the first index that is still inside the window.
		keep := 0
		for keep < len(bucket) && bucket[keep].Before(cutoff) {
			keep++
		}
		if keep > 0 {
			bucket = bucket[keep:]
		}
		if len(bucket) >= m.cfg.MaxPerWindow {
			m.window[agent] = bucket
			return fmt.Errorf("%w: %d calls in last %s for agent %q (limit %d)",
				ErrToolBudgetExhausted, len(bucket), m.cfg.Window, agent, m.cfg.MaxPerWindow)
		}
		// Append the new timestamp now (cap happens before commit so
		// a rejected call doesn't poison the window).
		bucket = append(bucket, now)
		if len(bucket) > m.windowSize {
			// Should never happen given MaxPerWindow < windowSize, but
			// guard anyway so a misconfiguration can't OOM.
			bucket = bucket[len(bucket)-m.windowSize:]
		}
		m.window[agent] = bucket
	}

	// All checks passed — commit the call.
	m.total[agent]++
	if m.lastTool[agent] == tool {
		m.streak[agent]++
	} else {
		m.lastTool[agent] = tool
		m.streak[agent] = 1
	}
	return nil
}

// Stats returns a snapshot of current usage for a given agent. Safe
// for concurrent use; intended for tests and operator dashboards.
func (m *Monitor) Stats(agent string) (total int, streak int, lastTool string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total[agent], m.streak[agent], m.lastTool[agent]
}

// Reset clears all counters. Used by tests; never called at runtime.
func (m *Monitor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total = make(map[string]int)
	m.lastTool = make(map[string]string)
	m.streak = make(map[string]int)
	m.window = make(map[string][]time.Time)
}
