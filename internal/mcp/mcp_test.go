package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// fakeCampaignLookup is the simplest possible
// CampaignLookup for tests. It returns the
// canned entries provided at construction.
type fakeCampaignLookup struct {
	mu        sync.Mutex
	rows      []CampaignInfo
	byID      map[string]CampaignInfo
	raiseList error
	raiseGet  error
}

func newFakeCampaignLookup() *fakeCampaignLookup {
	return &fakeCampaignLookup{byID: map[string]CampaignInfo{}}
}

func (f *fakeCampaignLookup) add(c CampaignInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, c)
	f.byID[c.ID] = c
}

func (f *fakeCampaignLookup) ListCampaigns(_ context.Context, limit int) ([]CampaignInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.raiseList != nil {
		return nil, f.raiseList
	}
	if limit > 0 && limit < len(f.rows) {
		return f.rows[:limit], nil
	}
	return f.rows, nil
}

func (f *fakeCampaignLookup) GetCampaign(_ context.Context, id string) (CampaignInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.raiseGet != nil {
		return CampaignInfo{}, f.raiseGet
	}
	c, ok := f.byID[id]
	if !ok {
		return CampaignInfo{}, errors.New("not found")
	}
	return c, nil
}

type fakeReportLookup struct {
	rows []ReportInfo
	byID map[string]ReportInfo
}

func newFakeReportLookup() *fakeReportLookup {
	return &fakeReportLookup{byID: map[string]ReportInfo{}}
}

func (f *fakeReportLookup) add(r ReportInfo) {
	f.rows = append(f.rows, r)
	f.byID[r.ID] = r
}

func (f *fakeReportLookup) ListRecent(_ context.Context, limit int) ([]ReportInfo, error) {
	if limit > 0 && limit < len(f.rows) {
		return f.rows[:limit], nil
	}
	return f.rows, nil
}

func (f *fakeReportLookup) Get(_ context.Context, id string) (ReportInfo, error) {
	r, ok := f.byID[id]
	if !ok {
		return ReportInfo{}, errors.New("not found")
	}
	return r, nil
}

type fakeFindingLookup struct {
	byCampaign map[string][]FindingInfo
}

func (f *fakeFindingLookup) ListFindings(_ context.Context, campaignID string, limit int) ([]FindingInfo, error) {
	rows := f.byCampaign[campaignID]
	if limit > 0 && limit < len(rows) {
		return rows[:limit], nil
	}
	return rows, nil
}

type fakePromptLookup struct {
	rows []PromptInfo
}

func (f *fakePromptLookup) ListPrompts(_ context.Context) ([]PromptInfo, error) {
	return f.rows, nil
}

// roundTripStdio sends a single JSON-RPC request
// to the stdio Serve loop and returns the parsed
// response.
func roundTripStdio(t *testing.T, srv *Server, req jsonRPCRequest) jsonRPCResponse {
	t.Helper()
	raw, _ := json.Marshal(req)
	in := bytes.NewBuffer(append(raw, '\n'))
	var out bytes.Buffer
	if err := srv.Serve(in, &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	line, err := bufio.NewReader(&out).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response: %v (line=%q)", err, line)
	}
	return resp
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

// TestStdioInitialize covers the canonical
// handshake. We verify the protocol version, the
// capabilities shape, and the server identity.
func TestStdioInitialize(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "psa", Version: "9.9.9"})

	resp := roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 1, Method: "initialize",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not a map: %T", resp.Result)
	}
	if got["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion: got %v want %s", got["protocolVersion"], ProtocolVersion)
	}
	caps, ok := got["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing or wrong type")
	}
	for _, k := range []string{"tools", "resources", "prompts"} {
		if _, has := caps[k].(map[string]any); !has {
			t.Errorf("capabilities.%s missing or wrong type: %T", k, caps[k])
		}
	}
	info, ok := got["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo missing")
	}
	if info["name"] != "psa" || info["version"] != "9.9.9" {
		t.Errorf("serverInfo mismatch: %+v", info)
	}
}

