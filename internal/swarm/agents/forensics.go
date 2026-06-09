package agents

import (
	"context"
	"encoding/json"
	"fmt"

	forensicspkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/forensics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/tuning"
	"github.com/google/uuid"
)

// ForensicsAgent wakes on FORENSIC_ARTIFACT findings and performs LLM-powered
// forensic analysis, publishing FORENSIC_ANALYSIS results.
type ForensicsAgent struct {
	forensics  *forensicspkg.Agent
	campaignID uuid.UUID
	parallel   int
	tun        *tuning.Settings
}

// NewForensicsAgent wraps a forensics agent for the swarm.
func NewForensicsAgent(inner *forensicspkg.Agent, campaignID uuid.UUID, parallel int, tun *tuning.Settings) *ForensicsAgent {
	if parallel <= 0 {
		parallel = 1
	}
	if tun == nil {
		tun = tuning.Default()
	}
	return &ForensicsAgent{forensics: inner, campaignID: campaignID, parallel: parallel, tun: tun}
}

// Name implements swarm.Agent.
func (a *ForensicsAgent) Name() string { return "forensics" }

// Trigger implements swarm.Agent.
func (a *ForensicsAgent) Trigger() blackboard.Predicate {
	return blackboard.Predicate{Types: []blackboard.FindingType{blackboard.TypeForensicArtifact}}
}

// MaxConcurrency implements swarm.Agent.
func (a *ForensicsAgent) MaxConcurrency() int { return a.parallel }

// Handle performs forensic analysis on collected evidence.
func (a *ForensicsAgent) Handle(ctx context.Context, f blackboard.Finding, board blackboard.Board) error {
	result, err := a.forensics.Analyze(ctx, f.Target, string(f.Data))
	if err != nil {
		return fmt.Errorf("forensic analysis: %w", err)
	}

	data, _ := json.Marshal(result)
	base, half := a.tun.Lookup(blackboard.TypeForensicAnalysis)
	_, err = board.Write(ctx, blackboard.Finding{
		CampaignID:    a.campaignID,
		AgentName:     a.Name(),
		Type:          blackboard.TypeForensicAnalysis,
		Target:        f.Target,
		Data:          data,
		PheromoneBase: base,
		HalfLifeSec:   half,
	})
	return err
}