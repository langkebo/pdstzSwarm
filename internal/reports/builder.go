package reports

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Builder renders a BuildInput into a fully-formed Report (Markdown +
// structured sections + summary). It is intentionally pure: given the
// same input it produces byte-identical Markdown, which means golden
// files in tests and reproducible reports across regenerations.
type Builder struct {
	// Now allows tests to inject a deterministic clock. If nil,
	// time.Now() is used.
	Now func() time.Time
}

// NewBuilder creates a Builder with the wall clock.
func NewBuilder() *Builder {
	return &Builder{Now: time.Now}
}

// Build produces a Report from the given input. It does not persist;
// the service layer is responsible for storing the result.
//
// Returns a Report with Status=StatusReady and a fully rendered
// Markdown body. If the input has zero findings, the report is still
// generated (with a "no findings" section) — operators want to know
// that a clean run happened.
func (b *Builder) Build(in BuildInput) (*Report, error) {
	if in.Campaign.ID == uuid.Nil {
		return nil, fmt.Errorf("reports: campaign id is required")
	}
	now := b.now()

	report := &Report{
		ID:         uuid.New(),
		CampaignID: in.Campaign.ID,
		Title:      b.composeTitle(in),
		Status:     StatusReady,
		Format:     FormatMarkdown,
		CreatedAt:  now,
		CompletedAt: &now,
	}

	sections := b.composeSections(in)
	report.Sections = sections
	report.Summary = b.composeSummary(in, sections)
	report.Markdown = b.composeMarkdown(in, sections, report.Summary)
	report.ByteSize = len(report.Markdown)

	return report, nil
}

func (b *Builder) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Builder) composeTitle(in BuildInput) string {
	if in.Campaign.Name != "" {
		return fmt.Sprintf("Penetration Test Report — %s", in.Campaign.Name)
	}
	return fmt.Sprintf("Penetration Test Report — %s", in.Campaign.Target)
}

// composeSections returns the report chapters in stable order. Each
// section is returned as a struct so the front-end can render them
// independently (e.g. an "executive summary" card on the list page).
func (b *Builder) composeSections(in BuildInput) []Section {
	secs := []Section{
		{Key: "metadata", Title: "Campaign Metadata", Order: 1, Body: b.renderMetadata(in)},
		{Key: "executive_summary", Title: "Executive Summary", Order: 2, Body: b.renderExecutiveSummary(in)},
		{Key: "risk_overview", Title: "Risk Overview", Order: 3, Body: b.renderRiskOverview(in)},
		{Key: "findings", Title: "Detailed Findings", Order: 4, Body: b.renderFindings(in)},
		{Key: "remediation", Title: "Remediation Roadmap", Order: 5, Body: b.renderRemediation(in)},
		{Key: "appendix", Title: "Appendix", Order: 6, Body: b.renderAppendix(in)},
	}
	return secs
}

func (b *Builder) composeSummary(in BuildInput, secs []Section) Summary {
	summary := Summary{
		TotalFindings: len(in.Findings),
		HasRemediation: false,
	}

	sevCounts := map[string]int{}
	cvssTotal := 0.0
	cvssCount := 0
	targets := map[string]struct{}{}
	agents := map[string]struct{}{}

	for _, f := range in.Findings {
		sev := strings.ToLower(f.Severity)
		switch sev {
		case "critical":
			summary.CriticalCount++
		case "high":
			summary.HighCount++
		case "medium":
			summary.MediumCount++
		case "low":
			summary.LowCount++
		case "informational", "info":
			summary.InfoCount++
		}
		sevCounts[sev]++

		if f.CVSSScore > 0 {
			cvssTotal += f.CVSSScore
			cvssCount++
		}
		if f.Target != "" {
			targets[f.Target] = struct{}{}
		}
		if f.AgentName != "" {
			agents[f.AgentName] = struct{}{}
		}
		if f.HasRemediation {
			summary.HasRemediation = true
		}
	}

	if cvssCount > 0 {
		summary.AverageCVSS = cvssTotal / float64(cvssCount)
	}
	summary.UniqueTargets = len(targets)
	summary.UniqueAgents = len(agents)
	summary.OverallRisk = computeOverallRisk(summary)

	if in.Campaign.StartedAt != nil {
		end := in.GeneratedAt
		if in.Campaign.CompletedAt != nil && !in.Campaign.CompletedAt.IsZero() {
			end = *in.Campaign.CompletedAt
		}
		summary.DurationSeconds = end.Sub(*in.Campaign.StartedAt).Seconds()
		if summary.DurationSeconds < 0 {
			summary.DurationSeconds = 0
		}
	}

	return summary
}

