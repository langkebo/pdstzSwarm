// Package reports implements the campaign-report generation pipeline.
//
// A Report is a point-in-time snapshot of a campaign's findings, surfaced
// as both Markdown (for human review / git diff) and a structured JSON
// record (for the front-end "Reports" page). Reports are immutable once
// generated — subsequent regenerations create new rows. This makes the
// audit trail linear and trivially diffable.
//
// Lifecycle:
//
//	queued   ─► generating ─► ready
//	                    │
//	                    └────► failed
//
// The Report service does not depend on a Blackboard; it reads the same
// campaign/findings source the API already exposes. This means reports
// work in legacy (no-blackboard) deployments just as well as in
// blackboard-driven ones.
package reports

import (
	"time"

	"github.com/google/uuid"
)

// ReportStatus tracks the lifecycle of a single report generation.
type ReportStatus string

const (
	// StatusQueued — record exists, builder has not started.
	StatusQueued ReportStatus = "queued"
	// StatusGenerating — builder is currently writing the report.
	StatusGenerating ReportStatus = "generating"
	// StatusReady — Markdown is fully written and persisted.
	StatusReady ReportStatus = "ready"
	// StatusFailed — builder errored; see ErrorMessage for details.
	StatusFailed ReportStatus = "failed"
)

// Format identifies an export format the report supports. Stored alongside
// the report so the front-end can pick the right download action.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatJSON     Format = "json"
)

// Report is the durable record of a generated campaign report.
type Report struct {
	ID          uuid.UUID    `json:"id"`
	CampaignID  uuid.UUID    `json:"campaign_id"`
	Title       string       `json:"title"`
	Status      ReportStatus `json:"status"`
	Format      Format       `json:"format"`
	Markdown    string       `json:"markdown,omitempty"`
	Summary     Summary      `json:"summary"`
	Sections    []Section    `json:"sections"`
	ByteSize    int          `json:"byte_size"`
	ErrorMessage string      `json:"error_message,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	CompletedAt *time.Time   `json:"completed_at,omitempty"`
}

// Summary is a quick-glance risk profile the front-end renders on the
// report list page. Computed deterministically by the builder from the
// campaign's findings.
type Summary struct {
	TotalFindings      int     `json:"total_findings"`
	CriticalCount      int     `json:"critical_count"`
	HighCount          int     `json:"high_count"`
	MediumCount        int     `json:"medium_count"`
	LowCount           int     `json:"low_count"`
	InfoCount          int     `json:"info_count"`
	AverageCVSS        float64 `json:"average_cvss"`
	OverallRisk        string  `json:"overall_risk"`
	UniqueTargets      int     `json:"unique_targets"`
	UniqueAgents       int     `json:"unique_agents"`
	DurationSeconds    float64 `json:"duration_seconds"`
	HasRemediation     bool    `json:"has_remediation"`
}

// Section is one chapter of the rendered report. The builder emits a
// fixed set of sections in order; this struct makes them inspectable
// from the front-end without re-parsing the Markdown.
type Section struct {
	Key   string `json:"key"`   // e.g. "executive_summary", "findings", "remediation"
	Title string `json:"title"` // human title
	Order int    `json:"order"` // explicit ordering
	Body  string `json:"body"`  // raw markdown
}

// FindingSnapshot is the projection of a finding used during report
// generation. We keep a copy of just the fields the report cares about
// to avoid leaking the full blackboard.Finding into the report layer
// (and to make report generation testable without a board).
type FindingSnapshot struct {
	ID            uuid.UUID `json:"id"`
	CampaignID    uuid.UUID `json:"campaign_id"`
	AgentName     string    `json:"agent_name"`
	FindingType   string    `json:"finding_type"`
	Target        string    `json:"target"`
	Severity      string    `json:"severity"`
	CVSSScore     float64   `json:"cvss_score"`
	Title         string    `json:"title,omitempty"`
	Description   string    `json:"description,omitempty"`
	Data          []byte    `json:"data,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	HasRemediation bool     `json:"has_remediation"`
}

// CampaignSnapshot is the projection of a campaign used during report
// generation. Mirrors pipeline.Campaign but decoupled so the reports
// package doesn't import pipeline (avoids import cycles if pipeline
// ever needs to embed a Report).
type CampaignSnapshot struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Target      string     `json:"target"`
	Objective   string     `json:"objective"`
	Status      string     `json:"status"`
	Mode        string     `json:"mode"`
	Provider    string     `json:"provider"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// BuildInput is the builder's input bundle. The service constructs this
// from the campaign state + blackboard query before calling Build().
type BuildInput struct {
	Campaign  CampaignSnapshot  `json:"campaign"`
	Findings  []FindingSnapshot `json:"findings"`
	GeneratedAt time.Time        `json:"generated_at"`
	// Generator identifies the agent / hook that produced the report
	// (e.g. "manual", "reporter-agent", "scheduled").
	Generator string `json:"generator"`
}
