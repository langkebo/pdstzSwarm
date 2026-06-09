package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/classifier"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/exploit"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/prompts"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/recon"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/agent/report"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/memory"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/scope"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools"
	"github.com/google/uuid"
)

// CampaignConfig holds everything needed to run a campaign.
type CampaignConfig struct {
	Target    string
	Scope     []string // domains and/or CIDRs
	Objective string
	Mode      string
	DryRun    bool
	OutputDir string
	Format    string
	Provider  string // override config provider
	APIKey    string // override config API key

	// CampaignID is the pre-existing campaign UUID from the API layer.
	// When set, the runner uses this ID instead of generating a new one,
	// ensuring event campaign_id fields match the API campaign ID.
	// When empty (e.g. CLI usage), the runner generates a new UUID.
	CampaignID string

	// Credentials extracted from the target string (e.g. "用户名xxx密码yyy").
	// Populated by parseCredentials before the pipeline starts.
	Credentials *ParsedCredentials

	// ExplorationBias scales pheromone weights in the swarm path.
	// "", "med" = default (1.0×); "low" = 0.7× (depth-first); "high" = 1.3× (breadth-first).
	ExplorationBias string

	// PublishThreshold is the minimum pheromone a finding must have to
	// appear in the final report. Default (0.5) is "bugbounty mode" —
	// only verified / not-superseded findings ship. 0.1 is "aggressive
	// mode" which includes suspected-but-unverified findings.
	PublishThreshold float64

	// Assist, when true, prompts the operator y/N before every executed
	// step. Designed for researchers running against fragile programs
	// where breaking the relationship is more costly than slower scans.
	// See cli.assistConfirm for the TTY implementation.
	Assist bool
}

// EventCallback is called for every campaign event (for TUI/streaming).
type EventCallback func(event pipeline.CampaignEvent)

// Runner executes a full campaign pipeline.
type Runner struct {
	cfg         *config.Config
	memoryStore *memory.MemoryStore
	cleanup     pipeline.CleanupRegistryIface
	strict      bool
	assist      exploit.ConfirmFunc // optional; nil = no human-in-the-loop

	// providerDecorator wraps the raw LLM provider after the
	// runner builds it. Used by the observability layer to
	// install decorators (e.g. LangFuse ObservingProvider)
	// without the engine package taking a direct dependency on
	// the observability packages.
	providerDecorator func(llm.Provider) llm.Provider
}

// Option customises Runner construction.
type Option func(*Runner)

// WithCleanupRegistry attaches a cleanup registry (Postgres or memory).
// If no option is passed, the runner falls back to an in-memory registry
// that executes cleanup commands via /bin/sh -c.
func WithCleanupRegistry(reg pipeline.CleanupRegistryIface) Option {
	return func(r *Runner) { r.cleanup = reg }
}

// WithStrictLLM turns any LLM error into a fatal campaign failure.
// Without strict mode, the runner continues with degraded output but
// emits error events to the stream.
func WithStrictLLM() Option {
	return func(r *Runner) { r.strict = true }
}

// WithAssistConfirmer wires a human-in-the-loop hook that's called
// before every executed step in assist mode (4.6.4). Only fires when
// CampaignConfig.Assist is also true — the option installs the
// callback; the flag turns it on.
func WithAssistConfirmer(fn exploit.ConfirmFunc) Option {
	return func(r *Runner) { r.assist = fn }
}

// WithLLMProviderDecorator registers a function that wraps the
// raw LLM provider after the runner builds it. The wrapper sees
// the cost-metered provider, so it can decorate or replace it.
//
// Used by the observability layer: the LangFuse ObservingProvider
// records every Complete / Stream call's token usage on top of the
// existing cost meter. Pass nil to disable.
func WithLLMProviderDecorator(fn func(llm.Provider) llm.Provider) Option {
	return func(r *Runner) { r.providerDecorator = fn }
}

