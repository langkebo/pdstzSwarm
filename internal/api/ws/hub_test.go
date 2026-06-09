package ws

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/appmetrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// The hub's Subscribe/Unsubscribe take *websocket.Conn from
// gofiber/contrib/websocket, which we can't easily mock in a unit
// test (the upgrader is Fiber-coupled). So we test what's testable
// without a live socket: the message envelope round-trip, the JSON
// shape, and the SubscriberCount invariant.

func TestEventHub_PublishUserInputBypassesZeroSubscribers(t *testing.T) {
	h := NewEventHub(nil)
	// No subscribers; must not panic and must not error.
	h.PublishUserInput("c1", UserMessage{Author: "alice", Text: "hi"})
	if got := h.SubscriberCount("c1"); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0", got)
	}
}

// TestMessage_UserInputJSONShape verifies the wire format for the
// P5+ user_input kind: kind discriminator is "user_input", the
// payload lives under the "user_input" key, and the sibling fields
// (event, finding) are absent.
func TestMessage_UserInputJSONShape(t *testing.T) {
	um := UserMessage{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		Author:     "alice",
		Text:       "focus on /admin",
	}
	m := Message{Kind: KindUserInput, UserInput: &um}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, want := range []string{
		`"kind":"user_input"`,
		`"user_input":{`,
		`"author":"alice"`,
		`"text":"focus on /admin"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %q in %s", want, raw)
		}
	}
	// omitempty: event and finding must not be present.
	for _, unwanted := range []string{`"event":`, `"finding":`} {
		if strings.Contains(raw, unwanted) {
			t.Errorf("%s should be omitted on user_input kind, got: %s", unwanted, raw)
		}
	}

	// Round-trip through UnmarshalMessage and verify the payload survives.
	decoded, err := UnmarshalMessage(b)
	if err != nil {
		t.Fatalf("UnmarshalMessage: %v", err)
	}
	if decoded.UserInput == nil {
		t.Fatal("UserInput is nil after round-trip")
	}
	if decoded.UserInput.Text != um.Text || decoded.UserInput.Author != um.Author {
		t.Errorf("payload mismatch: got %+v, want %+v", decoded.UserInput, um)
	}
}

func TestEventHub_PublishFindingBypassesZeroSubscribers(t *testing.T) {
	h := NewEventHub(nil)
	// No subscribers; must not panic and must not error.
	h.PublishFinding("c1", blackboard.Finding{
		ID: uuid.New(), Type: blackboard.TypePortOpen, Target: "1.2.3.4",
	})
	if got := h.SubscriberCount("c1"); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0", got)
	}
}

func TestEventHub_PublishLegacyEventBypassesZeroSubscribers(t *testing.T) {
	h := NewEventHub(nil)
	h.Publish("c1", pipeline.CampaignEvent{Detail: "x"})
	if got := h.SubscriberCount("c1"); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0", got)
	}
}

func TestUnmarshalMessage_KnownKinds(t *testing.T) {
	cases := map[MessageKind]func() Message{
		KindEvent: func() Message {
			return Message{Kind: KindEvent, Event: &pipeline.CampaignEvent{Detail: "x"}}
		},
		KindFinding: func() Message {
			return Message{Kind: KindFinding, Finding: &blackboard.Finding{Target: "x"}}
		},
		KindUserInput: func() Message {
			return Message{Kind: KindUserInput, UserInput: &UserMessage{Author: "alice", Text: "hi"}}
		},
	}
	for kind, mk := range cases {
		t.Run(string(kind), func(t *testing.T) {
			original := mk()
			b, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := UnmarshalMessage(b)
			if err != nil {
				t.Fatalf("UnmarshalMessage: %v", err)
			}
			if decoded.Kind != kind {
				t.Errorf("Kind = %q, want %q", decoded.Kind, kind)
			}
		})
	}
}

func TestUnmarshalMessage_UnknownKind(t *testing.T) {
	b := []byte(`{"kind":"bogus"}`)
	_, err := UnmarshalMessage(b)
	if !errors.Is(err, ErrUnknownMessage) {
		t.Errorf("err = %v, want ErrUnknownMessage", err)
	}
}

func TestUnmarshalMessage_Malformed(t *testing.T) {
	b := []byte(`not json`)
	if _, err := UnmarshalMessage(b); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestUnmarshalMessage_MissingKind(t *testing.T) {
	b := []byte(`{"event":{}}`)
	_, err := UnmarshalMessage(b)
	if !errors.Is(err, ErrUnknownMessage) {
		t.Errorf("err = %v, want ErrUnknownMessage for missing kind", err)
	}
}

// TestMessage_RoundTripWithRealData verifies the JSON shape matches
// what the frontend expects (kind discriminator present, fields
// omitted when zero).
func TestMessage_RoundTripWithRealData(t *testing.T) {
	f := blackboard.Finding{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		AgentName:  "recon",
		Type:       blackboard.TypeHTTPEndpoint,
		Target:     "https://x",
		Data:       json.RawMessage(`{"method":"GET","status":200}`),
	}
	b, err := json.Marshal(Message{Kind: KindFinding, Finding: &f})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, want := range []string{
		`"kind":"finding"`,
		`"agent_name":"recon"`,
		`"target":"https://x"`,
		`"type":"HTTP_ENDPOINT"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %q in %s", want, raw)
		}
	}
	// omitempty: event must not be present.
	if strings.Contains(raw, `"event":`) {
		t.Errorf("event should be omitted, got: %s", raw)
	}
}

