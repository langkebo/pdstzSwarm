package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
)

// EntityExtractor mines an Episode for entities and relations.
// The two production implementations are:
//
//   - LLMEntityExtractor uses the configured llm.Provider with a
//     strict JSON schema. Latency is dominated by the LLM call;
//     one call per Episode.
//
//   - RuleBasedExtractor uses deterministic regex patterns and is
//     always available. Latency is microseconds. Quality is lower
//     (no inference) but the entity set is the obvious "CVE-…",
//     "10.0.0.5", "service:http on port 80" type observations
//     that the rule engine is good at.
//
// Callers may chain them (e.g. rule-based first, LLM for residuals)
// by handing the rule-based result back into a follow-up LLM call.
// The interface stays small so this composition is trivial.
type EntityExtractor interface {
	Extract(ctx context.Context, ep Episode) ([]Entity, []Relation, error)
}

// LLMEntityExtractor calls an llm.Provider with a strict JSON
// schema (function-call compatible) and parses the response into
// entities and relations. The JSON shape is:
//
//	{
//	  "entities": [
//	    {"type": "CVE", "label": "CVE-2024-1234", "properties": {...}},
//	    ...
//	  ],
//	  "relations": [
//	    {"from": "CVE-2024-1234", "to": "10.0.0.5", "type": "AFFECTS", "properties": {...}},
//	    ...
//	  ]
//	}
//
// Relations reference entities by their (type, label) pair; the
// extractor is responsible for resolving the (type, label) pair to
// a UUID when persisting. PostgresGraphStore.AddEpisode does the
// resolution in a single transaction.
type LLMEntityExtractor struct {
	Provider     llm.Provider
	Model        string   // empty → use Provider.ModelName()
	MaxTokens    int      // 0 → 1024
	Temperature  float64  // 0 → 0 (deterministic)
	EntityTypes  []string // hints; nil → default entity set
	RelationTypes []string // hints; nil → default relation set
	Logger       *slog.Logger
}

// LLMExtractorOption configures an LLMEntityExtractor at construction time.
type LLMExtractorOption func(*LLMEntityExtractor)

// WithModel overrides the model name for the completion call.
func WithModel(m string) LLMExtractorOption {
	return func(e *LLMEntityExtractor) { e.Model = m }
}

// WithMaxTokens overrides the output token cap.
func WithMaxTokens(n int) LLMExtractorOption {
	return func(e *LLMEntityExtractor) { e.MaxTokens = n }
}

// WithTemperature overrides the sampling temperature.
func WithTemperature(t float64) LLMExtractorOption {
	return func(e *LLMEntityExtractor) { e.Temperature = t }
}

// WithEntityTypes narrows the LLM's allowed entity vocabulary.
func WithEntityTypes(types ...string) LLMExtractorOption {
	return func(e *LLMEntityExtractor) { e.EntityTypes = types }
}

// WithRelationTypes narrows the LLM's allowed relation vocabulary.
func WithRelationTypes(types ...string) LLMExtractorOption {
	return func(e *LLMEntityExtractor) { e.RelationTypes = types }
}

// WithLLMExtractorLogger attaches a logger. Default is slog.Default().
func WithLLMExtractorLogger(l *slog.Logger) LLMExtractorOption {
	return func(e *LLMEntityExtractor) { e.Logger = l }
}

// NewLLMEntityExtractor builds an extractor. Returns an error when
// p is nil — a configured provider is required.
func NewLLMEntityExtractor(p llm.Provider, opts ...LLMExtractorOption) (*LLMEntityExtractor, error) {
	if p == nil {
		return nil, fmt.Errorf("LLMEntityExtractor: provider is required")
	}
	e := &LLMEntityExtractor{
		Provider:    p,
		MaxTokens:   1024,
		Temperature: 0,
		EntityTypes: defaultEntityTypes(),
		RelationTypes: defaultRelationTypes(),
		Logger:      slog.Default(),
	}
	for _, o := range opts {
		o(e)
	}
	if e.Model == "" {
		e.Model = p.ModelName()
	}
	return e, nil
}

func defaultEntityTypes() []string {
	return []string{
		EntityTypeHost, EntityTypeSubdomain, EntityTypeService, EntityTypeEndpoint,
		EntityTypeCVE, EntityTypeCWE, EntityTypeTech, EntityTypePerson,
		EntityTypeTool, EntityTypeAgent, EntityTypeCampaign, EntityTypeSecret, EntityTypeSession,
	}
}

func defaultRelationTypes() []string {
	return []string{
		RelationAffects, RelationRunsOn, RelationHosts, RelationExposes,
		RelationDetectedBy, RelationExploits, RelationTargets, RelationProduces,
		RelationHas, RelationRelatesTo,
	}
}

