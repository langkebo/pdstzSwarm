package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// SyncConfig controls how skills are synced from the upstream source.
type SyncConfig struct {
	// SourceURL is the raw README.md URL to fetch from.
	SourceURL string

	// CachePath is the local JSON file used as a fallback cache.
	CachePath string

	// HTTPTimeout is the timeout for fetching the upstream source.
	HTTPTimeout time.Duration

	// GitHubToken is an optional GitHub API token for higher rate limits.
	// Set via GITHUB_TOKEN env var or passed explicitly.
	GitHubToken string
}

// DefaultSyncConfig returns a SyncConfig suitable for periodic background
// sync from the openclaw-sec-skills repository.
func DefaultSyncConfig() SyncConfig {
	return SyncConfig{
		SourceURL:   "https://raw.githubusercontent.com/Batman0506/openclaw-sec-skills/main/README.md",
		CachePath:   "",
		HTTPTimeout: 30 * time.Second,
		GitHubToken: os.Getenv("GITHUB_TOKEN"),
	}
}

// emojiToCategory maps the emoji prefixes used in the upstream README
// to our internal Category constants.
var emojiToCategory = map[string]Category{
	"🔒": CategoryCodeAudit,
	"⚔️": CategoryPentest,
	"⚔":  CategoryPentest,
	"🔍": CategoryReverseEngineering,
	"🏆": CategoryCTF,
	"🎯": CategoryThreatModeling,
	"📱": CategoryMobileSecurity,
	"🚨": CategoryIncidentResponse,
	"🛡️": CategorySecurityTools,
}

// skillRowPattern matches a Markdown table row of the form:
//
//	| **name** | description | [label](url) ... |
var skillRowPattern = regexp.MustCompile(`^\| \*\*(.+?)\*\* \| (.+?) \| (.+?)$`)

// sourceLinkPattern extracts [label](url) pairs from a table cell.
var sourceLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// sectionPattern matches a ### heading line.
var sectionPattern = regexp.MustCompile(`^### (.+)$`)

// SyncFromReader parses the upstream README content and returns a list
// of skills. The reader is consumed entirely. Skills are deduplicated
// by source URL.
func SyncFromReader(r io.Reader) ([]Skill, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("skills: read upstream: %w", err)
	}

	lines := strings.Split(string(raw), "\n")
	var (
		skills          []Skill
		currentCategory Category
		seen            = make(map[string]struct{})
	)

	for _, line := range lines {
		// Detect category sections.
		if m := sectionPattern.FindStringSubmatch(line); m != nil {
			title := m[1]
			for emoji, cat := range emojiToCategory {
				if strings.Contains(title, emoji) {
					currentCategory = cat
					break
				}
			}
			continue
		}

		if currentCategory == "" {
			continue
		}

		// Parse skill table rows.
		m := skillRowPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		name := strings.TrimSpace(m[1])
		desc := strings.TrimSpace(m[2])
		sourceCell := m[3]

		links := sourceLinkPattern.FindAllStringSubmatch(sourceCell, -1)
		if len(links) == 0 {
			continue
		}

		sourceURL := links[0][2]
		sourceLabel := links[0][1]

		// Only keep entries with a GitHub source URL.
		if !strings.Contains(sourceURL, "github.com") && !strings.HasPrefix(sourceURL, "http") {
			continue
		}

		// Deduplicate by source URL.
		if sourceURL != "" {
			if _, exists := seen[sourceURL]; exists {
				continue
			}
			seen[sourceURL] = struct{}{}
		}

		skills = append(skills, Skill{
			Name:        name,
			Description: desc,
			Category:    currentCategory,
			Source:      sourceURL,
			SourceType:  "github",
			SourceLabel: sourceLabel,
		})
	}

	if len(skills) == 0 {
		return nil, fmt.Errorf("skills: no skills parsed from upstream README")
	}

	return skills, nil
}

// SyncFromURL fetches the upstream README from cfg.SourceURL and parses
// it into a skill list. On network failure it falls back to the local
// cache if cfg.CachePath is set and exists.
func SyncFromURL(cfg SyncConfig) ([]Skill, error) {
	client := &http.Client{Timeout: cfg.HTTPTimeout}

	req, err := http.NewRequest("GET", cfg.SourceURL, nil)
	if err != nil {
		return fallbackCache(cfg)
	}
	if cfg.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GitHubToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fallbackCache(cfg)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fallbackCache(cfg)
	}

	skills, err := SyncFromReader(resp.Body)
	if err != nil {
		return fallbackCache(cfg)
	}

	// Persist to local cache.
	if cfg.CachePath != "" {
		data, _ := json.MarshalIndent(skills, "", "  ")
		_ = os.WriteFile(cfg.CachePath, data, 0644)
	}

	return skills, nil
}

// fallbackCache tries to load skills from the local cache file.
func fallbackCache(cfg SyncConfig) ([]Skill, error) {
	if cfg.CachePath == "" {
		return nil, fmt.Errorf("skills: fetch failed and no cache path configured")
	}
	data, err := os.ReadFile(cfg.CachePath)
	if err != nil {
		return nil, fmt.Errorf("skills: fetch failed and cache unavailable: %w", err)
	}
	var skills []Skill
	if err := json.Unmarshal(data, &skills); err != nil {
		return nil, fmt.Errorf("skills: cache parse error: %w", err)
	}
	return skills, nil
}