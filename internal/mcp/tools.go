package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/engine"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/google/uuid"
)

// CampaignLookup abstracts the campaign store so MCP
// tools can list running / completed campaigns without
// taking a direct dependency on internal/api. The
// default implementation in cli/mcp.go wires
// api.Server's sync.Map; tests can pass a fake.
type CampaignLookup interface {
	// ListCampaigns returns a JSON-friendly slice of
	// campaign metadata (id, target, status, scope,
	// started_at, finished_at). Implementations are
	// expected to be safe for concurrent calls.
	ListCampaigns(ctx context.Context, limit int) ([]CampaignInfo, error)
	// GetCampaign returns metadata for a single
	// campaign id, or CampaignNotFoundError if no such
	// campaign exists.
	GetCampaign(ctx context.Context, id string) (CampaignInfo, error)
}

// CampaignInfo is the JSON-friendly campaign metadata
// exposed through MCP. Mirrors pipeline.Campaign
// closely but is decoupled so the MCP package doesn't
// need to import internal/pipeline in tests.
type CampaignInfo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Target     string    `json:"target"`
	Scope      []string  `json:"scope,omitempty"`
	Status     string    `json:"status"`
	Mode       string    `json:"mode"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	FindingN   int       `json:"finding_count"`
}

// ReportLookup abstracts the reports store. Same
// rationale as CampaignLookup: keep MCP package
// independent of internal/api and easy to unit-test.
type ReportLookup interface {
	ListRecent(ctx context.Context, limit int) ([]ReportInfo, error)
	Get(ctx context.Context, id string) (ReportInfo, error)
}

// ReportInfo is the JSON-friendly shape of a
// reports.Report; we omit markdown and full findings
// blob to keep tool responses small.
type ReportInfo struct {
	ID         string    `json:"id"`
	CampaignID string    `json:"campaign_id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Format     string    `json:"format"`
	Summary    string    `json:"summary,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	FindingN   int       `json:"finding_count"`
}

// FindingLookup abstracts access to a campaign's
// classified findings, either from the live runner
// state or the persisted store.
type FindingLookup interface {
	ListFindings(ctx context.Context, campaignID string, limit int) ([]FindingInfo, error)
}

// FindingInfo is the JSON-friendly shape of a
// pipeline.ClassifiedFinding.
type FindingInfo struct {
	ID          string   `json:"id"`
	CampaignID  string   `json:"campaign_id"`
	Title       string   `json:"title"`
	Severity    string   `json:"severity"`
	CVSSScore   float64  `json:"cvss_score,omitempty"`
	CVEIDs      []string `json:"cve_ids,omitempty"`
	Description string   `json:"description,omitempty"`
	Target      string   `json:"target,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// PromptLookup abstracts access to the 35 PromptType
// catalogue. It exposes a read-only view of override
// status so an MCP client can ask "is this template
// customised?".
type PromptLookup interface {
	ListPrompts(ctx context.Context) ([]PromptInfo, error)
}

// PromptInfo describes a single PromptType for MCP.
type PromptInfo struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	Description string `json:"description"`
	HasOverride bool   `json:"has_override"`
	Variables   []string `json:"variables,omitempty"`
}

// ScopeChecker is the optional callback a host
// registers so MCP tools can validate a target is
// within the configured scope. Returning nil means
// "OK"; a non-nil error is shown to the model as a
// refusal reason. nil checker = no validation
// (allowed).
type ScopeChecker func(ctx context.Context, target string, scope []string) error

// ToolDeps wires the optional host-side dependencies
// an MCP tool may need. Any field may be nil; the
// corresponding tool will refuse to run if its dep is
// missing. This is how we keep the MCP package
// independent of internal/api.
type ToolDeps struct {
	Campaigns  CampaignLookup
	Reports    ReportLookup
	Findings   FindingLookup
	Prompts    PromptLookup
	ScopeCheck ScopeChecker
	// Config, when non-nil, is included in tool
	// output for `server_info` and friends (e.g. to
	// show configured provider / model).
	Config *config.Config
}

