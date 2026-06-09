package report

import (
	"context"
	"fmt"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
	"github.com/google/uuid"
)

// ReportAgent generates professional pentest reports using LLM.
type ReportAgent struct {
	provider      llm.Provider
	config        ReportAgentConfig
	skillInjector *skills.Injector
}

// ReportAgentConfig tunes the per-section generation parameters.
// All fields are optional; zero values fall back to the pre-P3-2
// hard-coded parameters so existing callers that build with
// NewReportAgent(provider) keep working unchanged.
type ReportAgentConfig struct {
	// ExecutiveSummaryMaxTokens / Temperature are applied to the
	// "executive_summary" section. 0 → 2048 / 0.3.
	ExecutiveSummaryMaxTokens int
	ExecutiveSummaryTemperature float64

	// RemediationMaxTokens / Temperature are applied to per-finding
	// remediation. 0 → 1024 / 0.2.
	RemediationMaxTokens    int
	RemediationTemperature  float64

	// NarrativeMaxTokens / Temperature are applied to the attack
	// narrative. 0 → 2048 / 0.3.
	NarrativeMaxTokens      int
	NarrativeTemperature    float64
}

// NewReportAgent creates a new report agent with the default config
// (pre-P3-2 hard-coded parameters).
func NewReportAgent(provider llm.Provider) *ReportAgent {
	return &ReportAgent{provider: provider, config: DefaultReportAgentConfig()}
}

// NewReportAgentWithConfig creates a new report agent with custom
// per-section parameters. Equivalent to NewReportAgent(provider) when
// cfg is the zero value.
func NewReportAgentWithConfig(provider llm.Provider, cfg ReportAgentConfig) *ReportAgent {
	return &ReportAgent{provider: provider, config: cfg}
}

// WithSkillInjector enables community skill recommendations in report
// sections (e.g. executive summary, remediation guidance).
func (r *ReportAgent) WithSkillInjector(inj *skills.Injector) {
	r.skillInjector = inj
}

// DefaultReportAgentConfig returns the pre-P3-2 hard-coded parameters
// as a struct, so the config object is observable / printable.
func DefaultReportAgentConfig() ReportAgentConfig {
	return ReportAgentConfig{
		ExecutiveSummaryMaxTokens:   2048,
		ExecutiveSummaryTemperature: 0.3,
		RemediationMaxTokens:        1024,
		RemediationTemperature:      0.2,
		NarrativeMaxTokens:          2048,
		NarrativeTemperature:        0.3,
	}
}

// Generate produces a full PentestReport from campaign data.
func (r *ReportAgent) Generate(ctx context.Context, campaign pipeline.Campaign, findings []pipeline.ClassifiedFinding, plan *pipeline.AttackPlan, results []pipeline.ExecutionResult) (*pipeline.PentestReport, error) {
	report := &pipeline.PentestReport{
		ID:          uuid.New(),
		CampaignID:  campaign.ID,
		Target:      campaign.Target,
		Objective:   campaign.Objective,
		GeneratedAt: time.Now(),
	}

	// Generate executive summary
	execSummary, err := r.generateSection(ctx, "executive_summary", campaign, findings)
	if err == nil {
		report.ExecutiveSummary = execSummary
	}

	// Generate finding writeups
	for _, f := range findings {
		reportFinding := pipeline.ReportFinding{
			ID:                 f.ID,
			Title:              f.Title,
			Severity:           f.Severity,
			CVSSScore:          f.CVSSScore,
			CVSSVector:         f.CVSSVector,
			Description:        f.Description,
			Evidence:           f.Evidence,
			AffectedComponents: []string{f.Target},
		}

		// Generate remediation for each finding
		remediation, err := r.generateRemediation(ctx, f)
		if err == nil {
			reportFinding.Remediation = remediation
		}

		report.Findings = append(report.Findings, reportFinding)
	}

	// Generate attack narrative
	if plan != nil {
		narrative, err := r.generateNarrative(ctx, campaign, plan, results)
		if err == nil {
			report.AttackNarrative = narrative
		}
	}

	// Build risk summary
	report.RiskSummary = buildRiskSummary(findings)

	// Generate remediation plan
	report.RemediationPlan = buildRemediationPlan(findings)

	return report, nil
}

func (r *ReportAgent) generateSection(ctx context.Context, section string, campaign pipeline.Campaign, findings []pipeline.ClassifiedFinding) (string, error) {
	var prompt string
	switch section {
	case "executive_summary":
		prompt = fmt.Sprintf(
			"Write a 2-3 paragraph executive summary for a penetration test report.\nTarget: %s\nObjective: %s\nTotal findings: %d critical, high-severity findings. Focus on business risk, not technical details. Write for a non-technical audience.",
			campaign.Target, campaign.Objective, len(findings),
		)
	default:
		return "", fmt.Errorf("unknown section: %s", section)
	}

	resp, err := r.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: r.systemPrompt("executive_summary"),
		Messages:     []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:    sectionMaxTokens(r.config.ExecutiveSummaryMaxTokens, 2048),
		Temperature:  sectionTemperature(r.config.ExecutiveSummaryTemperature, 0.3),
	})
	if err != nil {
		return "", err
	}

	return resp.Content, nil
}

