// Package mcp implements the Model Context Protocol (MCP) server
// for exposing pentestswarm capabilities to AI clients such as
// Claude Desktop, Cursor, and any MCP-compatible runtime.
//
// Two transports are supported:
//
//   - stdio (default): line-delimited JSON-RPC 2.0 over the
//     process's stdin/stdout. Used by Claude Desktop / Cursor by
//     pointing their MCP config at the `pentestswarm mcp serve`
//     command.
//
//   - http / sse: HTTP-based, with a plain JSON-RPC POST
//     endpoint plus a Server-Sent-Events stream for server-
//     pushed notifications. Useful for remote or multi-tenant
//     deployments where the MCP client cannot spawn the server
//     process directly.
//
// The Server struct is transport-agnostic: handleRequest is
// the protocol core, and Serve / HandlePOST / HandleSSE are
// thin I/O adapters. This keeps the protocol logic
// unit-testable without spinning up a real JSON-RPC stream.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ProtocolVersion is the MCP spec version this server
// advertises in its `initialize` response. The
// 2024-11-05 release is the first one Claude Desktop
// implemented end-to-end and is the de-facto
// common-denominator we target.
const ProtocolVersion = "2024-11-05"

// ServerInfo describes the running server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Notifier is the callback transports use to push
// `notifications/*` events to the client. It's set per
// transport (stdio outbound channel, SSE event-stream) and
// reset to nil on teardown.
type Notifier func(method string, params map[string]any)

// Server is the protocol-agnostic MCP server core. The
// underlying I/O is supplied by Serve (stdio) or
// HandlePOST / HandleSSE (HTTP). Tools, resources, and
// prompts are registered on a single Server; multi-server
// fan-out is handled by Manager (see manager.go).
type Server struct {
	info ServerInfo

	mu        sync.RWMutex
	tools     map[string]*MCPTool
	resources map[string]*MCPResource
	prompts   map[string]*MCPPrompt

	// notifierMu guards notifier + notifyEnabled.
	notifierMu     sync.RWMutex
	notifier       Notifier
	notifyEnabled  bool
}