// RegisterDefaultTools wires the full pentestswarm
// tool surface onto s:
//
//   - scan_target: full autonomous pentest (engine.Run)
//   - quick_recon: dry-run recon-only path
//   - explain_finding: audience-tailored explanation
//   - campaign_status / list_campaigns / get_campaign
//   - list_reports / get_report
//   - list_findings
//   - list_prompts
//   - check_scope
//   - list_tools (legacy alias)
//
// All tools degrade gracefully when the matching dep
// in `deps` is nil — they return a friendly message
// instead of a nil-deref.
func RegisterDefaultTools(s *Server, deps ToolDeps) {
	if deps.Config != nil {
		runner := engine.NewRunner(deps.Config)
		registerScanner(s, runner, deps)
	} else {
		// No config → tools return a friendly "no config" message.
		s.RegisterTool(MCPTool{
			Name:        "scan_target",
			Description: "Start a full autonomous penetration test. Requires server config; the MCP server was started without one (use `--config` to point at a YAML file with orchestrator settings).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"},"scope":{"type":"string"}},"required":["target","scope"]}`),
			Handler: notConfigured("scan_target"),
		})
		s.RegisterTool(MCPTool{
			Name:        "quick_recon",
			Description: "Run reconnaissance only. Requires server config; the MCP server was started without one (use `--config` to point at a YAML file with orchestrator settings).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}`),
			Handler: notConfigured("quick_recon"),
		})
	}

	// Campaign tools. These don't need engine.Runner,
	// just CampaignLookup.
	s.RegisterTool(MCPTool{
		Name:        "list_campaigns",
		Description: "List recent penetration-test campaigns with their status, target, and finding counts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"Maximum number of campaigns to return (default 20, max 100)"}}}`),
		Handler:     listCampaignsHandler(deps),
	})
	s.RegisterTool(MCPTool{
		Name:        "get_campaign",
		Description: "Get metadata for a single campaign by id.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"campaign_id":{"type":"string"}},"required":["campaign_id"]}`),
		Handler:     getCampaignHandler(deps),
	})
	s.RegisterTool(MCPTool{
		Name:        "campaign_status",
		Description: "Alias for get_campaign: returns the current status of a campaign.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"campaign_id":{"type":"string"}},"required":["campaign_id"]}`),
		Handler:     getCampaignHandler(deps),
	})

	// Report tools.
	s.RegisterTool(MCPTool{
		Name:        "list_reports",
		Description: "List recent penetration-test reports (title, summary, status, finding count).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"Maximum reports (default 20, max 100)"}}}`),
		Handler:     listReportsHandler(deps),
	})
	s.RegisterTool(MCPTool{
		Name:        "get_report",
		Description: "Get a single report by id (returns summary + metadata; markdown body omitted for size).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"report_id":{"type":"string"}},"required":["report_id"]}`),
		Handler:     getReportHandler(deps),
	})

	// Findings tool.
	s.RegisterTool(MCPTool{
		Name:        "list_findings",
		Description: "List classified findings for a campaign (severity, CVE, target).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"campaign_id":{"type":"string"},"limit":{"type":"integer","description":"Max findings (default 50)"}},"required":["campaign_id"]}`),
		Handler:     listFindingsHandler(deps),
	})

	// Prompts catalogue tool.
	s.RegisterTool(MCPTool{
		Name:        "list_prompts",
		Description: "List the 35 pentestswarm prompt templates with category and override status.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler:     listPromptsHandler(deps),
	})

	// Scope-check tool (the only tool that can refuse).
	s.RegisterTool(MCPTool{
		Name:        "check_scope",
		Description: "Validate a target against the configured scope rules. Returns {ok: true} if allowed, or {ok: false, reason: '...'} if out of scope.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"},"scope":{"type":"array","items":{"type":"string"}}},"required":["target"]}`),
		Handler:     checkScopeHandler(deps),
	})

	// AI-explanation tool. Not LLM-wired in this
	// version; the call returns a templated
	// explanation that prompts the model to fill in
	// the detail. Future revision can wire it to a
	// real LLM call.
	s.RegisterTool(MCPTool{
		Name:        "explain_finding",
		Description: "Return an audience-tailored explanation template for a vulnerability. The agent is expected to fill in the contextual details.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"description":{"type":"string"},"audience":{"type":"enum","enum":["developer","manager","executive"]}},"required":["description"]}`),
		Handler:     explainFindingHandler(),
	})

	// Legacy alias preserved for the existing
	// CLI/docs so we don't break anyone who scripted
	// against the original tool name. It returns a
	// short inventory of the registered tools.
	s.RegisterTool(MCPTool{
		Name:        "list_tools",
		Description: "List the available MCP tools exposed by this server.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler:     listInventory(s),
	})
}

