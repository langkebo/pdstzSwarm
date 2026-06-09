package ws

import (
	"encoding/json"
	"sync"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/appmetrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/fasthttp/websocket"
)

// EventHub manages WebSocket connections per campaign for real-time
// event streaming.
//
// The hub is message-agnostic: callers use Publish (legacy event) or
// PublishFinding (new blackboard finding). Both end up on the wire as
// the same Message envelope so subscribers receive a single ordered
// stream.
type EventHub struct {
	mu    sync.RWMutex
	conns map[string][]*websocket.Conn // campaignID -> connections

	// writeMu serializes concurrent WriteMessage calls across all
	// connections. fasthttp/websocket doesn't allow concurrent writes
	// on the same connection; this protects the per-conn invariant when
	// many publishers (legacy + finding) race.
	writeMu sync.Mutex

	// metrics is the optional observability bundle. When non-nil
	// (the common path under cmd/serve) Subscribe/Unsubscribe/Publish
	// record the corresponding psa_ws_active_connections /
	// psa_ws_messages_total / psa_ws_broadcast_errors_total
	// counters/gauges. When nil the hub is metric-free (zero
	// overhead) — used in unit tests that don't pull in
	// observability.
	//
	// The hub intentionally does NOT call metrics.NewRegistry() or
	// appmetrics.New() itself: the registry is process-wide state
	// owned by the API server (one global instance), and the hub
	// only borrows references.
	metrics *appmetrics.All
}

// NewEventHub creates a new event hub. The metrics bundle is
// optional — pass nil for tests / metric-free builds.
func NewEventHub(m *appmetrics.All) *EventHub {
	return &EventHub{
		conns:   make(map[string][]*websocket.Conn),
		metrics: m,
	}
}

// Subscribe registers a WebSocket connection for a campaign.
func (h *EventHub) Subscribe(campaignID string, conn *websocket.Conn) {
	h.mu.Lock()
	h.conns[campaignID] = append(h.conns[campaignID], conn)
	count := len(h.conns[campaignID])
	h.mu.Unlock()

	if h.metrics != nil {
		h.metrics.WSConnections.Set(
			metrics.Labels{"campaign_id": campaignID},
			float64(count),
		)
	}
}

// Unsubscribe removes a WebSocket connection.
func (h *EventHub) Unsubscribe(campaignID string, conn *websocket.Conn) {
	h.mu.Lock()
	conns := h.conns[campaignID]
	for i, c := range conns {
		if c == conn {
			h.conns[campaignID] = append(conns[:i], conns[i+1:]...)
			break
		}
	}
	if len(h.conns[campaignID]) == 0 {
		delete(h.conns, campaignID)
	}
	count := len(h.conns[campaignID])
	h.mu.Unlock()

	if h.metrics != nil {
		if count == 0 {
			// Drop the series to keep /metrics output tidy: a
			// campaign with zero subscribers shouldn't occupy a
			// line forever.
			h.metrics.WSConnections.Set(metrics.Labels{"campaign_id": campaignID}, 0)
		} else {
			h.metrics.WSConnections.Set(
				metrics.Labels{"campaign_id": campaignID},
				float64(count),
			)
		}
	}
}

// Publish sends a legacy CampaignEvent to all WebSocket subscribers.
// Kept for backwards compatibility with the 5-phase runner path; new
// code should call PublishFinding instead.
func (h *EventHub) Publish(campaignID string, event pipeline.CampaignEvent) {
	h.broadcast(campaignID, Message{Kind: KindEvent, Event: &event}, "event")
}

// PublishFinding sends a blackboard Finding to all WebSocket
// subscribers. The finding is wrapped in a Message envelope with
// Kind="finding" so the frontend can route it to the findings slice.
func (h *EventHub) PublishFinding(campaignID string, f blackboard.Finding) {
	h.broadcast(campaignID, Message{Kind: KindFinding, Finding: &f}, "finding")
}

// PublishUserInput broadcasts an operator-issued UserMessage to all
// WebSocket subscribers. P5+ 用户输入闭环. The dashboard renders
// these in the chat-style input dock and the xterm event stream so
// the operator can see their own message interleaved with agent
// output. Author is preserved so multi-tenant / shared-dashboard
// scenarios can attribute messages correctly.
func (h *EventHub) PublishUserInput(campaignID string, u UserMessage) {
	h.broadcast(campaignID, Message{Kind: KindUserInput, UserInput: &u}, "user_input")
}

// broadcast marshals the envelope and writes it to every subscriber.
// Uses a snapshot of the connection list (RLock) to avoid holding the
// read lock during I/O. The kind label ("event" / "finding" / ...)
// is fed into psa_ws_messages_total and psa_ws_broadcast_errors_total
// so a Prometheus query can break down traffic by message type.
func (h *EventHub) broadcast(campaignID string, msg Message, kind string) {
	h.mu.RLock()
	conns := append([]*websocket.Conn(nil), h.conns[campaignID]...)
	h.mu.RUnlock()

	if len(conns) == 0 {
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		// Should never happen for our well-typed envelope; log and bail.
		return
	}

	var errCount float64
	for _, conn := range conns {
		// writeMu serializes per-connection writes; the lock itself is
		// global but acquired only briefly.
		h.writeMu.Lock()
		writeErr := conn.WriteMessage(websocket.TextMessage, data)
		h.writeMu.Unlock()
		if writeErr != nil {
			// Connection dead — will be cleaned up on next Unsubscribe
			conn.Close()
			errCount++
		}
	}

	if h.metrics != nil {
		h.metrics.WSMessages.Add(metrics.Labels{"kind": kind}, float64(len(conns)))
		if errCount > 0 {
			h.metrics.WSBroadcastErr.Add(
				metrics.Labels{"campaign_id": campaignID, "kind": kind},
				errCount,
			)
		}
	}
}

// SubscriberCount returns the number of active subscribers for a campaign.
func (h *EventHub) SubscriberCount(campaignID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns[campaignID])
}

// WriteMessage writes a single message to a connection. It uses the
// hub's writeMu to serialize concurrent writes (fasthttp/websocket
// requires serialized writes per connection).
func (h *EventHub) WriteMessage(conn *websocket.Conn, msgType int, data []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	return conn.WriteMessage(msgType, data)
}