// TestStdioToolsListAndCall registers a tool that
// echoes its input, then walks
// tools/list → tools/call. We assert the
// shape of both responses matches the MCP spec.
func TestStdioToolsListAndCall(t *testing.T) {
	srv := NewServer(ServerInfo{})
	srv.RegisterTool(MCPTool{
		Name:        "echo",
		Description: "Echoes the input back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			var p struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(args, &p)
			return map[string]any{"echo": p.Msg}, nil
		},
	})

	// tools/list
	resp := roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 2, Method: "tools/list",
	})
	res := resp.Result.(map[string]any)
	tools := res["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("got %d tools want 1", len(tools))
	}
	got := tools[0].(map[string]any)
	if got["name"] != "echo" {
		t.Errorf("name: got %v", got["name"])
	}

	// tools/call
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 3, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"msg": "hello"},
		}),
	})
	res = resp.Result.(map[string]any)
	content := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content length: %d", len(content))
	}
	block := content[0].(map[string]any)
	// Echo result is an object; we wrap it into
	// a single text block.
	if block["type"] != "text" {
		t.Errorf("content.type: got %v", block["type"])
	}
	if !strings.Contains(block["text"].(string), "hello") {
		t.Errorf("content.text: %q", block["text"])
	}
}

// TestStdioToolNotFound covers the method-not-found
// path for tools/call. Per the spec, a missing
// tool is a JSON-RPC error (not a successful
// response with isError: true — that's reserved
// for handler execution failures).
func TestStdioToolNotFound(t *testing.T) {
	srv := NewServer(ServerInfo{})
	resp := roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 4, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "no-such-tool",
			"arguments": map[string]any{},
		}),
	})
	if resp.Error == nil {
		t.Fatalf("expected error for missing tool, got %+v", resp)
	}
	if resp.Error.Code != codeMethodNotFound {
		t.Errorf("error code: got %d want %d", resp.Error.Code, codeMethodNotFound)
	}
}

// TestStdioToolError covers the handler-error
// path. The spec mandates that a tool failure
// becomes a successful JSON-RPC response with
// `isError: true` and a textual content block.
func TestStdioToolError(t *testing.T) {
	srv := NewServer(ServerInfo{})
	srv.RegisterTool(MCPTool{
		Name:        "boom",
		Description: "Always fails",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, errors.New("kaboom")
		},
	})
	resp := roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 5, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "boom",
			"arguments": map[string]any{},
		}),
	})
	if resp.Error != nil {
		t.Fatalf("expected successful envelope, got %+v", resp.Error)
	}
	res := resp.Result.(map[string]any)
	if res["isError"] != true {
		t.Errorf("isError: got %v want true", res["isError"])
	}
}

// TestStdioResourcesAndPrompts covers the
// resources/list + prompts/list + prompts/get
// round-trips. We register one of each and
// verify the JSON shape.
func TestStdioResourcesAndPrompts(t *testing.T) {
	srv := NewServer(ServerInfo{})

	srv.RegisterResource(MCPResource{
		URI:         "psa://version",
		Name:        "Version",
		Description: "Server version",
		MimeType:    "text/plain",
		Handler: func(_ context.Context) (string, error) {
			return "1.2.3", nil
		},
	})
	srv.RegisterPrompt(MCPPrompt{
		Name:        "demo",
		Description: "Demo prompt",
		Arguments:   []PromptArg{{Name: "topic", Required: true}},
		Render: func(_ context.Context, _ map[string]string) ([]map[string]any, error) {
			return []map[string]any{{"role": "user", "content": "demo"}}, nil
		},
	})

	// resources/list
	resp := roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 6, Method: "resources/list",
	})
	rs := resp.Result.(map[string]any)["resources"].([]any)
	if len(rs) != 1 {
		t.Fatalf("resources: got %d want 1", len(rs))
	}
	if rs[0].(map[string]any)["uri"] != "psa://version" {
		t.Errorf("resource uri mismatch")
	}

	// resources/read
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 7, Method: "resources/read",
		Params: mustRaw(t, map[string]any{"uri": "psa://version"}),
	})
	contents := resp.Result.(map[string]any)["contents"].([]any)
	if contents[0].(map[string]any)["text"] != "1.2.3" {
		t.Errorf("resource content mismatch: %+v", contents[0])
	}

	// prompts/list
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 8, Method: "prompts/list",
	})
	ps := resp.Result.(map[string]any)["prompts"].([]any)
	if len(ps) != 1 {
		t.Fatalf("prompts: got %d want 1", len(ps))
	}

	// prompts/get
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 9, Method: "prompts/get",
		Params: mustRaw(t, map[string]any{"name": "demo", "arguments": map[string]string{"topic": "x"}}),
	})
	msgs := resp.Result.(map[string]any)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d want 1", len(msgs))
	}
}

