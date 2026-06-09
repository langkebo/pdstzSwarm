package skills

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed data/*.json
var dataFS embed.FS

// Registry is a thread-safe in-memory skill catalogue loaded from the
// embedded JSON export of the openclaw-sec-skills community index.
type Registry struct {
	mu sync.RWMutex

	skills   []Skill               // all skills, insertion order
	byName   map[string]int        // name → index into skills
	bySource map[string]int        // source URL → index into skills
	byCat    map[Category][]int    // category → indices
}

// defaultRegistry is the singleton loaded from the embedded data files
// at init time. Use Global() to access it or NewRegistry() for a
// custom, empty registry suitable for testing.
var defaultRegistry *Registry

func init() {
	r, err := NewRegistry()
	if err != nil {
		// Failed to load the embedded skills data.
		panic("skills: failed to load embedded data: " + err.Error())
	}
	defaultRegistry = r
}

// NewRegistry loads all skill data files from the embedded FS and
// returns a populated Registry. The embedded directory
// internal/skills/data contains JSON files exported from the
// openclaw-sec-skills repository. Returns an error if no data files
// are found or if JSON parsing fails.
func NewRegistry() (*Registry, error) {
	entries, err := dataFS.ReadDir("data")
	if err != nil {
		return nil, fmt.Errorf("skills: read data dir: %w", err)
	}

	r := &Registry{
		byName:   make(map[string]int),
		bySource: make(map[string]int),
		byCat:    make(map[Category][]int),
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := dataFS.ReadFile("data/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", entry.Name(), err)
		}

		var skills []Skill
		if err := json.Unmarshal(raw, &skills); err != nil {
			return nil, fmt.Errorf("skills: parse %s: %w", entry.Name(), err)
		}

		for i := range skills {
			r.add(skills[i])
		}
	}

	if len(r.skills) == 0 {
		return nil, fmt.Errorf("skills: no skill data files found in data/")
	}

	return r, nil
}

// add inserts a skill into the registry. Must be called while the
// caller holds the write lock or during initialisation.
func (r *Registry) add(s Skill) {
	idx := len(r.skills)
	r.skills = append(r.skills, s)

	nameKey := strings.ToLower(s.Name)
	if _, exists := r.byName[nameKey]; !exists {
		r.byName[nameKey] = idx
	}
	if s.Source != "" {
		r.bySource[s.Source] = idx
	}
	r.byCat[s.Category] = append(r.byCat[s.Category], idx)
}

// Global returns the default, embedded, read-only registry.
func Global() *Registry { return defaultRegistry }

// All returns a shallow copy of every registered skill.
func (r *Registry) All() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, len(r.skills))
	copy(out, r.skills)
	return out
}

// Len returns the total number of skills.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.skills)
}

// ByName looks up a skill by its case-insensitive name. Returns nil
// when no match is found.
func (r *Registry) ByName(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.byName[strings.ToLower(name)]
	if !ok {
		return nil
	}
	s := r.skills[idx]
	return &s
}

// BySource looks up a skill by its canonical source URL. Returns nil
// when no match is found.
func (r *Registry) BySource(source string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.bySource[source]
	if !ok {
		return nil
	}
	s := r.skills[idx]
	return &s
}

// ByCategory returns all skills belonging to the given category, in
// insertion order.
func (r *Registry) ByCategory(c Category) []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	indices := r.byCat[c]
	out := make([]Skill, len(indices))
	for i, idx := range indices {
		out[i] = r.skills[idx]
	}
	return out
}

// CategoryStats returns a mapping from category to skill count.
func (r *Registry) CategoryStats() map[Category]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[Category]int, len(r.byCat))
	for cat, indices := range r.byCat {
		m[cat] = len(indices)
	}
	return m
}

// Search performs a case-insensitive substring match against skill
// names and descriptions. Results are ranked: exact name matches
// first, then name prefix matches, then description matches.
func (r *Registry) Search(query string) []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}

	type hit struct {
		skill Skill
		rank  int // lower = better
	}
	var hits []hit

	for _, s := range r.skills {
		nameLower := strings.ToLower(s.Name)
		descLower := strings.ToLower(s.Description)

		rank := 100
		switch {
		case nameLower == q:
			rank = 1
		case strings.HasPrefix(nameLower, q):
			rank = 2
		case strings.Contains(nameLower, q):
			rank = 3
		case strings.HasPrefix(descLower, q):
			rank = 4
		case strings.Contains(descLower, q):
			rank = 5
		default:
			// Also search tags.
			for _, tag := range s.Tags() {
				if tag == q {
					rank = 6
					break
				}
			}
		}
		if rank <= 6 {
			hits = append(hits, hit{skill: s, rank: rank})
		}
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })

	out := make([]Skill, len(hits))
	for i, h := range hits {
		out[i] = h.skill
	}
	return out
}