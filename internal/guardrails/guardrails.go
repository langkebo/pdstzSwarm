// Package guardrails implements the commercial execution guardrails
// for the docker tool runner. P2 商业化护栏.
//
// Why a separate package: P2-1 A/B/C established the **technical**
// guardrails (image whitelist, cgroup limits, network/rootfs
// isolation). P2 adds the **commercial** guardrails on top:
//
//   - HostNetworkAuditor: detects when a Request asks for
//     NetworkMode="host" — a high-trust posture that bypasses the
//     container's netns. We never block it (it's a feature, not a
//     bug), but every use is recorded as a LangFuse event and a
//     structured-log WARN so operators can audit it post-hoc.
//   - QuotaGuard: per-(tool, agent) call counters with an
//     auto-downgrade policy. When a tool exceeds its call budget
//     in a campaign, subsequent calls have their Isolation
//     posture downgraded (e.g. network=bridge → network=none,
//     read-write rootfs → read-only). The goal is graceful
//     degradation under misbehaving agents rather than a hard
//     block: a tool that hits its quota still runs, just safer.
//   - EventReporter: a single sink that fans events out to
//     LangFuse (when configured) and a slog.Logger (always).
//     Operators get one log line per event regardless of whether
//     LangFuse is wired up.
//
// All three components are concurrency-safe. A single Guardrails
// instance is shared by every container run within a campaign
// (the docker.Runner takes a *Guardrails in its Config).
//
// The package is intentionally pluggable: a nil *Guardrails
// disables the entire layer, and any sub-component (Auditor /
// QuotaGuard / Reporter) can be replaced or augmented by callers
// that need different behavior.
package guardrails

import (
	"log/slog"
	"sync"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/langfuse"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools/docker"
)

// EventKind enumerates the kinds of events the guardrail layer
// emits. The string values are stable — they go into LangFuse
// event names and into the structured log — so adding a new kind
// is a deliberate, non-breaking act.
type EventKind string

const (
	// EventHostNetworkUsed: a Request asked for network="host".
	// Always a warning — host network bypasses the netns
	// isolation the rest of the system relies on. Operators
	// audit this to detect over-broad tooling or compromised
	// agent prompts.
	EventHostNetworkUsed EventKind = "host_network_used"

	// EventQuotaExceeded: a tool crossed its call budget. The
	// downgrade policy was applied (or attempted). Operators
	// can correlate "agent stuck on the same tool" with this
	// event in the LangFuse trace.
	EventQuotaExceeded EventKind = "quota_exceeded"

	// EventIsolationDowngraded: a Request's effective isolation
	// was downgraded from what the caller asked for. Always
	// paired with a reason (host_network, quota, etc.) in the
	// event's metadata.
	EventIsolationDowngraded EventKind = "isolation_downgraded"
)

// Event is the unit the guardrail layer reports. We intentionally
// keep it small and JSON-friendly — every field lands in
// LangFuse's event-create body and in the slog line.
//
// Tool / Agent / Target are the natural correlation keys: they
// let an operator pivot from "this LangFuse event" to "every call
// this tool made" or "every event this agent triggered".
type Event struct {
	// Kind is the discriminator — see EventKind constants.
	Kind EventKind

	// Tool is the name of the tool that triggered the event
	// (e.g. "nmap", "sqlmap", "httpx"). Empty if the event
	// is not tool-scoped.
	Tool string

	// Agent is the agent name that issued the call (e.g.
	// "recon", "exploit"). Empty if not agent-scoped.
	Agent string

	// Target is the in-scope target the tool was pointed at.
	// We log it so audit trails are actionable; the operator
	// can immediately see "we used host network against
	// 10.0.0.5" rather than having to join on a separate
	// span log.
	Target string

	// Reason is a short human-readable description of why
	// the event fired. We deliberately keep it short so it
	// fits in a single log line and shows up cleanly in
	// LangFuse's UI; full diagnostic detail goes in Metadata.
	Reason string

	// Metadata is the catch-all for structured detail that
	// doesn't fit in Reason. We use map[string]any so the
	// LangFuse bridge can pass it through verbatim, and so
	// tests can assert on it without coupling to a schema.
	Metadata map[string]any

	// Time is when the event was generated. We accept an
	// injected clock via Guardrails.Now; default is time.Now.
	Time time.Time
}