func (r *ReportAgent) generateRemediation(ctx context.Context, finding pipeline.ClassifiedFinding) (string, error) {
	resp, err := r.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: "You are a security remediation expert. Provide specific, actionable remediation steps.",
		Messages: []llm.Message{
			{
				Role:    "user",
				Content: fmt.Sprintf("Provide remediation steps for this vulnerability:\nTitle: %s\nSeverity: %s\nCVSS: %.1f\nDescription: %s", finding.Title, finding.Severity, finding.CVSSScore, finding.Description),
			},
		},
		MaxTokens:   sectionMaxTokens(r.config.RemediationMaxTokens, 1024),
		Temperature: sectionTemperature(r.config.RemediationTemperature, 0.2),
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (r *ReportAgent) generateNarrative(ctx context.Context, campaign pipeline.Campaign, plan *pipeline.AttackPlan, results []pipeline.ExecutionResult) (string, error) {
	resp, err := r.provider.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: "You are a penetration testing report writer. Write the attack narrative as a sequence of events, telling the story of this penetration test from initial recon to final findings.",
		Messages: []llm.Message{
			{
				Role:    "user",
				Content: fmt.Sprintf("Write an attack narrative for target %s. %d attack paths were tested with %d execution results. Reasoning: %s", campaign.Target, len(plan.Paths), len(results), plan.Reasoning),
			},
		},
		MaxTokens:   sectionMaxTokens(r.config.NarrativeMaxTokens, 2048),
		Temperature: sectionTemperature(r.config.NarrativeTemperature, 0.3),
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// sectionMaxTokens returns the configured max-tokens, falling back to
// the supplied default when the config field is zero.
func sectionMaxTokens(configured, defaultValue int) int {
	if configured <= 0 {
		return defaultValue
	}
	return configured
}

// sectionTemperature returns the configured temperature, falling back
// to the supplied default when the config field is zero or negative.
// Negative values are explicitly treated as "use default" so a config
// typo (-0.1) doesn't accidentally freeze the sampler.
func sectionTemperature(configured, defaultValue float64) float64 {
	if configured <= 0 {
		return defaultValue
	}
	return configured
}

func buildRiskSummary(findings []pipeline.ClassifiedFinding) pipeline.RiskSummary {
	summary := pipeline.RiskSummary{}
	for _, f := range findings {
		switch f.Severity {
		case pipeline.SeverityCritical:
			summary.CriticalCount++
		case pipeline.SeverityHigh:
			summary.HighCount++
		case pipeline.SeverityMedium:
			summary.MediumCount++
		case pipeline.SeverityLow:
			summary.LowCount++
		case pipeline.SeverityInformational:
			summary.InfoCount++
		}
	}

	switch {
	case summary.CriticalCount > 0:
		summary.OverallRisk = "critical"
	case summary.HighCount > 0:
		summary.OverallRisk = "high"
	case summary.MediumCount > 0:
		summary.OverallRisk = "medium"
	default:
		summary.OverallRisk = "low"
	}

	return summary
}

func buildRemediationPlan(findings []pipeline.ClassifiedFinding) []pipeline.RemediationItem {
	var items []pipeline.RemediationItem
	for i, f := range findings {
		items = append(items, pipeline.RemediationItem{
			Priority: i + 1,
			Finding:  f.Title,
			Action:   "Remediate " + f.Title,
			Effort:   estimateEffort(f.Severity),
			Impact:   string(f.Severity),
		})
	}
	return items
}

func estimateEffort(severity pipeline.Severity) string {
	switch severity {
	case pipeline.SeverityCritical:
		return "immediate"
	case pipeline.SeverityHigh:
		return "1-2 days"
	case pipeline.SeverityMedium:
		return "1 week"
	default:
		return "low priority"
	}
}

// systemPrompt returns the base system prompt optionally enriched with
// community skill recommendations when a SkillInjector is configured.
func (r *ReportAgent) systemPrompt(section string) string {
	base := "You are a professional penetration testing report writer. Write clear, concise, and actionable content."
	if r.skillInjector == nil {
		return base
	}
	query := "pentest report " + section
	recs := r.skillInjector.ForObjective(query)
	if len(recs) == 0 {
		return base
	}
	return base + "\n" + skills.MustInject(recs)
}