// MCPTool is a callable tool exposed to the MCP client.
// The JSON tags mirror the MCP spec's `tools/list` shape.
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`

	// Handler is invoked when an MCP client calls this tool.
	// Implementations are expected to be safe for concurrent
	// calls; the Server does not serialise tool invocations.
	Handler func(ctx context.Context, args json.RawMessage) (any, error)
}

// MCPResource is a read-only resource exposed to the MCP client.
type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
	Handler     func(ctx context.Context) (string, error)
}

// MCPPrompt is a reusable prompt template exposed to the
// MCP client. Following the spec, prompts carry an
// `arguments` schema (name + description + required) and
// a render function that produces the message list.
type MCPPrompt struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Arguments   []PromptArg    `json:"arguments,omitempty"`
	Render      func(ctx context.Context, args map[string]string) ([]map[string]any, error)
}

// PromptArg describes a single argument accepted by an
// MCPPrompt. Matches the MCP `prompts/get` argument shape.
type PromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}

// NewServer constructs a Server with the given identity
// (name + version). The returned Server has no tools,
// resources, or prompts; register them before calling
// Serve.
func NewServer(info ServerInfo) *Server {
	if info.Name == "" {
		info.Name = "pentestswarm"
	}
	if info.Version == "" {
		info.Version = "1.0.0"
	}
	return &Server{
		info:          info,
		tools:         make(map[string]*MCPTool),
		resources:     make(map[string]*MCPResource),
		prompts:       make(map[string]*MCPPrompt),
		notifyEnabled: true,
	}
}

// RegisterTool adds (or replaces) a tool by name. The
// tool becomes visible in subsequent `tools/list`
// responses and invokable via `tools/call`.
func (s *Server) RegisterTool(t MCPTool) {
	s.mu.Lock()
	s.tools[t.Name] = &t
	s.mu.Unlock()
	s.notifyListChanged("tools")
}

// RegisterResource adds (or replaces) a resource by URI.
func (s *Server) RegisterResource(r MCPResource) {
	s.mu.Lock()
	s.resources[r.URI] = &r
	s.mu.Unlock()
	s.notifyListChanged("resources")
}

// RegisterPrompt adds (or replaces) a prompt template by
// name.
func (s *Server) RegisterPrompt(p MCPPrompt) {
	s.mu.Lock()
	s.prompts[p.Name] = &p
	s.mu.Unlock()
	s.notifyListChanged("prompts")
}

// SetNotifyEnabled toggles whether the server emits
// `notifications/*` messages to its outbound transport.
// Used by the HTTP handler to silence the per-request
// notifier (it has no place to deliver events).
func (s *Server) SetNotifyEnabled(enabled bool) {
	s.notifierMu.Lock()
	s.notifyEnabled = enabled
	s.notifierMu.Unlock()
}

// SetNotifier installs the transport's outbound channel
// for server-pushed notifications. nil clears it.
func (s *Server) SetNotifier(n Notifier) {
	s.notifierMu.Lock()
	s.notifier = n
	s.notifierMu.Unlock()
}

// Tools returns a snapshot of the registered tools. The
// returned slice is a copy and safe to mutate.
func (s *Server) Tools() []MCPTool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MCPTool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, *t)
	}
	return out
}

// Resources returns a snapshot of the registered resources.
func (s *Server) Resources() []MCPResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MCPResource, 0, len(s.resources))
	for _, r := range s.resources {
		out = append(out, *r)
	}
	return out
}

// Prompts returns a snapshot of the registered prompts.
func (s *Server) Prompts() []MCPPrompt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MCPPrompt, 0, len(s.prompts))
	for _, p := range s.prompts {
		out = append(out, *p)
	}
	return out
}

// ServerInfo returns the running server's identity.
func (s *Server) ServerInfo() ServerInfo { return s.info }

func (s *Server) notifyListChanged(kind string) {
	s.notifierMu.RLock()
	enabled := s.notifyEnabled
	n := s.notifier
	s.notifierMu.RUnlock()
	if !enabled || n == nil {
		return
	}
	n("notifications/"+kind+"/list_changed", map[string]any{})
}

// ----------------------------------------------------------------------------
// JSON-RPC 2.0 plumbing
// ----------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Result  any            `json:"result,omitempty"`
	Error   *jsonRPCError  `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCNotification struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// Standard JSON-RPC error codes. See
// https://www.jsonrpc.org/specification#error_object
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// handleRequest dispatches a parsed JSON-RPC request to
// the matching method handler. The returned values
// (result, rpcErr) are written back by the caller.
func (s *Server) handleRequest(ctx context.Context, req jsonRPCRequest) (any, *jsonRPCError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)

	case "ping":
		// MCP 2024-11-05 keeps a top-level `ping` for liveness;
		// return an empty map so the client knows we're alive.
		return map[string]any{}, nil

	case "tools/list":
		return s.handleToolsList(req)

	case "tools/call":
		return s.handleToolsCall(ctx, req)

	case "resources/list":
		return s.handleResourcesList(req)

	case "resources/read":
		return s.handleResourcesRead(ctx, req)

	case "prompts/list":
		return s.handlePromptsList(req)

	case "prompts/get":
		return s.handlePromptsGet(ctx, req)

	default:
		return nil, &jsonRPCError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("Method %q not found", req.Method),
		}
	}
}

func (s *Server) handleInitialize(req jsonRPCRequest) (any, *jsonRPCError) {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools":     map[string]any{"listChanged": true},
			"resources": map[string]any{"subscribe": false, "listChanged": true},
			"prompts":   map[string]any{"listChanged": true},
		},
		"serverInfo": map[string]any{
			"name":    s.info.Name,
			"version": s.info.Version,
		},
	}, nil
}

func (s *Server) handleToolsList(req jsonRPCRequest) (any, *jsonRPCError) {
	tools := s.Tools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": json.RawMessage(t.InputSchema),
		})
	}
	return map[string]any{"tools": out}, nil
}

func (s *Server) handleToolsCall(ctx context.Context, req jsonRPCRequest) (any, *jsonRPCError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, &jsonRPCError{Code: codeInvalidParams, Message: "Invalid params: " + err.Error()}
	}

	s.mu.RLock()
	tool, ok := s.tools[params.Name]
	s.mu.RUnlock()
	if !ok {
		return nil, &jsonRPCError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("Tool %q not found", params.Name),
		}
	}

	result, err := tool.Handler(ctx, params.Arguments)
	if err != nil {
		// Tool failure is reported back to the client as a
		// successful JSON-RPC response with `isError: true`
		// and a textual content block, NOT as a JSON-RPC
		// error. This matches the MCP spec and lets the
		// agent see the failure message in its tool-result
		// history.
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Error: " + err.Error()},
			},
			"isError": true,
		}, nil
	}

	// Coerce the handler's result into the MCP content-array
	// shape. Strings become a single text block; everything
	// else is JSON-encoded into one text block so structured
	// results survive the round trip without a custom mime.
	return map[string]any{
		"content": []map[string]any{contentBlock(result)},
	}, nil
}

func (s *Server) handleResourcesList(req jsonRPCRequest) (any, *jsonRPCError) {
	resources := s.Resources()
	out := make([]map[string]any, 0, len(resources))
	for _, r := range resources {
		out = append(out, map[string]any{
			"uri":         r.URI,
			"name":        r.Name,
			"description": r.Description,
			"mimeType":    r.MimeType,
		})
	}
	return map[string]any{"resources": out}, nil
}

func (s *Server) handleResourcesRead(ctx context.Context, req jsonRPCRequest) (any, *jsonRPCError) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, &jsonRPCError{Code: codeInvalidParams, Message: "Invalid params: " + err.Error()}
	}
	s.mu.RLock()
	r, ok := s.resources[params.URI]
	s.mu.RUnlock()
	if !ok {
		return nil, &jsonRPCError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("Resource %q not found", params.URI),
		}
	}
	content, err := r.Handler(ctx)
	if err != nil {
		return nil, &jsonRPCError{Code: codeInternalError, Message: err.Error()}
	}
	return map[string]any{
		"contents": []map[string]any{
			{"uri": params.URI, "mimeType": r.MimeType, "text": content},
		},
	}, nil
}

func (s *Server) handlePromptsList(req jsonRPCRequest) (any, *jsonRPCError) {
	prompts := s.Prompts()
	out := make([]map[string]any, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, map[string]any{
			"name":        p.Name,
			"description": p.Description,
			"arguments":   p.Arguments,
		})
	}
	return map[string]any{"prompts": out}, nil
}

func (s *Server) handlePromptsGet(ctx context.Context, req jsonRPCRequest) (any, *jsonRPCError) {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, &jsonRPCError{Code: codeInvalidParams, Message: "Invalid params: " + err.Error()}
	}
	s.mu.RLock()
	p, ok := s.prompts[params.Name]
	s.mu.RUnlock()
	if !ok {
		return nil, &jsonRPCError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("Prompt %q not found", params.Name),
		}
	}
	msgs, err := p.Render(ctx, params.Arguments)
	if err != nil {
		return nil, &jsonRPCError{Code: codeInternalError, Message: err.Error()}
	}
	return map[string]any{
		"description": p.Description,
		"messages":    msgs,
	}, nil
}

// contentBlock turns an arbitrary handler result into the
// MCP content-array element shape. Strings become text
// blocks; everything else is JSON-encoded into a single
// text block so structured results survive the round trip
// without a custom mime type.
func contentBlock(v any) map[string]any {
	switch x := v.(type) {
	case string:
		return map[string]any{"type": "text", "text": x}
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return map[string]any{"type": "text", "text": fmt.Sprintf("marshal error: %v", err)}
		}
		return map[string]any{"type": "text", "text": string(b)}
	}
}

// ----------------------------------------------------------------------------
// stdio transport
// ----------------------------------------------------------------------------

// Serve runs the line-delimited JSON-RPC 2.0 read loop on
// the given input/output pair. The function returns when
// the input stream reaches EOF (typically when the MCP
// client closes its end of the pipe). It is safe to invoke
// on os.Stdin / os.Stdout from `main()`.
//
// While Serve is running, RegisterTool / RegisterResource /
// RegisterPrompt calls deliver `notifications/*/list_changed`
// events on the outbound stream. This is the
// "live registration" feature that lets the server pick up
// plugins added after Serve started.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)

	// Wire the notifier to write JSON lines on the outbound
	// stream. This is best-effort; if the client has closed
	// the pipe, the write fails and we just lose the
	// notification.
	notifCh := make(chan []byte, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case msg, ok := <-notifCh:
				if !ok {
					return
				}
				_, _ = w.Write(msg)
				_, _ = w.Write([]byte("\n"))
			case <-done:
				return
			}
		}
	}()
	s.SetNotifier(func(method string, params map[string]any) {
		nb, _ := json.Marshal(jsonRPCNotification{
			JSONRPC: "2.0", Method: method, Params: params,
		})
		select {
		case notifCh <- nb:
		default:
			// drop notification if the outbound buffer is full
		}
	})
	defer func() {
		s.SetNotifier(nil)
		close(notifCh)
		<-done
	}()

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("reading stdin: %w", err)
		}

		// Tolerate leading whitespace from clients that
		// pretty-print the JSON.
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeResponse(w, jsonRPCResponse{
				JSONRPC: "2.0", ID: nil,
				Error: &jsonRPCError{Code: codeParseError, Message: "Parse error"},
			})
			continue
		}

		// Per JSON-RPC 2.0, requests without an `id` are
		// notifications and must not produce a response.
		if req.ID == nil && req.Method != "" {
			_, _ = s.handleRequest(context.Background(), req)
			continue
		}

		result, rpcErr := s.handleRequest(context.Background(), req)
		if rpcErr != nil {
			s.writeResponse(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			continue
		}
		s.writeResponse(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	}
}

func (s *Server) writeResponse(w io.Writer, resp jsonRPCResponse) {
	data, _ := json.Marshal(resp)
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n"))
}

// ----------------------------------------------------------------------------
// HTTP / SSE transport
// ----------------------------------------------------------------------------

// HandlePOST processes a single JSON-RPC request over
// plain HTTP. The request body must be a single JSON
// object (not an array — the MCP spec is explicit that
// the HTTP transport carries one request per call to
// keep servers stateless). The response is a single JSON
// object with the matching `id`.
func (s *Server) HandlePOST(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: nil,
			Error: &jsonRPCError{Code: codeParseError, Message: "Parse error"},
		})
		return
	}

	// Notifications (no id) return 204 per the MCP HTTP
	// transport draft.
	if req.ID == nil && req.Method != "" {
		_, _ = s.handleRequest(r.Context(), req)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	result, rpcErr := s.handleRequest(r.Context(), req)
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	if rpcErr != nil {
		resp.Error = rpcErr
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleSSE upgrades the connection to a Server-Sent
// Events stream. The first event sent immediately after
// the upgrade is an `endpoint` hint telling the client
// where to POST subsequent requests (we use the same
// `/mcp/sse` URL with a POST body). The stream stays
// open for the lifetime of the underlying HTTP
// connection; the client closes it.
func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 1. Tell the client where to POST requests.
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /mcp/sse\n\n")
	flusher.Flush()

	// 2. Wire the notifier to push SSE events.
	notifCh := make(chan []byte, 32)
	s.SetNotifier(func(method string, params map[string]any) {
		body, _ := json.Marshal(jsonRPCNotification{JSONRPC: "2.0", Method: method, Params: params})
		frame := []byte("event: message\ndata: " + string(body) + "\n\n")
		select {
		case notifCh <- frame:
		default:
		}
	})
	defer s.SetNotifier(nil)

	// 3. Heartbeat every 15s so intermediate proxies don't
	// time the connection out.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case frame := <-notifCh:
			_, _ = w.Write(frame)
			flusher.Flush()
		}
	}
}
