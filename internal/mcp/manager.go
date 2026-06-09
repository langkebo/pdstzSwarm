package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// ServerStatus reports the high-level state of a
// per-server entry inside the Manager.
type ServerStatus string

const (
	// ServerStatusEnabled means the server is wired
	// up and its tools are visible to the host
	// transport. New tools registered after this
	// status is set will still be propagated
	// (`list_changed` notification).
	ServerStatusEnabled ServerStatus = "enabled"
	// ServerStatusDisabled means the server is in
	// the catalogue but its tools are hidden from
	// the host transport. Used to keep a server in
	// inventory but temporarily mute it.
	ServerStatusDisabled ServerStatus = "disabled"
)

// ManagedServer is one entry in a Manager. Each
// managed server has its own Server instance plus a
// per-tool allow-list. When the allow-list is empty,
// the server is considered "all tools enabled".
// When non-empty, only the named tools are exposed
// to the host transport.
type ManagedServer struct {
	Name        string
	Description string
	Server      *Server
	Status      ServerStatus

	// Allow is the set of tool names this server
	// exposes. nil/empty = all tools enabled.
	// Read through the Allowed() / SetAllow() helpers
	// so concurrent updates don't race with the
	// fan-out read path.
	mu    sync.RWMutex
	allow map[string]struct{}
}

// Allowed reports whether the named tool is in the
// allow-list. An empty allow-list means "all".
func (m *ManagedServer) Allowed(tool string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.allow) == 0 {
		return true
	}
	_, ok := m.allow[tool]
	return ok
}

// SetAllow replaces the allow-list with the given
// set. nil clears the list (== "all tools enabled").
// Duplicates in the input are de-duplicated.
func (m *ManagedServer) SetAllow(allow []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(allow) == 0 {
		m.allow = nil
		return
	}
	m.allow = make(map[string]struct{}, len(allow))
	for _, name := range allow {
		m.allow[name] = struct{}{}
	}
}

// AllowList returns a sorted copy of the current
// allow-list. nil/empty means "all tools enabled".
func (m *ManagedServer) AllowList() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.allow) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.allow))
	for name := range m.allow {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Enable flips the server to the enabled state.
// Tools become visible to the host transport.
func (m *ManagedServer) Enable() {
	m.mu.Lock()
	m.Status = ServerStatusEnabled
	m.mu.Unlock()
}

// Disable flips the server to disabled. Tools
// remain in the per-server catalogue but are hidden
// from the host transport.
func (m *ManagedServer) Disable() {
	m.mu.Lock()
	m.Status = ServerStatusDisabled
	m.mu.Unlock()
}

// Manager owns a set of ManagedServer entries. It
// supports a single fan-out host transport: the
// tools/resources/prompts visible to that transport
// are the union of all *enabled* servers, filtered
// through each server's allow-list.
//
// This is the canonical way to host the
// pentestswarm "core" server plus any
// community-contributed tool server (e.g. an
// `nmap-mcp`, a `metasploit-mcp`) under a single
// SSE / stdio endpoint.
type Manager struct {
	mu      sync.RWMutex
	servers map[string]*ManagedServer
}

// NewManager constructs an empty Manager. Use
// Add / Remove / Get to manage the catalogue.
func NewManager() *Manager {
	return &Manager{servers: make(map[string]*ManagedServer)}
}

// Add registers a new managed server. If a server
// with the same name already exists it is replaced
// (the previous one is removed from the fan-out
// and its tools are dropped from the host view).
func (m *Manager) Add(s *ManagedServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.Status == "" {
		s.Status = ServerStatusEnabled
	}
	m.servers[s.Name] = s
}

// Remove drops a server by name. Returns true if
// the server was present and got removed.
func (m *Manager) Remove(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.servers[name]; !ok {
		return false
	}
	delete(m.servers, name)
	return true
}

// Get fetches a managed server by name. Returns
// (nil, false) if no such server is registered.
func (m *Manager) Get(name string) (*ManagedServer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.servers[name]
	return s, ok
}

// Names returns the catalogue of registered server
// names, alphabetically sorted. Used by the
// `servers/list` handler.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.servers))
	for n := range m.servers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// StatusReport is a JSON-friendly snapshot of a
// single managed server. Returned by
// Manager.StatusAll.
type StatusReport struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Status      ServerStatus `json:"status"`
	ToolCount   int          `json:"tool_count"`
	Tools       []string     `json:"tools"`
	Allow       []string     `json:"allow,omitempty"`
	ServerInfo  ServerInfo   `json:"server_info"`
}

