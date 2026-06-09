package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
)

// OrchestratorAgent coordinates all specialist agents using a ReAct loop.
type OrchestratorAgent struct {
	provider      llm.Provider
	budgetManager *llm.BudgetManager
	tools         map[string]OrchestratorTool
	maxIterations int
	maxTokens     int
	temperature   float64
	eventSink     func(pipeline.CampaignEvent)
	skillInjector *skills.Injector // nil → skill injection disabled
}

// OrchestratorTool is a function the orchestrator can call.
type OrchestratorTool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Execute     func(ctx context.Context, args string) (string, error)
}

// OrchestratorConfig configures the orchestrator.
//
// New fields (Summarizer, MaxTokens, Temperature) default to the
// pre-P3-2 hard-coded values when left zero, so callers that build the
// agent with the old shape (Provider + MaxIterations + EventSink) keep
// working unchanged. To opt into parameterised summarisation, set
// Summarizer to a non-nil *llm.Summarizer.
type OrchestratorConfig struct {
	Provider      llm.Provider
	MaxIterations int
	EventSink     func(pipeline.CampaignEvent) // callback for real-time event streaming
	Summarizer    *llm.Summarizer              // optional P3-2: delegate summarisation
	MaxTokens     int                          // 0 → 4096 (preserves prior hard-coded default)
	Temperature   float64                      // 0 → 0.1 (preserves prior hard-coded default)
	SkillInjector *skills.Injector             // optional: enables skill recommendations in system prompt
}

// NewOrchestratorAgent creates a new orchestrator.
func NewOrchestratorAgent(cfg OrchestratorConfig) *OrchestratorAgent {
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	temperature := cfg.Temperature
	if temperature <= 0 {
		temperature = 0.1
	}

	var bm *llm.BudgetManager
	if cfg.Summarizer != nil {
		bm = llm.NewBudgetManagerWithSummarizer(cfg.Summarizer)
	} else {
		bm = llm.NewBudgetManager(cfg.Provider.ContextWindow())
		bm.MaxTokens = 2048 // preserve prior hard-coded summary parameters
		bm.Temperature = 0
	}

	return &OrchestratorAgent{
		provider:      cfg.Provider,
		budgetManager: bm,
		tools:         make(map[string]OrchestratorTool),
		maxIterations: maxIter,
		maxTokens:     maxTokens,
		temperature:   temperature,
		eventSink:     cfg.EventSink,
		skillInjector: cfg.SkillInjector,
	}
}

// RegisterTool adds a tool the orchestrator can call.
func (o *OrchestratorAgent) RegisterTool(tool OrchestratorTool) {
	o.tools[tool.Name] = tool
}

// Run starts the ReAct loop for a campaign.
func (o *OrchestratorAgent) Run(ctx context.Context, campaign pipeline.Campaign) error {
	// Build the system prompt, optionally enriched with skill recommendations.
	sysPrompt := o.buildSystemPrompt(campaign.Objective, campaign.Target)

	messages := []llm.Message{
		{
			Role: "user",
			Content: fmt.Sprintf(
				"Campaign started.\nTarget: %s\nObjective: %s\nMode: %s\n\nBegin the penetration test. Start with reconnaissance.",
				campaign.Target, campaign.Objective, campaign.Mode,
			),
		},
	}

	// Build tool definitions for the LLM
	var llmTools []llm.Tool
	for _, t := range o.tools {
		llmTools = append(llmTools, llm.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}

	for iteration := 0; iteration < o.maxIterations; iteration++ {
		// Check context cancellation (emergency stop)
		select {
		case <-ctx.Done():
			o.emitEvent(campaign, pipeline.EventStateChange, "orchestrator", "Campaign aborted", nil)
			return ctx.Err()
		default:
		}

		// Manage token budget
		if o.budgetManager.NeedsSummarization(messages) {
			summarized, err := o.budgetManager.Summarize(ctx, messages, o.provider)
			if err == nil {
				messages = summarized
			}
		}

		// Send to LLM
		req := llm.CompletionRequest{
			SystemPrompt: sysPrompt,
			Messages:     messages,
			Tools:        llmTools,
			MaxTokens:    o.maxTokens,
			Temperature:  o.temperature,
		}

		resp, err := o.provider.Complete(ctx, req)
		if err != nil {
			o.emitEvent(campaign, pipeline.EventError, "orchestrator", "LLM call failed: "+err.Error(), nil)
			return fmt.Errorf("orchestrator LLM call failed: %w", err)
		}

		// Process reasoning text
		if resp.Content != "" {
			o.emitEvent(campaign, pipeline.EventThought, "orchestrator", resp.Content, nil)
			messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})
		}

		// Check for campaign completion
		if resp.StopReason == "end_turn" && len(resp.ToolCalls) == 0 {
			// LLM chose to stop — check if it signaled completion
			if isCompletionSignal(resp.Content) {
				o.emitEvent(campaign, pipeline.EventMilestone, "orchestrator", "Campaign complete", nil)
				return nil
			}
		}

		// Process tool calls
		if len(resp.ToolCalls) == 0 {
			// No tool call and no completion — ask LLM to take action
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "What's the next step? Use one of the available tools to continue the campaign.",
			})
			continue
		}

		for _, tc := range resp.ToolCalls {
			o.emitEvent(campaign, pipeline.EventToolCall, "orchestrator",
				fmt.Sprintf("Calling %s", tc.Name), json.RawMessage(tc.Arguments))

			tool, ok := o.tools[tc.Name]
			if !ok {
				toolResult := fmt.Sprintf("Tool %q not found. Available tools: %v", tc.Name, o.toolNames())
				messages = append(messages, llm.Message{
					Role:       "tool",
					Content:    toolResult,
					ToolCallID: tc.ID,
				})
				continue
			}

			// Execute the tool
			result, err := tool.Execute(ctx, tc.Arguments)
			if err != nil {
				result = fmt.Sprintf("Tool %s failed: %s", tc.Name, err)
				o.emitEvent(campaign, pipeline.EventError, tc.Name, result, nil)
			} else {
				o.emitEvent(campaign, pipeline.EventToolResult, tc.Name, truncate(result, 500), nil)
			}

			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	o.emitEvent(campaign, pipeline.EventMilestone, "orchestrator", "Max iterations reached", nil)
	return fmt.Errorf("orchestrator reached max iterations (%d)", o.maxIterations)
}

