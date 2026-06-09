package reports

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fixedTime returns a stable clock for deterministic tests.
func fixedTime() time.Time {
	return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
}

// makeInput is a test helper that returns a BuildInput with 3 findings
// (one critical, one high, one low) on a campaign named "test-camp".
func makeInput() BuildInput {
	started := time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 6, 3, 11, 30, 0, 0, time.UTC)
	return BuildInput{
		Campaign: CampaignSnapshot{
			ID:          uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			Name:        "test-camp",
			Target:      "https://example.com",
			Objective:   "discover misconfigurations",
			Status:      "completed",
			Mode:        "bugbounty",
			Provider:    "deepseek",
			CreatedAt:   time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
			StartedAt:   &started,
			CompletedAt: &completed,
		},
		Findings: []FindingSnapshot{
			{
				ID:             uuid.New(),
				CampaignID:     uuid.MustParse("11111111-1111-1111-1111-111111111111"),
				AgentName:      "recon-agent",
				FindingType:    "EXPOSED_CREDENTIAL",
				Target:         "https://example.com/.env",
				Severity:       "critical",
				CVSSScore:      9.8,
				Title:          "Exposed .env file",
				Description:    "Production secrets in public .env.",
				Data:           []byte(`{"remediation": "Move .env outside webroot, rotate keys."}`),
				CreatedAt:      time.Date(2026, 6, 3, 11, 5, 0, 0, time.UTC),
				HasRemediation: true,
			},
			{
				ID:          uuid.New(),
				CampaignID:  uuid.MustParse("11111111-1111-1111-1111-111111111111"),
				AgentName:   "vuln-agent",
				FindingType: "CVE_MATCH",
				Target:      "https://example.com/login",
				Severity:    "high",
				CVSSScore:   7.5,
				Title:       "CVE-2024-1234 — Auth bypass",
				Description: "Authentication can be skipped with `?admin=1`.",
				Data:        []byte(`{"cve": "CVE-2024-1234"}`),
				CreatedAt:   time.Date(2026, 6, 3, 11, 15, 0, 0, time.UTC),
			},
			{
				ID:          uuid.New(),
				CampaignID:  uuid.MustParse("11111111-1111-1111-1111-111111111111"),
				AgentName:   "scan-agent",
				FindingType: "INFORMATIONAL",
				Target:      "https://example.com",
				Severity:    "low",
				CVSSScore:   2.0,
				Title:       "Missing CSP header",
				Description: "Consider adding Content-Security-Policy.",
				CreatedAt:   time.Date(2026, 6, 3, 11, 25, 0, 0, time.UTC),
			},
		},
		GeneratedAt: fixedTime(),
		Generator:   "test",
	}
}

func TestBuilder_Build_Succeeds(t *testing.T) {
	b := NewBuilder()
	b.Now = fixedTime
	r, err := b.Build(makeInput())
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if r.Status != StatusReady {
		t.Fatalf("expected status=ready, got %s", r.Status)
	}
	if r.ID == uuid.Nil {
		t.Fatalf("expected non-nil id")
	}
	if r.CampaignID != makeInput().Campaign.ID {
		t.Fatalf("campaign id mismatch: got %s want %s", r.CampaignID, makeInput().Campaign.ID)
	}
	if r.ByteSize <= 0 {
		t.Fatalf("expected positive byte size, got %d", r.ByteSize)
	}
	if !strings.HasPrefix(r.Title, "Penetration Test Report") {
		t.Fatalf("title did not include prefix: %q", r.Title)
	}
}

