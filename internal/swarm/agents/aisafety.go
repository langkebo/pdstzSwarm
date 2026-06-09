package agents

import (
	"context"
	"encoding/json"
	"fmt"

	aisafetypkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/aisafety"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/tuning"
	"github.com/google/uuid"
)

// AISafetyAgent wakes on AI_SYSTEM_ARTIFACT findings and performs LLM-powered
// AI safety and security analysis, publishing AI_SAFETY_ANALYSIS results.
type AISafetyAgent struct {
	aisafety   *aisafetypkg.Agent
	campaignID uuid.UUID
	parallel   int
	tun        *tuning.Settings
}

// NewAISafetyAgent wraps an AI safety agent for the swarm.
func NewAISafetyAgent(inner *aisafetypkg.Agent, campaignID uuid.UUID, parallel int, tun *tuning.Settings) *AISafetyAgent {
	if parallel <= 0 {
		parallel = 1
	}
	if tun == nil {
		tun = tuning.Default()
	}
	return &AISafetyAgent{aisafety: inner, campaignID: campaignID, parallel: parallel, tun: tun}
}

// Name implements swarm.Agent.
func (a *AISafetyAgent) Name() string { return "aisafety" }

// Trigger implements swarm.Agent.
func (a *AISafetyAgent) Trigger() blackboard.Predicate {
	return blackboard.Predicate{Types: []blackboard.FindingType{blackboard.TypeAISystemArtifact}}
}

// MaxConcurrency implements swarm.Agent.
func (a *AISafetyAgent) MaxConcurrency() int { return a.parallel }

// Handle performs AI safety and security auditing.
func (a *AISafetyAgent) Handle(ctx context.Context, f blackboard.Finding, board blackboard.Board) error {
	// Data contains the AI system description JSON with system_desc, system_prompt, tool_defs fields.
	var input struct {
		SystemDesc   string `json:"system_desc"`
		SystemPrompt string `json:"system_prompt"`
		ToolDefs     string `json:"tool_defs"`
	}
	_ = json.Unmarshal(f.Data, &input)

	result, err := a.aisafety.Audit(ctx, input.SystemDesc, input.SystemPrompt, input.ToolDefs)
	if err != nil {
		return fmt.Errorf("AI safety audit: %w", err)
	}

	data, _ := json.Marshal(result)
	base, half := a.tun.Lookup(blackboard.TypeAISafetyAnalysis)
	_, err = board.Write(ctx, blackboard.Finding{
		CampaignID:    a.campaignID,
		AgentName:     a.Name(),
		Type:          blackboard.TypeAISafetyAnalysis,
		Target:        f.Target,
		Data:          data,
		PheromoneBase: base,
		HalfLifeSec:   half,
	})
	return err
}