// WithLLMMetrics installs the Prometheus metrics decorator on
// every LLM provider the runner builds. The bundle is shared
// with the API server (constructed in cli/serve.go via
// NewServerWithObservability) so the LLM call counts land in
// the same /metrics endpoint as the HTTP request counts.
//
// The decorator wraps the cost-metered provider on top of the
// user-supplied WithLLMProviderDecorator (if any). The chain is:
//
//	rawProvider → costMeter → userDecorator → metricsDecorator
//
// The metrics decorator is *always* the outermost wrapper so
// the latency histogram captures the full observed call time
// (including any cost-meter or user-decorator overhead).
func WithLLMMetrics(bundle MetricsBundle) Option {
	return func(r *Runner) {
		prev := r.providerDecorator
		r.providerDecorator = func(p llm.Provider) llm.Provider {
			if prev != nil {
				p = prev(p)
			}
			return bundle.Wrap(p)
		}
	}
}

// MetricsBundle is the minimal contract the engine package needs
// to install a metrics decorator on the LLM provider. We don't
// take a direct dependency on internal/observability/appmetrics
// to avoid a cycle (engine is imported by api, which is imported
// by appmetrics). The interface is satisfied by
// *appmetrics.All; see cli/serve.go for the wiring.
type MetricsBundle interface {
	Wrap(llm.Provider) llm.Provider
}

