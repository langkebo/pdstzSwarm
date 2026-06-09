package agents

import (
	"context"
	"fmt"

	orchestratorpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/orchestrator"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// OrchestratorAgent monitors the blackboard and emits periodic strategic
// recommendations. Unlike the full ReAct-loop orchestrator (used in
// sequential mode), the swarm orchestrator operates as a reactive observer
// that injects CAMPAIGN_COMPLETE when swarm quiescence is detected.
type OrchestratorAgent struct {
	o          *orchestratorpkg.OrchestratorAgent
	campaignID uuid.UUID
	objective  string
}

// NewOrchestratorAgent wraps the orchestrator for swarm usage.
func NewOrchestratorAgent(inner *orchestratorpkg.OrchestratorAgent, campaignID uuid.UUID, objective string) *OrchestratorAgent {
	return &OrchestratorAgent{o: inner, campaignID: campaignID, objective: objective}
}

// Name implements swarm.Agent.
func (a *OrchestratorAgent) Name() string { return "orchestrator" }

// Trigger implements swarm.Agent — wakes periodically to assess swarm progress.
func (a *OrchestratorAgent) Trigger() blackboard.Predicate {
	return blackboard.Predicate{
		Types: []blackboard.FindingType{
			blackboard.TypeCampaignComplete,
			blackboard.TypeAgentError,
		},
	}
}

// MaxConcurrency implements swarm.Agent.
func (a *OrchestratorAgent) MaxConcurrency() int { return 1 }

// Handle processes swarm-level meta events.
func (a *OrchestratorAgent) Handle(ctx context.Context, f blackboard.Finding, board blackboard.Board) error {
	switch f.Type {
	case blackboard.TypeCampaignComplete:
		// Signal confirmed — let the scheduler wind down naturally.
		return nil
	case blackboard.TypeAgentError:
		// Log and continue; the error is already on the blackboard for
		// other agents to react to (e.g., report agent includes errors).
		return nil
	default:
		return fmt.Errorf("unexpected finding type for orchestrator: %s", f.Type)
	}
}