package engine

import (
	"context"
	"fmt"
	"time"

	aisafetypkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/aisafety"
	classifierpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/classifier"
	exploitpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/exploit"
	forensicspkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/forensics"
	mobilepkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/mobile"
	orchestratorpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/orchestrator"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/prompts"
	reconpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/recon"
	reportpkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/report"
	reversepkg "github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/reverse"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/scope"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/agents"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/tuning"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools"
	"github.com/google/uuid"
)

// RunSwarm executes a campaign using the stigmergic swarm (blackboard +
// scheduler) rather than the sequential 5-phase runner.
//
// It is intentionally API-compatible with Run so the CLI can flip between
// them via --swarm. The swarm path terminates via two conditions:
//
//   - the campaign context is cancelled (SIGINT, deadline, etc.), OR
//   - the per-campaign time budget elapses, after which the runner writes
//     CAMPAIGN_COMPLETE and waits for the report agent to finish.
//
// Budget is currently time-based: DefaultSwarmTimeBudget below. A future
// revision will watch for blackboard quiescence instead.
func (r *Runner) RunSwarm(ctx context.Context, cc CampaignConfig, onEvent EventCallback) error {
	start := time.Now()
	campaignID := uuid.New()

	scopeDef, err := buildScope(cc.Scope)
	if err != nil {
		return fmt.Errorf("invalid scope: %w", err)
	}

	campaign := pipeline.Campaign{
		ID:        campaignID,
		Name:      fmt.Sprintf("swarm-%s-%s", cc.Target, time.Now().Format("20060102-150405")),
		Target:    cc.Target,
		Objective: cc.Objective,
		Status:    pipeline.StatusPlanned,
		Mode:      pipeline.CampaignMode(cc.Mode),
		Scope: pipeline.ScopeDefinition{
			AllowedDomains: scopeDef.AllowedDomains,
			AllowedCIDRs:   scopeDef.AllowedCIDRs,
		},
		CreatedAt: time.Now(),
	}

	emit := func(eventType pipeline.EventType, agent, detail string) {
		if onEvent != nil {
			onEvent(pipeline.CampaignEvent{
				ID:         uuid.New(),
				CampaignID: campaignID,
				Timestamp:  time.Now(),
				EventType:  eventType,
				AgentName:  agent,
				Detail:     detail,
			})
		}
	}

	// Build LLM provider (shared by all agents for now; per-agent providers
	// are a drop-in via llm.NewAgentProvider once benchmarking proves it
	// pays off for cost/latency).
	orchestratorCfg := r.cfg.Orchestrator
	if cc.Provider != "" {
		orchestratorCfg.Provider = cc.Provider
	}
	if cc.APIKey != "" {
		orchestratorCfg.APIKey = cc.APIKey
	}
	rawProvider, err := prompts.NewProviderWithRetry(orchestratorCfg)
	if err != nil {
		return fmt.Errorf("failed to create LLM provider: %w", err)
	}
	// Wrap the provider with a cost meter so every Complete call feeds
	// both the live-spend events and the final ROI footer.
	meter := llm.NewMeter(orchestratorCfg.Model)
	provider := meter.Wrap(rawProvider)
	// Then run any observability decorator (LangFuse ObservingProvider,
	// etc.) on top of the cost-metered provider. The decorator
	// records the same Complete/Stream calls; meter + decorator
	// compose cleanly because they share the same llm.Provider
	// surface.
	if r.providerDecorator != nil {
		provider = r.providerDecorator(provider)
	}

	emit(pipeline.EventStateChange, "engine", "Swarm campaign initialized")

	// Live cost meter: on a ticker, emit current $ spend so --follow
	// surfaces it without each agent having to self-report.
	meterCtx, meterCancel := context.WithCancel(ctx)
	defer meterCancel()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-meterCtx.Done():
				return
			case <-t.C:
			}
			u, spent := meter.Snapshot()
			emit(pipeline.EventMilestone, "cost",
				fmt.Sprintf("spent $%.3f so far (%d in / %d cached / %d out)",
					spent, u.InputTokens, u.CacheReadInputTokens, u.OutputTokens))
		}
	}()

	// Always run cleanup on exit, including SIGINT/budget cancellation.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if rep := r.cleanup.RunCleanup(cleanupCtx, campaignID); rep != nil && rep.TotalCount > 0 {
			emit(pipeline.EventMilestone, "cleanup",
				fmt.Sprintf("Cleanup ran %d actions (%d executed, %d failed)",
					rep.TotalCount, len(rep.Executed), len(rep.Failed)))
		}
	}()

	// Build the blackboard. Memory-backed for now — Postgres variant is
	// selected in the CLI when a DB pool is available.
	board := blackboard.NewMemoryBoard(nil)

	// Build specialist agents (reusing the existing stack).
	coordinator := tools.NewCoordinator()
	reconOpts := []reconpkg.Option{
		reconpkg.WithErrorSink(func(err error) { emit(pipeline.EventError, "recon", err.Error()) }),
	}
	classifierOpts := []classifierpkg.Option{
		classifierpkg.WithErrorSink(func(err error) { emit(pipeline.EventError, "classifier", err.Error()) }),
	}
	if r.strict {
		reconOpts = append(reconOpts, reconpkg.WithStrict())
		classifierOpts = append(classifierOpts, classifierpkg.WithStrict())
	}
	// Wire community skill recommendations into every agent that supports
	// them so that system prompts are enriched with relevant capabilities
	// from the openclaw-sec-skills index.
	injector := skills.NewInjector(0)
	reconOpts = append(reconOpts, reconpkg.WithSkillInjector(injector))
	classifierOpts = append(classifierOpts, classifierpkg.WithSkillInjector(injector))
	reconInner := reconpkg.NewReconAgent(provider, coordinator, reconOpts...)
	classifierInner := classifierpkg.NewClassifierAgent(provider, classifierOpts...)
	exploitInner := exploitpkg.NewExploitAgent(provider, exploitpkg.WithExploitSkillInjector(injector))
	reportInner := reportpkg.NewReportAgent(provider)
	reportInner.WithSkillInjector(injector)
	renderer := reportpkg.NewRenderer()

	// New specialist agents (Phase 2: 4 → 9 agents).
	reverseInner := reversepkg.New(provider)
	reverseInner.WithSkillInjector(injector)
	mobileInner := mobilepkg.New(provider)
	mobileInner.WithSkillInjector(injector)
	forensicsInner := forensicspkg.New(provider)
	forensicsInner.WithSkillInjector(injector)
	aisafetyInner := aisafetypkg.New(provider)
	aisafetyInner.WithSkillInjector(injector)

	orchestratorAgent := orchestratorpkg.NewOrchestratorAgent(orchestratorpkg.OrchestratorConfig{
		Provider:      provider,
		SkillInjector: injector,
		EventSink: func(e pipeline.CampaignEvent) {
			emit(e.EventType, e.AgentName, e.Detail)
		},
	})

	executor := exploitpkg.NewExecutor(
		&scope.ScopeDefinition{AllowedDomains: scopeDef.AllowedDomains, AllowedCIDRs: scopeDef.AllowedCIDRs},
		r.cleanup,
		cc.DryRun,
	)
	if cc.Assist {
		executor = executor.WithConfirm(r.assist)
	}

	// Pheromone tuning: config file if present, else embedded defaults.
	// --exploration-bias on the CLI applies a multiplier at lookup time.
	tuningSettings, _ := tuning.Load("config/pheromones.yaml")
	tuningSettings = tuningSettings.WithBias(tuning.Bias(cc.ExplorationBias))

	swarmAgents := []swarm.Agent{
		agents.NewReconAgent(reconInner, &scope.ScopeDefinition{
			AllowedDomains: scopeDef.AllowedDomains,
			AllowedCIDRs:   scopeDef.AllowedCIDRs,
		}, campaignID, 1, tuningSettings),
		agents.NewClassifierAgent(classifierInner, campaignID, 3),
		agents.NewExploitAgent(exploitInner, executor, cc.Objective, campaignID, 2, cc.DryRun, tuningSettings),
		agents.NewReportAgent(reportInner, renderer, campaign, cc.OutputDir, cc.Format, cc.PublishThreshold,
			func(paths map[string]string) {
				for k, p := range paths {
					emit(pipeline.EventToolResult, "report", fmt.Sprintf("%s report: %s", k, p))
				}
			}).WithROI(func() float64 { _, s := meter.Snapshot(); return s }, nil),
		agents.NewReverseAgent(reverseInner, campaignID, 2, tuningSettings),
		agents.NewMobileAgent(mobileInner, campaignID, 2, tuningSettings),
		agents.NewForensicsAgent(forensicsInner, campaignID, 2, tuningSettings),
		agents.NewAISafetyAgent(aisafetyInner, campaignID, 2, tuningSettings),
		// Orchestrator monitors the board and injects strategy adjustments.
		agents.NewOrchestratorAgent(orchestratorAgent, campaignID, cc.Objective),
	}

	sched := swarm.NewScheduler(board, campaignID,
		swarm.WithEventSink(func(e swarm.Event) {
			switch e.Type {
			case "agent_started":
				emit(pipeline.EventToolCall, e.AgentName, fmt.Sprintf("handling %s", e.FindingID))
			case "agent_finished":
				emit(pipeline.EventToolResult, e.AgentName, fmt.Sprintf("done in %s", e.Detail))
			case "agent_error":
				emit(pipeline.EventError, e.AgentName, e.Detail)
			case "budget_exceeded":
				emit(pipeline.EventMilestone, "scheduler", "budget exceeded — winding down")
			case "campaign_complete":
				emit(pipeline.EventMilestone, "scheduler", "campaign complete signal received")
			}
		}),
	)
	for _, a := range swarmAgents {
		sched.Register(a)
	}

	// Seed the swarm. Without this nothing triggers.
	if err := agents.Seed(ctx, board, campaignID, cc.Target, cc.Objective, tuningSettings); err != nil {
		return fmt.Errorf("seed swarm: %w", err)
	}
	emit(pipeline.EventThought, "orchestrator", fmt.Sprintf("Swarm deployed against %s", cc.Target))

	// Drive the swarm. A separate goroutine writes CAMPAIGN_COMPLETE after
	// the time budget expires, so the report agent fires and the scheduler
	// exits cleanly.
	schedCtx, schedCancel := context.WithCancel(ctx)
	defer schedCancel()

	budget := DefaultSwarmTimeBudget
	go func() {
		select {
		case <-schedCtx.Done():
			return
		case <-time.After(budget):
			_ = agents.Seed
			_, _ = board.Write(schedCtx, blackboard.Finding{
				CampaignID:    campaignID,
				AgentName:     "engine",
				Type:          blackboard.TypeCampaignComplete,
				Target:        cc.Target,
				PheromoneBase: 1.0,
				HalfLifeSec:   300,
			})
		}
	}()

	if err := sched.Run(schedCtx); err != nil && err != context.Canceled {
		return fmt.Errorf("swarm scheduler: %w", err)
	}

	elapsed := time.Since(start).Round(time.Second)

	// Final cost summary. The ROI verdict (bounty value vs. spend) lands
	// at the bottom of the rendered report itself — see report.WithROI.
	u, spent := meter.Snapshot()
	emit(pipeline.EventMilestone, "cost",
		fmt.Sprintf("total spent $%.3f  (input %d, cached %d, output %d)",
			spent, u.InputTokens, u.CacheReadInputTokens, u.OutputTokens))

	emit(pipeline.EventMilestone, "orchestrator",
		fmt.Sprintf("Swarm campaign complete in %s — see ./reports", elapsed))
	return nil
}

// DefaultSwarmTimeBudget is the default wall-clock cap for a swarm campaign.
// Can be overridden at runtime via CampaignConfig / config later.
const DefaultSwarmTimeBudget = 20 * time.Minute
