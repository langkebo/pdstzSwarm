package skills

import (
	"strings"
)

// Injector produces skill recommendations tailored to a target description
// or campaign objective. It bridges the skills registry with the prompt
// system so that each agent / campaign run receives a domain-relevant
// subset of the skill catalogue instead of the whole thing.
type Injector struct {
	reg      *Registry
	maxSkills int // cap per injection
}

// NewInjector creates an injector backed by the default global registry.
// maxSkills caps the number of recommendations per call (0 = unlimited).
func NewInjector(maxSkills int) *Injector {
	if maxSkills <= 0 {
		maxSkills = 12
	}
	return &Injector{reg: Global(), maxSkills: maxSkills}
}

// Recommendation bundles a Skill with a short explanation of why it was
// selected and how it can be used.
type Recommendation struct {
	Skill  Skill  `json:"skill"`
	Reason string `json:"reason"`
}

// ForObjective returns skill recommendations relevant to a campaign
// objective description (e.g. "bug bounty on example.com",
// "CTF — capture the root flag", "full pentest of internal network").
func (in *Injector) ForObjective(objective string) []Recommendation {
	return in.ForObjectiveWithCat(objective, nil)
}

// ForObjectiveWithCat is like ForObjective but additionally restricts
// results to the given categories. Pass nil to search all categories.
func (in *Injector) ForObjectiveWithCat(objective string, categories []Category) []Recommendation {
	lower := strings.ToLower(objective)

	// Determine which categories are relevant to this objective.
	var relevantCats []Category
	if categories != nil {
		relevantCats = categories
	} else {
		relevantCats = inferCategories(lower)
	}

	// Collect skills from relevant categories.
	var candidates []Skill
	for _, cat := range relevantCats {
		candidates = append(candidates, in.reg.ByCategory(cat)...)
	}

	// Score and rank candidates against the objective.
	type scored struct {
		skill Skill
		score int
	}
	var scoredList []scored
	for _, s := range candidates {
		score := relevanceScore(lower, s)
		if score > 0 {
			scoredList = append(scoredList, scored{skill: s, score: score})
		}
	}

	// Sort by score descending, then by name.
	for i := 0; i < len(scoredList); i++ {
		for j := i + 1; j < len(scoredList); j++ {
			if scoredList[j].score > scoredList[i].score ||
				(scoredList[j].score == scoredList[i].score && scoredList[j].skill.Name < scoredList[i].skill.Name) {
				scoredList[i], scoredList[j] = scoredList[j], scoredList[i]
			}
		}
	}

	// Cap.
	if len(scoredList) > in.maxSkills {
		scoredList = scoredList[:in.maxSkills]
	}

	// Build recommendations.
	out := make([]Recommendation, len(scoredList))
	for i, sc := range scoredList {
		out[i] = Recommendation{
			Skill:  sc.skill,
			Reason: reasonFor(lower, sc.skill),
		}
	}
	return out
}

// ForTarget is a shorthand for recommendations keyed on a specific
// target (e.g. "api.example.com" → API-security skills).
func (in *Injector) ForTarget(target string) []Recommendation {
	return in.ForObjective(target)
}

