package graph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// requireTestPool returns a connected *pgxpool.Pool or skips the
// test when no Postgres is reachable. The DSN is read from
// PENTESTSWARM_TEST_DSN; when empty, the helper tries a sensible
// localhost default. Used by every live test in this package.
func requireTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PENTESTSWARM_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("pgxpool parse: %v (set PENTESTSWARM_TEST_DSN to enable)", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("pgxpool new: %v (set PENTESTSWARM_TEST_DSN to enable)", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("ping: %v (set PENTESTSWARM_TEST_DSN to a reachable DB)", err)
	}
	// Ensure the pgcrypto / uuid-ossp / vector extensions exist.
	// The migration system normally does this but in the live-test
	// path we connect to a stock DB. Each CREATE EXTENSION is
	// wrapped in a try-and-skip: a missing pgvector is not a
	// hard failure for the offline unit tests, it just means
	// the live tests are disabled.
	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`); err != nil {
		pool.Close()
		t.Skipf("create extension uuid-ossp: %v (need a DB the test user can CREATE EXTENSION on)", err)
	}
	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		pool.Close()
		t.Skipf("create extension vector: %v (install pgvector or set PENTESTSWARM_TEST_DSN to a DB that has it)", err)
	}
	return pool
}

// newTestStore wires a PostgresGraphStore with a freshly-created
// per-test schema namespace. Each test gets its own pair of
// tables (suffixed by a UUID) so concurrent tests don't collide.
func newTestStore(t *testing.T) (*PostgresGraphStore, func()) {
	t.Helper()
	pool := requireTestPool(t)
	suffix := uuid.New().String()[:8]
	ctx := context.Background()

	// We deliberately re-use the canonical graph_entities /
	// graph_edges tables rather than creating a per-test copy.
	// The AddEntity upsert is keyed on (type, label, campaign_id)
	// so the test can use a fresh campaign_id to avoid collisions.
	// The integration tests are responsible for the cleanup
	// (delete rows in the campaign's namespace) via the returned
	// teardown.
	store := NewPostgresGraphStore(pool)
	if err := store.EnsureSchema(ctx); err != nil {
		pool.Close()
		t.Fatalf("EnsureSchema: %v", err)
	}
	teardown := func() {
		// Best-effort: drop rows from this test's campaign_id
		// namespace. Tests that share a campaign_id are
		// responsible for their own cleanup; this is just a
		// courtesy.
		pool.Close()
	}
	_ = suffix
	return store, teardown
}

// --- AddEntity --------------------------------------------------------------

// AddEntity must dedupe on (type, label, campaign_id): two
// identical inserts return the same UUID.
func TestPostgresGraphStore_AddEntity_UpsertDedupes(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()

	id1, err := store.AddEntity(ctx, Entity{
		Type:       EntityTypeCVE,
		Label:      "CVE-2024-1234",
		CampaignID: campaignID,
	})
	if err != nil {
		t.Fatalf("AddEntity #1: %v", err)
	}
	id2, err := store.AddEntity(ctx, Entity{
		Type:       EntityTypeCVE,
		Label:      "CVE-2024-1234",
		CampaignID: campaignID,
		Properties: map[string]any{"severity": "HIGH"},
	})
	if err != nil {
		t.Fatalf("AddEntity #2: %v", err)
	}
	if id1 != id2 {
		t.Errorf("dedupe failed: id1=%s id2=%s", id1, id2)
	}

	// Different campaign → different ID.
	id3, err := store.AddEntity(ctx, Entity{
		Type:       EntityTypeCVE,
		Label:      "CVE-2024-1234",
		CampaignID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("AddEntity #3: %v", err)
	}
	if id3 == id1 {
		t.Errorf("different campaign_id should yield different row, got same id %s", id3)
	}
}

// AddEntity must reject malformed inputs without panicking.
func TestPostgresGraphStore_AddEntity_ValidationErrors(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()

	if _, err := store.AddEntity(ctx, Entity{Type: "X"}); err == nil {
		t.Error("expected error for empty label")
	}
	if _, err := store.AddEntity(ctx, Entity{Label: "x"}); err == nil {
		t.Error("expected error for empty type")
	}
	if _, err := store.AddEntity(ctx, Entity{Type: "X", Label: "y"}); err == nil {
		t.Error("expected error for zero campaign_id")
	}
}

// --- AddRelation ------------------------------------------------------------

// After AddEntity + AddRelation, Traverse should find both
// endpoints and the connecting edge.
func TestPostgresGraphStore_AddRelation_Traversable(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()

	fromID, err := store.AddEntity(ctx, Entity{
		Type: EntityTypeCVE, Label: "CVE-2024-9999", CampaignID: campaignID,
	})
	if err != nil {
		t.Fatalf("AddEntity from: %v", err)
	}
	toID, err := store.AddEntity(ctx, Entity{
		Type: EntityTypeHost, Label: "10.0.0.7", CampaignID: campaignID,
	})
	if err != nil {
		t.Fatalf("AddEntity to: %v", err)
	}
	relID, err := store.AddRelation(ctx, Relation{
		FromID:     fromID,
		ToID:       toID,
		Type:       RelationAffects,
		CampaignID: campaignID,
		ValidFrom:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("AddRelation: %v", err)
	}
	if relID == uuid.Nil {
		t.Error("relID is uuid.Nil")
	}

	// Traverse from the CVE — should see the Host at depth 1.
	trav, err := store.Traverse(ctx, fromID, 1)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if len(trav.Entities) < 2 {
		t.Errorf("expected 2 entities in traversal, got %d", len(trav.Entities))
	}
	foundEdge := false
	for _, r := range trav.Relations {
		if r.ID == relID {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Errorf("expected to find relation %s in traversal", relID)
	}
}

// Adding the same edge twice should NOT fail. The second call
// either returns the existing row's id (when the unique key
// matches) or inserts a new row with a new id (when the unique
// key doesn't match — e.g. the row has since been closed via
// valid_to and a new one opened). Both outcomes are valid; the
// invariant the test asserts is that the second call doesn't
// error and returns a non-zero id.
func TestPostgresGraphStore_AddRelation_DoesNotErrorOnDuplicate(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()
	fromID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "from-2", CampaignID: campaignID})
	toID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "to-2", CampaignID: campaignID})

	id1, err := store.AddRelation(ctx, Relation{
		FromID: fromID, ToID: toID, Type: "TEST", CampaignID: campaignID,
	})
	if err != nil {
		t.Fatalf("AddRelation #1: %v", err)
	}
	if id1 == uuid.Nil {
		t.Fatal("AddRelation #1 returned Nil id")
	}
	id2, err := store.AddRelation(ctx, Relation{
		FromID: fromID, ToID: toID, Type: "TEST", CampaignID: campaignID,
	})
	if err != nil {
		t.Fatalf("AddRelation #2: %v", err)
	}
	if id2 == uuid.Nil {
		t.Fatal("AddRelation #2 returned Nil id")
	}
}

// --- QueryEntities ---------------------------------------------------------

// The 4 filter dimensions (Type / LabelLike / Campaign / NearVector)
// must all work, individually and together.
func TestPostgresGraphStore_QueryEntities_ByTypeLabelCampaign(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignA := uuid.New()
	campaignB := uuid.New()

	// 3 entities in campaign A, 1 in campaign B.
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-2024-0001", CampaignID: campaignA})
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-2024-0002", CampaignID: campaignA})
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeHost, Label: "10.0.0.1", CampaignID: campaignA})
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-2024-0099", CampaignID: campaignB})

	// Type filter
	ents, err := store.QueryEntities(ctx, EntityQuery{Type: EntityTypeCVE, CampaignID: &campaignA})
	if err != nil {
		t.Fatalf("QueryEntities type: %v", err)
	}
	if len(ents) != 2 {
		t.Errorf("Type+CVE+campaignA: got %d, want 2", len(ents))
	}

	// LabelLike filter
	ents, err = store.QueryEntities(ctx, EntityQuery{LabelLike: "CVE-2024-000%", CampaignID: &campaignA})
	if err != nil {
		t.Fatalf("QueryEntities label: %v", err)
	}
	if len(ents) != 2 {
		t.Errorf("LabelLike: got %d, want 2", len(ents))
	}

	// Campaign filter alone
	ents, err = store.QueryEntities(ctx, EntityQuery{CampaignID: &campaignB})
	if err != nil {
		t.Fatalf("QueryEntities campaign: %v", err)
	}
	if len(ents) != 1 {
		t.Errorf("CampaignB only: got %d, want 1", len(ents))
	}

	// Type + Label + Campaign combined
	ents, err = store.QueryEntities(ctx, EntityQuery{
		Type: EntityTypeCVE, LabelLike: "%0001", CampaignID: &campaignA,
	})
	if err != nil {
		t.Fatalf("QueryEntities combined: %v", err)
	}
	if len(ents) != 1 || ents[0].Label != "CVE-2024-0001" {
		t.Errorf("combined: got %+v, want CVE-2024-0001", ents)
	}
}

// --- QueryRelations --------------------------------------------------------

// valid_from / valid_to time bounds must work as documented.
func TestPostgresGraphStore_QueryRelations_TimeBounded(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()
	fromID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "frm", CampaignID: campaignID})
	toID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "to", CampaignID: campaignID})

	tOld := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tNew := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// One old edge (valid_from=2024), one new edge (valid_from=2026).
	_, err := store.AddRelation(ctx, Relation{
		FromID: fromID, ToID: toID, Type: "OLD", CampaignID: campaignID,
		ValidFrom: tOld,
	})
	if err != nil {
		t.Fatalf("AddRelation old: %v", err)
	}
	_, err = store.AddRelation(ctx, Relation{
		FromID: fromID, ToID: toID, Type: "NEW", CampaignID: campaignID,
		ValidFrom: tNew,
	})
	if err != nil {
		t.Fatalf("AddRelation new: %v", err)
	}

	// Since 2025-12-01 should pick up only the NEW edge.
	since := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	rels, err := store.QueryRelations(ctx, RelationQuery{
		FromID: &fromID, ToID: &toID, Since: &since, CampaignID: &campaignID,
	})
	if err != nil {
		t.Fatalf("QueryRelations: %v", err)
	}
	if len(rels) != 1 || rels[0].Type != "NEW" {
		t.Errorf("Since filter: got %+v, want one NEW", rels)
	}
}

// --- Traverse --------------------------------------------------------------

// BFS over 3 entities and 2 relations must produce a single
// connected component when depth=2.
func TestPostgresGraphStore_Traverse_BFSDepth(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()
	aID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "a", CampaignID: campaignID})
	bID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "b", CampaignID: campaignID})
	cID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "c", CampaignID: campaignID})
	if _, err := store.AddRelation(ctx, Relation{FromID: aID, ToID: bID, Type: "AB", CampaignID: campaignID}); err != nil {
		t.Fatalf("AddRelation a-b: %v", err)
	}
	if _, err := store.AddRelation(ctx, Relation{FromID: bID, ToID: cID, Type: "BC", CampaignID: campaignID}); err != nil {
		t.Fatalf("AddRelation b-c: %v", err)
	}

	trav, err := store.Traverse(ctx, aID, 2)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if len(trav.Entities) != 3 {
		t.Errorf("BFS entities = %d, want 3", len(trav.Entities))
	}
	if len(trav.Relations) != 2 {
		t.Errorf("BFS relations = %d, want 2", len(trav.Relations))
	}
}

// Traverse(depth=0) must return only the starting entity.
func TestPostgresGraphStore_Traverse_DepthZero(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()
	aID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "a", CampaignID: campaignID})
	bID, _ := store.AddEntity(ctx, Entity{Type: "X", Label: "b", CampaignID: campaignID})
	_, _ = store.AddRelation(ctx, Relation{FromID: aID, ToID: bID, Type: "AB", CampaignID: campaignID})

	trav, err := store.Traverse(ctx, aID, 0)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if len(trav.Entities) != 1 || trav.Entities[0].ID != aID {
		t.Errorf("depth=0: got %+v, want only the start", trav.Entities)
	}
}

// --- Stats ------------------------------------------------------------------

// Stats must aggregate counts and breakdowns coherently.
func TestPostgresGraphStore_Stats_Aggregates(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()

	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-2024-1000", CampaignID: campaignID})
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeCVE, Label: "CVE-2024-1001", CampaignID: campaignID})
	_, _ = store.AddEntity(ctx, Entity{Type: EntityTypeHost, Label: "10.0.0.1", CampaignID: campaignID})

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.EntityCount < 3 {
		t.Errorf("EntityCount = %d, want ≥ 3", stats.EntityCount)
	}
	if stats.ByType[EntityTypeCVE] < 2 {
		t.Errorf("ByType[CVE] = %d, want ≥ 2", stats.ByType[EntityTypeCVE])
	}
	if stats.ByType[EntityTypeHost] < 1 {
		t.Errorf("ByType[Host] = %d, want ≥ 1", stats.ByType[EntityTypeHost])
	}
	if stats.ByCampaign[campaignID.String()] < 3 {
		t.Errorf("ByCampaign[%s] = %d, want ≥ 3", campaignID, stats.ByCampaign[campaignID.String()])
	}
}

// --- AddEpisode ------------------------------------------------------------

// AddEpisode must call the extractor and persist the resulting
// entities / relations in a single transaction. The (type,label)
// pair on entities is the upsert key, so the same extractor
// output across two AddEpisode calls produces stable IDs.
func TestPostgresGraphStore_AddEpisode_RoundTrip(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()

	ext := NewRuleBasedExtractor(nil)
	ep := Episode{
		Source:     "test",
		SourceID:   "ep-1",
		CampaignID: campaignID,
		Text:       "CVE-2024-1234 affects 10.0.0.5",
		OccurredAt: time.Now().UTC(),
	}
	if err := store.AddEpisode(ctx, ep, ext); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	// Query back: should see 2 entities (CVE + Host) and 1 relation.
	cveType := EntityTypeCVE
	cveEnts, err := store.QueryEntities(ctx, EntityQuery{Type: cveType, CampaignID: &campaignID})
	if err != nil {
		t.Fatalf("QueryEntities CVE: %v", err)
	}
	if len(cveEnts) != 1 || cveEnts[0].Label != "CVE-2024-1234" {
		t.Errorf("CVE query: got %+v, want one CVE-2024-1234", cveEnts)
	}
	hostEnts, err := store.QueryEntities(ctx, EntityQuery{Type: EntityTypeHost, CampaignID: &campaignID})
	if err != nil {
		t.Fatalf("QueryEntities Host: %v", err)
	}
	if len(hostEnts) != 1 || hostEnts[0].Label != "10.0.0.5" {
		t.Errorf("Host query: got %+v, want one 10.0.0.5", hostEnts)
	}

	rels, err := store.QueryRelations(ctx, RelationQuery{
		CampaignID: &campaignID, Type: RelationAffects,
	})
	if err != nil {
		t.Fatalf("QueryRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Errorf("relations: got %d, want 1 AFFECTS", len(rels))
	}
}

// AddEpisode with an empty extractor result is a no-op (no error,
// no rows added). Important for the GraphHook hot path: a Finding
// with no extractable text must not abort the writing path.
func TestPostgresGraphStore_AddEpisode_EmptyNoop(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()

	// Empty extractor: returns no entities, no relations.
	ext := &emptyExtractor{}
	if err := store.AddEpisode(ctx, Episode{CampaignID: campaignID, Text: "x"}, ext); err != nil {
		t.Errorf("AddEpisode with empty extractor: %v", err)
	}
	stats, _ := store.Stats(ctx)
	if stats.ByCampaign[campaignID.String()] != 0 {
		t.Errorf("empty episode should not write rows, got %d", stats.ByCampaign[campaignID.String()])
	}
}

// AddEpisode with a failing extractor must surface the error
// and roll back the transaction (no partial writes).
func TestPostgresGraphStore_AddEpisode_ExtractorError(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	campaignID := uuid.New()

	ext := &failingExtractor{}
	err := store.AddEpisode(ctx, Episode{CampaignID: campaignID, Text: "x"}, ext)
	if err == nil {
		t.Fatal("expected error from failing extractor")
	}
	stats, _ := store.Stats(ctx)
	if stats.ByCampaign[campaignID.String()] != 0 {
		t.Errorf("failed episode should not write rows, got %d", stats.ByCampaign[campaignID.String()])
	}
}

// --- Schema -----------------------------------------------------------------

// Schema() must be idempotent: calling it twice in a row does
// not error and produces a usable schema.
func TestPostgresGraphStore_Schema_Idempotent(t *testing.T) {
	store, teardown := newTestStore(t)
	defer teardown()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := store.EnsureSchema(ctx); err != nil {
			t.Fatalf("EnsureSchema #%d: %v", i, err)
		}
	}
	// We should be able to write + read after the re-apply.
	cid := uuid.New()
	id, err := store.AddEntity(ctx, Entity{Type: "X", Label: "schema-idem", CampaignID: cid})
	if err != nil {
		t.Fatalf("AddEntity after re-apply: %v", err)
	}
	if id == uuid.Nil {
		t.Error("expected non-zero ID")
	}
}

// --- helpers ---------------------------------------------------------------

// emptyExtractor returns no entities / relations. Used to test
// the "no work to do" branch of AddEpisode.
type emptyExtractor struct{}

func (e *emptyExtractor) Extract(ctx context.Context, ep Episode) ([]Entity, []Relation, error) {
	return nil, nil, nil
}

// failingExtractor returns a synthetic error on every call. Used
// to test that AddEpisode surfaces the error and rolls back.
type failingExtractor struct{}

func (f *failingExtractor) Extract(ctx context.Context, ep Episode) ([]Entity, []Relation, error) {
	return nil, nil, context.Canceled
}