// NewRunner creates a campaign runner.
func NewRunner(cfg *config.Config, opts ...Option) *Runner {
	r := &Runner{
		cfg:         cfg,
		memoryStore: memory.NewMemoryStore(),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.cleanup == nil {
		r.cleanup = pipeline.NewMemoryCleanupRegistry(pipeline.DefaultCleanupExec)
	}
	return r
}

// Run executes a complete penetration test campaign.
func (r *Runner) Run(ctx context.Context, cc CampaignConfig, onEvent EventCallback) error {
	start := time.Now()

	// Parse credentials from target string (e.g. "https://example.com 用户名xxx密码yyy")
	// and clean the target URL so tools receive a proper URL.
	cleanTarget, creds := parseCredentials(cc.Target)
	if creds != nil {
		cc.Credentials = creds
		cc.Target = cleanTarget
	}

	// Use the pre-existing campaign ID from the API layer when available,
	// otherwise generate a new UUID (CLI / standalone usage).
	var campaignID uuid.UUID
	if cc.CampaignID != "" {
		parsed, err := uuid.Parse(cc.CampaignID)
		if err != nil {
			return fmt.Errorf("invalid campaign ID: %w", err)
		}
		campaignID = parsed
	} else {
		campaignID = uuid.New()
	}

	// Build scope definition
	scopeDef, err := buildScope(cc.Scope)
	if err != nil {
		return fmt.Errorf("invalid scope: %w", err)
	}

	// Build campaign metadata (for report generation). When the campaign
	// ID came from the API layer, we construct a lightweight campaign
	// object here rather than duplicating the full API state.
	// Strip protocol prefixes from the target so the name is
	// filesystem-safe (e.g. "https://example.com" → "example.com").
	safeTarget := sanitizeTarget(cc.Target)
	campaign := pipeline.Campaign{
		ID:        campaignID,
		Name:      fmt.Sprintf("scan-%s-%s", safeTarget, time.Now().Format("20060102-150405")),
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

	// Emit credential detection event if credentials were parsed from the target
	// or provided via API fields
	if creds != nil {
		emit(pipeline.EventThought, "engine", fmt.Sprintf("Credentials detected — username: %s, target: %s", creds.Username, cleanTarget))
	} else if cc.Credentials != nil {
		emit(pipeline.EventThought, "engine", fmt.Sprintf("Credentials provided — username: %s, target: %s", cc.Credentials.Username, cc.Target))
	}

	// State machine
	sm := pipeline.NewStateMachine(&campaign, func(e pipeline.CampaignEvent) {
		if onEvent != nil {
			onEvent(e)
		}
	})

	// Initialize LLM provider
	orchestratorCfg := r.cfg.Orchestrator
	if cc.Provider != "" {
		orchestratorCfg.Provider = cc.Provider
	}
	if cc.APIKey != "" {
		orchestratorCfg.APIKey = cc.APIKey
	}

	provider, err := prompts.NewProviderWithRetry(orchestratorCfg)
	if err != nil {
		return fmt.Errorf("failed to create LLM provider: %w", err)
	}

	// Wire community skill recommendations into every agent that supports
	// them so system prompts are enriched with relevant capabilities from
	// the openclaw-sec-skills index.
	injector := skills.NewInjector(0)

	emit(pipeline.EventStateChange, "engine", "Campaign initialized")

	// Always run registered cleanup on exit — normal completion, failure,
	// or context cancellation (SIGINT). Uses a detached context so cleanup
	// still runs after the campaign context is cancelled.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if rep := r.cleanup.RunCleanup(cleanupCtx, campaignID); rep != nil && rep.TotalCount > 0 {
			emit(pipeline.EventMilestone, "cleanup",
				fmt.Sprintf("Cleanup ran %d actions (%d executed, %d failed)",
					rep.TotalCount, len(rep.Executed), len(rep.Failed)))
		}
	}()

	// --- Phase 1: RECON ---
	if err := sm.Start(); err != nil {
		return err
	}
	if err := sm.BeginRecon(); err != nil {
		return err
	}

	emit(pipeline.EventThought, "orchestrator", fmt.Sprintf("Starting reconnaissance on %s", cc.Target))

	// Build tool options from config so tools inherit the
	// configured timeouts instead of using hard-coded defaults.
	toolOpts := tools.Options{
		"timeout": r.cfg.Tools.DefaultTimeout,
		"depth":   r.cfg.Tools.Katana.Depth,
	}
	if len(r.cfg.Tools.Nuclei.Severity) > 0 {
		toolOpts["severity"] = r.cfg.Tools.Nuclei.Severity
	}
	// Pass parsed credentials to tools for authenticated scanning
	if cc.Credentials != nil {
		toolOpts["username"] = cc.Credentials.Username
		toolOpts["password"] = cc.Credentials.Password
	}

	coordinator := tools.NewCoordinator()
	coordinator.SetHooks(&tools.ToolHooks{
		OnSkip: func(name, target, reason string) {
			emit(pipeline.EventToolResult, "recon", fmt.Sprintf("Tool %s skipped: %s", name, reason))
		},
		OnDone: func(name, target string, result *tools.ToolResult, err error) {
			if err != nil {
				emit(pipeline.EventToolResult, "recon", fmt.Sprintf("Tool %s error: %v", name, err))
			} else if result != nil && result.Error != nil {
				emit(pipeline.EventToolResult, "recon", fmt.Sprintf("Tool %s failed: %v", name, result.Error))
			} else if result != nil {
				outputLen := len(result.RawOutput)
				emit(pipeline.EventToolResult, "recon", fmt.Sprintf("Tool %s completed: %d bytes output", name, outputLen))
			}
		},
	})

	reconOpts := []recon.Option{
		recon.WithErrorSink(func(err error) {
			emit(pipeline.EventError, "recon", err.Error())
		}),
	}
	if r.strict {
		reconOpts = append(reconOpts, recon.WithStrict())
	}
	reconOpts = append(reconOpts, recon.WithSkillInjector(injector))
	reconAgent := recon.NewReconAgent(provider, coordinator, reconOpts...)
	reconPlan := reconAgent.PlanRecon(cc.Target)

	emit(pipeline.EventToolCall, "recon", fmt.Sprintf("Running tools: %s", strings.Join(reconPlan.ToolOrder, ", ")))

	surface, err := reconAgent.Execute(ctx, reconPlan, &scope.ScopeDefinition{
		AllowedDomains: scopeDef.AllowedDomains,
		AllowedCIDRs:   scopeDef.AllowedCIDRs,
	}, campaignID, toolOpts)
	if err != nil {
		emit(pipeline.EventError, "recon", fmt.Sprintf("Recon failed: %s", err))
		sm.Fail("recon failed")
		return fmt.Errorf("recon failed: %w", err)
	}

	emit(pipeline.EventToolResult, "recon", fmt.Sprintf("Found %d subdomains, %d hosts, %d endpoints",
		len(surface.Subdomains), len(surface.Hosts), len(surface.Endpoints)))

	// Expand scope with IPs discovered during reconnaissance.
	// Without this, subsequent phases (exploit) will fail with scope
	// violations when trying to target the IPs that were found.
	for _, host := range surface.Hosts {
		if host.IP != "" {
			// Add each discovered IP as a /32 CIDR
			cidr := host.IP + "/32"
			found := false
			for _, existing := range scopeDef.AllowedCIDRs {
				if existing == cidr {
					found = true
					break
				}
			}
			if !found {
				scopeDef.AllowedCIDRs = append(scopeDef.AllowedCIDRs, cidr)
			}
		}
		// Also add discovered hostnames to allowed domains
		for _, hostname := range host.Hostnames {
			found := false
			for _, existing := range scopeDef.AllowedDomains {
				if existing == hostname {
					found = true
					break
				}
			}
			if !found && hostname != "" {
				scopeDef.AllowedDomains = append(scopeDef.AllowedDomains, hostname)
			}
		}
	}
	// Also add discovered subdomains
	for _, sub := range surface.Subdomains {
		if sub.Domain != "" {
			found := false
			for _, existing := range scopeDef.AllowedDomains {
				if existing == sub.Domain {
					found = true
					break
				}
			}
			if !found {
				scopeDef.AllowedDomains = append(scopeDef.AllowedDomains, sub.Domain)
			}
		}
	}

	// --- Phase 2: CLASSIFY ---
	if err := sm.BeginClassifying(); err != nil {
		return err
	}

	emit(pipeline.EventThought, "orchestrator", "Classifying findings — mapping CVEs, scoring CVSS, filtering false positives")

	classifierOpts := []classifier.Option{
		classifier.WithErrorSink(func(err error) {
			emit(pipeline.EventError, "classifier", err.Error())
		}),
	}
	if r.strict {
		classifierOpts = append(classifierOpts, classifier.WithStrict())
	}
	classifierOpts = append(classifierOpts, classifier.WithSkillInjector(injector))
	classifierAgent := classifier.NewClassifierAgent(provider, classifierOpts...)

	// Build raw findings from attack surface
	rawFindings := extractRawFindings(surface, campaignID)

	emit(pipeline.EventToolCall, "classifier", fmt.Sprintf("Classifying %d raw findings", len(rawFindings)))

	findingSet, err := classifierAgent.Classify(ctx, campaignID, rawFindings)
	if err != nil {
		emit(pipeline.EventError, "classifier", fmt.Sprintf("Classification failed: %s", err))
		sm.Fail("classification failed")
		return fmt.Errorf("classification failed: %w", err)
	}

	for _, f := range findingSet.Findings {
		// Carry the full finding in event.Data so the API / dashboard can
		// render severity and CVSS without re-parsing the detail string.
		data, _ := json.Marshal(f)
		if onEvent != nil {
			onEvent(pipeline.CampaignEvent{
				ID:         uuid.New(),
				CampaignID: campaignID,
				Timestamp:  time.Now(),
				EventType:  pipeline.EventFindingDiscovered,
				AgentName:  "classifier",
				Detail:     fmt.Sprintf("[%s] %s (CVSS: %.1f) on %s", strings.ToUpper(string(f.Severity)), f.Title, f.CVSSScore, f.Target),
				Data:       data,
			})
		}
	}

	emit(pipeline.EventToolResult, "classifier", fmt.Sprintf("Classified %d findings (%d filtered as FP). Severity: %d critical, %d high, %d medium",
		findingSet.Summary.TotalFindings, findingSet.Summary.FilteredAsFP,
		findingSet.Summary.BySeverity[pipeline.SeverityCritical],
		findingSet.Summary.BySeverity[pipeline.SeverityHigh],
		findingSet.Summary.BySeverity[pipeline.SeverityMedium]))

	// --- Phase 3: PLAN ---
	if err := sm.BeginPlanning(); err != nil {
		return err
	}

	emit(pipeline.EventThought, "orchestrator", "Building attack plan — constructing exploitation chains")

	exploitAgent := exploit.NewExploitAgent(provider, exploit.WithExploitSkillInjector(injector))

	var attackPlan *pipeline.AttackPlan
	if !cc.DryRun && len(findingSet.Findings) > 0 {
		attackPlan, err = exploitAgent.BuildPlan(ctx, *findingSet, cc.Objective)
		if err != nil {
			emit(pipeline.EventError, "exploit", fmt.Sprintf("Plan construction failed: %s", err))
		} else {
			emit(pipeline.EventToolResult, "exploit", fmt.Sprintf("Built %d attack paths. Top path: %s (%.0f%% estimated success)",
				len(attackPlan.Paths),
				pathName(attackPlan),
				pathProb(attackPlan)*100))
		}
	}

	// --- Phase 4: EXECUTE (if not dry-run) ---
	var execResults []pipeline.ExecutionResult
	if !cc.DryRun && attackPlan != nil && len(attackPlan.Paths) > 0 {
		if err := sm.BeginExecuting(); err != nil {
			return err
		}

		emit(pipeline.EventThought, "orchestrator", "Executing top attack paths")

		executor := exploit.NewExecutor(
			&scope.ScopeDefinition{AllowedDomains: scopeDef.AllowedDomains, AllowedCIDRs: scopeDef.AllowedCIDRs},
			r.cleanup,
			cc.DryRun,
		)

		for _, path := range attackPlan.Paths[:min(3, len(attackPlan.Paths))] {
			for _, step := range path.Steps {
				if step.Command == "" {
					continue
				}
				emit(pipeline.EventStepExecuted, "exploit", fmt.Sprintf("Executing: %s", step.Name))

				result, err := executor.Execute(ctx, step, campaignID)
				if err != nil {
					emit(pipeline.EventError, "exploit", fmt.Sprintf("Step failed: %s", err))
					continue
				}
				execResults = append(execResults, *result)

				if result.Success {
					emit(pipeline.EventToolResult, "exploit", fmt.Sprintf("Step succeeded: %s", step.Name))
				} else {
					emit(pipeline.EventToolResult, "exploit", fmt.Sprintf("Step failed: %s", step.Name))
				}
			}
		}
	}

	// --- Phase 5: REPORT ---
	if err := sm.BeginReporting(); err != nil {
		return err
	}

	emit(pipeline.EventThought, "orchestrator", "Generating penetration test report")

	reportAgent := report.NewReportAgent(provider)
	reportAgent.WithSkillInjector(injector)
	pentestReport, err := reportAgent.Generate(ctx, campaign, findingSet.Findings, attackPlan, execResults)
	if err != nil {
		emit(pipeline.EventError, "report", fmt.Sprintf("Report generation failed: %s", err))
		sm.Fail("report generation failed")
		return fmt.Errorf("report generation failed: %w", err)
	}

	// Render and save report
	renderer := report.NewRenderer()
	outputDir := cc.OutputDir
	if outputDir == "" {
		outputDir = "./reports"
	}
	os.MkdirAll(outputDir, 0755)

	// Sanitize the campaign name for use as a filename: strip the
	// protocol prefix ("https://", "http://") and replace any
	// remaining path separators / special chars with safe dashes.
	// Without this, "https://www.pdsu.edu.cn" → "https:/www..."
	// after filepath.Join, which creates phantom directories.
	safeName := sanitizeFilename(campaign.Name)
	reportPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s", safeName, campaignID.String()[:8]))

	if cc.Format == "all" || cc.Format == "md" || cc.Format == "" {
		md, _ := renderer.ToMarkdown(pentestReport)
		os.WriteFile(reportPath+".md", md, 0644)
		emit(pipeline.EventToolResult, "report", fmt.Sprintf("Markdown report: %s.md", reportPath))
	}
	if cc.Format == "all" || cc.Format == "json" {
		js, _ := renderer.ToJSON(pentestReport)
		os.WriteFile(reportPath+".json", js, 0644)
		emit(pipeline.EventToolResult, "report", fmt.Sprintf("JSON report: %s.json", reportPath))
	}
	if cc.Format == "all" || cc.Format == "html" {
		html, _ := renderer.ToHTML(pentestReport)
		os.WriteFile(reportPath+".html", html, 0644)
		emit(pipeline.EventToolResult, "report", fmt.Sprintf("HTML report: %s.html", reportPath))
	}
	if cc.Format == "all" || cc.Format == "sarif" {
		if sarif, err := renderer.ToSARIF(pentestReport); err == nil {
			os.WriteFile(reportPath+".sarif", sarif, 0644)
			emit(pipeline.EventToolResult, "report", fmt.Sprintf("SARIF report: %s.sarif", reportPath))
		}
	}

	// Complete
	sm.Complete()

	elapsed := time.Since(start).Round(time.Second)
	emit(pipeline.EventMilestone, "orchestrator", fmt.Sprintf(
		"Campaign complete in %s. %d findings (%d critical, %d high). Risk: %s. Report: %s",
		elapsed, len(findingSet.Findings),
		findingSet.Summary.BySeverity[pipeline.SeverityCritical],
		findingSet.Summary.BySeverity[pipeline.SeverityHigh],
		pentestReport.RiskSummary.OverallRisk,
		reportPath))

	// Save learned patterns to memory
	patterns := memory.ExtractPatterns(surface, findingSet.Findings)
	for _, p := range patterns {
		r.memoryStore.Save(p)
	}

	return nil
}

