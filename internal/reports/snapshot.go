package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// BoardSnapshotter reads findings for a campaign from the blackboard
// and returns them as FindingSnapshot values. The reports package
// depends on this thin interface instead of blackboard.Board so it can
// be tested without a database.
type BoardSnapshotter interface {
	Query(ctx context.Context, p blackboard.Predicate) ([]blackboard.Finding, error)
}

// SnapshotCampaign converts a pipeline.Campaign into a CampaignSnapshot.
// It does not touch the network.
func SnapshotCampaign(c pipeline.Campaign) CampaignSnapshot {
	return CampaignSnapshot{
		ID:          c.ID,
		Name:        c.Name,
		Target:      c.Target,
		Objective:   c.Objective,
		Status:      string(c.Status),
		Mode:        string(c.Mode),
		Provider:    c.Provider,
		CreatedAt:   c.CreatedAt,
		StartedAt:   c.StartedAt,
		CompletedAt: c.CompletedAt,
	}
}

// SnapshotFinding converts a blackboard.Finding into a FindingSnapshot.
// The Data field is preserved as raw JSON so the report's <details> block
// can render the original payload.
func SnapshotFinding(f blackboard.Finding) FindingSnapshot {
	hasRem := hasRemediation(f)
	title, desc, sev, cvss := extractTitleDesc(f)
	return FindingSnapshot{
		ID:             f.ID,
		CampaignID:     f.CampaignID,
		AgentName:      f.AgentName,
		FindingType:    string(f.Type),
		Target:         f.Target,
		Severity:       sev,
		CVSSScore:      cvss,
		Title:          title,
		Description:    desc,
		Data:           f.Data,
		CreatedAt:      f.CreatedAt,
		HasRemediation: hasRem,
	}
}

// SnapshotFindingsForCampaign queries the blackboard for findings
// belonging to a campaign and projects each one. The Predicate doesn't
// carry a campaign id (the table is global), so the caller is expected
// to pass a limit and the function filters in-process.
func SnapshotFindingsForCampaign(ctx context.Context, board BoardSnapshotter, campaignID uuid.UUID, limit int) ([]FindingSnapshot, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := board.Query(ctx, blackboard.Predicate{Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("querying blackboard: %w", err)
	}
	out := make([]FindingSnapshot, 0, len(rows))
	for _, f := range rows {
		if f.CampaignID != campaignID {
			continue
		}
		out = append(out, SnapshotFinding(f))
	}
	return out, nil
}

// extractTitleDesc peeks at the blackboard.Finding.Data JSON to surface
// the most useful fields as Title / Description. Many finding types
// (CVE_MATCH, EXPLOIT_CHAIN, MISCONFIGURATION, etc.) put human-readable
// strings there; surfacing them gives the report a real title rather
// than just "CVE_MATCH".
func extractTitleDesc(f blackboard.Finding) (title, desc, severity string, cvss float64) {
	if len(f.Data) == 0 {
		return prettifyType(string(f.Type)), "", "", 0
	}
	var payload map[string]any
	if err := json.Unmarshal(f.Data, &payload); err != nil {
		return prettifyType(string(f.Type)), string(f.Data), "", 0
	}

	if v, ok := payload["title"].(string); ok && v != "" {
		title = v
	}
	if v, ok := payload["description"].(string); ok && v != "" {
		desc = v
	}
	if v, ok := payload["detail"].(string); ok && desc == "" {
		desc = v
	}
	if v, ok := payload["severity"].(string); ok {
		severity = normalizeSeverity(v)
	}
	if v, ok := payload["cvss_score"].(float64); ok {
		cvss = v
	}
	if v, ok := payload["cvss"].(float64); ok && cvss == 0 {
		cvss = v
	}

	if title == "" {
		title = prettifyType(string(f.Type))
	}
	return title, desc, severity, cvss
}

func normalizeSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "crit", "critical":
		return "critical"
	case "hi", "high":
		return "high"
	case "med", "medium", "moderate":
		return "medium"
	case "lo", "low":
		return "low"
	case "info", "informational", "information":
		return "informational"
	default:
		return s
	}
}

func hasRemediation(f blackboard.Finding) bool {
	if len(f.Data) == 0 {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal(f.Data, &payload); err != nil {
		return false
	}
	if v, ok := payload["remediation"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := payload["fix"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := payload["recommendation"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}

// nowMillis is a small helper used by tests for time injection.
func nowMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
