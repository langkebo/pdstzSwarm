package ws

import (
	"encoding/json"
	"testing"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// BenchmarkMessage_MarshalFinding measures the per-finding cost of
// wrapping a blackboard.Finding in the Message envelope and
// serializing it. This is the cost the bridge pays on every published
// finding; we want it sub-microsecond.
func BenchmarkMessage_MarshalFinding(b *testing.B) {
	f := blackboard.Finding{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		AgentName:  "recon",
		Type:       blackboard.TypeHTTPEndpoint,
		Target:     "https://example.com/api",
		Data:       json.RawMessage(`{"method":"GET","status":200,"length":1024}`),
	}
	m := Message{Kind: KindFinding, Finding: &f}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(m)
	}
}

// BenchmarkMessage_MarshalEvent measures the legacy event cost.
func BenchmarkMessage_MarshalEvent(b *testing.B) {
	e := pipeline.CampaignEvent{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		EventType:  pipeline.EventThought,
		Detail:     "Considering 3 candidate exploits for /api/v1/users",
	}
	m := Message{Kind: KindEvent, Event: &e}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(m)
	}
}

// BenchmarkUnmarshalMessage_Finding measures the wire-decoding cost
// on the frontend (mirrored in Go for the server-side test pass).
func BenchmarkUnmarshalMessage_Finding(b *testing.B) {
	raw := []byte(`{"kind":"finding","finding":{"id":"0f6deb68-36f1-488e-9c3f-f710cca6ce10","campaign_id":"03643dc5-8584-4e97-b860-68ff90e452e0","agent_name":"recon","type":"HTTP_ENDPOINT","target":"https://x","data":"eyJtZXRob2QiOiJHRVQiLCJzdGF0dXMiOjIwMH0="}}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = UnmarshalMessage(raw)
	}
}
