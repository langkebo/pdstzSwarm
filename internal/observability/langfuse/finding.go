package langfuse

import (
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
)

// FindingObserver turns blackboard writes into LangFuse "event"
// events so the UI can correlate findings with the spans that
// produced them.
//
// Construction is trivial — no goroutines, no lifecycle. Callers
// pass the observer to FindingsHook (in internal/api) and Forget
// it after campaign shutdown.
type FindingObserver struct {
	cli *Client
}

// NewFindingObserver returns a FindingObserver. cli may be
// disabled; the observer degrades to a noop.
func NewFindingObserver(cli *Client) *FindingObserver {
	return &FindingObserver{cli: cli}
}

// OnFinding is the callback a blackboard subscriber should invoke
// for each new finding. It emits a LangFuse event-create event
// carrying the finding summary; full finding content stays in the
// blackboard (LangFuse is for trace context, not for storing all
// the data).
//
// The blackboard's Finding carries a JSON Data blob whose shape
// depends on FindingType. We never log the full Data (it can
// contain payloads, exfiltrated secrets, etc.) — we just record
// its size and the top-level type.
func (f *FindingObserver) OnFinding(finding blackboard.Finding) {
	if f == nil || !f.cli.Enabled() {
		return
	}
	id := newID("evt")
	f.cli.Enqueue(Event{
		ID:        id,
		Timestamp: f.cli.now(),
		Type:      string(BodyEventCreate),
		Body: Body{
			Type:  BodyEventCreate,
			ID:    id,
			Name:  "finding." + string(finding.Type),
			Level: typeToLevel(finding.Type),
			Input: map[string]any{
				"finding_id":   finding.ID,
				"campaign_id":  finding.CampaignID,
				"agent":        finding.AgentName,
				"type":         finding.Type,
				"target":       finding.Target,
				"data_bytes":   len(finding.Data),
				"pheromone":    finding.PheromoneBase,
				"half_life":    finding.HalfLifeSec,
				"created_at":   finding.CreatedAt.Format(time.RFC3339),
			},
			Output: truncateForLog(string(finding.Data), 500),
		},
	})
}

// typeToLevel maps FindingType to a LangFuse log level so the UI
// can filter for high-signal findings.
func typeToLevel(t blackboard.FindingType) string {
	switch t {
	case blackboard.TypeExploitChain, blackboard.TypeExploitResult,
		blackboard.TypeSecretLeak, blackboard.TypePotentialSQLI,
		blackboard.TypeMisconfig, blackboard.TypeAgentError:
		return "WARNING"
	case blackboard.TypeCVEMatch, blackboard.TypeCVSSScore:
		return "WARNING"
	default:
		return "DEFAULT"
	}
}

// truncateForLog caps a free-form string to a fixed byte length
// to keep the LangFuse payload bounded. The cut is mid-byte
// safe for ASCII; multi-byte text gets re-truncated to the last
// rune boundary so we never emit invalid UTF-8.
func truncateForLog(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	out := s[:maxBytes]
	// Walk back to a rune boundary. The first byte that has the
	// high bit clear is the start of a rune. If we cut into the
	// middle of a multi-byte rune, the loop backs up.
	for i := len(out) - 1; i > 0; i-- {
		if out[i]&0xC0 != 0x80 { // 10xxxxxx is a continuation byte
			out = out[:i]
			break
		}
	}
	return out + "…"
}