func (o *OrchestratorAgent) emitEvent(campaign pipeline.Campaign, eventType pipeline.EventType, agent, detail string, data json.RawMessage) {
	if o.eventSink == nil {
		return
	}

	o.eventSink(pipeline.CampaignEvent{
		CampaignID: campaign.ID,
		Timestamp:  time.Now(),
		EventType:  eventType,
		AgentName:  agent,
		Detail:     detail,
		Data:       data,
	})
}

func (o *OrchestratorAgent) toolNames() []string {
	var names []string
	for name := range o.tools {
		names = append(names, name)
	}
	return names
}

func isCompletionSignal(content string) bool {
	for _, signal := range []string{"campaign complete", "objective reached", "all paths exhausted", "report generated"} {
		if containsIgnoreCase(content, signal) {
			return true
		}
	}
	return false
}

func containsIgnoreCase(s, substr string) bool {
	s = toLower(s)
	substr = toLower(substr)
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b[i] = s[i] + 32
		} else {
			b[i] = s[i]
		}
	}
	return string(b)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// buildSystemPrompt composes the final system prompt by appending skill
// recommendations when a SkillInjector is configured and the objective
// resolves to relevant skills.
func (o *OrchestratorAgent) buildSystemPrompt(objective, target string) string {
	prompt := orchestratorSystemPrompt
	if o.skillInjector == nil {
		return prompt
	}

	// Search both the objective and the target for relevant skills.
	query := objective
	if target != "" && target != objective {
		query = objective + " " + target
	}
	recs := o.skillInjector.ForObjective(query)
	if len(recs) == 0 {
		return prompt
	}

	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n")
	b.WriteString(skills.MustInject(recs))
	return b.String()
}

const orchestratorSystemPrompt = `You are the orchestrator of an autonomous penetration testing platform. You coordinate four specialist agents:

1. **Recon Agent**: Discovers subdomains, ports, services, endpoints, and technologies
2. **Classifier Agent**: Maps findings to CVEs, scores CVSS with full vector strings, filters false positives, maps to MITRE ATT&CK
3. **Exploit Agent**: Constructs and executes multi-step attack chains with MITRE ATT&CK technique mapping
4. **Report Agent**: Generates professional pentest reports with executive summaries, finding narratives, CVSS scores, and remediation guidance

## Your job
- Plan the campaign strategy based on the target and objective
- Decide which agent to invoke and when, with specific context and questions
- Track token/time budget and adapt the strategy when resources are constrained
- Know when to pivot (dead end, diminishing returns) and when to deepen (critical finding found)
- Know when to stop (objective reached, all paths exhausted, or budget exhausted)

## State machine
- PLANNING → INITIALIZING → EXECUTING → VALIDATING → COMPLETE
- Can also trigger ABORTED (user request, scope violation) or FAILED (unrecoverable error)

During EXECUTING, run a loop:
1. RECON — gather attack surface (subdomains, ports, endpoints, technologies)
2. CLASSIFY — turn raw recon into actionable findings (CVE, CWE, CVSS, ATT&CK)
3. EXPLOIT — build attack chains from classified findings
4. CONFIRM — verify findings with reproduction (optional but recommended for HIGH/CRITICAL)
5. REPORT — compile deliverable

Adapt the loop based on the objective:
- "Find all open ports" → skip EXPLOIT and CONFIRM, go to REPORT after CLASSIFY
- "Crit-level vulns only" → spend more on EXPLOIT and CONFIRM, less on RECON breadth
- "Full pentest" → run all phases thoroughly

## Dispatch strategy
- **Recon**: attack surface is unknown or stale (no recon in last 5 minutes)
- **Classifier**: new recon output arrived and hasn't been classified
- **Exploit**: classified findings with exploitability >= POSSIBLE and confidence >= 0.5
- **Triage**: multiple findings need prioritisation or conflicting severity assessments
- **Report**: campaign complete or user requested mid-campaign report

## Budget management
- Token budget: prefer concise agent prompts with specific questions over broad "analyse everything" instructions
- Time budget: if a tool runs > 60 seconds, consider cancelling and trying a different approach
- Depth vs breadth: after 2 dead ends on the same path, pivot to a different attack surface area
- Parallelism: when possible, dispatch independent agents in parallel

## Adaptation rules
- Recon returns no interesting endpoints → try a different scanner or broaden the scope
- Classifier consistently returns LOW confidence → ask for more recon detail
- Exploit can't build a chain → ask classifier to re-examine for missed attack paths
- Finding confirmed as false positive → remove from circulation and note the reason

Use the available tools to coordinate the agents. Think step by step about what to do next.

When the campaign objective is achieved or all attack paths are exhausted, declare "Campaign complete" and invoke the report generator.

IMPORTANT: Never target anything outside the defined scope. If you're unsure, ask.`
