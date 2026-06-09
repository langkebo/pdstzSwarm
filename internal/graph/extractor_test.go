package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
)

// --- Test fixtures ----------------------------------------------------------

// stubProvider is a controllable llm.Provider. It returns the
// configured reply (or error) and records every call for
// assertions. Implements the full Provider interface so it can
// stand in for OpenAI / DeepSeek / Claude in tests.
type stubProvider struct {
	model    string
	reply    string
	err      error
	calls    atomic.Int64
	lastReq  atomic.Value // last CompletionRequest observed
}

func newStubProvider(model, reply string) *stubProvider {
	return &stubProvider{model: model, reply: reply}
}

func (s *stubProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	s.calls.Add(1)
	s.lastReq.Store(req)
	if s.err != nil {
		return nil, s.err
	}
	return &llm.CompletionResponse{Content: s.reply}, nil
}

func (s *stubProvider) Stream(ctx context.Context, req llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Done: true}
	close(ch)
	return ch, nil
}

func (s *stubProvider) HealthCheck(ctx context.Context) error { return nil }
func (s *stubProvider) ModelName() string                       { return s.model }
func (s *stubProvider) ContextWindow() int                      { return 128000 }
func (s *stubProvider) SupportsToolUse() bool                  { return false }

// --- LLMEntityExtractor tests -----------------------------------------------

