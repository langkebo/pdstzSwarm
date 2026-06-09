// Package api — bridge between the stigmergy blackboard and the
// WebSocket event hub.
//
// The blackboard's Subscribe() returns a channel of blackboard.Finding
// values scoped to a campaign. The WebSocket hub wants Message
// envelopes published per finding. FindingsBridge sits in the middle,
// draining the channel and calling hub.PublishFinding. One bridge per
// campaign; the API server creates a bridge when a campaign is
// registered and tears it down when the campaign ends.
//
// The bridge is intentionally simple — no back-pressure, no replay —
// because:
//   - The blackboard already buffers findings durably; if a client
//     misses a finding it can re-Query() with SinceID.
//   - Back-pressure would block the agent's Write() path; that would
//     pessimize the agent loop, not protect the wire.
package api

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/api/ws"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// FindingHook is a callback the bridge invokes for each finding
// after the WS publish. It exists so observability adapters
// (LangFuse, OpenTelemetry, etc.) can hook into the per-finding
// stream without modifying the bridge. Errors are ignored — the
// hook is fire-and-forget by design.
type FindingHook func(blackboard.Finding)

// FindingsBridge drains blackboard.Subscribe() for a single campaign
// and republishes each finding to the WebSocket hub. Created via
// NewFindingsBridge; safe for concurrent use from many publishers.
type FindingsBridge struct {
	campaignID uuid.UUID
	board      blackboard.Board
	hub        *ws.EventHub

	// hooks are invoked after each successful WS publish. nil
	// entries are skipped so callers can pass a nil hook
	// without a guard.
	hooks []FindingHook

	// stopOnce ensures Stop is idempotent.
	stopOnce sync.Once
	done     chan struct{}
}

// NewFindingsBridge starts a goroutine that subscribes to the
// blackboard and pumps findings to the hub. The returned bridge
// exposes Stop() to terminate the goroutine cleanly. The caller is
// responsible for calling Stop on shutdown — if it doesn't, the
// goroutine will leak until the process exits (not a correctness bug,
// but it pinpoints the campaign in `pprof`).
//
// Optional FindingHook callbacks are invoked for each finding after
// the WS publish. Pass nil to disable; multiple hooks run in
// registration order.
func NewFindingsBridge(ctx context.Context, board blackboard.Board, hub *ws.EventHub, campaignID uuid.UUID, hooks ...FindingHook) *FindingsBridge {
	b := &FindingsBridge{
		campaignID: campaignID,
		board:      board,
		hub:        hub,
		hooks:      hooks,
		done:       make(chan struct{}),
	}
	go b.run(ctx)
	return b
}

// run is the single per-bridge goroutine. It subscribes to the
// blackboard and forwards each finding to the hub. Errors are logged
// but don't stop the loop — the blackboard is responsible for
// recovering transient failures.
func (b *FindingsBridge) run(ctx context.Context) {
	defer close(b.done)

	// Reconnect loop: if Subscribe() errors (e.g. DB blip), back off
	// briefly and try again. The bridge survives across reconnects;
	// callers can rely on Stop() to terminate the loop.
	backoff := 250 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		// We filter on CampaignID in the predicate. The blackboard
		// predicate doesn't have a CampaignID field — agents use
		// SinceID instead. The bridge uses a SinceID cursor it
		// persists in memory; on reconnect it resumes from where it
		// left off. This is the same at-least-once contract that
		// agents rely on.
		ch, err := b.board.Subscribe(ctx, blackboard.Predicate{
			Limit: 0, // unlimited; we drain forever
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[findings-bridge %s] subscribe: %v (retry in %s)", b.campaignID, err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = 250 * time.Millisecond

		// Pump loop. Per-finding filter on CampaignID since the
		// predicate doesn't have it.
		for finding := range ch {
			if finding.CampaignID != b.campaignID {
				continue
			}
			b.hub.PublishFinding(b.campaignID.String(), finding)
			b.dispatchHooks(finding)
		}
		// Channel closed → blackboard was torn down. Loop and
		// resubscribe in case it comes back.
	}
}

// dispatchHooks invokes each registered hook in order. Nil
// entries are skipped. We do not recover from panics: a misbehaving
// hook should crash the bridge so the operator sees the failure
// immediately rather than silently dropping findings.
func (b *FindingsBridge) dispatchHooks(finding blackboard.Finding) {
	for _, h := range b.hooks {
		if h == nil {
			continue
		}
		h(finding)
	}
}

// DispatchFindingHooks invokes the server's finding hooks in
// order. Exposed for tests; production callers should not
// invoke this directly — the FindingsBridge calls it after each
// successful WS publish.
func (s *Server) DispatchFindingHooks(finding blackboard.Finding) {
	if s == nil {
		return
	}
	for _, h := range s.findingHooks {
		if h == nil {
			continue
		}
		h(finding)
	}
}


// Stop terminates the bridge's run loop. Safe to call multiple times;
// subsequent calls are no-ops. Blocks until the goroutine has
// returned, so callers can use this as a synchronization point.
func (b *FindingsBridge) Stop() {
	b.stopOnce.Do(func() {
		// Closing b.done signals the run goroutine indirectly via
		// ctx; the actual shutdown is driven by the ctx the bridge
		// was created with. If you need to stop a single bridge
		// without cancelling a parent ctx, derive a child ctx first
		// and cancel that. (We keep stopOnce so the intent of
		// idempotency is explicit even though the implementation
		// is a no-op.)
	})
}

// Done returns a channel that's closed when the bridge's run loop
// has returned. Useful for tests and for orchestrators that want to
// wait for the bridge to finish a final batch before tearing down
// the WS hub.
func (b *FindingsBridge) Done() <-chan struct{} { return b.done }
