package api

import (
	"sync/atomic"
	"testing"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// TestWithFindingHook_AddsHook verifies the fluent API on Server
// accumulates hooks in registration order. Nil hooks are
// silently dropped (callers don't need a guard).
func TestWithFindingHook_AddsHook(t *testing.T) {
	s := &Server{}
	calls := atomic.Int32{}
	s.WithFindingHook(func(_ blackboard.Finding) { calls.Add(1) })
	s.WithFindingHook(nil) // must be ignored
	s.WithFindingHook(func(_ blackboard.Finding) { calls.Add(1) })

	if got := len(s.findingHooks); got != 2 {
		t.Errorf("findingHooks len = %d, want 2 (one nil should be dropped)", got)
	}

	// Replay a synthetic finding through DispatchFindingHooks.
	// With no nil deref in flight we should see exactly 2 invocations.
	s.DispatchFindingHooks(blackboard.Finding{ID: uuid.New()})
	if got := calls.Load(); got != 2 {
		t.Errorf("DispatchFindingHooks fired %d, want 2", got)
	}
}

// TestWithFindingHook_NilSafe covers the "caller is being
// defensive" cases: nil receiver and nil hook arg must not
// panic.
func TestWithFindingHook_NilSafe(t *testing.T) {
	var s *Server
	if got := s.WithFindingHook(func(_ blackboard.Finding) {}); got != nil {
		t.Errorf("WithFindingHook on nil should stay nil")
	}
	s = &Server{}
	s.WithFindingHook(nil)
	if len(s.findingHooks) != 0 {
		t.Errorf("nil hook should be dropped, got %d", len(s.findingHooks))
	}
}

// TestDispatchHooks_FiresInOrder verifies hooks are called in
// the order they were registered. Order matters when one hook
// gates another (e.g. cost meter must run before trace export).
func TestDispatchHooks_FiresInOrder(t *testing.T) {
	s := &Server{}
	var seen []int
	for i := 0; i < 3; i++ {
		i := i
		s.WithFindingHook(func(_ blackboard.Finding) { seen = append(seen, i) })
	}
	s.DispatchFindingHooks(blackboard.Finding{})
	if len(seen) != 3 || seen[0] != 0 || seen[1] != 1 || seen[2] != 2 {
		t.Errorf("order = %v, want [0 1 2]", seen)
	}
}

// TestDispatchFindingHooks_NilReceiver makes sure the nil-safe
// path doesn't panic for defensive callers.
func TestDispatchFindingHooks_NilReceiver(t *testing.T) {
	var s *Server
	s.DispatchFindingHooks(blackboard.Finding{}) // must not panic
}