// ----------------------------------------------------------------------------
// Per-tool handlers
// ----------------------------------------------------------------------------

func registerScanner(s *Server, runner *engine.Runner, deps ToolDeps) {
	s.RegisterTool(MCPTool{
		Name:        "scan_target",
		Description: "Start a full autonomous penetration test against a target. Returns a textual log of events; finding summaries are returned as a separate tool call (list_findings).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","description":"Target domain or IP"},"scope":{"type":"string","description":"Comma-separated scope list (CIDR or domain)"},"objective":{"type":"string","description":"What to find (default: find all vulnerabilities)"},"mode":{"type":"string","enum":["manual","assist","swarm"],"description":"Run mode (default manual)"}},"required":["target","scope"]}`),
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var params struct {
				Target    string `json:"target"`
				Scope     string `json:"scope"`
				Objective string `json:"objective"`
				Mode      string `json:"mode"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return nil, err
			}
			if params.Objective == "" {
				params.Objective = "find all vulnerabilities"
			}
			if params.Mode == "" {
				params.Mode = "manual"
			}

			// Pre-flight scope check (if wired).
			if deps.ScopeCheck != nil {
				if err := deps.ScopeCheck(ctx, params.Target, strings.Split(params.Scope, ",")); err != nil {
					return map[string]any{
						"refused": true,
						"reason":  err.Error(),
					}, nil
				}
			}

			runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
			defer cancel()

			var events []string
			cc := engine.CampaignConfig{
				Target:    params.Target,
				Scope:     strings.Split(params.Scope, ","),
				Objective: params.Objective,
				Mode:      params.Mode,
				Format:    "md",
				OutputDir: "./reports",
			}

			err := runner.Run(runCtx, cc, func(event pipeline.CampaignEvent) {
				events = append(events, fmt.Sprintf("[%s] %s: %s", event.EventType, event.AgentName, event.Detail))
			})
			if err != nil {
				events = append(events, "Error: "+err.Error())
			}
			return map[string]any{
				"target": params.Target,
				"events": events,
				"count":  len(events),
			}, nil
		},
	})

	s.RegisterTool(MCPTool{
		Name:        "quick_recon",
		Description: "Run reconnaissance only against a target (dry-run; no exploitation). Returns the discovered attack surface as a textual summary.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"},"scope":{"type":"string","description":"Comma-separated scope (defaults to target)"}},"required":["target"]}`),
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var params struct {
				Target string `json:"target"`
				Scope  string `json:"scope"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return nil, err
			}
			scope := params.Scope
			if scope == "" {
				scope = params.Target
			}

			// Pre-flight scope check.
			if deps.ScopeCheck != nil {
				if err := deps.ScopeCheck(ctx, params.Target, strings.Split(scope, ",")); err != nil {
					return map[string]any{
						"refused": true,
						"reason":  err.Error(),
					}, nil
				}
			}

			runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()

			events := []string{}
			cc := engine.CampaignConfig{
				Target:    params.Target,
				Scope:     strings.Split(scope, ","),
				Objective: "reconnaissance only",
				Mode:      "manual",
				DryRun:    true,
				Format:    "md",
				OutputDir: "./reports",
			}

			_ = runner.Run(runCtx, cc, func(event pipeline.CampaignEvent) {
				if event.EventType == pipeline.EventToolResult || event.EventType == pipeline.EventFindingDiscovered {
					events = append(events, event.Detail)
				}
			})
			return map[string]any{
				"target": params.Target,
				"events": events,
				"count":  len(events),
			}, nil
		},
	})
}

func listCampaignsHandler(deps ToolDeps) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		if deps.Campaigns == nil {
			return map[string]any{"error": "campaign lookup not configured", "campaigns": []any{}}, nil
		}
		var params struct {
			Limit int `json:"limit"`
		}
		_ = json.Unmarshal(args, &params)
		if params.Limit <= 0 {
			params.Limit = 20
		}
		if params.Limit > 100 {
			params.Limit = 100
		}
		cs, err := deps.Campaigns.ListCampaigns(ctx, params.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"count":     len(cs),
			"campaigns": cs,
		}, nil
	}
}