func computeOverallRisk(s Summary) string {
	switch {
	case s.CriticalCount > 0:
		return "critical"
	case s.HighCount >= 3:
		return "high"
	case s.HighCount > 0:
		return "high"
	case s.MediumCount >= 3:
		return "medium"
	case s.MediumCount > 0:
		return "low"
	case s.LowCount > 0 || s.InfoCount > 0:
		return "low"
	default:
		return "informational"
	}
}

func (b *Builder) composeMarkdown(in BuildInput, secs []Section, summary Summary) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", b.composeTitle(in)))
	sb.WriteString(fmt.Sprintf("_Generated at %s by %s_\n\n", in.GeneratedAt.UTC().Format(time.RFC3339), defaultStr(in.Generator, "pentest-swarm-ai")))

	// Table of contents
	sb.WriteString("## Table of Contents\n\n")
	for _, s := range secs {
		sb.WriteString(fmt.Sprintf("- [%s](#%s)\n", s.Title, anchorID(s.Title)))
	}
	sb.WriteString("\n---\n\n")

	// Sections in order
	for _, s := range secs {
		sb.WriteString(fmt.Sprintf("## %s\n\n", s.Title))
		sb.WriteString(s.Body)
		if !strings.HasSuffix(s.Body, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Footer
	sb.WriteString("---\n\n")
	sb.WriteString(fmt.Sprintf(
		"_Report SHA is not embedded to keep the document git-friendly. "+
			"Total findings: %d • Risk: %s • Duration: %s_\n",
		summary.TotalFindings,
		summary.OverallRisk,
		humanDuration(summary.DurationSeconds),
	))

	return sb.String()
}

func (b *Builder) renderMetadata(in BuildInput) string {
	c := in.Campaign
	var sb strings.Builder
	sb.WriteString("| Field | Value |\n")
	sb.WriteString("| --- | --- |\n")
	sb.WriteString(fmt.Sprintf("| Campaign ID | `%s` |\n", c.ID))
	sb.WriteString(fmt.Sprintf("| Name | %s |\n", mdEscape(c.Name)))
	sb.WriteString(fmt.Sprintf("| Target | `%s` |\n", mdEscape(c.Target)))
	sb.WriteString(fmt.Sprintf("| Objective | %s |\n", mdEscape(c.Objective)))
	sb.WriteString(fmt.Sprintf("| Mode | %s |\n", mdEscape(c.Mode)))
	sb.WriteString(fmt.Sprintf("| Status | %s |\n", mdEscape(c.Status)))
	sb.WriteString(fmt.Sprintf("| LLM Provider | %s |\n", mdEscape(c.Provider)))
	sb.WriteString(fmt.Sprintf("| Created | %s |\n", c.CreatedAt.UTC().Format(time.RFC3339)))
	if c.StartedAt != nil {
		sb.WriteString(fmt.Sprintf("| Started | %s |\n", c.StartedAt.UTC().Format(time.RFC3339)))
	}
	if c.CompletedAt != nil {
		sb.WriteString(fmt.Sprintf("| Completed | %s |\n", c.CompletedAt.UTC().Format(time.RFC3339)))
	}
	return sb.String()
}

func (b *Builder) renderExecutiveSummary(in BuildInput) string {
	summary := b.composeSummary(in, nil)
	verdict := pickVerdict(summary)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Overall Risk: %s.** %s\n\n", strings.ToUpper(summary.OverallRisk), verdict))
	sb.WriteString("This report consolidates the findings produced by the Pentest-Swarm-AI ")
	sb.WriteString("stigmergic swarm during the campaign. ")
	sb.WriteString(fmt.Sprintf("Across %d finding(s) the swarm identified ", summary.TotalFindings))
	sb.WriteString(fmt.Sprintf("%d critical, %d high, %d medium, %d low, and %d informational issues. ",
		summary.CriticalCount, summary.HighCount, summary.MediumCount, summary.LowCount, summary.InfoCount))
	if summary.UniqueTargets > 0 {
		sb.WriteString(fmt.Sprintf("Findings touch %d unique target(s) ", summary.UniqueTargets))
	}
	if summary.UniqueAgents > 0 {
		sb.WriteString(fmt.Sprintf("and were contributed by %d agent(s). ", summary.UniqueAgents))
	}
	sb.WriteString("\n\n")
	if summary.AverageCVSS > 0 {
		sb.WriteString(fmt.Sprintf("Average CVSS across scored findings: **%.2f**.\n\n", summary.AverageCVSS))
	}
	if summary.DurationSeconds > 0 {
		sb.WriteString(fmt.Sprintf("Campaign duration: **%s**.\n\n", humanDuration(summary.DurationSeconds)))
	}
	return sb.String()
}

func (b *Builder) renderRiskOverview(in BuildInput) string {
	summary := b.composeSummary(in, nil)
	var sb strings.Builder
	sb.WriteString("```\n")
	sb.WriteString("Risk Distribution\n")
	sb.WriteString("─────────────────\n")
	sb.WriteString(fmt.Sprintf("CRITICAL  %3d  %s\n", summary.CriticalCount, severityBar(summary.CriticalCount, 20)))
	sb.WriteString(fmt.Sprintf("HIGH      %3d  %s\n", summary.HighCount, severityBar(summary.HighCount, 20)))
	sb.WriteString(fmt.Sprintf("MEDIUM    %3d  %s\n", summary.MediumCount, severityBar(summary.MediumCount, 20)))
	sb.WriteString(fmt.Sprintf("LOW       %3d  %s\n", summary.LowCount, severityBar(summary.LowCount, 20)))
	sb.WriteString(fmt.Sprintf("INFO      %3d  %s\n", summary.InfoCount, severityBar(summary.InfoCount, 20)))
	sb.WriteString("```\n\n")
	if summary.AverageCVSS > 0 {
		sb.WriteString(fmt.Sprintf("Average CVSS: **%.2f**\n", summary.AverageCVSS))
	}
	return sb.String()
}

func (b *Builder) renderFindings(in BuildInput) string {
	if len(in.Findings) == 0 {
		return "_No findings were produced by the campaign. This is unusual — " +
			"verify the scope and that the target was reachable._\n"
	}

	// Sort: critical first, then by CVSS desc, then by created time.
	sorted := make([]FindingSnapshot, len(in.Findings))
	copy(sorted, in.Findings)
	sort.SliceStable(sorted, func(i, j int) bool {
		si, sj := severityRank(sorted[i].Severity), severityRank(sorted[j].Severity)
		if si != sj {
			return si < sj
		}
		if sorted[i].CVSSScore != sorted[j].CVSSScore {
			return sorted[i].CVSSScore > sorted[j].CVSSScore
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	var sb strings.Builder
	for i, f := range sorted {
		sb.WriteString(fmt.Sprintf("### %d. %s\n\n", i+1, defaultStr(f.Title, prettifyType(f.FindingType))))
		sb.WriteString(fmt.Sprintf("- **Severity:** `%s`\n", strings.ToUpper(defaultStr(f.Severity, "unknown"))))
		if f.CVSSScore > 0 {
			sb.WriteString(fmt.Sprintf("- **CVSS:** %.2f\n", f.CVSSScore))
		}
		sb.WriteString(fmt.Sprintf("- **Target:** `%s`\n", mdEscape(f.Target)))
		sb.WriteString(fmt.Sprintf("- **Type:** `%s`\n", f.FindingType))
		if f.AgentName != "" {
			sb.WriteString(fmt.Sprintf("- **Discovered by:** `%s`\n", f.AgentName))
		}
		sb.WriteString(fmt.Sprintf("- **Discovered at:** %s\n", f.CreatedAt.UTC().Format(time.RFC3339)))
		sb.WriteString("\n")
		if f.Description != "" {
			sb.WriteString(f.Description)
			sb.WriteString("\n\n")
		}
		if len(f.Data) > 0 {
			sb.WriteString("<details><summary>Raw payload</summary>\n\n```json\n")
			sb.WriteString(prettyJSON(f.Data))
			sb.WriteString("\n```\n\n</details>\n\n")
		}
	}
	return sb.String()
}

func (b *Builder) renderRemediation(in BuildInput) string {
	withRem := []FindingSnapshot{}
	for _, f := range in.Findings {
		if f.HasRemediation {
			withRem = append(withRem, f)
		}
	}
	if len(withRem) == 0 {
		return "_No findings included a remediation hint. Operators should " +
			"manually triage the items above._\n"
	}
	// Highest severity first.
	sort.SliceStable(withRem, func(i, j int) bool {
		return severityRank(withRem[i].Severity) < severityRank(withRem[j].Severity)
	})

	var sb strings.Builder
	for i, f := range withRem {
		sb.WriteString(fmt.Sprintf("%d. **[%s] %s** — `%s`\n",
			i+1,
			strings.ToUpper(defaultStr(f.Severity, "unknown")),
			defaultStr(f.Title, prettifyType(f.FindingType)),
			mdEscape(f.Target),
		))
		// If description looks like JSON, surface it; otherwise echo as text.
		if f.Description != "" {
			sb.WriteString("   - " + strings.TrimSpace(f.Description) + "\n")
		}
	}
	return sb.String()
}

func (b *Builder) renderAppendix(in BuildInput) string {
	agents := map[string]int{}
	targets := map[string]int{}
	for _, f := range in.Findings {
		if f.AgentName != "" {
			agents[f.AgentName]++
		}
		if f.Target != "" {
			targets[f.Target]++
		}
	}

	var sb strings.Builder
	sb.WriteString("### Agents\n\n")
	if len(agents) == 0 {
		sb.WriteString("_No agent attribution recorded._\n")
	} else {
		keys := sortedKeys(agents)
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf("- `%s` — %d finding(s)\n", k, agents[k]))
		}
	}
	sb.WriteString("\n### Top Targets\n\n")
	if len(targets) == 0 {
		sb.WriteString("_No target attribution recorded._\n")
	} else {
		keys := sortedKeys(targets)
		// cap to top 10
		limit := 10
		if len(keys) < limit {
			limit = len(keys)
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(fmt.Sprintf("- `%s` — %d finding(s)\n", keys[i], targets[keys[i]]))
		}
	}
	return sb.String()
}

// --- helpers ---

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func mdEscape(s string) string {
	// Minimal escape: only escape backticks and pipes inside table cells.
	r := strings.NewReplacer("|", `\|`, "`", "`\\u200B`")
	return r.Replace(s)
}

func anchorID(title string) string {
	id := strings.ToLower(title)
	id = strings.ReplaceAll(id, " ", "-")
	id = strings.ReplaceAll(id, "/", "-")
	return id
}

func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	case "informational", "info":
		return 4
	default:
		return 5
	}
}

func severityBar(count, max int) string {
	if max <= 0 {
		return ""
	}
	if count > max {
		count = max
	}
	return strings.Repeat("█", count) + strings.Repeat("░", max-count)
}

func pickVerdict(s Summary) string {
	switch s.OverallRisk {
	case "critical":
		return "Immediate remediation is required."
	case "high":
		return "Remediation should be scheduled this sprint."
	case "medium":
		return "Triage within the next release window."
	case "low":
		return "Track in the standard backlog."
	default:
		return "No material risk was identified."
	}
}

func humanDuration(seconds float64) string {
	if seconds <= 0 {
		return "n/a"
	}
	d := time.Duration(seconds * float64(time.Second))
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

func prettyJSON(b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(b)
	}
	return string(out)
}

func prettifyType(t string) string {
	t = strings.ReplaceAll(t, "_", " ")
	if t == "" {
		return "Untitled Finding"
	}
	words := strings.Fields(t)
	for i, w := range words {
		if len(w) == 0 {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if m[keys[i]] == m[keys[j]] {
			return keys[i] < keys[j]
		}
		return m[keys[i]] > m[keys[j]]
	})
	return keys
}


