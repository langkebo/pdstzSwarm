package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/reports"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
	"github.com/google/uuid"
)

// WithReportHandler installs the report handler on the server and
// registers its routes under /api/v1/reports. Calling it more than
// once replaces the previous handler. Pass a nil handler to disable
// the /api/v1/reports routes entirely.
//
// The server wires the handler's Campaigns / Findings / Board
// callbacks to the live server instance during this call.
//
// Typical wiring:
//
//	reportSvc := reports.NewService(reports.NewStore(pool), reports.NewBuilder())
//	srv := api.NewServer(...).WithReportHandler(reports.NewHandler(reportSvc, "api"))
func (s *Server) WithReportHandler(h *reports.Handler) *Server {
	if s == nil {
		return s
	}
	s.reportHandler = h
	h.Campaigns = s.resolveCampaignSnapshot
	h.Findings = s.resolveCampaignFindings
	if s.board != nil {
		h.Board = s.board
	}
	api := s.app.Group("/api/v1")
	reports.Register(api, h)
	return s
}

// resolveCampaignSnapshot returns a CampaignSnapshot for a campaign id.
// Prefers the live in-memory state (freshest StartedAt / CompletedAt);
// returns an error if the campaign is unknown.
func (s *Server) resolveCampaignSnapshot(ctx context.Context, id uuid.UUID) (*reports.CampaignSnapshot, error) {
	val, ok := s.campaigns.Load(id.String())
	if !ok {
		return nil, fmt.Errorf("campaign %s not found", id)
	}
	state := val.(*CampaignState)
	snap := reports.SnapshotCampaign(state.Campaign)
	return &snap, nil
}

// resolveCampaignFindings returns the findings for a campaign. The
// blackboard is the source of truth when present; otherwise the
// in-memory state.Findings is used.
func (s *Server) resolveCampaignFindings(ctx context.Context, campaignID uuid.UUID) ([]reports.FindingSnapshot, error) {
	if s.board != nil {
		rows, err := s.board.Query(ctx, blackboard.Predicate{Limit: 1000})
		if err == nil {
			out := make([]reports.FindingSnapshot, 0, len(rows))
			for _, f := range rows {
				if f.CampaignID == campaignID {
					out = append(out, reports.SnapshotFinding(f))
				}
			}
			return out, nil
		}
	}
	val, ok := s.campaigns.Load(campaignID.String())
	if !ok {
		return nil, nil
	}
	state := val.(*CampaignState)
	out := make([]reports.FindingSnapshot, 0, len(state.Findings))
	for _, f := range state.Findings {
		out = append(out, snapshotFromClassified(f))
	}
	return out, nil
}

// snapshotFromClassified translates a pipeline.ClassifiedFinding into a
// reports.FindingSnapshot. Exists so reports can be generated for
// legacy campaigns whose findings are in the in-memory state.Findings
// slice (i.e. not yet replicated to the blackboard).
func snapshotFromClassified(f pipeline.ClassifiedFinding) reports.FindingSnapshot {
	data, _ := jsonMarshalMap(map[string]any{
		"title":       f.Title,
		"description": f.Description,
		"severity":    string(f.Severity),
		"cvss_score":  f.CVSSScore,
		"cvss_vector": f.CVSSVector,
		"category":    f.AttackCategory,
		"cve_ids":     f.CVEIDs,
	})
	// ClassifiedFinding has no top-level Remediation string; surface the
	// evidence list as a hint that remediation data is available in the
	// full evidence chain.
	hasRem := len(f.Evidence) > 0 || f.Reproduce != nil
	return reports.FindingSnapshot{
		ID:             f.ID,
		CampaignID:     f.CampaignID,
		AgentName:      "",
		FindingType:    f.AttackCategory,
		Target:         f.Target,
		Severity:       string(f.Severity),
		CVSSScore:      f.CVSSScore,
		Title:          f.Title,
		Description:    f.Description,
		Data:           data,
		CreatedAt:      f.ClassifiedAt,
		HasRemediation: hasRem,
	}
}

// jsonMarshalMap is a small helper that serializes a map to JSON
// without the api package depending on encoding/json directly in every
// file.
func jsonMarshalMap(m map[string]any) ([]byte, error) {
	return json.Marshal(m)
}
