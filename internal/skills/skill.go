package skills

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Skill represents a single AI Agent security skill curated from the
// openclaw-sec-skills community index.
type Skill struct {
	// Name is the human-readable skill name (e.g. "sast-skills").
	Name string `json:"name"`

	// Description is a one-line summary of what the skill does.
	Description string `json:"description"`

	// Category classifies the skill into a security domain.
	Category Category `json:"category"`

	// Source is the canonical URL (usually a GitHub repository).
	Source string `json:"source"`

	// SourceType indicates the origin platform ("github", etc.).
	SourceType string `json:"source_type"`

	// SourceLabel is the display text for the source link.
	SourceLabel string `json:"source_label"`
}

// OwnerRepo parses the GitHub owner/repo from Source.
// Returns ("", "") if Source is not a GitHub URL.
func (s Skill) OwnerRepo() (owner, repo string) {
	if !strings.Contains(s.Source, "github.com") {
		return "", ""
	}
	// Strip protocol and domain.
	rest := s.Source
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if after, ok := strings.CutPrefix(rest, prefix); ok {
			rest = after
			break
		}
	}
	// Strip trailing .git
	rest = strings.TrimSuffix(rest, ".git")
	// Split owner/repo (ignore extra path segments).
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// Tags returns a set of inferred tags from the skill's name and description.
// This is a simple heuristic; a full NLP pipeline would replace it.
func (s Skill) Tags() []string {
	lower := strings.ToLower(s.Name + " " + s.Description)
	var tags []string
	for _, kw := range commonTags {
		if strings.Contains(lower, kw) {
			tags = append(tags, kw)
		}
	}
	return tags
}

// commonTags is a curated list of keywords used for heuristic tagging.
var commonTags = []string{
	"owasp", "mitre", "nist", "cve", "cwe", "cvss",
	"sql", "xss", "csrf", "ssrf", "idor", "ssti",
	"nuclei", "nmap", "burp", "metasploit", "zap",
	"java", "php", "python", "go", "rust", "javascript",
	"android", "ios", "ios", "mobile",
	"docker", "kubernetes", "terraform", "aws", "gcp", "azure",
	"api", "graphql", "rest",
	"solidity", "smart-contract", "web3",
	"frida", "ghidra", "ida", "binary",
	"ctf", "pwn", "crypto", "forensics",
	"dfir", "soc", "incident", "malware",
	"llm", "ai", "prompt-injection", "jailbreak",
	"bug-bounty", "bounty", "red-team", "blue-team",
	"ad", "active-directory",
	"supply-chain",
}

// MarshalJSON is a custom encoder that writes the category as its
// string key rather than the display name.
func (s Skill) MarshalJSON() ([]byte, error) {
	type alias Skill
	return json.Marshal(&struct {
		Category string `json:"category"`
		*alias
	}{
		Category: string(s.Category),
		alias:    (*alias)(&s),
	})
}

// UnmarshalJSON is a custom decoder that parses the category string
// back into the Category type.
func (s *Skill) UnmarshalJSON(data []byte) error {
	type alias Skill
	aux := &struct {
		Category string `json:"category"`
		*alias
	}{
		alias: (*alias)(s),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	s.Category = ParseCategory(aux.Category)
	if s.Category == "" {
		return fmt.Errorf("skills: unknown category %q", aux.Category)
	}
	return nil
}