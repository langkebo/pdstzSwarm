package agents

import (
	"context"
	"encoding/json"
	"fmt"

	reconpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/recon"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/scope"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/tuning"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools"
	"github.com/google/uuid"
)

// interestingEndpointType is a virtual finding-type used only for tuning
// lookup — katana-flagged "interesting" endpoints get their own pheromone
// row so they surface to downstream agents faster. On the wire they're
// still written as blackboard.TypeHTTPEndpoint.
const interestingEndpointType blackboard.FindingType = "HTTP_ENDPOINT_INTERESTING"

// ReconAgent wakes on TARGET_REGISTERED and publishes one finding per
// discovered subdomain / port / endpoint / technology. Batched tool
// execution is retained inside the underlying recon agent — the swarm
// value is that downstream agents can react to each finding independently,
// without waiting for the full surface to be assembled.
type ReconAgent struct {
	recon      *reconpkg.ReconAgent
	scopeDef   *scope.ScopeDefinition
	campaignID uuid.UUID
	parallel   int
	tun        *tuning.Settings
}

// NewReconAgent constructs a swarm wrapper around the existing recon agent.
// Pass nil for tun to get the baked-in default pheromone tuning.
func NewReconAgent(inner *reconpkg.ReconAgent, scopeDef *scope.ScopeDefinition, campaignID uuid.UUID, parallel int, tun *tuning.Settings) *ReconAgent {
	if parallel <= 0 {
		parallel = 1
	}
	if tun == nil {
		tun = tuning.Default()
	}
	return &ReconAgent{recon: inner, scopeDef: scopeDef, campaignID: campaignID, parallel: parallel, tun: tun}
}

// Name implements swarm.Agent.
func (a *ReconAgent) Name() string { return "recon" }

// Trigger implements swarm.Agent — recon wakes on newly-registered targets.
func (a *ReconAgent) Trigger() blackboard.Predicate {
	return blackboard.Predicate{Types: []blackboard.FindingType{blackboard.TypeTargetRegistered}}
}

// MaxConcurrency implements swarm.Agent.
func (a *ReconAgent) MaxConcurrency() int { return a.parallel }

// Handle runs recon against the target and fans out findings to the blackboard.
func (a *ReconAgent) Handle(ctx context.Context, f blackboard.Finding, board blackboard.Board) error {
	plan := a.recon.PlanRecon(f.Target)
	surface, err := a.recon.Execute(ctx, plan, a.scopeDef, a.campaignID, tools.Options{})
	if err != nil {
		return fmt.Errorf("recon execute: %w", err)
	}

	// Every write pulls its pheromone from the tuning table so operators
	// can steer the swarm via config/pheromones.yaml + --exploration-bias.
	write := func(t blackboard.FindingType, onWire blackboard.FindingType, target string, payload any) {
		if onWire == "" {
			onWire = t
		}
		base, half := a.tun.Lookup(t)
		data, _ := json.Marshal(payload)
		_, _ = board.Write(ctx, blackboard.Finding{
			CampaignID:    a.campaignID,
			AgentName:     a.Name(),
			Type:          onWire,
			Target:        target,
			Data:          data,
			PheromoneBase: base,
			HalfLifeSec:   half,
		})
	}

	for _, sd := range surface.Subdomains {
		write(blackboard.TypeSubdomain, "", sd.Domain, sd)
	}
	for _, host := range surface.Hosts {
		for _, port := range host.OpenPorts {
			write(blackboard.TypePortOpen, "", fmt.Sprintf("%s:%d", host.IP, port),
				map[string]any{"ip": host.IP, "port": port, "service": host.Services[port]})
			if svc, ok := host.Services[port]; ok && svc.Name != "" {
				write(blackboard.TypeService, "", fmt.Sprintf("%s:%d/%s", host.IP, port, svc.Name), svc)
			}
		}
	}
	for _, ep := range surface.Endpoints {
		tuneKey := blackboard.TypeHTTPEndpoint
		if ep.Interesting {
			tuneKey = interestingEndpointType
		}
		write(tuneKey, blackboard.TypeHTTPEndpoint, ep.URL, ep)
	}
	for tech, version := range surface.Technologies {
		write(blackboard.TypeTechnology, "", tech,
			map[string]string{"technology": tech, "version": version})
	}

	return nil
}
