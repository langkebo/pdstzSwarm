package swarm

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMonitor_DisabledByDefault(t *testing.T) {
	m := NewMonitor(MonitorConfig{})
	if m.Enabled() {
		t.Fatal("expected Enabled()==false when all caps are zero")
	}
	for i := 0; i < 1000; i++ {
		if err := m.Allow("recon", "nmap"); err != nil {
			t.Fatalf("Allow on disabled monitor returned %v", err)
		}
	}
	total, streak, last := m.Stats("recon")
	if total != 1000 || streak != 1000 || last != "nmap" {
		t.Fatalf("stats = (%d, %d, %q), want (1000, 1000, nmap)", total, streak, last)
	}
}

func TestMonitor_TotalCapTrips(t *testing.T) {
	m := NewMonitor(MonitorConfig{MaxTotalPerAgent: 3})
	if !m.Enabled() {
		t.Fatal("expected Enabled()")
	}
	for i := 0; i < 3; i++ {
		if err := m.Allow("recon", "nmap"); err != nil {
			t.Fatalf("Allow #%d returned %v", i, err)
		}
	}
	err := m.Allow("recon", "nmap")
	if !errors.Is(err, ErrToolBudgetExhausted) {
		t.Fatalf("want ErrToolBudgetExhausted, got %v", err)
	}
}

func TestMonitor_StreakResetsAcrossTools(t *testing.T) {
	m := NewMonitor(MonitorConfig{MaxSameToolStreak: 2})
	if err := m.Allow("recon", "nmap"); err != nil {
		t.Fatal(err)
	}
	if err := m.Allow("recon", "nmap"); err != nil {
		t.Fatal(err)
	}
	if err := m.Allow("recon", "httpx"); err != nil {
		t.Fatalf("switching tool should reset streak: %v", err)
	}
	// nmap, nmap, httpx → streak is 1 (for httpx). One more nmap resets
	// to 1 (a different tool's streak counter).
	if err := m.Allow("recon", "nmap"); err != nil {
		t.Fatal(err)
	}
	// nmap, nmap, httpx, nmap → "nmap" streak=1, "httpx" streak=1.
	total, _, _ := m.Stats("recon")
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
}

func TestMonitor_StreakCapTrips(t *testing.T) {
	m := NewMonitor(MonitorConfig{MaxSameToolStreak: 2})
	_ = m.Allow("recon", "nmap")
	_ = m.Allow("recon", "nmap")
	err := m.Allow("recon", "nmap")
	if !errors.Is(err, ErrToolBudgetExhausted) {
		t.Fatalf("want ErrToolBudgetExhausted, got %v", err)
	}
}

func TestMonitor_WindowCapTrips(t *testing.T) {
	m := NewMonitor(MonitorConfig{MaxPerWindow: 3, Window: 100 * time.Millisecond})
	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := m.Allow("recon", "nmap"); err != nil {
			t.Fatalf("Allow #%d returned %v", i, err)
		}
	}
	err := m.Allow("recon", "nmap")
	if !errors.Is(err, ErrToolBudgetExhausted) {
		t.Fatalf("want ErrToolBudgetExhausted, got %v", err)
	}
	// After the window passes, calls should be allowed again.
	time.Sleep(150 * time.Millisecond)
	if err := m.Allow("recon", "nmap"); err != nil {
		t.Fatalf("post-window Allow returned %v", err)
	}
	_ = now // not used; kept to document that we don't depend on a fake clock here
}

func TestMonitor_Concurrent(t *testing.T) {
	m := NewMonitor(MonitorConfig{MaxTotalPerAgent: 5000})
	var wg sync.WaitGroup
	var allowed int32
	var rejected int32
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				if err := m.Allow("recon", "nmap"); err != nil {
					atomic.AddInt32(&rejected, 1)
				} else {
					atomic.AddInt32(&allowed, 1)
				}
			}
		}()
	}
	wg.Wait()
	// 8000 calls, 5000 allowed, 3000 rejected.
	if got := atomic.LoadInt32(&allowed); got != 5000 {
		t.Fatalf("allowed = %d, want 5000", got)
	}
	if got := atomic.LoadInt32(&rejected); got != 3000 {
		t.Fatalf("rejected = %d, want 3000", got)
	}
	total, _, _ := m.Stats("recon")
	if total != 5000 {
		t.Fatalf("total tracked = %d, want 5000 (only allowed calls are counted)", total)
	}
}

func TestMonitor_PerAgentIsolation(t *testing.T) {
	m := NewMonitor(MonitorConfig{MaxTotalPerAgent: 2})
	_ = m.Allow("recon", "nmap")
	_ = m.Allow("recon", "nmap")
	if err := m.Allow("recon", "nmap"); !errors.Is(err, ErrToolBudgetExhausted) {
		t.Fatalf("recon should be capped: %v", err)
	}
	// A different agent must still have full budget.
	if err := m.Allow("exploit", "sqlmap"); err != nil {
		t.Fatalf("exploit should be unaffected: %v", err)
	}
}