// Guardrails is the top-level orchestrator. A Runner takes one
// of these in its Config and calls AuditAndDowngrade before every
// Run(); the returned Request is the (possibly mutated) one to
// actually execute.
//
// The zero value is a noop. To get behavior, populate the
// sub-components and pass a non-nil Logger / LangFuse client.
type Guardrails struct {
	// Auditor detects risky posture (currently: host network).
	// nil = skip the audit step entirely.
	Auditor *HostNetworkAuditor

	// Quota counts calls and applies downgrade policy.
	// nil = no quota tracking.
	Quota *QuotaGuard

	// Reporter fans events out to LangFuse and the logger.
	// nil = noop reporter; events are dropped silently. We
	// never want a missing reporter to block a tool call.
	Reporter EventReporter

	// Now is the clock. Override in tests; default time.Now.
	Now func() time.Time
}

// EventReporter is the interface both the real LangFuse-backed
// reporter and the test fake implement. We keep it small on
// purpose: a guardrail event is "report this" or "drop it" —
// there is no ack/nack.
type EventReporter interface {
	// Report submits an event for delivery. Implementations
	// must be safe for concurrent use and must NOT block
	// the caller (LangFuse ingestion is async; slog is
	// already async-ish). Returning an error is allowed
	// but the call site currently ignores it.
	Report(ev Event)
}

// LangFuseReporter is the production reporter. It emits
// event-create events to LangFuse AND logs the same event to
// the provided slog.Logger at WARN level. The dual-write is
// deliberate: LangFuse gives us the dashboard / correlation,
// slog gives us the immutable on-disk audit trail (LangFuse
// may be down, on a different network, or simply not
// configured in dev).
type LangFuseReporter struct {
	cli    *langfuse.Client
	logger *slog.Logger
	once   sync.Once
}

// NewLangFuseReporter returns a Reporter that writes to both
// LangFuse (when cli is non-nil and enabled) and a slog.Logger
// (always, when logger is non-nil). Either sink being nil
// short-circuits just that sink — the other still fires.
func NewLangFuseReporter(cli *langfuse.Client, logger *slog.Logger) *LangFuseReporter {
	return &LangFuseReporter{cli: cli, logger: logger}
}

// Report implements EventReporter.
func (r *LangFuseReporter) Report(ev Event) {
	if r == nil {
		return
	}
	// Slog path — always on when logger is set. We log at
	// WARN for any guardrail event because the layer only
	// fires on policy violations / risky posture. INFO would
	// be misleading.
	if r.logger != nil {
		r.logger.Warn("guardrail event",
			slog.String("kind", string(ev.Kind)),
			slog.String("tool", ev.Tool),
			slog.String("agent", ev.Agent),
			slog.String("target", ev.Target),
			slog.String("reason", ev.Reason),
			slog.Any("metadata", ev.Metadata),
			slog.Time("time", ev.Time),
		)
	}
	// LangFuse path — only when client is configured. We
	// use the same "event-create" payload as
	// FindingObserver, with the event name being
	// "guardrail.<kind>".
	if r.cli != nil && r.cli.Enabled() {
		r.cli.Enqueue(langfuse.Event{
			ID:        newID(),
			Timestamp: ev.Time,
			Type:      string(langfuse.BodyEventCreate),
			Body: langfuse.Body{
				Type:    langfuse.BodyEventCreate,
				ID:      newID(),
				Name:    "guardrail." + string(ev.Kind),
				Level:   "WARNING",
				Input:   eventToInput(ev),
				Output:  ev.Reason,
				StartTime: ev.Time,
			},
		})
	}
}

