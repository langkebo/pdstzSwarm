package graph

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/swarm/blackboard"
)

// --- GraphHook --------------------------------------------------------------

// The hook must convert a Finding into an Episode and persist the
// extracted entities/relations in the graph.
func TestGraphHook_OnWrite_ExtractsAndPersists(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ext := NewRuleBasedExtractor(nil)
	hook := NewGraphHook(store, ext, WithSkipTypes()) // disable default skip-list
	f := &blackboard.Finding{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		AgentName:  "recon",
		Type:       blackboard.TypeCVEMatch,
		Target:     "10.0.0.5",
		Data:       []byte(`{"cve":"CVE-2024-1234"}`),
		CreatedAt:  time.Now().UTC(),
	}
	if err := hook.OnWrite(context.Background(), f); err != nil {
		t.Fatalf("OnWrite: %v", err)
	}
	calls, skipped, errs, extracted := hook.Stats()
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if errs != 0 {
		t.Errorf("errs = %d, want 0", errs)
	}
	if extracted != 1 {
		t.Errorf("extracted = %d, want 1", extracted)
	}

	// Verify the entities are in the store.
	ents, err := store.QueryEntities(context.Background(), EntityQuery{})
	if err != nil {
		t.Fatalf("QueryEntities: %v", err)
	}
	if len(ents) < 1 {
		t.Errorf("expected entities in store, got 0")
	}
}

// The default skip-list (TypeAgentError, TypeCampaignComplete)
// must keep those types out of the graph.
func TestGraphHook_SkipsConfiguredTypes(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ext := NewRuleBasedExtractor(nil)
	hook := NewGraphHook(store, ext) // default skip list
	for _, ft := range []blackboard.FindingType{blackboard.TypeAgentError, blackboard.TypeCampaignComplete} {
		f := &blackboard.Finding{
			ID:         uuid.New(),
			CampaignID: uuid.New(),
			AgentName:  "system",
			Type:       ft,
			Target:     "10.0.0.5",
			Data:       []byte(`CVE-2024-1234 affects 10.0.0.5`),
		}
		if err := hook.OnWrite(context.Background(), f); err != nil {
			t.Fatalf("OnWrite %s: %v", ft, err)
		}
	}
	calls, skipped, _, _ := hook.Stats()
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	ents, _ := store.QueryEntities(context.Background(), EntityQuery{})
	if len(ents) != 0 {
		t.Errorf("skipped types should not write entities, got %d", len(ents))
	}
}

// An extractor error must NOT panic and must NOT block future
// writes. The hot path is the blackboard writing path, so the
// hook must degrade gracefully.
func TestGraphHook_ExtractorErrorDoesNotPanic(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ext := &failingHookExtractor{}
	hook := NewGraphHook(store, ext, WithSkipTypes())
	f := &blackboard.Finding{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		AgentName:  "x",
		Type:       blackboard.TypeCVEMatch,
		Target:     "x",
		Data:       []byte(`{}`),
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("OnWrite panicked: %v", r)
		}
	}()
	err := hook.OnWrite(context.Background(), f)
	if err == nil {
		t.Error("expected error from failing extractor")
	}
	calls, _, errs, _ := hook.Stats()
	if calls != 1 || errs != 1 {
		t.Errorf("Stats = (calls=%d, errs=%d), want (1, 1)", calls, errs)
	}
	// A subsequent call must still go through.
	ext2 := NewRuleBasedExtractor(nil)
	hook2 := NewGraphHook(store, ext2, WithSkipTypes())
	if err := hook2.OnWrite(context.Background(), f); err != nil {
		t.Errorf("second hook call: %v", err)
	}
}