func getCampaignHandler(deps ToolDeps) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		if deps.Campaigns == nil {
			return map[string]any{"error": "campaign lookup not configured"}, nil
		}
		var params struct {
			CampaignID string `json:"campaign_id"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		c, err := deps.Campaigns.GetCampaign(ctx, params.CampaignID)
		if err != nil {
			return map[string]any{"error": err.Error(), "campaign_id": params.CampaignID}, nil
		}
		return c, nil
	}
}

func listReportsHandler(deps ToolDeps) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		if deps.Reports == nil {
			return map[string]any{"error": "report lookup not configured", "reports": []any{}}, nil
		}
		var params struct {
			Limit int `json:"limit"`
		}
		_ = json.Unmarshal(args, &params)
		if params.Limit <= 0 {
			params.Limit = 20
		}
		if params.Limit > 100 {
			params.Limit = 100
		}
		rs, err := deps.Reports.ListRecent(ctx, params.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"count":   len(rs),
			"reports": rs,
		}, nil
	}
}

func getReportHandler(deps ToolDeps) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		if deps.Reports == nil {
			return map[string]any{"error": "report lookup not configured"}, nil
		}
		var params struct {
			ReportID string `json:"report_id"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		r, err := deps.Reports.Get(ctx, params.ReportID)
		if err != nil {
			return map[string]any{"error": err.Error(), "report_id": params.ReportID}, nil
		}
		return r, nil
	}
}

func listFindingsHandler(deps ToolDeps) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		if deps.Findings == nil {
			return map[string]any{"error": "findings lookup not configured", "findings": []any{}}, nil
		}
		var params struct {
			CampaignID string `json:"campaign_id"`
			Limit      int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		if params.Limit <= 0 {
			params.Limit = 50
		}
		if params.Limit > 500 {
			params.Limit = 500
		}
		fs, err := deps.Findings.ListFindings(ctx, params.CampaignID, params.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"campaign_id": params.CampaignID,
			"count":       len(fs),
			"findings":    fs,
		}, nil
	}
}

func listPromptsHandler(deps ToolDeps) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		if deps.Prompts == nil {
			return map[string]any{
				"error":   "prompts lookup not configured",
				"prompts": []any{},
			}, nil
		}
		ps, err := deps.Prompts.ListPrompts(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"count":   len(ps),
			"prompts": ps,
		}, nil
	}
}

func checkScopeHandler(deps ToolDeps) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var params struct {
			Target string   `json:"target"`
			Scope  []string `json:"scope"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		// No scope check wired = always allow.
		// Returns a friendly note in the result so
		// the model can show "this is the
		// permissive mode" in the response.
		if deps.ScopeCheck == nil {
			return map[string]any{
				"ok":   true,
				"note": "no scope check configured; all targets allowed",
			}, nil
		}
		err := deps.ScopeCheck(ctx, params.Target, params.Scope)
		if err == nil {
			return map[string]any{
				"ok": true,
				"target": params.Target,
				"scope":  params.Scope,
			}, nil
		}
		// Refusal: return a non-nil error so the
		// server-side handler wraps it in an
		// `isError: true` content block. This
		// matches the spec and lets the model
		// see "Error: out of scope" in its tool
		// result history.
		return nil, fmt.Errorf("refused: target %q is out of scope: %s", params.Target, err.Error())
	}
}

func explainFindingHandler() func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var params struct {
			Description string `json:"description"`
			Audience    string `json:"audience"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		if params.Audience == "" {
			params.Audience = "developer"
		}
		// Build a templated explanation that the
		// agent fills in with contextual details.
		// We don't make a real LLM call from the MCP
		// tool because (a) most MCP clients want
		// their own model to do the elaboration and
		// (b) the recursive-LLM-call pattern adds
		// latency without much value.
		return map[string]any{
			"description": params.Description,
			"audience":    params.Audience,
			"template":    audienceTemplate(params.Audience, params.Description),
			"note":        "This is a template; the agent should expand each section with environment-specific context.",
		}, nil
	}
}