// newID generates a short unique identifier for LangFuse
// event/span IDs. We avoid pulling in a UUID dep here —
// crypto/rand + 8 hex chars is sufficient (collision rate
// is negligible at our event volumes).
func newID() string {
	var b [8]byte
	// crypto/rand.Read can't fail under normal conditions;
	// the underlying reader is /dev/urandom on Linux. We
	// ignore the error because there's nothing useful to
	// do if the kernel's RNG is broken.
	_, _ = randRead(b[:])
	return "gr-" + hexEncode(b[:])
}

// eventToInput flattens an Event into the input map LangFuse
// stores. We deliberately do NOT pass full Metadata as a nested
// object — flat keys are easier to filter on in the LangFuse
// UI.
func eventToInput(ev Event) map[string]any {
	out := map[string]any{
		"kind":   string(ev.Kind),
		"tool":   ev.Tool,
		"agent":  ev.Agent,
		"target": ev.Target,
		"time":   ev.Time.Format(time.RFC3339Nano),
	}
	for k, v := range ev.Metadata {
		// Don't let a malicious metadata key shadow our
		// own fields. Tool/agent/target/kind are reserved.
		switch k {
		case "kind", "tool", "agent", "target", "time":
			continue
		}
		out[k] = v
	}
	return out
}

// AuditAndDowngrade is the single entry point the docker.Runner
// calls before every Run(). It walks the (tool, agent) tuple
// through the auditor and quota guard, collects any events,
// reports them, and returns a possibly-mutated Request whose
// Isolation posture reflects any downgrades applied.
//
// The returned events slice is the list of events generated
// during this call — useful for tests and for callers that
// want to forward the events elsewhere.
//
// Concurrency: safe; takes the QuotaGuard's mutex briefly.
func (g *Guardrails) AuditAndDowngrade(tool, agent, target string, req docker.Request) (docker.Request, []Event) {
	if g == nil {
		return req, nil
	}
	now := g.now()
	var events []Event

	// 1. Auditor pass — detect risky posture (host network).
	if g.Auditor != nil {
		if ev := g.Auditor.Inspect(req, tool, agent, target, now); ev != nil {
			events = append(events, *ev)
		}
	}

	// 2. Quota pass — count, then mutate req.Isolation if
	// the policy says downgrade. The mutated req is what
	// the docker.Runner ultimately builds the container
	// from, so the downgrade takes effect on this call.
	if g.Quota != nil {
		newIso, ev := g.Quota.Observe(req.Isolation, tool, agent, target, now)
		if ev != nil {
			events = append(events, *ev)
		}
		// Only allocate a new pointer when we actually
		// have a downgraded posture to set. Avoiding the
		// alloc is what keeps the hot path free when no
		// downgrade fires.
		if newIso != nil {
			req.Isolation = newIso
		}
	}

	// 3. Fan out the events. A nil Reporter is tolerated —
	// events drop silently. The Reporter implementations
	// are themselves nil-safe.
	if g.Reporter != nil && len(events) > 0 {
		for i := range events {
			g.Reporter.Report(events[i])
		}
	}

	return req, events
}

// now returns the configured clock or time.Now.
func (g *Guardrails) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// BeforeRun satisfies the docker.GuardrailHook interface.
// It is the adapter the docker.Runner calls; the rest of
// the package's API (AuditAndDowngrade) is unchanged.
//
// Returning only the Request — not the events — keeps the
// interface narrow and lets the docker package stay free of
// guardrail-specific types. Events are emitted via the
// configured Reporter as a side effect of this call.
func (g *Guardrails) BeforeRun(tool, agent, target string, req docker.Request) docker.Request {
	out, _ := g.AuditAndDowngrade(tool, agent, target, req)
	return out
}