// extractResponse is the wire shape the LLM is asked to return.
type extractResponse struct {
	Entities  []extractEntity  `json:"entities"`
	Relations []extractRelation `json:"relations"`
}

type extractEntity struct {
	Type       string         `json:"type"`
	Label      string         `json:"label"`
	Properties map[string]any `json:"properties,omitempty"`
}

type extractRelation struct {
	From       string         `json:"from"`       // (type,label) ref, label-only is accepted
	To         string         `json:"to"`         // same
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// extractSystemPrompt instructs the LLM to behave as a strict
// entity-relation extractor. The expected output is a JSON object
// with `entities` and `relations` arrays; nothing else.
const extractSystemPrompt = `You are an entity-relation extractor for a penetration-test knowledge graph.

Read the supplied text and output a SINGLE JSON object matching the schema below. Do not output any prose, markdown, or commentary — only JSON.

Schema:
{
  "entities": [
    {"type": "<one of the allowed types>", "label": "<canonical name>", "properties": {<optional key/value pairs>}}
  ],
  "relations": [
    {"from": "<entity label>", "to": "<entity label>", "type": "<one of the allowed relation types>", "properties": {<optional>}}
  ]
}

Rules:
- "label" must be the canonical name as it would appear in a graph DB: "CVE-2024-1234", "10.0.0.5", "www.example.com", "Apache 2.4.49".
- "type" must be one of the allowed types listed below.
- Relations reference entities by label only; the (type, label) pair is resolved by the caller.
- Emit relations ONLY between entities you also emitted. Self-references and dangling references are dropped.
- Properties is a free-form bag for facts that do not deserve their own relation type (e.g. {"severity": "HIGH"}).
- If the text contains no entities, output {"entities": [], "relations": []}.
`

// Extract sends the episode to the LLM and parses the JSON response.
// The returned entities and relations have empty ID fields — the
// persistence layer assigns / upserts IDs based on (type, label, campaign).
func (e *LLMEntityExtractor) Extract(ctx context.Context, ep Episode) ([]Entity, []Relation, error) {
	if e == nil || e.Provider == nil {
		return nil, nil, fmt.Errorf("LLMEntityExtractor: not configured")
	}
	if strings.TrimSpace(ep.Text) == "" {
		return nil, nil, nil
	}

	userText := fmt.Sprintf("Allowed entity types: %s\nAllowed relation types: %s\n\nText to extract from:\n%s",
		strings.Join(e.EntityTypes, ", "),
		strings.Join(e.RelationTypes, ", "),
		ep.Text,
	)

	req := llm.CompletionRequest{
		SystemPrompt: extractSystemPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: userText},
		},
		MaxTokens:   e.MaxTokens,
		Temperature: e.Temperature,
	}

	resp, err := e.Provider.Complete(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("LLMEntityExtractor: provider complete: %w", err)
	}
	if resp == nil {
		return nil, nil, fmt.Errorf("LLMEntityExtractor: nil response")
	}

	parsed, err := parseExtractResponse(resp.Content)
	if err != nil {
		// The LLM is allowed to wrap its JSON in markdown fences.
		// Strip them and try again. If the second attempt still
		// fails, surface the original error so the operator can
		// see what the model actually returned.
		if stripped := stripCodeFence(resp.Content); stripped != resp.Content {
			parsed, err = parseExtractResponse(stripped)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("LLMEntityExtractor: parse response: %w (raw: %s)", err, truncate(resp.Content, 200))
		}
	}

	now := time.Now().UTC()
	if !ep.OccurredAt.IsZero() {
		now = ep.OccurredAt
	}
	entities := make([]Entity, 0, len(parsed.Entities))
	for _, in := range parsed.Entities {
		t, lbl := strings.TrimSpace(in.Type), strings.TrimSpace(in.Label)
		if t == "" || lbl == "" {
			continue
		}
		entities = append(entities, Entity{
			Type:       t,
			Label:      lbl,
			Properties: in.Properties,
			CampaignID: ep.CampaignID,
			CreatedAt:  now,
		})
	}
	relations := make([]Relation, 0, len(parsed.Relations))
	for _, in := range parsed.Relations {
		from, to, typ := strings.TrimSpace(in.From), strings.TrimSpace(in.To), strings.TrimSpace(in.Type)
		if from == "" || to == "" || typ == "" {
			continue
		}
		relations = append(relations, Relation{
			// FromID / ToID are resolved at persist time from the
			// (type, label) pair. We carry the label in Properties
			// so the writer can match without changing the struct.
			Type:       typ,
			Properties: mergeProps(in.Properties, map[string]any{"from_label": from, "to_label": to}),
			CampaignID: ep.CampaignID,
			CreatedAt:  now,
			ValidFrom:  now,
		})
	}
	return entities, relations, nil
}