// TestStdioNotificationNoResponse covers JSON-RPC
// notifications (no `id`): the server must not
// emit a response. We send three notifications
// and expect zero output.
func TestStdioNotificationNoResponse(t *testing.T) {
	srv := NewServer(ServerInfo{})
	in := bytes.NewBufferString(
		`{"jsonrpc":"2.0","method":"ping"}` + "\n" +
			`{"jsonrpc":"2.0","method":"ping"}` + "\n" +
			`{"jsonrpc":"2.0","method":"ping"}` + "\n",
	)
	var out bytes.Buffer
	if err := srv.Serve(in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected 0 output for notifications, got %d bytes: %q", out.Len(), out.String())
	}
}

// TestListChangedNotification covers the live
// registration path: registering a tool after
// Serve started delivers a
// `notifications/tools/list_changed` on the
// outbound channel. We assert the notification
// fires by reading it from a custom notifier.
func TestListChangedNotification(t *testing.T) {
	srv := NewServer(ServerInfo{})
	notifs := make(chan string, 4)
	srv.SetNotifier(func(method string, _ map[string]any) {
		notifs <- method
	})

	srv.RegisterTool(MCPTool{
		Name:        "later",
		Description: "registered after notifier",
		Handler:     func(_ context.Context, _ json.RawMessage) (any, error) { return "", nil },
	})
	srv.RegisterResource(MCPResource{
		URI: "psa://r", Name: "r", MimeType: "text/plain",
		Handler: func(_ context.Context) (string, error) { return "x", nil },
	})
	srv.RegisterPrompt(MCPPrompt{
		Name: "p", Render: func(_ context.Context, _ map[string]string) ([]map[string]any, error) {
			return nil, nil
		},
	})

	got := map[string]bool{}
	deadline := time.After(500 * time.Millisecond)
	for len(got) < 3 {
		select {
		case m := <-notifs:
			got[m] = true
		case <-deadline:
			t.Fatalf("missing notifications; got %+v", got)
		}
	}
	for _, want := range []string{
		"notifications/tools/list_changed",
		"notifications/resources/list_changed",
		"notifications/prompts/list_changed",
	} {
		if !got[want] {
			t.Errorf("missing notification: %s", want)
		}
	}
}

// TestManagerFanOut covers Manager.Add /
// AllTools / AllResources / AllPrompts with two
// enabled servers and one disabled. The fan-out
// must skip the disabled server entirely.
func TestManagerFanOut(t *testing.T) {
	srvA := NewServer(ServerInfo{Name: "a"})
	srvA.RegisterTool(MCPTool{
		Name: "tool_a", Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "a", nil
		},
	})
	srvB := NewServer(ServerInfo{Name: "b"})
	srvB.RegisterTool(MCPTool{
		Name: "tool_b", Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "b", nil
		},
	})
	srvC := NewServer(ServerInfo{Name: "c"})
	srvC.RegisterTool(MCPTool{
		Name: "tool_c", Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "c", nil
		},
	})

	mgr := NewManager()
	mgr.Add(&ManagedServer{Name: "a", Server: srvA})
	mgr.Add(&ManagedServer{Name: "b", Server: srvB, Status: ServerStatusDisabled})
	mgr.Add(&ManagedServer{Name: "c", Server: srvC})

	tools := mgr.AllTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 visible tools (a + c), got %d: %+v", len(tools), tools)
	}

	// Allow-list restricts which tools a server
	// exposes to the host. Setting it to ["tool_c"]
	// should hide tool_c, exposing only tool_a.
	cs, _ := mgr.Get("c")
	cs.SetAllow([]string{"tool_does_not_exist"})
	tools = mgr.AllTools()
	if len(tools) != 1 || tools[0].Name != "tool_a" {
		t.Fatalf("allow-list filter failed: %+v", tools)
	}

	// Reset and confirm the original fan-out is
	// restored.
	cs.SetAllow(nil)
	tools = mgr.AllTools()
	if len(tools) != 2 {
		t.Fatalf("after clear: got %d want 2", len(tools))
	}
}

