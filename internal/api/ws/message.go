// Package ws — message envelope for the live campaign stream.
//
// The legacy API published a single event type (pipeline.CampaignEvent)
// and the frontend derived severity by substring matching the detail
// field. That doesn't survive structured findings from the new
// stigmergy blackboard (blackboard.Finding), so the message format is
// upgraded to a tagged union:
//
//	{ "kind": "event",       "event":       <CampaignEvent> }
//	{ "kind": "finding",     "finding":     <Finding>      }
//	{ "kind": "user_input",  "user_input":  <UserMessage>  }
//
// The discriminator is the "kind" field. Frontend code branches on it
// to route the payload to the right store slice. Using a discriminated
// union (not a parallel "finding" boolean) keeps the wire format
// forward-compatible: future message kinds (e.g. "agent_state") just
// add a new case without breaking old clients.
//
// The "user_input" kind (P5+ 用户输入闭环) carries a UserMessage
// payload — operator guidance injected into a running campaign via
// POST /api/v1/campaigns/:id/input. The frontend renders these in
// both the chat-style input dock and the xterm event stream so the
// operator can see their own message interleaved with agent output.
package ws

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// MessageKind enumerates the values of Message.Kind.
type MessageKind string

const (
	KindEvent     MessageKind = "event"
	KindFinding   MessageKind = "finding"
	KindUserInput MessageKind = "user_input"
	KindSnapshot  MessageKind = "snapshot"
)

// UserMessage is a single operator-issued message injected into a
// running campaign. Persisted on CampaignState.UserInputs so the
// input dock can replay history on reconnect, and broadcast via the
// WebSocket hub so all live subscribers see it in real time.
//
// Author is the operator username (or "anonymous" for unauthenticated
// builds). The AI swarm can opt to subscribe to user_input findings
// via the blackboard to react to operator guidance — for now this is
// audit-only on the Go side, with the dashboard's input dock doing
// the human-visible rendering.
type UserMessage struct {
	ID         uuid.UUID `json:"id"`
	CampaignID uuid.UUID `json:"campaign_id"`
	Author     string    `json:"author"`
	Text       string    `json:"text"`
	Timestamp  time.Time `json:"timestamp"`
}

// Message is the envelope published by EventHub. Exactly one of
// Event, Finding, UserInput, or Events is set; the others are left at
// their zero values.
type Message struct {
	Kind      MessageKind              `json:"kind"`
	Event     *pipeline.CampaignEvent  `json:"event,omitempty"`
	Finding   *blackboard.Finding      `json:"finding,omitempty"`
	UserInput *UserMessage             `json:"user_input,omitempty"`
	Events    []pipeline.CampaignEvent `json:"events,omitempty"`
}

// MarshalBinary implements encoding.BinaryMarshaler so the hub's
// existing WriteMessage call continues to work.
func (m Message) MarshalBinary() ([]byte, error) { return json.Marshal(m) }

// ErrUnknownMessage is returned by UnmarshalMessage when the kind
// discriminator is missing or not recognized. Callers should drop the
// message and log — never panic.
var ErrUnknownMessage = errors.New("ws: unknown message kind")

// UnmarshalMessage decodes a wire-format byte slice into a Message.
// The inverse of MarshalBinary. The user_input kind is decoded into
// the UserInput field; legacy clients that lack the field silently
// drop messages of that kind.
func UnmarshalMessage(b []byte) (Message, error) {
	var probe struct {
		Kind MessageKind `json:"kind"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return Message{}, err
	}
	switch probe.Kind {
	case KindEvent:
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			return Message{}, err
		}
		return m, nil
	case KindFinding:
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			return Message{}, err
		}
		return m, nil
	case KindUserInput:
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			return Message{}, err
		}
		return m, nil
	case KindSnapshot:
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			return Message{}, err
		}
		return m, nil
	default:
		return Message{}, ErrUnknownMessage
	}
}