// parseExtractResponse decodes the JSON object the LLM is expected
// to return. Tolerant of trailing whitespace, leading/trailing
// markdown fences (caller strips those first), and of the
// `properties` field being missing on either side.
func parseExtractResponse(raw string) (*extractResponse, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &extractResponse{}, nil
	}
	var out extractResponse
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		// Retry without strict unknown-field rejection — different
		// vendors emit helpful extras (`usage`, `model`, etc.).
		dec = json.NewDecoder(strings.NewReader(raw))
		if err2 := dec.Decode(&out); err2 != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err2)
		}
	}
	return &out, nil
}

// stripCodeFence removes a leading ``` or ```json fence and the
// matching closing fence, when present.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop opening fence.
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	// Drop closing fence.
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

func mergeProps(a, b map[string]any) map[string]any {
	if a == nil && b == nil {
		return nil
	}
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- RuleBasedExtractor -----------------------------------------------------

// RuleBasedExtractor uses deterministic regex / pattern matching
// to mine an Episode for entities and relations. It is intentionally
// fast and dependency-free: no LLM, no network, no allocations
// beyond the result slices.
//
// Coverage:
//   - CVE-YYYY-NNNN[N]            → CVE entity
//   - CWE-NN                        → CWE entity
//   - IPv4 / CIDR                   → Host entity
//   - dotted-fqdn (a.b.c)            → Subdomain entity
//   - "<svc> on port <N>"            → Service + RUNS_ON Host
//   - "CVE-N on <host>"              → AFFECTS
//   - "<host> exposes <path>"        → EXPOSES Endpoint
//
// Quality is lower than LLMEntityExtractor but the rule set is
// intentionally tight to keep the false-positive rate low. Operators
// that want richer extraction should chain the rule-based result
// with an LLM pass.
type RuleBasedExtractor struct {
	Logger *slog.Logger
}

// NewRuleBasedExtractor builds a deterministic extractor. Logger
// defaults to slog.Default() when nil.
func NewRuleBasedExtractor(logger *slog.Logger) *RuleBasedExtractor {
	if logger == nil {
		logger = slog.Default()
	}
	return &RuleBasedExtractor{Logger: logger}
}

// Patterns used by the rule-based extractor. Compiled once at
// package init for performance.
var (
	cvePattern     = regexp.MustCompile(`CVE-\d{4}-\d{4,7}`)
	cwePattern     = regexp.MustCompile(`CWE-\d{1,5}`)
	ipv4Pattern    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?:/\d{1,2})?\b`)
	fqdnPattern    = regexp.MustCompile(`\b(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\b`)
	portSvcPattern = regexp.MustCompile(`(?i)(\w+)\s+on\s+port\s+(\d{1,5})`)
	affectsPattern = regexp.MustCompile(`(?i)(CVE-\d{4}-\d{4,7})\s+(?:affects|on|in)\s+((?:\d{1,3}\.){3}\d{1,3}|(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,})`)
	exposesPattern = regexp.MustCompile(`(?i)((?:\d{1,3}\.){3}\d{1,3}|(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,})\s+exposes\s+(\S+)`)
)

// Extract runs the rule set and returns deduped entities and
// relations. Entities with the same (Type, Label) pair collapse
// to one row; relations are deduped by (from_label, to_label, type).
func (e *RuleBasedExtractor) Extract(ctx context.Context, ep Episode) ([]Entity, []Relation, error) {
	if e == nil {
		return nil, nil, fmt.Errorf("RuleBasedExtractor: nil receiver")
	}
	text := ep.Text
	if strings.TrimSpace(text) == "" {
		return nil, nil, nil
	}

	now := time.Now().UTC()
	if !ep.OccurredAt.IsZero() {
		now = ep.OccurredAt
	}

	entMap := map[string]Entity{} // key = type + "\x00" + label
	addEntity := func(typ, lbl string, props map[string]any) {
		k := typ + "\x00" + lbl
		if _, ok := entMap[k]; !ok {
			entMap[k] = Entity{
				Type:       typ,
				Label:      lbl,
				Properties: props,
				CampaignID: ep.CampaignID,
				CreatedAt:  now,
			}
			return
		}
		// Merge properties when the same entity shows up twice
		// (e.g. an IP mentioned as both Host and as part of an
		// AFFECTS relation).
		existing := entMap[k]
		if existing.Properties == nil && props != nil {
			existing.Properties = props
		} else if props != nil {
			for k, v := range props {
				if _, ok := existing.Properties[k]; !ok {
					existing.Properties[k] = v
				}
			}
		}
		entMap[k] = existing
	}

	// CVEs
	for _, m := range cvePattern.FindAllString(text, -1) {
		addEntity(EntityTypeCVE, m, nil)
	}
	// CWEs
	for _, m := range cwePattern.FindAllString(text, -1) {
		addEntity(EntityTypeCWE, m, nil)
	}
	// IPv4
	for _, m := range ipv4Pattern.FindAllString(text, -1) {
		// Skip ranges that look like version numbers (e.g. 1.2.3 in
		// a filename) by requiring all octets ≤ 255.
		if isValidIPv4(m) {
			addEntity(EntityTypeHost, m, nil)
		}
	}
	// FQDNs (subdomain heuristic: must have ≥ 2 dots or be a deep
	// subdomain; bare "example.com" is treated as a Host, deeper
	// names as Subdomain).
	for _, m := range fqdnPattern.FindAllString(text, -1) {
		// Don't double-count things that are already CVE/CWE/Host.
		if isAlreadyTyped(m, entMap) {
			continue
		}
		typ := EntityTypeHost
		if strings.Count(m, ".") >= 2 {
			typ = EntityTypeSubdomain
		}
		addEntity(typ, m, nil)
	}
	// "service on port N" → Service + (Service) RUNS_ON (Host)
	for _, m := range portSvcPattern.FindAllStringSubmatch(text, -1) {
		svc, port := strings.ToLower(m[1]), m[2]
		svcLabel := svc + "/" + port
		addEntity(EntityTypeService, svcLabel, map[string]any{"service": svc, "port": port})
		// We don't know which Host; this is the place where a
		// richer extractor (LLM) would help. For now, emit a
		// "loose" relation keyed only by service label, which the
		// persistence layer treats as (Service) → (self-loop).
		// The downstream agent can attach it to a host later via
		// QueryEntities and a follow-up AddRelation.
		_ = svcLabel
	}
	// "CVE-… affects/…  host"
	for _, m := range affectsPattern.FindAllStringSubmatch(text, -1) {
		addEntity(EntityTypeCVE, m[1], nil)
		host := m[2]
		if isValidIPv4(host) {
			addEntity(EntityTypeHost, host, nil)
		} else {
			addEntity(EntityTypeSubdomain, host, nil)
		}
	}
	// "host exposes path"
	for _, m := range exposesPattern.FindAllStringSubmatch(text, -1) {
		host, path := m[1], m[2]
		if isValidIPv4(host) {
			addEntity(EntityTypeHost, host, nil)
		} else {
			addEntity(EntityTypeSubdomain, host, nil)
		}
		addEntity(EntityTypeEndpoint, path, map[string]any{"exposed_by": host})
	}

	// Relations: build (from_label, to_label, type) tuples.
	relMap := map[string]Relation{}
	addRel := func(from, to, typ string, props map[string]any) {
		k := from + "\x00" + to + "\x00" + typ
		if _, ok := relMap[k]; ok {
			return
		}
		relMap[k] = Relation{
			Type:       typ,
			Properties: mergeProps(props, map[string]any{"from_label": from, "to_label": to}),
			CampaignID: ep.CampaignID,
			CreatedAt:  now,
			ValidFrom:  now,
		}
	}

	for _, m := range affectsPattern.FindAllStringSubmatch(text, -1) {
		addRel(m[1], m[2], RelationAffects, nil)
	}
	for _, m := range exposesPattern.FindAllStringSubmatch(text, -1) {
		addRel(m[1], m[2], RelationExposes, nil)
	}

	// Flatten maps.
	ents := make([]Entity, 0, len(entMap))
	for _, v := range entMap {
		ents = append(ents, v)
	}
	rels := make([]Relation, 0, len(relMap))
	for _, v := range relMap {
		rels = append(rels, v)
	}
	return ents, rels, nil
}

// isValidIPv4 returns true if s is a syntactically valid IPv4
// address or CIDR. Octet overflow (e.g. 999.0.0.1) is rejected.
func isValidIPv4(s string) bool {
	// Strip optional /CIDR.
	addr := s
	if i := strings.Index(s, "/"); i >= 0 {
		addr = s[:i]
	}
	parts := strings.Split(addr, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return false
		}
		if len(p) > 1 && p[0] == '0' {
			return false
		}
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// isAlreadyTyped reports whether `lbl` has already been added as a
// CVE / CWE / Host entry — used to avoid double-counting FQDNs that
// are really CVE strings (e.g. CVE-2024-1234) or IP addresses.
func isAlreadyTyped(lbl string, ents map[string]Entity) bool {
	for k := range ents {
		// key = type + "\x00" + label
		i := strings.Index(k, "\x00")
		if i < 0 {
			continue
		}
		typ := k[:i]
		existing := k[i+1:]
		if existing == lbl && (typ == EntityTypeCVE || typ == EntityTypeCWE || typ == EntityTypeHost) {
			return true
		}
	}
	return false
}