// MustInject builds a human-readable, ready-to-append prompt block
// from the recommendations. Returns an empty string when there are no
// recommendations, so callers can safely concatenate the result into
// a system prompt without extra conditionals.
func MustInject(recommendations []Recommendation) string {
	if len(recommendations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Relevant Open-Source AI Agent Security Skills\n")
	b.WriteString("The community has published the following AI agent skills for this domain.\n")
	b.WriteString("You may reference these as prior art / additional capability sources when\n")
	b.WriteString("planning your approach.\n\n")
	for i, rec := range recommendations {
		b.WriteString(formatRecommendation(i+1, rec))
	}
	return b.String()
}

// Format returns a compact inline summary of the recommendations (suitable
// for logging or debugging).
func Format(recommendations []Recommendation) string {
	if len(recommendations) == 0 {
		return "none"
	}
	parts := make([]string, len(recommendations))
	for i, r := range recommendations {
		parts[i] = r.Skill.Name + " (" + string(r.Skill.Category) + ")"
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

// inferCategories maps an objective description to relevant skill categories.
func inferCategories(lower string) []Category {
	var cats []Category

	if containsAny(lower, "code audit", "代码审计", "sast", "white-box", "static analysis", "source code", "dependency") {
		cats = append(cats, CategoryCodeAudit)
	}
	if containsAny(lower, "pentest", "渗透测试", "攻防", "红队", "red team", "bug bounty", "漏洞", "vulnerability",
		"exploit", "利用", "靶场", "靶机") {
		cats = append(cats, CategoryPentest)
	}
	if containsAny(lower, "reverse", "逆向", "binary", "二进制", "decompile", "反编译", "ghidra", "ida", "frida", "crack") {
		cats = append(cats, CategoryReverseEngineering)
	}
	if containsAny(lower, "ctf", "capture the flag", "夺旗", "flag", "pwn", "forensics", "取证") {
		cats = append(cats, CategoryCTF)
	}
	if containsAny(lower, "threat model", "威胁建模", "attack path", "攻击路径", "mitre", "att&ck", "kill chain") {
		cats = append(cats, CategoryThreatModeling)
	}
	if containsAny(lower, "mobile", "android", "ios", "apk", "ipa") {
		cats = append(cats, CategoryMobileSecurity)
	}
	if containsAny(lower, "incident", "应急", "dfir", "forensic", "取证", "breach", "入侵", "malware", "恶意软件") {
		cats = append(cats, CategoryIncidentResponse)
	}
	if containsAny(lower, "tool", "工具", "scanner", "扫描", "nuclei", "burp", "metasploit", "fuzz") {
		cats = append(cats, CategorySecurityTools)
	}

	// Fallback: return all categories.
	if len(cats) == 0 {
		cats = AllCategories()
	}
	return cats
}

// relevanceScore computes a simple heuristic score for how well a skill
// matches the objective description.
func relevanceScore(lower string, s Skill) int {
	score := 1 // base score for being in a relevant category

	names := strings.ToLower(s.Name)
	desc := strings.ToLower(s.Description)

	// Name contains keywords from objective.
	for _, kw := range extractKeywords(lower) {
		if strings.Contains(names, kw) {
			score += 3
		}
		if strings.Contains(desc, kw) {
			score += 1
		}
	}

	// Tags match.
	for _, tag := range s.Tags() {
		if strings.Contains(lower, tag) {
			score += 2
		}
	}

	return score
}

// extractKeywords splits the objective into tokens and filters noise words.
func extractKeywords(lower string) []string {
	noise := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "of": true, "to": true,
		"in": true, "for": true, "on": true, "and": true, "or": true, "with": true,
		"all": true, "full": true, "find": true, "test": true, "run": true,
	}
	var words []string
	for _, w := range strings.Fields(lower) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if len(w) < 3 || noise[w] {
			continue
		}
		words = append(words, w)
	}
	return words
}

// reasonFor produces a one-line explanation of why a skill was recommended.
func reasonFor(lower string, s Skill) string {
	for _, tag := range s.Tags() {
		if strings.Contains(lower, tag) {
			return "matches keyword \"" + tag + "\" from your objective"
		}
	}
	return "commonly used in " + s.Category.DisplayName() + " scenarios"
}

// formatRecommendation renders a single recommendation as a markdown list item.
func formatRecommendation(i int, rec Recommendation) string {
	s := rec.Skill
	var b strings.Builder
	b.WriteString("**")
	b.WriteString(string(rune('0'+i/10)))
	b.WriteString(string(rune('0'+i%10)))
	b.WriteString(". ")
	b.WriteString(s.Name)
	b.WriteString("**")
	if s.Description != "" {
		b.WriteString(" — ")
		b.WriteString(s.Description)
	}
	b.WriteString("\n")
	b.WriteString("   Category: ")
	b.WriteString(s.Category.Emoji())
	b.WriteString(" ")
	b.WriteString(s.Category.DisplayName())
	b.WriteString(" | Source: <")
	b.WriteString(s.Source)
	b.WriteString(">\n")
	return b.String()
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}