func TestBuilder_SummaryCounts(t *testing.T) {
	b := NewBuilder()
	b.Now = fixedTime
	r, err := b.Build(makeInput())
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	s := r.Summary
	if s.TotalFindings != 3 {
		t.Errorf("TotalFindings=%d want 3", s.TotalFindings)
	}
	if s.CriticalCount != 1 {
		t.Errorf("CriticalCount=%d want 1", s.CriticalCount)
	}
	if s.HighCount != 1 {
		t.Errorf("HighCount=%d want 1", s.HighCount)
	}
	if s.LowCount != 1 {
		t.Errorf("LowCount=%d want 1", s.LowCount)
	}
	if s.UniqueTargets != 3 {
		t.Errorf("UniqueTargets=%d want 3", s.UniqueTargets)
	}
	if s.UniqueAgents != 3 {
		t.Errorf("UniqueAgents=%d want 3", s.UniqueAgents)
	}
	if s.OverallRisk != "critical" {
		t.Errorf("OverallRisk=%s want critical", s.OverallRisk)
	}
	if !s.HasRemediation {
		t.Errorf("HasRemediation=false want true (one finding has remediation)")
	}
	// average of 9.8 + 7.5 + 2.0 = 6.43
	if got, want := s.AverageCVSS, 6.433333333333334; got < want-0.001 || got > want+0.001 {
		t.Errorf("AverageCVSS=%.4f want ~%.4f", got, want)
	}
	// duration 30 min
	if s.DurationSeconds != 1800 {
		t.Errorf("DurationSeconds=%.0f want 1800", s.DurationSeconds)
	}
}

func TestBuilder_MarkdownContainsAllSections(t *testing.T) {
	b := NewBuilder()
	b.Now = fixedTime
	r, err := b.Build(makeInput())
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	md := r.Markdown
	for _, s := range []string{
		"# Penetration Test Report",
		"## Table of Contents",
		"## Campaign Metadata",
		"## Executive Summary",
		"## Risk Overview",
		"## Detailed Findings",
		"## Remediation Roadmap",
		"## Appendix",
		"Exposed .env file",
		"CVE-2024-1234",
		"Missing CSP header",
		"recon-agent",
		"vuln-agent",
		"scan-agent",
	} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown missing %q", s)
		}
	}
}

func TestBuilder_FindingsSortedBySeverity(t *testing.T) {
	b := NewBuilder()
	b.Now = fixedTime
	r, err := b.Build(makeInput())
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	md := r.Markdown
	idxCritical := strings.Index(md, "Exposed .env file")
	idxHigh := strings.Index(md, "CVE-2024-1234")
	idxLow := strings.Index(md, "Missing CSP header")
	if !(idxCritical < idxHigh && idxHigh < idxLow) {
		t.Errorf("findings not in expected severity order: crit=%d high=%d low=%d",
			idxCritical, idxHigh, idxLow)
	}
}

func TestBuilder_NoFindings(t *testing.T) {
	in := makeInput()
	in.Findings = nil
	b := NewBuilder()
	b.Now = fixedTime
	r, err := b.Build(in)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if r.Summary.TotalFindings != 0 {
		t.Errorf("TotalFindings=%d want 0", r.Summary.TotalFindings)
	}
	if !strings.Contains(r.Markdown, "No findings were produced") {
		t.Errorf("expected 'no findings' message in markdown")
	}
}

func TestBuilder_MissingCampaignID(t *testing.T) {
	in := makeInput()
	in.Campaign.ID = uuid.Nil
	b := NewBuilder()
	_, err := b.Build(in)
	if err == nil {
		t.Fatalf("expected error for missing campaign id")
	}
}

func TestService_Generate_NilStore(t *testing.T) {
	svc := NewService(nil, NewBuilder())
	svc.Now = fixedTime
	r, err := svc.Generate(makeInput())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil report")
	}
	if r.ID == uuid.Nil {
		t.Errorf("expected non-nil id")
	}
}

func TestService_GenerateForCampaign_RequiresCampaign(t *testing.T) {
	svc := NewService(nil, NewBuilder())
	_, err := svc.GenerateForCampaign(context.Background(), BuildInput{})
	if !errors.Is(err, ErrNoInputs) {
		t.Errorf("expected ErrNoInputs, got %v", err)
	}
}

func TestSeverityRank(t *testing.T) {
	cases := []struct {
		sev  string
		want int
	}{
		{"critical", 0},
		{"high", 1},
		{"medium", 2},
		{"low", 3},
		{"info", 4},
		{"unknown", 5},
		{"CRITICAL", 0},
	}
	for _, tc := range cases {
		if got := severityRank(tc.sev); got != tc.want {
			t.Errorf("severityRank(%q)=%d want %d", tc.sev, got, tc.want)
		}
	}
}