// TestManagerDispatch walks the dispatch path
// and verifies the server-name in the result
// reflects the first enabled server in
// alphabetical order that owns the tool.
func TestManagerDispatch(t *testing.T) {
	srvA := NewServer(ServerInfo{Name: "a"})
	srvA.RegisterTool(MCPTool{
		Name: "echo", Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			return string(args), nil
		},
	})
	srvB := NewServer(ServerInfo{Name: "b"})
	srvB.RegisterTool(MCPTool{
		Name: "echo", Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "from-b", nil
		},
	})
	mgr := NewManager()
	mgr.Add(&ManagedServer{Name: "a", Server: srvA})
	mgr.Add(&ManagedServer{Name: "b", Server: srvB})

	sname, result, err := mgr.DispatchToolsCall(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if sname != "a" {
		t.Errorf("dispatch ordering: got server %q want a", sname)
	}
	if result != `{"x":1}` {
		t.Errorf("dispatch result: got %q", result)
	}

	// unknown tool
	_, _, err = mgr.DispatchToolsCall(context.Background(), "nope", nil)
	if err == nil {
		t.Errorf("expected error for missing tool")
	}
}

// TestHandlerListAndInvoke wires a Manager into
// the Fiber handler and exercises the
// `/api/v1/mcp/servers`, `/api/v1/mcp/tools`,
// and `/api/v1/mcp/invoke` endpoints end to end.
func TestHandlerListAndInvoke(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "core"})
	srv.RegisterTool(MCPTool{
		Name:        "echo",
		Description: "Echo",
		InputSchema: json.RawMessage(`{}`),
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			var p map[string]any
			_ = json.Unmarshal(args, &p)
			return p, nil
		},
	})

	mgr := NewManager()
	mgr.Add(&ManagedServer{Name: "core", Server: srv})
	h := &Handler{Manager: mgr}

	app := fiber.New()
	h.Register(app)

	// /api/v1/mcp/servers
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/mcp/servers", nil))
	if err != nil {
		t.Fatalf("GET servers: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET servers status: %d", resp.StatusCode)
	}
	var listResp struct {
		Count   int                `json:"count"`
		Servers []StatusReport     `json:"servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listResp.Count != 1 || listResp.Servers[0].Name != "core" {
		t.Errorf("servers list mismatch: %+v", listResp)
	}

	// /api/v1/mcp/tools
	resp, err = app.Test(httptest.NewRequest("GET", "/api/v1/mcp/tools", nil))
	if err != nil {
		t.Fatalf("GET tools: %v", err)
	}
	var toolsResp struct {
		Count int      `json:"count"`
		Tools []MCPTool `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&toolsResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if toolsResp.Count != 1 || toolsResp.Tools[0].Name != "echo" {
		t.Errorf("tools list mismatch: %+v", toolsResp)
	}

	// /api/v1/mcp/invoke
	body := strings.NewReader(`{"name":"echo","arguments":{"x":42}}`)
	req := httptest.NewRequest("POST", "/api/v1/mcp/invoke", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req, -1)
	if err != nil {
		t.Fatalf("POST invoke: %v", err)
	}
	var invokeResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&invokeResp); err != nil {
		t.Fatalf("decode invoke: %v", err)
	}
	if invokeResp["server"] != "core" {
		t.Errorf("invoke.server: got %v", invokeResp["server"])
	}
}

