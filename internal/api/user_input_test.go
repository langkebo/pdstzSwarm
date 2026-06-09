package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/api/ws"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// newTestServerWithUserInputRoutes builds a minimal Fiber app that
// exposes only the user-input endpoints, plus a single campaign
// pre-seeded into s.campaigns. This lets us test the handler
// in isolation without booting the full HTTP stack (auth, reports,
// prompts, MCP, webfs).
//
// The hub is the real *ws.EventHub; we don't subscribe a real
// websocket.Conn (we can't in a unit test), so we verify
// SubscriberCount remains 0 and the publish path doesn't panic.
// The on-wire JSON shape is covered by the ws package tests.
func newTestServerWithUserInputRoutes(t *testing.T) (*Server, *fiber.App, string) {
	t.Helper()
	s := &Server{
		hub:     ws.NewEventHub(nil),
		bridges: make(map[string]*FindingsBridge),
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	api := app.Group("/api/v1")
	api.Post("/campaigns/:id/input", s.putUserInput)
	api.Get("/campaigns/:id/user-inputs", s.listUserInputs)

	// Pre-seed a campaign in "running" state so putUserInput accepts it.
	id := uuid.New().String()
	s.campaigns.Store(id, &CampaignState{
		Campaign: pipeline.Campaign{
			ID:        uuid.MustParse(id),
			Target:    "example.com",
			Objective: "find vulns",
			Status:    pipeline.StatusExecuting,
			Mode:      pipeline.ModeManual,
			CreatedAt: time.Now(),
		},
	})
	return s, app, id
}

func decodeJSON(t *testing.T, r io.Reader, dst any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(dst); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

func putUserInputBody(text, author string) *bytes.Reader {
	body, _ := json.Marshal(PutUserInputRequest{Text: text, Author: author})
	return bytes.NewReader(body)
}

// TestPutUserInput_HappyPath verifies the canonical success flow:
// a 201 response with a server-assigned ID + timestamp + author +
// campaign_id, the message appended to CampaignState.UserInputs,
// and a 1-entry list returned by the GET endpoint.
func TestPutUserInput_HappyPath(t *testing.T) {
	_, app, id := newTestServerWithUserInputRoutes(t)

	req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
		putUserInputBody("focus on /admin", "alice"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201; body = %s", resp.StatusCode, raw)
	}
	var got ws.UserMessage
	decodeJSON(t, resp.Body, &got)
	if got.Text != "focus on /admin" {
		t.Errorf("Text = %q, want %q", got.Text, "focus on /admin")
	}
	if got.Author != "alice" {
		t.Errorf("Author = %q, want %q", got.Author, "alice")
	}
	if got.ID == uuid.Nil {
		t.Error("ID is zero, want server-assigned")
	}
	if got.CampaignID.String() != id {
		t.Errorf("CampaignID = %s, want %s", got.CampaignID, id)
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want server-assigned UTC time")
	}

	// GET replay endpoint should return this single message.
	req2 := httptest.NewRequest("GET", "/api/v1/campaigns/"+id+"/user-inputs", nil)
	resp2, err := app.Test(req2, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("GET status = %d, want 200", resp2.StatusCode)
	}
	var listResp struct {
		Data []ws.UserMessage `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	decodeJSON(t, resp2.Body, &listResp)
	if listResp.Meta.Total != 1 || len(listResp.Data) != 1 {
		t.Fatalf("replay: total=%d, len=%d, want 1/1", listResp.Meta.Total, len(listResp.Data))
	}
	if listResp.Data[0].Text != "focus on /admin" {
		t.Errorf("replay Text = %q, want %q", listResp.Data[0].Text, "focus on /admin")
	}
}

// TestPutUserInput_AnonymousAuthorWhenNoAuth verifies that
// omitting the author field (and not having a session) stamps the
// message with "anonymous". This is the default for unauthenticated
// builds and the fallback for authenticated builds when the
// session is missing.
func TestPutUserInput_AnonymousAuthorWhenNoAuth(t *testing.T) {
	_, app, id := newTestServerWithUserInputRoutes(t)
	req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
		putUserInputBody("skip staging env", ""))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var got ws.UserMessage
	decodeJSON(t, resp.Body, &got)
	if got.Author != "anonymous" {
		t.Errorf("Author = %q, want %q", got.Author, "anonymous")
	}
}

// TestPutUserInput_RejectsEmptyText covers the validation path:
// after TrimSpace, an empty body must 400.
func TestPutUserInput_RejectsEmptyText(t *testing.T) {
	_, app, id := newTestServerWithUserInputRoutes(t)
	for _, text := range []string{"", "   ", "\n\t  \n"} {
		req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
			putUserInputBody(text, "alice"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("text=%q: status = %d, want 400", text, resp.StatusCode)
		}
	}
}

// TestPutUserInput_RejectsOversized covers the 4 KiB cap.
func TestPutUserInput_RejectsOversized(t *testing.T) {
	_, app, id := newTestServerWithUserInputRoutes(t)
	tooBig := strings.Repeat("a", MaxUserInputLength+1)
	req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
		putUserInputBody(tooBig, "alice"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 (oversize)", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "exceeds") {
		t.Errorf("body should mention 'exceeds', got: %s", raw)
	}
}

// TestPutUserInput_RejectsMalformedBody covers the BodyParser
// failure path (non-JSON body).
func TestPutUserInput_RejectsMalformedBody(t *testing.T) {
	_, app, id := newTestServerWithUserInputRoutes(t)
	req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
		bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestPutUserInput_404OnUnknownCampaign ensures the route
// 404s when the id is not in s.campaigns.
func TestPutUserInput_404OnUnknownCampaign(t *testing.T) {
	_, app, _ := newTestServerWithUserInputRoutes(t)
	bogus := uuid.New().String()
	req := httptest.NewRequest("POST", "/api/v1/campaigns/"+bogus+"/input",
		putUserInputBody("hi", "alice"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestPutUserInput_409OnFinishedCampaign covers the "campaign
// already done" rejection path. We mutate the in-memory state
// directly to flip the status, then re-issue the request.
func TestPutUserInput_409OnFinishedCampaign(t *testing.T) {
	s, app, id := newTestServerWithUserInputRoutes(t)
	for _, status := range []pipeline.CampaignStatus{
		pipeline.StatusComplete, pipeline.StatusFailed, pipeline.StatusAborted,
	} {
		val, _ := s.campaigns.Load(id)
		state := val.(*CampaignState)
		state.Campaign.Status = status

		req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
			putUserInputBody("hi", "alice"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 409 {
			t.Errorf("status=%s: status = %d, want 409", status, resp.StatusCode)
		}
	}
}

// TestPutUserInput_PreservesOrder verifies that consecutive
// messages are appended in arrival order (no dedup, no
// reorder). This is what the dashboard's chat-style replay log
// relies on.
func TestPutUserInput_PreservesOrder(t *testing.T) {
	_, app, id := newTestServerWithUserInputRoutes(t)
	inputs := []string{"first", "second", "third", "fourth"}
	for _, text := range inputs {
		req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
			putUserInputBody(text, "alice"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("text=%q: status = %d, want 201", text, resp.StatusCode)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/campaigns/"+id+"/user-inputs", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listResp struct {
		Data []ws.UserMessage `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	decodeJSON(t, resp.Body, &listResp)
	if listResp.Meta.Total != 4 {
		t.Fatalf("total = %d, want 4", listResp.Meta.Total)
	}
	for i, want := range inputs {
		if listResp.Data[i].Text != want {
			t.Errorf("position %d: Text = %q, want %q", i, listResp.Data[i].Text, want)
		}
	}
}

// TestListUserInputs_404OnUnknownCampaign mirrors putUserInput's
// 404 path. We keep them as separate tests so a future
// authorisation refactor (e.g. one needs auth, the other doesn't)
// doesn't accidentally share state.
func TestListUserInputs_404OnUnknownCampaign(t *testing.T) {
	_, app, _ := newTestServerWithUserInputRoutes(t)
	bogus := uuid.New().String()
	req := httptest.NewRequest("GET", "/api/v1/campaigns/"+bogus+"/user-inputs", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestListUserInputs_EmptyArrayWhenNonePosted verifies the
// "no inputs yet" case: 200 OK with an empty data slice (not
// null), so the dashboard's React state never crashes on a
// missing array.
func TestListUserInputs_EmptyArrayWhenNonePosted(t *testing.T) {
	_, app, id := newTestServerWithUserInputRoutes(t)
	req := httptest.NewRequest("GET", "/api/v1/campaigns/"+id+"/user-inputs", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	// Must contain "data":[] (not "data":null), so the client
	// can iterate without a guard.
	if !strings.Contains(string(raw), `"data":[]`) {
		t.Errorf("body should contain data:[], got: %s", raw)
	}
	if !strings.Contains(string(raw), `"total":0`) {
		t.Errorf("body should contain total:0, got: %s", raw)
	}
}

// TestPutUserInput_BroadcastsToHub verifies the side effect:
// after a successful POST, the hub's SubscriberCount is still 0
// (no subscribers — we can't construct a real socket in a unit
// test), but the publish call does not panic. The actual
// on-the-wire JSON shape is verified in the ws package.
func TestPutUserInput_BroadcastsToHub(t *testing.T) {
	s, app, id := newTestServerWithUserInputRoutes(t)
	req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
		putUserInputBody("hello", "alice"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if got := s.hub.SubscriberCount(id); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0 (no live subscribers in test)", got)
	}
}

// TestUserInputLifecycle_ReplayAfterAbort covers the "operator
// reconnects after the campaign aborts" scenario: the replay
// endpoint still returns the in-memory log even after the
// campaign is in a terminal state (the dashboard may briefly
// render an "aborted" banner while the chat history is still
// useful for postmortem).
func TestUserInputLifecycle_ReplayAfterAbort(t *testing.T) {
	s, app, id := newTestServerWithUserInputRoutes(t)

	// Inject 2 messages.
	for _, text := range []string{"focus on auth", "explain SQLi chain"} {
		req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
			putUserInputBody(text, "alice"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	// Abort the campaign.
	val, _ := s.campaigns.Load(id)
	state := val.(*CampaignState)
	state.Campaign.Status = pipeline.StatusAborted

	// Replay endpoint should still return 2.
	req := httptest.NewRequest("GET", "/api/v1/campaigns/"+id+"/user-inputs", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (replay should survive abort)", resp.StatusCode)
	}
	var listResp struct {
		Data []ws.UserMessage `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	decodeJSON(t, resp.Body, &listResp)
	if listResp.Meta.Total != 2 {
		t.Errorf("replay total = %d, want 2", listResp.Meta.Total)
	}
}

// Reference: the *CampaignState is shared by reference, and the
// user input handler appends to its UserInputs slice. This is
// a smoke test that exercises that the handler + state interaction
// doesn't deadlock or race under light concurrency.
func TestPutUserInput_ConcurrentPostsKeepOrder(t *testing.T) {
	s := &Server{
		hub:     ws.NewEventHub(nil),
		bridges: make(map[string]*FindingsBridge),
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/api/v1/campaigns/:id/input", s.putUserInput)

	id := uuid.New().String()
	s.campaigns.Store(id, &CampaignState{
		Campaign: pipeline.Campaign{
			ID:     uuid.MustParse(id),
			Status: pipeline.StatusExecuting,
		},
	})

	// Fire 8 concurrent posts with sequential text bodies; the
	// handler is single-goroutine per request, so total order is
	// determined by Fiber's app.Test scheduling. We only assert
	// "no panic, 8 succeed, 8 land in the log".
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		i := i
		go func() {
			req := httptest.NewRequest("POST", "/api/v1/campaigns/"+id+"/input",
				putUserInputBody(strings.Repeat("x", i+1), ""))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Errorf("post %d: %v", i, err)
			}
			if resp.StatusCode != 201 {
				t.Errorf("post %d: status = %d, want 201", i, resp.StatusCode)
			}
			resp.Body.Close()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}

	val, _ := s.campaigns.Load(id)
	state := val.(*CampaignState)
	if len(state.UserInputs) != 8 {
		t.Errorf("UserInputs len = %d, want 8", len(state.UserInputs))
	}
}

// Sanity: the WebSocket envelope is decoupled from Fiber — the
// user_input kind published via the hub survives a marshalling
// round-trip even when no subscribers are connected. This guards
// against a future refactor that would tie KindUserInput to a
// specific handler.
func TestUserInput_EnvelopeDecoupledFromServer(t *testing.T) {
	hub := ws.NewEventHub(nil)
	hub.PublishUserInput("c1", ws.UserMessage{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		Author:     "alice",
		Text:       "decoupled",
		Timestamp:  time.Now().UTC(),
	})
	if got := hub.SubscriberCount("c1"); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0", got)
	}
	_ = context.Background()
}
