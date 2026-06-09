package skills

import (
	"fmt"
	"io"
	"testing"
)

func TestGlobalRegistry(t *testing.T) {
	r := Global()
	if r == nil {
		t.Fatal("Global() returned nil")
	}
	if r.Len() == 0 {
		t.Fatal("Global registry is empty")
	}
	t.Logf("Total skills loaded: %d", r.Len())
}

func TestCategoryStats(t *testing.T) {
	r := Global()
	stats := r.CategoryStats()
	total := 0
	for _, cat := range AllCategories() {
		count := stats[cat]
		total += count
		t.Logf("  %s %s: %d", cat.Emoji(), cat.DisplayName(), count)
	}
	t.Logf("  Total (sum): %d", total)
	if total != r.Len() {
		t.Errorf("category sum %d != registry len %d", total, r.Len())
	}
}

func TestByName(t *testing.T) {
	r := Global()
	known := []string{"sast-skills", "pentest-skills", "ctf-skills", "claude-code-pentest"}
	for _, name := range known {
		s := r.ByName(name)
		if s == nil {
			t.Errorf("ByName(%q) returned nil", name)
		} else {
			t.Logf("  ✅ %s → [%s] %s", name, s.Category.DisplayName(), s.Source)
		}
	}

	// Unknown name.
	if s := r.ByName("definitely-not-a-real-skill-name"); s != nil {
		t.Errorf("ByName(nonexistent) returned %v", s)
	}
}

func TestByCategory(t *testing.T) {
	r := Global()
	for _, cat := range AllCategories() {
		list := r.ByCategory(cat)
		if len(list) == 0 {
			t.Errorf("ByCategory(%s) returned 0 skills", cat.DisplayName())
		} else {
			t.Logf("  %s %s: %d skills (first: %s)", cat.Emoji(), cat.DisplayName(), len(list), list[0].Name)
		}
	}
}

func TestSearch(t *testing.T) {
	r := Global()
	tests := []struct {
		query    string
		minHits  int
	}{
		{"nuclei", 1},
		{"android", 1},
		{"owasp", 1},
		{"CTF", 1},
		{"malware", 1},
		{"frida", 0},  // might or might not match
	}
	for _, tc := range tests {
		results := r.Search(tc.query)
		t.Logf("Search(%q) → %d hits", tc.query, len(results))
		for _, s := range results {
			t.Logf("    [%s] %s", s.Category.DisplayName(), s.Name)
		}
		if len(results) < tc.minHits {
			t.Errorf("Search(%q) got %d hits, want >= %d", tc.query, len(results), tc.minHits)
		}
	}
}

func TestBySource(t *testing.T) {
	r := Global()
	all := r.All()
	if len(all) == 0 {
		t.Fatal("no skills to test")
	}
	first := all[0]
	if first.Source == "" {
		t.Skip("first skill has no source URL")
	}
	found := r.BySource(first.Source)
	if found == nil {
		t.Errorf("BySource(%q) returned nil", first.Source)
	} else if found.Name != first.Name {
		t.Errorf("BySource(%q) returned %q, want %q", first.Source, found.Name, first.Name)
	}
}

func TestOwnerRepo(t *testing.T) {
	s := Skill{Source: "https://github.com/trailofbits/skills"}
	owner, repo := s.OwnerRepo()
	if owner != "trailofbits" || repo != "skills" {
		t.Errorf("OwnerRepo() = (%q, %q), want (trailofbits, skills)", owner, repo)
	}

	s2 := Skill{Source: "https://github.com/Armur-Ai/Pentest-Swarm-AI.git"}
	owner2, repo2 := s2.OwnerRepo()
	if owner2 != "Armur-Ai" || repo2 != "Pentest-Swarm-AI" {
		t.Errorf("OwnerRepo() = (%q, %q), want (Armur-Ai, Pentest-Swarm-AI)", owner2, repo2)
	}
}

func TestTags(t *testing.T) {
	s := Skill{Name: "java-audit-skills", Description: "Java code audit skill for OWASP and CVE scanning"}
	tags := s.Tags()
	t.Logf("Tags for %q: %v", s.Name, tags)
	if len(tags) == 0 {
		t.Error("expected at least one tag")
	}
}

func TestParseCategory(t *testing.T) {
	tests := []struct {
		input string
		want  Category
	}{
		{"code_audit", CategoryCodeAudit},
		{"代码审计", CategoryCodeAudit},
		{"pentest", CategoryPentest},
		{"渗透测试", CategoryPentest},
		{"ctf", CategoryCTF},
		{"CTF竞赛", CategoryCTF},
		{"unknown", ""},
	}
	for _, tc := range tests {
		got := ParseCategory(tc.input)
		if got != tc.want {
			t.Errorf("ParseCategory(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSyncFromReader(t *testing.T) {
	// Parse a minimal README snippet.
	input := `### 🔒 代码审计
> White-box audit
| Skill | 描述 | 来源 |
|-------|------|------|
| **test-skill** | A test skill for verification | [github](https://github.com/test/test-skill) |
`
	skills, err := SyncFromReader(&stringReader{data: input})
	if err != nil {
		t.Fatalf("SyncFromReader failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Name != "test-skill" {
		t.Errorf("name = %q, want test-skill", s.Name)
	}
	if s.Category != CategoryCodeAudit {
		t.Errorf("category = %q, want code_audit", s.Category)
	}
	if s.Source != "https://github.com/test/test-skill" {
		t.Errorf("source = %q", s.Source)
	}
}

func TestSyncDedup(t *testing.T) {
	// Verify duplicates are filtered by source URL.
	input := `### ⚔️ 渗透测试
> Pentest
| Skill | 描述 | 来源 |
|-------|------|------|
| **dup-a** | First copy | [github](https://github.com/foo/bar) |
| **dup-b** | Second copy (should be skipped) | [github](https://github.com/foo/bar) |
| **unique** | Different one | [github](https://github.com/foo/baz) |
`
	skills, err := SyncFromReader(&stringReader{data: input})
	if err != nil {
		t.Fatalf("SyncFromReader failed: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 unique skills, got %d: %v", len(skills), skills)
	}
}

func TestAll(t *testing.T) {
	r := Global()
	all := r.All()
	if len(all) != r.Len() {
		t.Errorf("All() returned %d, Len() = %d", len(all), r.Len())
	}
	// Verify it's a copy.
	if len(all) > 0 {
		all[0].Name = "MODIFIED"
		orig := r.All()
		if orig[0].Name == "MODIFIED" {
			t.Error("All() should return a copy, not a reference to internal slice")
		}
	}
}

func TestCategoryDisplayName(t *testing.T) {
	for _, cat := range AllCategories() {
		name := cat.DisplayName()
		emoji := cat.Emoji()
		fmt.Printf("  %s %s", emoji, name)
		if name == "" || emoji == "" {
			t.Errorf("Category %q has empty display name or emoji", cat)
		}
	}
}

// stringReader is a helper that implements io.Reader for a string.
type stringReader struct {
	data string
	pos  int
}

func (s *stringReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += n
	if s.pos >= len(s.data) {
		return n, io.EOF
	}
	return n, nil
}