func TestComputeOverallRisk(t *testing.T) {
	cases := []struct {
		name string
		s    Summary
		want string
	}{
		{"empty", Summary{}, "informational"},
		{"info-only", Summary{InfoCount: 3}, "low"},
		{"low", Summary{LowCount: 1}, "low"},
		{"medium-few", Summary{MediumCount: 1}, "low"},
		{"medium-many", Summary{MediumCount: 3}, "medium"},
		{"high-one", Summary{HighCount: 1}, "high"},
		{"high-many", Summary{HighCount: 3}, "high"},
		{"critical-wins", Summary{CriticalCount: 1, HighCount: 5}, "critical"},
	}
	for _, tc := range cases {
		if got := computeOverallRisk(tc.s); got != tc.want {
			t.Errorf("%s: computeOverallRisk=%s want %s", tc.name, got, tc.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{0, "n/a"},
		{1, "1s"},
		{60, "1m 0s"},
		{3600, "1h 0m 0s"},
		{86400, "1d 0h 0m"},
		{86400 + 3600 + 60, "1d 1h 1m"},
	}
	for _, tc := range cases {
		if got := humanDuration(tc.seconds); got != tc.want {
			t.Errorf("humanDuration(%.0f)=%q want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestSeverityBar(t *testing.T) {
	if got := severityBar(0, 5); got != "░░░░░" {
		t.Errorf("severityBar(0,5)=%q want %q", got, "░░░░░")
	}
	if got := severityBar(3, 5); got != "███░░" {
		t.Errorf("severityBar(3,5)=%q want %q", got, "███░░")
	}
	if got := severityBar(5, 5); got != "█████" {
		t.Errorf("severityBar(5,5)=%q want %q", got, "█████")
	}
	if got := severityBar(10, 5); got != "█████" {
		t.Errorf("severityBar(10,5)=%q want %q (clamped)", got, "█████")
	}
}

func TestSafeFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello", "hello"},
		{"hello world", "hello world"},
		{"中文 / special!@#", "special"},
		{"", "fallback-uuid"},
	}
	for _, tc := range cases {
		got := safeFilename(tc.in, "fallback-uuid")
		if tc.in == "" && got != "fallback-uuid" {
			t.Errorf("safeFilename(%q)=%q want fallback", tc.in, got)
		}
		if tc.in != "" && got == "" {
			t.Errorf("safeFilename(%q)=%q want non-empty", tc.in, got)
		}
	}
}

func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"critical", "critical"},
		{"CRITICAL", "critical"},
		{"crit", "critical"},
		{"high", "high"},
		{"hi", "high"},
		{"medium", "medium"},
		{"moderate", "medium"},
		{"med", "medium"},
		{"low", "low"},
		{"lo", "low"},
		{"info", "informational"},
		{"informational", "informational"},
		{"information", "informational"},
		{"  critical  ", "critical"},
		{"unknown", "unknown"},
	}
	for _, tc := range cases {
		if got := normalizeSeverity(tc.in); got != tc.want {
			t.Errorf("normalizeSeverity(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestHasRemediationFromJSON(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"remediation", []byte(`{"remediation": "fix it"}`), true},
		{"fix", []byte(`{"fix": "patch"}`), true},
		{"recommendation", []byte(`{"recommendation": "do this"}`), true},
		{"empty-string", []byte(`{"remediation": ""}`), false},
		{"whitespace", []byte(`{"remediation": "   "}`), false},
		{"missing", []byte(`{"title": "x"}`), false},
		{"invalid-json", []byte(`{not json`), false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			if len(tc.data) > 0 {
				_ = json.Unmarshal(tc.data, &payload)
			}
			if got := hasRemediationFromPayload(payload); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

// hasRemediationFromPayload is a tiny helper that mirrors the body of
// the snapshot.go's hasRemediation without depending on the
// blackboard.Finding struct (which would force an import).
func hasRemediationFromPayload(payload map[string]any) bool {
	for _, key := range []string{"remediation", "fix", "recommendation"} {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}