func audienceTemplate(audience, desc string) string {
	switch audience {
	case "executive":
		return fmt.Sprintf("**Business impact of %s**\n\n- Why this matters for the business\n- Likelihood × consequence\n- Recommended next step (single line)", desc)
	case "manager":
		return fmt.Sprintf("**%s — engineering manager briefing**\n\n- What it is (1 sentence)\n- Who needs to know\n- Effort estimate (S/M/L) and dependencies\n- Compliance implications (PCI / SOC2 / GDPR)", desc)
	default: // developer
		return fmt.Sprintf("**%s — developer deep-dive**\n\n- Root cause (1 paragraph)\n- Reproduction steps (curl / payload)\n- Fix recommendation (code snippet)\n- Regression test outline", desc)
	}
}

func listInventory(s *Server) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		out := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.Tools() {
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
			})
		}
		return map[string]any{"tools": out, "count": len(out)}, nil
	}
}

func notConfigured(name string) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		return map[string]any{
			"refused": true,
			"reason":  fmt.Sprintf("Tool %q is registered but the MCP server was started without an orchestrator config. Re-run with `--config /path/to/config.yaml` (or set PENTESTSWARM_ORCHESTRATOR_API_KEY) to enable it.", name),
		}, nil
	}
}

// ----------------------------------------------------------------------------
// Resources
// ----------------------------------------------------------------------------