// StatusAll returns one StatusReport per registered
// server, sorted by name. Use this for the
// `/api/v1/mcp/servers` endpoint.
func (m *Manager) StatusAll() []StatusReport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := sortedServerNames(m.servers)

	out := make([]StatusReport, 0, len(names))
	for _, n := range names {
		s := m.servers[n]
		tools := s.Server.Tools()
		toolNames := make([]string, 0, len(tools))
		for _, t := range tools {
			toolNames = append(toolNames, t.Name)
		}
		sort.Strings(toolNames)

		allow := s.AllowList()

		out = append(out, StatusReport{
			Name:        s.Name,
			Description: s.Description,
			Status:      s.Status,
			ToolCount:   len(tools),
			Tools:       toolNames,
			Allow:       allow,
			ServerInfo:  s.Server.ServerInfo(),
		})
	}
	return out
}

// AllTools returns the union of tools from all
// *enabled* servers, with each tool filtered
// through its server's allow-list. Used by the
// fan-out host transport to build its `tools/list`
// response. The result is a copy; safe to mutate.
func (m *Manager) AllTools() []MCPTool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []MCPTool
	for _, name := range sortedServerNames(m.servers) {
		s := m.servers[name]
		if s.Status != ServerStatusEnabled {
			continue
		}
		for _, t := range s.Server.Tools() {
			if !s.Allowed(t.Name) {
				continue
			}
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AllResources returns the union of resources from
// all enabled servers. Duplicates (by URI) are
// resolved with first-wins ordering (alphabetical
// by server name).
func (m *Manager) AllResources() []MCPResource {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]struct{})
	var out []MCPResource
	for _, name := range sortedServerNames(m.servers) {
		s := m.servers[name]
		if s.Status != ServerStatusEnabled {
			continue
		}
		for _, r := range s.Server.Resources() {
			if _, dup := seen[r.URI]; dup {
				continue
			}
			seen[r.URI] = struct{}{}
			out = append(out, r)
		}
	}
	return out
}

// AllPrompts returns the union of prompts from all
// enabled servers. Duplicates (by name) are
// resolved with first-wins ordering.
func (m *Manager) AllPrompts() []MCPPrompt {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]struct{})
	var out []MCPPrompt
	for _, name := range sortedServerNames(m.servers) {
		s := m.servers[name]
		if s.Status != ServerStatusEnabled {
			continue
		}
		for _, p := range s.Server.Prompts() {
			if _, dup := seen[p.Name]; dup {
				continue
			}
			seen[p.Name] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// DispatchToolsCall looks up the tool by name
// across all enabled servers, and if found, calls
// it on the owning server. Returns
// (serverName, result, error). The third value is
// non-nil only when the tool isn't in any enabled
// server; per-tool handler errors come back as
// (serverName, result, nil) and the result carries
// the MCP `isError` content block.
func (m *Manager) DispatchToolsCall(ctx context.Context, name string, args json.RawMessage) (string, any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, sname := range sortedServerNames(m.servers) {
		s := m.servers[sname]
		if s.Status != ServerStatusEnabled {
			continue
		}
		if !s.Allowed(name) {
			continue
		}
		for _, t := range s.Server.Tools() {
			if t.Name != name {
				continue
			}
			result, err := t.Handler(ctx, args)
			return sname, result, err
		}
	}
	return "", nil, fmt.Errorf("tool %q not found in any enabled MCP server", name)
}

func sortedServerNames(servers map[string]*ManagedServer) []string {
	out := make([]string, 0, len(servers))
	for n := range servers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
