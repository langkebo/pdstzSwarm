package agents

import (
	"context"
	"encoding/json"
	"fmt"

	mobilepkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/mobile"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/tuning"
	"github.com/google/uuid"
)

// MobileAgent wakes on MOBILE_ARTIFACT findings and performs LLM-powered
// mobile app security analysis, publishing MOBILE_ANALYSIS results.
type MobileAgent struct {
	mobile     *mobilepkg.Agent
	campaignID uuid.UUID
	parallel   int
	tun        *tuning.Settings
}

// NewMobileAgent wraps a mobile security agent for the swarm.
func NewMobileAgent(inner *mobilepkg.Agent, campaignID uuid.UUID, parallel int, tun *tuning.Settings) *MobileAgent {
	if parallel <= 0 {
		parallel = 1
	}
	if tun == nil {
		tun = tuning.Default()
	}
	return &MobileAgent{mobile: inner, campaignID: campaignID, parallel: parallel, tun: tun}
}

// Name implements swarm.Agent.
func (a *MobileAgent) Name() string { return "mobile" }

// Trigger implements swarm.Agent.
func (a *MobileAgent) Trigger() blackboard.Predicate {
	return blackboard.Predicate{Types: []blackboard.FindingType{blackboard.TypeMobileArtifact}}
}

// MaxConcurrency implements swarm.Agent.
func (a *MobileAgent) MaxConcurrency() int { return a.parallel }

// Handle performs mobile security analysis.
func (a *MobileAgent) Handle(ctx context.Context, f blackboard.Finding, board blackboard.Board) error {
	result, err := a.mobile.Analyze(ctx, f.Target, string(f.Data))
	if err != nil {
		return fmt.Errorf("mobile analysis: %w", err)
	}

	data, _ := json.Marshal(result)
	base, half := a.tun.Lookup(blackboard.TypeMobileAnalysis)
	_, err = board.Write(ctx, blackboard.Finding{
		CampaignID:    a.campaignID,
		AgentName:     a.Name(),
		Type:          blackboard.TypeMobileAnalysis,
		Target:        f.Target,
		Data:          data,
		PheromoneBase: base,
		HalfLifeSec:   half,
	})
	return err
}