// The extractor must validate the strict JSON shape and translate
// it into []Entity / []Relation with empty ID fields. The IDs are
// assigned by the persistence layer.
func TestLLMEntityExtractor_ValidatesSchema(t *testing.T) {
	raw := `{
		"entities": [
			{"type": "CVE", "label": "CVE-2024-1234", "properties": {"severity": "HIGH"}},
			{"type": "Host", "label": "10.0.0.5"}
		],
		"relations": [
			{"from": "CVE-2024-1234", "to": "10.0.0.5", "type": "AFFECTS"}
		]
	}`
	p := newStubProvider("test-model", raw)
	ext, err := NewLLMEntityExtractor(p, WithLLMExtractorLogger(nil))
	if err != nil {
		t.Fatalf("NewLLMEntityExtractor: %v", err)
	}
	ents, rels, err := ext.Extract(context.Background(), Episode{
		CampaignID: uuid.New(),
		Text:       "CVE-2024-1234 affects 10.0.0.5",
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ents) != 2 {
		t.Errorf("len(ents) = %d, want 2", len(ents))
	}
	if len(rels) != 1 {
		t.Errorf("len(rels) = %d, want 1", len(rels))
	}
	if ents[0].Type != "CVE" || ents[0].Label != "CVE-2024-1234" {
		t.Errorf("ent[0] = %+v", ents[0])
	}
	if ents[0].Properties["severity"] != "HIGH" {
		t.Errorf("ent[0] properties lost: %+v", ents[0].Properties)
	}
	if rels[0].Type != "AFFECTS" {
		t.Errorf("rel[0].Type = %s, want AFFECTS", rels[0].Type)
	}
	if p.calls.Load() != 1 {
		t.Errorf("provider calls = %d, want 1", p.calls.Load())
	}
}

// A ```json``` fenced response must be unwrapped and parsed.
func TestLLMEntityExtractor_StripsCodeFence(t *testing.T) {
	raw := "```json\n" + `{"entities":[{"type":"Host","label":"10.0.0.5"}],"relations":[]}` + "\n```"
	p := newStubProvider("test-model", raw)
	ext, _ := NewLLMEntityExtractor(p)
	ents, _, err := ext.Extract(context.Background(), Episode{Text: "10.0.0.5"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ents) != 1 || ents[0].Label != "10.0.0.5" {
		t.Errorf("ents = %+v", ents)
	}
}

// An empty (entities:[], relations:[]) response is a successful
// no-op — no error, no entities, no relations.
func TestLLMEntityExtractor_HandlesEmptyResponse(t *testing.T) {
	p := newStubProvider("test-model", `{"entities": [], "relations": []}`)
	ext, _ := NewLLMEntityExtractor(p)
	ents, rels, err := ext.Extract(context.Background(), Episode{Text: "no entities here"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ents) != 0 || len(rels) != 0 {
		t.Errorf("ents/rels should be empty, got %+v / %+v", ents, rels)
	}
}

// A malformed response must NOT panic; it must return an error
// (so the calling hook can log + drop).
func TestLLMEntityExtractor_HandlesMalformedJSON(t *testing.T) {
	p := newStubProvider("test-model", "not-json-at-all")
	ext, _ := NewLLMEntityExtractor(p)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Extract panicked on malformed JSON: %v", r)
		}
	}()
	_, _, err := ext.Extract(context.Background(), Episode{Text: "x"})
	if err == nil {
		t.Fatal("Extract: expected error for malformed JSON, got nil")
	}
}

// An empty / whitespace-only episode text returns a successful
// no-op without ever calling the provider. The hot path for the
// blackboard hook.
func TestLLMEntityExtractor_SkipsEmptyText(t *testing.T) {
	p := newStubProvider("test-model", "")
	ext, _ := NewLLMEntityExtractor(p)
	_, _, err := ext.Extract(context.Background(), Episode{Text: "   \t\n  "})
	if err != nil {
		t.Errorf("Extract: %v", err)
	}
	if p.calls.Load() != 0 {
		t.Errorf("provider calls = %d, want 0 (no work)", p.calls.Load())
	}
}

// A provider error must surface so the hook can log + drop.
func TestLLMEntityExtractor_ProviderErrorPropagates(t *testing.T) {
	p := newStubProvider("test-model", "")
	p.err = errors.New("rate limited")
	ext, _ := NewLLMEntityExtractor(p)
	_, _, err := ext.Extract(context.Background(), Episode{Text: "x"})
	if err == nil {
		t.Fatal("expected error from provider")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should mention rate limited, got: %v", err)
	}
}

// NewLLMEntityExtractor(nil) must return an error.
func TestLLMEntityExtractor_NilProviderRejected(t *testing.T) {
	_, err := NewLLMEntityExtractor(nil)
	if err == nil {
		t.Error("expected error for nil provider")
	}
}

// The model field is filled from Provider.ModelName() when the
// caller doesn't override.
func TestLLMEntityExtractor_DefaultModelFromProvider(t *testing.T) {
	p := newStubProvider("claude-test", "{}")
	ext, _ := NewLLMEntityExtractor(p)
	if ext.Model != "claude-test" {
		t.Errorf("Model = %q, want claude-test", ext.Model)
	}
}

// --- RuleBasedExtractor tests -----------------------------------------------

// The rule engine must recognise the canonical CVE pattern.
func TestRuleBasedExtractor_CVEDetection(t *testing.T) {
	ext := NewRuleBasedExtractor(nil)
	ents, _, err := ext.Extract(context.Background(), Episode{Text: "CVE-2024-1234"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("expected 1 entity, got %d (%+v)", len(ents), ents)
	}
	if ents[0].Type != EntityTypeCVE || ents[0].Label != "CVE-2024-1234" {
		t.Errorf("entity = %+v, want CVE/CVE-2024-1234", ents[0])
	}
}

// IPv4 must be classified as Host, and the syntactic validator
// must reject invalid octets.
func TestRuleBasedExtractor_HostnameDetection(t *testing.T) {
	ext := NewRuleBasedExtractor(nil)
	ents, _, err := ext.Extract(context.Background(), Episode{
		Text: "scan hit 10.0.0.5 and 999.0.0.1 (bad octet, dropped)",
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// 10.0.0.5 should be picked up; 999.0.0.1 is rejected.
	found := false
	for _, e := range ents {
		if e.Label == "10.0.0.5" && e.Type == EntityTypeHost {
			found = true
		}
		if e.Label == "999.0.0.1" {
			t.Errorf("999.0.0.1 should be rejected, got %+v", e)
		}
	}
	if !found {
		t.Errorf("10.0.0.5 not in entities: %+v", ents)
	}
}

// The "service on port N" pattern creates a Service entity.
func TestRuleBasedExtractor_ServiceDetection(t *testing.T) {
	ext := NewRuleBasedExtractor(nil)
	ents, _, err := ext.Extract(context.Background(), Episode{
		Text: "nmap reports http on port 80",
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	found := false
	for _, e := range ents {
		if e.Type == EntityTypeService && (e.Label == "http/80" || e.Label == "http") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a Service entity, got %+v", ents)
	}
}

// "CVE-X affects/… host" must produce BOTH the CVE and the host,
// and a single AFFECTS relation.
func TestRuleBasedExtractor_AffectsPattern(t *testing.T) {
	ext := NewRuleBasedExtractor(nil)
	ents, rels, err := ext.Extract(context.Background(), Episode{
		Text: "CVE-2024-1234 affects 10.0.0.5",
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var hasCVE, hasHost bool
	for _, e := range ents {
		if e.Type == EntityTypeCVE && e.Label == "CVE-2024-1234" {
			hasCVE = true
		}
		if e.Type == EntityTypeHost && e.Label == "10.0.0.5" {
			hasHost = true
		}
	}
	if !hasCVE || !hasHost {
		t.Errorf("missing CVE/Host: %+v", ents)
	}
	if len(rels) != 1 {
		t.Errorf("expected 1 relation, got %d: %+v", len(rels), rels)
	}
	if len(rels) > 0 && rels[0].Type != RelationAffects {
		t.Errorf("rel[0].Type = %s, want AFFECTS", rels[0].Type)
	}
}

// "host exposes path" must produce a Subdomain/Host + an
// Endpoint entity and an EXPOSES relation.
func TestRuleBasedExtractor_ExposesPattern(t *testing.T) {
	ext := NewRuleBasedExtractor(nil)
	ents, rels, err := ext.Extract(context.Background(), Episode{
		Text: "www.example.com exposes /admin/login",
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var hasEndpoint, hasHost bool
	for _, e := range ents {
		if e.Type == EntityTypeEndpoint {
			hasEndpoint = true
		}
		if e.Type == EntityTypeSubdomain && e.Label == "www.example.com" {
			hasHost = true
		}
	}
	if !hasEndpoint {
		t.Errorf("expected Endpoint entity, got %+v", ents)
	}
	if !hasHost {
		t.Errorf("expected Subdomain entity for www.example.com, got %+v", ents)
	}
	if len(rels) != 1 || rels[0].Type != RelationExposes {
		t.Errorf("relations = %+v, want one EXPOSES", rels)
	}
}

// Identical entities (same Type+Label) must dedupe to one.
func TestRuleBasedExtractor_DedupesEntities(t *testing.T) {
	ext := NewRuleBasedExtractor(nil)
	ents, _, _ := ext.Extract(context.Background(), Episode{
		Text: "CVE-2024-1234 and CVE-2024-1234 again",
	})
	cveCount := 0
	for _, e := range ents {
		if e.Type == EntityTypeCVE && e.Label == "CVE-2024-1234" {
			cveCount++
		}
	}
	if cveCount != 1 {
		t.Errorf("CVE count = %d, want 1 (deduped)", cveCount)
	}
}

// Empty / whitespace text must return a successful no-op.
func TestRuleBasedExtractor_EmptyText(t *testing.T) {
	ext := NewRuleBasedExtractor(nil)
	ents, rels, err := ext.Extract(context.Background(), Episode{Text: "   "})
	if err != nil {
		t.Errorf("Extract: %v", err)
	}
	if len(ents) != 0 || len(rels) != 0 {
		t.Errorf("empty text should yield no entities/relations")
	}
}

// --- Common test helpers ----------------------------------------------------

// _ ensures the encoder package is used; guards against an
// accidental refactor that drops the json import.
var _ = json.Marshal