// OnWrite passes the finding through when the text is very
// short (e.g. just the Type), since the LLM/rule-based
// extractor will likely return nothing useful. We don't count
// it as "skipped" — only explicit skip-list matches and
// truly-empty episodes are. This test pins that semantic.
func TestGraphHook_ShortTextGoesThrough(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ext := NewRuleBasedExtractor(nil)
	hook := NewGraphHook(store, ext, WithSkipTypes())
	f := &blackboard.Finding{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		AgentName:  "",
		Type:       blackboard.TypeSubdomain,
		Target:     "",
		Data:       nil,
	}
	if err := hook.OnWrite(context.Background(), f); err != nil {
		t.Errorf("OnWrite: %v", err)
	}
	_, skipped, _, _ := hook.Stats()
	// skipped stays 0 because the text is non-empty (": SUBDOMAIN")
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 (short text still goes through)", skipped)
	}
}

// WithIncludeAllTypes inverts the skip-list. Useful for
// operators that want everything graphed (even errors).
func TestGraphHook_IncludeAllTypes(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ext := NewRuleBasedExtractor(nil)
	hook := NewGraphHook(store, ext, WithIncludeAllTypes())
	f := &blackboard.Finding{
		ID:         uuid.New(),
		CampaignID: uuid.New(),
		AgentName:  "system",
		Type:       blackboard.TypeAgentError,
		Target:     "10.0.0.5",
		Data:       []byte(`CVE-2024-1234 affects 10.0.0.5`),
	}
	if err := hook.OnWrite(context.Background(), f); err != nil {
		t.Fatalf("OnWrite: %v", err)
	}
	_, skipped, _, _ := hook.Stats()
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 (IncludeAllTypes set)", skipped)
	}
	ents, _ := store.QueryEntities(context.Background(), EntityQuery{})
	if len(ents) == 0 {
		t.Errorf("IncludeAllTypes should write entities, got 0")
	}
}

// Name returns "graph" for observability.
func TestGraphHook_Name(t *testing.T) {
	hook := NewGraphHook(NewMockNeo4jGraphStore(), NewRuleBasedExtractor(nil))
	if hook.Name() != "graph" {
		t.Errorf("Name = %q, want graph", hook.Name())
	}
}

// nil receiver must not panic.
func TestGraphHook_NilSafe(t *testing.T) {
	var h *GraphHook
	if err := h.OnWrite(context.Background(), &blackboard.Finding{}); err != nil {
		t.Errorf("nil receiver: %v", err)
	}
}

// --- MockNeo4jGraphStore (in-memory driver) --------------------------------

// The mock must implement the GraphStore contract end-to-end so
// tests can use it as a drop-in replacement for the Postgres or
// real-Neo4j backends.
func TestMockNeo4jGraphStore_AddEntityAndQuery(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ctx := context.Background()
	campaignID := uuid.New()

	_, err := store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-1", CampaignID: campaignID})
	if err != nil {
		t.Fatalf("AddEntity: %v", err)
	}
	_, err = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-2", CampaignID: campaignID})
	if err != nil {
		t.Fatalf("AddEntity #2: %v", err)
	}
	_, err = store.AddEntity(ctx, Entity{Type: EntityTypeHost, Label: "10.0.0.1", CampaignID: campaignID})
	if err != nil {
		t.Fatalf("AddEntity #3: %v", err)
	}

	ents, err := store.QueryEntities(ctx, EntityQuery{Type: EntityTypeCVE, CampaignID: &campaignID})
	if err != nil {
		t.Fatalf("QueryEntities: %v", err)
	}
	if len(ents) != 2 {
		t.Errorf("CVE entities: got %d, want 2", len(ents))
	}
}

// Traverse in the mock must do BFS correctly.
func TestMockNeo4jGraphStore_Traverse(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ctx := context.Background()
	campaignID := uuid.New()
	aID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "a", CampaignID: campaignID})
	bID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "b", CampaignID: campaignID})
	cID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "c", CampaignID: campaignID})
	_, _ = store.AddRelation(ctx, Relation{FromID: aID, ToID: bID, Type: "AB", CampaignID: campaignID})
	_, _ = store.AddRelation(ctx, Relation{FromID: bID, ToID: cID, Type: "BC", CampaignID: campaignID})

	trav, err := store.Traverse(ctx, aID, 2)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if len(trav.Entities) != 3 {
		t.Errorf("BFS entities: got %d, want 3", len(trav.Entities))
	}
}

