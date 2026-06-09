package swarm

import (
	"sync/atomic"
	"testing"
)

// BenchmarkMonitor_Allow_Disabled measures the zero-overhead path
// when the monitor is configured but no cap fires. Establishes the
// baseline cost the scheduler pays for the extra check.
func BenchmarkMonitor_Allow_Disabled(b *testing.B) {
	m := NewMonitor(MonitorConfig{})
	b.RunParallel(func(pb *testing.PB) {
		var i int64
		for pb.Next() {
			n := atomic.AddInt64(&i, 1)
			_ = m.Allow("recon", toolName(n))
		}
	})
}

// BenchmarkMonitor_Allow_AllCaps measures the worst-case cost when
// every cap is configured and must be evaluated on every call. This
// is the real "production" path; we want it sub-200 ns/op.
func BenchmarkMonitor_Allow_AllCaps(b *testing.B) {
	m := NewMonitor(MonitorConfig{
		MaxTotalPerAgent:  1_000_000,
		MaxSameToolStreak: 1000,
		MaxPerWindow:      1_000_000,
		Window:            60_000_000_000, // 60s
	})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var i int64
		for pb.Next() {
			n := atomic.AddInt64(&i, 1)
			_ = m.Allow("recon", toolName(n))
		}
	})
}

// BenchmarkMonitor_Allow_ManyAgents measures the cost when there are
// many concurrent agents (e.g. 32 specialist workers in one campaign).
// The per-agent maps in Monitor must stay cheap to look up.
func BenchmarkMonitor_Allow_ManyAgents(b *testing.B) {
	m := NewMonitor(MonitorConfig{
		MaxTotalPerAgent:  1_000_000,
		MaxSameToolStreak: 1000,
		MaxPerWindow:      1_000_000,
		Window:            60_000_000_000,
	})
	b.ResetTimer()
	agents := []string{"recon", "exploit", "report", "pivot", "phish",
		"fuzz", "supply", "auth", "web", "binary", "ctf", "bug-bounty"}
	tools := []string{"nmap", "sqlmap", "httpx", "nuclei", "ffuf", "amass"}
	b.RunParallel(func(pb *testing.PB) {
		var i int64
		for pb.Next() {
			n := atomic.AddInt64(&i, 1)
			_ = m.Allow(agents[int(n)%len(agents)], tools[int(n)%len(tools)])
		}
	})
}

func toolName(n int64) string {
	switch n % 4 {
	case 0:
		return "nmap"
	case 1:
		return "sqlmap"
	case 2:
		return "httpx"
	default:
		return "nuclei"
	}
}