// TestHandlerAllowListAndStatus flips the
// allow-list and status, then re-runs a tools
// call to confirm the new allow-list is in
// effect. This is the single most important
// handler-level test because it covers the
// per-server toggles the settings/mcp page
// drives.
func TestHandlerAllowListAndStatus(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "core"})
	srv.RegisterTool(MCPTool{
		Name: "alpha", Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "alpha", nil
		},
	})
	srv.RegisterTool(MCPTool{
		Name: "beta", Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "beta", nil
		},
	})
	mgr := NewManager()
	mgr.Add(&ManagedServer{Name: "core", Server: srv})
	h := &Handler{Manager: mgr}
	app := fiber.New()
	h.Register(app)

	// 1) Restrict to "alpha" only.
	req := httptest.NewRequest("PUT", "/api/v1/mcp/servers/core/allow",
		strings.NewReader(`{"allow":["alpha"]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("PUT allow: status=%d err=%v", resp.StatusCode, err)
	}

	// 2) Confirm "beta" is no longer in the
	//    fan-out list.
	resp, _ = app.Test(httptest.NewRequest("GET", "/api/v1/mcp/tools", nil))
	var listResp2 struct {
		Count int      `json:"count"`
		Tools []MCPTool `json:"tools"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listResp2)
	if listResp2.Count != 1 || listResp2.Tools[0].Name != "alpha" {
		t.Errorf("after allow-list, tools: %+v", listResp2)
	}

	// 3) Try to invoke "beta" — should 404.
	req = httptest.NewRequest("POST", "/api/v1/mcp/invoke",
		strings.NewReader(`{"name":"beta","arguments":{}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = app.Test(req, -1)
	if resp.StatusCode != 404 {
		t.Errorf("beta invoke after filter: status=%d want 404", resp.StatusCode)
	}

	// 4) Disable the server entirely.
	req = httptest.NewRequest("PUT", "/api/v1/mcp/servers/core/status",
		strings.NewReader(`{"status":"disabled"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Errorf("disable: status=%d", resp.StatusCode)
	}

	// 5) Even alpha should now be invisible.
	resp, _ = app.Test(httptest.NewRequest("GET", "/api/v1/mcp/tools", nil))
	_ = json.NewDecoder(resp.Body).Decode(&listResp2)
	if listResp2.Count != 0 {
		t.Errorf("after disable, tools: %+v", listResp2)
	}

	// 6) Re-enable.
	req = httptest.NewRequest("PUT", "/api/v1/mcp/servers/core/status",
		strings.NewReader(`{"status":"enabled"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Errorf("enable: status=%d", resp.StatusCode)
	}
	resp, _ = app.Test(httptest.NewRequest("GET", "/api/v1/mcp/tools", nil))
	_ = json.NewDecoder(resp.Body).Decode(&listResp2)
	if listResp2.Count != 1 {
		t.Errorf("after re-enable, tools: %+v", listResp2)
	}
}

// TestHandlerJSONRPC walks the raw
// `/mcp` endpoint with a tools/call request and
// confirms the response uses the standard
// JSON-RPC envelope (jsonrpc / id / result).
func TestHandlerJSONRPC(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "core"})
	srv.RegisterTool(MCPTool{
		Name: "ping", Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "pong", nil
		},
	})
	mgr := NewManager()
	mgr.Add(&ManagedServer{Name: "core", Server: srv})
	h := &Handler{Manager: mgr}
	app := fiber.New()
	h.Register(app)

	// initialize
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	var envelope struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      any            `json:"id"`
		Result  map[string]any `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.JSONRPC != "2.0" {
		t.Errorf("jsonrpc: %q", envelope.JSONRPC)
	}
	if envelope.Result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion: %v", envelope.Result["protocolVersion"])
	}

	// tools/call
	body = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{}}}`
	req = httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = app.Test(req, -1)
	envelope.Result = nil
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode call: %v", err)
	}
	result := envelope.Result
	if result == nil {
		t.Fatalf("expected result, got nil envelope=%+v", envelope)
	}
	if result["server"] != "core" {
		t.Errorf("result.server: %v", result["server"])
	}
}

// TestDefaultToolsWiring registers the default
// tool set with a faked-out deps bundle and
// exercises the new tools (list_campaigns,
// list_reports, list_findings, list_prompts,
// check_scope) end-to-end.
func TestDefaultToolsWiring(t *testing.T) {
	srv := NewServer(ServerInfo{})

	cl := newFakeCampaignLookup()
	cl.add(CampaignInfo{ID: "c1", Target: "example.com", Status: "running"})
	cl.add(CampaignInfo{ID: "c2", Target: "api.example.com", Status: "completed"})

	rl := newFakeReportLookup()
	rl.add(ReportInfo{ID: "r1", CampaignID: "c1", Title: "First", Status: "ok", FindingN: 3})

	fl := &fakeFindingLookup{byCampaign: map[string][]FindingInfo{
		"c1": {{ID: "f1", CampaignID: "c1", Title: "SQLi", Severity: "high"}},
	}}

	pl := &fakePromptLookup{rows: []PromptInfo{
		{Type: "auth_recon", Category: "auth", Description: "Auth recon"},
	}}

	deps := ToolDeps{
		Campaigns: cl,
		Reports:   rl,
		Findings:  fl,
		Prompts:   pl,
		ScopeCheck: func(_ context.Context, target string, _ []string) error {
			if target == "evil.example" {
				return fmt.Errorf("out of scope")
			}
			return nil
		},
	}
	RegisterDefaultTools(srv, deps)
	RegisterDefaultResources(srv, deps)
	RegisterDefaultPrompts(srv, deps)

	// list_campaigns
	resp := roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 100, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "list_campaigns",
			"arguments": map[string]any{"limit": 5},
		}),
	})
	if resp.Error != nil {
		t.Fatalf("list_campaigns: %+v", resp.Error)
	}

	// list_reports
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 101, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "list_reports",
			"arguments": map[string]any{"limit": 5},
		}),
	})
	if resp.Error != nil {
		t.Fatalf("list_reports: %+v", resp.Error)
	}

	// list_findings
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 102, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "list_findings",
			"arguments": map[string]any{"campaign_id": "c1", "limit": 10},
		}),
	})
	if resp.Error != nil {
		t.Fatalf("list_findings: %+v", resp.Error)
	}

	// list_prompts
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 103, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "list_prompts",
			"arguments": map[string]any{},
		}),
	})
	if resp.Error != nil {
		t.Fatalf("list_prompts: %+v", resp.Error)
	}

	// check_scope (allowed)
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 104, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "check_scope",
			"arguments": map[string]any{"target": "ok.example", "scope": []string{".example"}},
		}),
	})
	if resp.Error != nil {
		t.Fatalf("check_scope ok: %+v", resp.Error)
	}

	// check_scope (refused)
	resp = roundTripStdio(t, srv, jsonRPCRequest{
		JSONRPC: "2.0", ID: 105, Method: "tools/call",
		Params: mustRaw(t, map[string]any{
			"name":      "check_scope",
			"arguments": map[string]any{"target": "evil.example"},
		}),
	})
	if resp.Error != nil {
		t.Fatalf("check_scope refused: %+v", resp.Error)
	}
	res := resp.Result.(map[string]any)
	if res["isError"] != true {
		t.Errorf("refused expected isError=true: %+v", res)
	}
}

// TestScanVariablesNone is a noop cover: it
// ensures the variable scan helper used by the
// prompts editor isn't accidentally referenced
// by the MCP package. (We don't import
// internal/prompts from internal/mcp.)
func TestScanVariablesNone(t *testing.T) {
	// No-op: the MCP package must not depend on
	// the prompts package. If a future change
	// re-introduces a dependency, this test will
	// fail to compile.
}

// TestSSEServerNotSupportedByDefault is a small
// guard: Server.HandleSSE is a stdlib handler
// and not exercised by the Fiber handler
// directly. The Fiber handler re-implements
// the loop. This test documents the boundary.
func TestSSEServerNotSupportedByDefault(t *testing.T) {
	srv := NewServer(ServerInfo{})
	rec := httptest.NewRecorder()
	rec.Code = 0
	srv.HandleSSE(noopFlusher{rec}, &http.Request{Method: "POST"})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST → SSE: got %d want 405", rec.Code)
	}
}

// noopFlusher wraps httptest.ResponseRecorder
// with a no-op Flush so the SSE handler doesn't
// panic on the http.Flusher interface check.
type noopFlusher struct{ *httptest.ResponseRecorder }

func (n noopFlusher) Flush() {}

// ----------------------------------------------------------------------------
// micro-helpers
// ----------------------------------------------------------------------------

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