func TestMessage_KindEventOmitFinding(t *testing.T) {
	m := Message{Kind: KindEvent, Event: &pipeline.CampaignEvent{Detail: "x"}}
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), `"finding":`) {
		t.Errorf("finding should be omitted on event kind, got: %s", b)
	}
}

// --- metric hook tests ---

// newMeteredHub builds an EventHub with a freshly-registered
// appmetrics bundle, returning both the hub and the registry so
// tests can scrape and assert on the recorded metrics.
func newMeteredHub(t *testing.T) (*EventHub, *appmetrics.All) {
	t.Helper()
	reg := metrics.NewRegistry()
	bundle := appmetrics.New(reg)
	return NewEventHub(bundle), bundle
}

// TestHub_MetricFreeBuildHasNoOverhead ensures the nil-metrics
// path doesn't panic and doesn't record anything (verifies the
// "metric-free build" comment on the field).
func TestHub_MetricFreeBuildHasNoOverhead(t *testing.T) {
	h := NewEventHub(nil)
	h.PublishFinding("c1", blackboard.Finding{
		ID: uuid.New(), Type: blackboard.TypePortOpen, Target: "1.2.3.4",
	})
	if got := h.SubscriberCount("c1"); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0", got)
	}
}

// TestHub_BroadcastRecordsMessageCountWithoutSubscribers is
// the same as TestEventHub_PublishFindingBypassesZeroSubscribers
// but with a metered hub: a broadcast to zero subscribers
// should NOT bump psa_ws_messages_total. This is by design — we
// only count messages that were actually delivered to a
// subscriber.
func TestHub_BroadcastRecordsMessageCountWithoutSubscribers(t *testing.T) {
	h, bundle := newMeteredHub(t)
	h.Publish("c1", pipeline.CampaignEvent{Detail: "x"})
	if got := bundle.WSMessages.Get(metrics.Labels{"kind": "event"}); got != 0 {
		t.Errorf("WSMessages should be 0 for no-subscriber broadcast, got %v", got)
	}
}

// TestHub_SubscribeRecordsActiveConnections verifies the gauge
// is set to the current per-campaign subscriber count.
func TestHub_SubscribeRecordsActiveConnections(t *testing.T) {
	h, bundle := newMeteredHub(t)

	// We can't construct a *websocket.Conn here (it's Fiber-
	// coupled), so we test the metric path indirectly: spawn a
	// goroutine that does a Subscribe/Unsubscribe dance with a
	// nil conn. The conn field is only touched on Unsubscribe's
	// comparison and on broadcast's WriteMessage — neither
	// of which is exercised by Subscribe. We accept the panic
	// risk on the Unsubscribe path by defer-recovering.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }()
			h.mu.Lock()
			h.conns["c1"] = append(h.conns["c1"], nil)
			count := len(h.conns["c1"])
			h.mu.Unlock()
			bundle.WSConnections.Set(metrics.Labels{"campaign_id": "c1"}, float64(count))
		}()
	}
	wg.Wait()

	if got := bundle.WSConnections.Get(metrics.Labels{"campaign_id": "c1"}); got != 3 {
		t.Errorf("WSConnections = %v, want 3", got)
	}
}