// --- Helpers ---

func buildScope(scopes []string) (*scope.ScopeDefinition, error) {
	def := &scope.ScopeDefinition{}
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// If it contains a /, treat as CIDR
		if strings.Contains(s, "/") {
			def.AllowedCIDRs = append(def.AllowedCIDRs, s)
		} else {
			def.AllowedDomains = append(def.AllowedDomains, s)
		}
	}
	if len(def.AllowedCIDRs) == 0 && len(def.AllowedDomains) == 0 {
		return nil, fmt.Errorf("scope must contain at least one domain or CIDR")
	}
	return def, nil
}

func extractRawFindings(surface *pipeline.AttackSurface, campaignID uuid.UUID) []pipeline.RawFinding {
	var findings []pipeline.RawFinding
	seen := make(map[string]bool) // dedup key → true

	for _, host := range surface.Hosts {
		for _, port := range host.OpenPorts {
			svc := host.Services[port]
			detail := fmt.Sprintf("Port %d open", port)
			if svc.Name != "" {
				detail = fmt.Sprintf("Port %d open — %s %s", port, svc.Name, svc.Version)
			}
			dedupKey := fmt.Sprintf("port:%s:%d", host.IP, port)
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true
			findings = append(findings, pipeline.RawFinding{
				ID:           uuid.New(),
				CampaignID:   campaignID,
				Source:       "naabu",
				Type:         "open_port",
				Target:       host.IP,
				Detail:       detail,
				DiscoveredAt: time.Now(),
			})
		}
	}

	for _, ep := range surface.Endpoints {
		if ep.Interesting {
			dedupKey := fmt.Sprintf("ep:%s", ep.URL)
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true
			findings = append(findings, pipeline.RawFinding{
				ID:           uuid.New(),
				CampaignID:   campaignID,
				Source:       "katana",
				Type:         "interesting_endpoint",
				Target:       ep.URL,
				Detail:       fmt.Sprintf("Interesting endpoint: %s (%s) — %s", ep.URL, ep.Method, ep.Notes),
				DiscoveredAt: time.Now(),
			})
		}
	}

	for tech, version := range surface.Technologies {
		dedupKey := fmt.Sprintf("tech:%s", tech)
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		findings = append(findings, pipeline.RawFinding{
			ID:           uuid.New(),
			CampaignID:   campaignID,
			Source:       "httpx",
			Type:         "technology",
			Target:       surface.Target,
			Detail:       fmt.Sprintf("Technology detected: %s %s", tech, version),
			DiscoveredAt: time.Now(),
		})
	}

	return findings
}