// RegisterDefaultResources wires the read-only
// resources MCP clients can fetch. Each one returns
// JSON describing a server-side facet. Resources are
// useful for the model's "let me check the project
// state" step before deciding which tool to call.
func RegisterDefaultResources(s *Server, deps ToolDeps) {
	s.RegisterResource(MCPResource{
		URI:         "pentestswarm://server/info",
		Name:        "Server info",
		Description: "Server identity, protocol version, and the orchestrator provider/model that back the tools.",
		MimeType:    "application/json",
		Handler: func(ctx context.Context) (string, error) {
			info := map[string]any{
				"name":    s.ServerInfo().Name,
				"version": s.ServerInfo().Version,
				"protocol": ProtocolVersion,
				"tools":   len(s.Tools()),
				"resources": len(s.Resources()),
				"prompts":   len(s.Prompts()),
			}
			if deps.Config != nil {
				info["provider"] = deps.Config.Orchestrator.Provider
				info["model"] = deps.Config.Orchestrator.Model
				info["context_window"] = deps.Config.Orchestrator.ContextWindow
			}
			b, _ := json.MarshalIndent(info, "", "  ")
			return string(b), nil
		},
	})

	s.RegisterResource(MCPResource{
		URI:         "pentestswarm://config/summary",
		Name:        "Configuration summary",
		Description: "Sanitised view of the orchestrator + tools configuration (no secrets).",
		MimeType:    "application/json",
		Handler: func(ctx context.Context) (string, error) {
			if deps.Config == nil {
				return "{}", nil
			}
			c := deps.Config
			out := map[string]any{
				"orchestrator": map[string]any{
					"provider":       c.Orchestrator.Provider,
					"model":          c.Orchestrator.Model,
					"context_window": c.Orchestrator.ContextWindow,
					"max_tokens":     c.Orchestrator.MaxTokens,
					"temperature":    c.Orchestrator.Temperature,
					"has_fallback":   c.Orchestrator.Fallback.Provider != "",
				},
				"agents": map[string]any{
					"recon":      nonEmptyAgent(c.Agents.Recon),
					"classifier": nonEmptyAgent(c.Agents.Classifier),
					"exploit":    nonEmptyAgent(c.Agents.Exploit),
					"report":     nonEmptyAgent(c.Agents.Report),
				},
				"tools": map[string]any{
					"default_timeout_seconds": c.Tools.DefaultTimeout,
				},
				"monitor": map[string]any{
					"enabled":              c.Monitor.Enabled,
					"max_total_per_agent":  c.Monitor.MaxTotalPerAgent,
					"max_same_tool_streak": c.Monitor.MaxSameToolStreak,
				},
				"scope": map[string]any{
					"enforce_strict": c.Scope.EnforceStrict,
				},
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return string(b), nil
		},
	})

	s.RegisterResource(MCPResource{
		URI:         "pentestswarm://capabilities",
		Name:        "Capabilities",
		Description: "Stable list of MCP capabilities (tools, resources, prompts) — used by clients that want to discover the surface without parsing tools/list.",
		MimeType:    "application/json",
		Handler: func(ctx context.Context) (string, error) {
			tools := s.Tools()
			resources := s.Resources()
			prompts := s.Prompts()
			out := map[string]any{
				"protocol_version": ProtocolVersion,
				"server_info":      s.ServerInfo(),
				"tool_names":       namesOfTools(tools),
				"resource_uris":    namesOfResources(resources),
				"prompt_names":     namesOfPrompts(prompts),
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return string(b), nil
		},
	})
}

func nonEmptyAgent(a config.AgentModelConfig) map[string]any {
	if a.Provider == "" && a.Model == "" {
		return map[string]any{"inherits_orchestrator": true}
	}
	return map[string]any{
		"provider": a.Provider,
		"model":    a.Model,
	}
}

func namesOfTools(tools []MCPTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func namesOfResources(rs []MCPResource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.URI)
	}
	return out
}

func namesOfPrompts(ps []MCPPrompt) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// ----------------------------------------------------------------------------
// Prompts
// ----------------------------------------------------------------------------

// RegisterDefaultPrompts wires the user-facing prompt
// templates exposed over MCP. The model can fetch
// these to scaffold a new conversation with the right
// system message + user message pair.
func RegisterDefaultPrompts(s *Server, deps ToolDeps) {
	s.RegisterPrompt(MCPPrompt{
		Name:        "pentest-scope",
		Description: "Initial scoping message for a new penetration test. Asks the user to confirm the in-scope targets, the rules of engagement, and the stop conditions.",
		Arguments: []PromptArg{
			{Name: "target", Description: "The target domain or CIDR under test", Required: true},
			{Name: "scope", Description: "Comma-separated in-scope assets", Required: true},
			{Name: "rules_of_engagement", Description: "Free-text rules (e.g. no DDoS, business hours only)", Required: false},
		},
		Render: func(ctx context.Context, args map[string]string) ([]map[string]any, error) {
			target := args["target"]
			scope := args["scope"]
			roe := args["rules_of_engagement"]
			if roe == "" {
				roe = "no DDoS / DoS, no social engineering, business hours only"
			}
			return []map[string]any{
				{
					"role": "assistant",
					"content": "You are an autonomous penetration-testing agent. Always validate scope before any action, prefer non-destructive techniques, and emit a finding event with severity, CVE ids, and CVSS score for every confirmed issue.",
				},
				{
					"role": "user",
					"content": fmt.Sprintf("Please run a full penetration test against **%s** with the following scope:\n\n- In-scope: %s\n- Rules of engagement: %s\n\nStart with reconnaissance, then exploit, then report. Stop and ask for confirmation before any step that may impact production.", target, scope, roe),
				},
			}, nil
		},
	})

	s.RegisterPrompt(MCPPrompt{
		Name:        "finding-report",
		Description: "Format a confirmed finding into a one-paragraph executive summary + a developer-facing remediation plan.",
		Arguments: []PromptArg{
			{Name: "title", Description: "Finding title (e.g. 'SQL injection in /api/login')", Required: true},
			{Name: "severity", Description: "Severity bucket (critical|high|medium|low|info)", Required: true},
			{Name: "cve", Description: "CVE id (optional)", Required: false},
			{Name: "description", Description: "Raw description / proof of concept", Required: true},
		},
		Render: func(ctx context.Context, args map[string]string) ([]map[string]any, error) {
			title := args["title"]
			severity := args["severity"]
			cve := args["cve"]
			desc := args["description"]
			cveNote := ""
			if cve != "" {
				cveNote = fmt.Sprintf(" (CVE: %s)", cve)
			}
			return []map[string]any{
				{
					"role": "assistant",
					"content": "You are a security analyst writing for both executives and developers. Keep the executive summary under 60 words; keep the remediation plan concrete and code-level where possible.",
				},
				{
					"role": "user",
					"content": fmt.Sprintf("Produce a finding report for:\n\n- **Title:** %s\n- **Severity:** %s%s\n- **Detail:** %s\n\nOutput sections:\n\n1. Executive summary (≤ 60 words, business impact only)\n2. Affected component(s)\n3. Proof of concept / reproduction\n4. Recommended fix (with code snippet if possible)\n5. Regression test outline", title, severity, cveNote, desc),
				},
			}, nil
		},
	})
}

// Ensure uuid is used (tool deps may need it later
// when wiring real implementations).
var _ = uuid.Nil
