package agents

import (
	"context"
	"encoding/json"
	"fmt"

	reversepkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/reverse"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/tuning"
	"github.com/google/uuid"
)

// ReverseAgent wakes on BINARY_ARTIFACT findings and performs LLM-powered
// reverse engineering analysis, publishing REVERSE_ANALYSIS results.
type ReverseAgent struct {
	reverse    *reversepkg.Agent
	campaignID uuid.UUID
	parallel   int
	tun        *tuning.Settings
}

// NewReverseAgent wraps a reverse engineering agent for the swarm.
func NewReverseAgent(inner *reversepkg.Agent, campaignID uuid.UUID, parallel int, tun *tuning.Settings) *ReverseAgent {
	if parallel <= 0 {
		parallel = 1
	}
	if tun == nil {
		tun = tuning.Default()
	}
	return &ReverseAgent{reverse: inner, campaignID: campaignID, parallel: parallel, tun: tun}
}

// Name implements swarm.Agent.
func (a *ReverseAgent) Name() string { return "reverse" }

// Trigger implements swarm.Agent.
func (a *ReverseAgent) Trigger() blackboard.Predicate {
	return blackboard.Predicate{Types: []blackboard.FindingType{blackboard.TypeBinaryArtifact}}
}

// MaxConcurrency implements swarm.Agent.
func (a *ReverseAgent) MaxConcurrency() int { return a.parallel }

// Handle performs reverse engineering analysis on a binary artifact.
func (a *ReverseAgent) Handle(ctx context.Context, f blackboard.Finding, board blackboard.Board) error {
	result, err := a.reverse.Analyze(ctx, f.Target, string(f.Data))
	if err != nil {
		return fmt.Errorf("reverse analysis: %w", err)
	}

	data, _ := json.Marshal(result)
	base, half := a.tun.Lookup(blackboard.TypeReverseAnalysis)
	_, err = board.Write(ctx, blackboard.Finding{
		CampaignID:    a.campaignID,
		AgentName:     a.Name(),
		Type:          blackboard.TypeReverseAnalysis,
		Target:        f.Target,
		Data:          data,
		PheromoneBase: base,
		HalfLifeSec:   half,
	})
	return err
}