func pathName(plan *pipeline.AttackPlan) string {
	if len(plan.Paths) > 0 {
		return plan.Paths[0].Name
	}
	return "none"
}

func pathProb(plan *pipeline.AttackPlan) float64 {
	if len(plan.Paths) > 0 {
		return plan.Paths[0].EstimatedSuccessProbability
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sanitizeFilename replaces characters that are unsafe in file names
// with dashes. It handles the common case of URL targets leaking into
// campaign names (e.g. "scan-https://www.example.com" → "scan-https-www.example.com").
func sanitizeFilename(name string) string {
	// Strip protocol prefixes that contain "://"
	replacer := strings.NewReplacer(
		"://", "-",
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
	)
	return replacer.Replace(name)
}

// sanitizeTarget strips the protocol prefix from a URL target
// so it can be safely used in file names.
// e.g. "https://www.example.com" → "www.example.com"
func sanitizeTarget(target string) string {
	t := target
	t = strings.TrimPrefix(t, "https://")
	t = strings.TrimPrefix(t, "http://")
	// Remove credential suffixes like " 用户名xxx密码yyy"
	if idx := strings.IndexFunc(t, func(r rune) bool { return r == ' ' || r == '\t' }); idx != -1 {
		t = t[:idx]
	}
	return t
}

// ParsedCredentials holds username/password extracted from the target string.
type ParsedCredentials struct {
	Username string
	Password string
}

// parseCredentials extracts credentials from a target string that contains
// patterns like "用户名xxx密码yyy" or "username:xxx password:yyy" and returns
// the cleaned URL and the extracted credentials.
func parseCredentials(target string) (cleanURL string, creds *ParsedCredentials) {
	cleanURL = target
	creds = nil

	// Pattern 1: Chinese "用户名xxx密码yyy"
	if idx := strings.Index(target, "用户名"); idx != -1 {
		rest := target[idx+len("用户名"):]
		username := rest
		password := ""

		if pidx := strings.Index(rest, "密码"); pidx != -1 {
			username = rest[:pidx]
			password = rest[pidx+len("密码"):]
		}

		cleanURL = strings.TrimSpace(target[:idx])
		creds = &ParsedCredentials{
			Username: strings.TrimSpace(username),
			Password: strings.TrimSpace(password),
		}
		return
	}

	// Pattern 2: English "username:xxx password:yyy" or "user:xxx pass:yyy"
	lower := strings.ToLower(target)
	for _, prefix := range []string{"username:", "user:", "username "} {
		if idx := strings.Index(lower, prefix); idx != -1 {
			rest := target[idx+len(prefix):]
			username := rest
			password := ""

			for _, pp := range []string{"password:", "pass:", "password ", "pass "} {
				if pidx := strings.Index(strings.ToLower(rest), pp); pidx != -1 {
					username = rest[:pidx]
					password = rest[pidx+len(pp):]
					break
				}
			}

			cleanURL = strings.TrimSpace(target[:idx])
			creds = &ParsedCredentials{
				Username: strings.TrimSpace(username),
				Password: strings.TrimSpace(password),
			}
			return
		}
	}

	return
}

// Ensure json import is used (for future DB persistence)
var _ = json.Marshal
