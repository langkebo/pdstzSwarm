package skills

import (
	"strings"
	"testing"
)

func TestInjectorScenarios(t *testing.T) {
	inj := NewInjector(8)

	scenarios := []struct {
		objective   string
		minRecs     int
		wantCat     Category // at least one recommendation from this category
	}{
		{
			objective: "bug bounty pentest on api.example.com — find all vulnerabilities",
			minRecs:   3,
			wantCat:   CategoryPentest,
		},
		{
			objective: "CTF competition — capture the root flag from internal network",
			minRecs:   2,
			wantCat:   CategoryCTF,
		},
		{
			objective: "reverse engineering challenge — analyze android APK for malware",
			minRecs:   2,
			wantCat:   CategoryReverseEngineering,
		},
		{
			objective: "incident response — investigate a suspected breach on web server",
			minRecs:   1,
			wantCat:   CategoryIncidentResponse,
		},
		{
			objective: "code audit of a Java Spring Boot application for OWASP Top 10",
			minRecs:   2,
			wantCat:   CategoryCodeAudit,
		},
		{
			objective: "mobile security test — pentest this Android app",
			minRecs:   2,
			wantCat:   CategoryMobileSecurity,
		},
	}

	for _, sc := range scenarios {
		recs := inj.ForObjective(sc.objective)
		t.Logf("Scenario: %s", sc.objective)
		t.Logf("  → %d recommendations: %s", len(recs), Format(recs))

		if len(recs) < sc.minRecs {
			t.Errorf("got %d recs, want >= %d", len(recs), sc.minRecs)
		}

		hasCat := false
		for _, r := range recs {
			if r.Skill.Category == sc.wantCat {
				hasCat = true
				break
			}
		}
		if !hasCat && len(recs) > 0 {
			t.Errorf("no recommendation from category %s", sc.wantCat.DisplayName())
		}

		// Verify MustInject produces valid output.
		block := MustInject(recs)
		if len(recs) > 0 && block == "" {
			t.Error("MustInject returned empty string for non-empty recommendations")
		}
		if len(recs) == 0 && block != "" {
			t.Error("MustInject returned non-empty string for empty recommendations")
		}
	}
}

func TestInjectorEmpty(t *testing.T) {
	inj := NewInjector(8)
	recs := inj.ForObjective("")
	if len(recs) > 0 {
		t.Logf("empty objective returned %d recs (fallback to all categories)", len(recs))
	}
}

func TestInjectorMustInjectEmpty(t *testing.T) {
	block := MustInject(nil)
	if block != "" {
		t.Error("MustInject(nil) should return empty string")
	}
	block = MustInject([]Recommendation{})
	if block != "" {
		t.Error("MustInject([]Recommendation{}) should return empty string")
	}
}

func TestInjectorFormat(t *testing.T) {
	tests := []struct {
		recs []Recommendation
		want string
	}{
		{nil, "none"},
		{[]Recommendation{}, "none"},
		{
			[]Recommendation{{
				Skill:  Skill{Name: "test-skill", Category: CategoryPentest},
				Reason: "test",
			}},
			"test-skill (pentest)",
		},
	}
	for _, tc := range tests {
		got := Format(tc.recs)
		if got != tc.want {
			t.Errorf("Format(%v) = %q, want %q", tc.recs, got, tc.want)
		}
	}
}

func TestMustInjectFormatting(t *testing.T) {
	recs := []Recommendation{
		{
			Skill: Skill{
				Name:        "sast-skills",
				Description: "SAST security scanning",
				Category:    CategoryCodeAudit,
				Source:      "https://github.com/test/sast-skills",
			},
			Reason: "matches keyword",
		},
	}

	block := MustInject(recs)
	if !strings.Contains(block, "sast-skills") {
		t.Error("MustInject should contain skill name")
	}
	if !strings.Contains(block, "SAST") {
		t.Error("MustInject should contain description")
	}
	if !strings.Contains(block, "代码审计") {
		t.Error("MustInject should contain display name")
	}
	if !strings.Contains(block, "github.com/test/sast-skills") {
		t.Error("MustInject should contain source URL")
	}
	t.Logf("Injected block:\n%s", block)
}