// Stats on the mock must report what was inserted.
func TestMockNeo4jGraphStore_Stats(t *testing.T) {
	store := NewMockNeo4jGraphStore()
	ctx := context.Background()
	campaignID := uuid.New()
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-1", CampaignID: campaignID})
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-2", CampaignID: campaignID})
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.EntityCount != 2 {
		t.Errorf("EntityCount = %d, want 2", stats.EntityCount)
	}
	if stats.ByType[EntityTypeCVE] != 2 {
		t.Errorf("ByType[CVE] = %d, want 2", stats.ByType[EntityTypeCVE])
	}
}

// --- PostgresBoard integration ---------------------------------------------

// AddGraphHook must register the hook so it fires on Write, and
// the error path (extractor fails) must not break the writing
// path. The integration runs against a real Postgres to validate
// the actual AddEpisode SQL works on a fresh store.
func TestGraphHook_IntegrationWithPostgresBoard(t *testing.T) {
	pool := requireTestPool(t)
	defer pool.Close()
	ctx := context.Background()

	campaignID := uuid.New()
	// Create the campaigns + swarm_findings tables (FK target
	// for the blackboard) if they don't exist in this database.
	// The test should be self-sufficient — the live-test DB
	// does not necessarily have migrations applied.
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS campaigns (
			id UUID PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		t.Fatalf("create campaigns: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO campaigns (id, name) VALUES ($1, $2)
		 ON CONFLICT (id) DO NOTHING`,
		campaignID, "graph-test"); err != nil {
		t.Fatalf("insert campaign: %v", err)
	}
	// The blackboard's swarm_findings table is the minimum
	// surface the integration test needs. We don't recreate the
	// full schema (embedding column, indexes, etc.) — the
	// blackboard's Write path is happy with a subset, and the
	// tests are testing graph behaviour, not the blackboard.
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS swarm_findings (
			id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			campaign_id UUID NOT NULL,
			agent_name TEXT NOT NULL,
			finding_type TEXT NOT NULL,
			target TEXT NOT NULL,
			data JSONB NOT NULL DEFAULT '{}'::jsonb,
			pheromone_base DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			half_life_sec INTEGER NOT NULL DEFAULT 3600,
			embedding vector(384),
			superseded_by UUID,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		t.Skipf("create swarm_findings: %v (vector(384) may not be supported)", err)
	}

	// Set up the board + graph.
	store := NewPostgresGraphStore(pool)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	ext := NewRuleBasedExtractor(nil)
	hook := NewGraphHook(store, ext, WithSkipTypes())

	board := blackboard.NewPostgresBoard(pool)
	board.AddGraphHook(hook)

	// Write a Finding that should be extracted.
	id, err := board.Write(ctx, blackboard.Finding{
		CampaignID: campaignID,
		AgentName:  "recon",
		Type:       blackboard.TypeCVEMatch,
		Target:     "10.0.0.5",
		Data:       []byte(`{"cve":"CVE-2024-1234"}`),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("Write returned Nil id")
	}
	calls, _, errs, _ := hook.Stats()
	if calls != 1 {
		t.Errorf("hook calls = %d, want 1", calls)
	}
	if errs != 0 {
		t.Errorf("hook errs = %d, want 0", errs)
	}

	// Verify the graph picked up the entities.
	cveEnts, err := store.QueryEntities(ctx, EntityQuery{Type: EntityTypeCVE, CampaignID: &campaignID})
	if err != nil {
		t.Fatalf("QueryEntities: %v", err)
	}
	if len(cveEnts) == 0 {
		t.Errorf("expected CVE entities in graph for campaign %s, got 0", campaignID)
	}
}

// --- helpers ---------------------------------------------------------------

// failingHookExtractor returns a synthetic error on every call.
// Used to test the hook's fail-soft semantics.
type failingHookExtractor struct {
	calls atomic.Int64
}

func (f *failingHookExtractor) Extract(ctx context.Context, ep Episode) ([]Entity, []Relation, error) {
	f.calls.Add(1)
	return nil, nil, errors.New("synthetic extractor error")
}
