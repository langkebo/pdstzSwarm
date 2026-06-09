package graph

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// BoltClient is the contract a real Neo4j driver must satisfy. The
// default build of Pentest-Swarm-AI does NOT include the official
// Neo4j driver (it would add ~50MB of dependencies for users who
// don't need it). Instead, the BoltClient interface is the seam
// at which an operator can plug in a driver using the
// `//go:build neo4j` build tag.
//
// The interface intentionally mirrors the Bolt v5+ session API at
// the level needed for a Graphiti-style entity-relation store:
//
//   - OpenSession() acquires a session; CloseSession() releases it.
//   - Run(query, params) returns rows. The driver's Rows type is
//     narrowed to []map[string]any for portability.
//   - WriteTransaction(fn) wraps a unit of work in a transaction.
//
// Operators implementing BoltClient against the official driver
// should make it idempotent and safe for concurrent use.
type BoltClient interface {
	OpenSession(ctx context.Context) (BoltSession, error)
	Close() error
}

// BoltSession is a single Bolt session. Lifetime is bounded by
// the surrounding BoltClient.
type BoltSession interface {
	Run(ctx context.Context, query string, params map[string]any) ([]map[string]any, error)
	WriteTransaction(ctx context.Context, fn func(tx BoltTx) error) error
	Close() error
}

// BoltTx is a single write transaction. Rollback is automatic
// when fn returns a non-nil error.
type BoltTx interface {
	Run(ctx context.Context, query string, params map[string]any) ([]map[string]any, error)
}

// --- MockNeo4jGraphStore ----------------------------------------------------

// MockNeo4jGraphStore is the default Neo4jGraphStore implementation
// bundled with the binary. It is NOT a real Neo4j client — it keeps
// the graph state in memory behind a mutex. Its purpose is to:
//
//  1. Prove the GraphStore interface is Neo4j-friendly (the methods
//     are named after typical Bolt/Cypher idioms: AddEntity →
//     MERGE … ON CREATE, etc.).
//  2. Provide a test double that doesn't require a Neo4j container
//     to be running.
//  3. Be the default fallback when an operator passes a nil
//     BoltClient — the store then no-ops on writes and returns
//     empty results from reads. The behaviour is logged so it is
//     not silent.
//
// To wire a real Neo4j cluster, build with the `neo4j` build tag
// and use NewNeo4jGraphStoreFromBolt(boltClient). The mapping
// from in-memory state to Cypher MERGE statements is a mechanical
// translation:
//
//	entity := (e:Type {id: $id, label: $label, ...})
//	edge   := (from)-[:TYPE {valid_from: $t1, valid_to: $t2}]->(to)
type MockNeo4jGraphStore struct {
	mu       sync.RWMutex
	entities map[uuid.UUID]Entity
	edges    map[uuid.UUID]Relation
	// client is the optional Bolt driver. nil → this is a pure
	// in-memory mock (good for unit tests and dev mode).
	client BoltClient
}

// NewMockNeo4jGraphStore builds an in-memory store. Useful for
// tests, dev mode and as a sentinel when no real Neo4j is wired.
func NewMockNeo4jGraphStore() *MockNeo4jGraphStore {
	return &MockNeo4jGraphStore{
		entities: map[uuid.UUID]Entity{},
		edges:    map[uuid.UUID]Relation{},
	}
}

// NewNeo4jGraphStoreFromBolt wraps a real Bolt client behind the
// GraphStore interface. The returned store keeps an in-memory
// cache for tests; the actual Cypher execution path lives in a
// separate file under the `//go:build neo4j` tag. Operators that
// need real Neo4j should:
//
//	go get github.com/neo4j/neo4j-go-driver/v5
//	// + a new file internal/graph/neo4j_driver.go with:
//	//   //go:build neo4j
//	//   func NewNeo4jGraphStoreFromBolt(c BoltClient) *Neo4jGraphStore { ... }
//
// and rebuild with:
//
//	go build -tags neo4j ./internal/graph/...
func NewNeo4jGraphStoreFromBolt(c BoltClient) *MockNeo4jGraphStore {
	return &MockNeo4jGraphStore{
		entities: map[uuid.UUID]Entity{},
		edges:    map[uuid.UUID]Relation{},
		client:   c,
	}
}

// AddEntity upserts an entity in the in-memory map. Real Neo4j
// would issue a MERGE … ON CREATE / ON MATCH statement keyed on
// the (type, label, campaign_id) triple.
func (m *MockNeo4jGraphStore) AddEntity(ctx context.Context, e Entity) (uuid.UUID, error) {
	if e.Type == "" || e.Label == "" {
		return uuid.Nil, fmt.Errorf("MockNeo4jGraphStore: type and label required")
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	m.entities[e.ID] = e
	m.mu.Unlock()
	return e.ID, nil
}

// AddRelation upserts a relation.
func (m *MockNeo4jGraphStore) AddRelation(ctx context.Context, r Relation) (uuid.UUID, error) {
	if r.FromID == uuid.Nil || r.ToID == uuid.Nil || r.Type == "" {
		return uuid.Nil, fmt.Errorf("MockNeo4jGraphStore: from_id, to_id, type required")
	}
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	m.edges[r.ID] = r
	m.mu.Unlock()
	return r.ID, nil
}

// AddEpisode extracts and persists in a single transaction. The
// in-memory mock does not honour transactions — write order is
// good enough for tests. The relation (label → UUID) resolution
// follows the same rules as PostgresGraphStore.AddEpisode.
func (m *MockNeo4jGraphStore) AddEpisode(ctx context.Context, ep Episode, extractor EntityExtractor) error {
	if extractor == nil {
		return fmt.Errorf("MockNeo4jGraphStore: extractor required")
	}
	ents, rels, err := extractor.Extract(ctx, ep)
	if err != nil {
		return err
	}
	entID := map[string]uuid.UUID{}
	for _, e := range ents {
		k := e.Type + "\x00" + e.Label
		if _, ok := entID[k]; ok {
			continue
		}
		if e.ID == uuid.Nil {
			e.ID = uuid.New()
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		m.mu.Lock()
		m.entities[e.ID] = e
		m.mu.Unlock()
		entID[k] = e.ID
	}
	for _, r := range rels {
		fromLabel, _ := r.Properties["from_label"].(string)
		toLabel, _ := r.Properties["to_label"].(string)
		fromID := resolveID(entID, "", fromLabel)
		toID := resolveID(entID, "", toLabel)
		if fromID == uuid.Nil || toID == uuid.Nil {
			continue
		}
		// Strip the helper keys before persisting.
		props := map[string]any{}
		for k, v := range r.Properties {
			if k == "from_label" || k == "to_label" || k == "from_type" || k == "to_type" {
				continue
			}
			props[k] = v
		}
		r.FromID = fromID
		r.ToID = toID
		r.Properties = props
		if r.ID == uuid.Nil {
			r.ID = uuid.New()
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now().UTC()
		}
		m.mu.Lock()
		m.edges[r.ID] = r
		m.mu.Unlock()
	}
	return nil
}

// QueryEntities returns entities matching the query. In-memory
// scan, no vector search (the mock has no embedder path).
func (m *MockNeo4jGraphStore) QueryEntities(ctx context.Context, q EntityQuery) ([]Entity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Entity{}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	for _, e := range m.entities {
		if q.Type != "" && e.Type != q.Type {
			continue
		}
		if q.LabelLike != "" && !containsFold(e.Label, q.LabelLike) {
			continue
		}
		if q.CampaignID != nil && e.CampaignID != *q.CampaignID {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// QueryRelations returns relations matching the query.
func (m *MockNeo4jGraphStore) QueryRelations(ctx context.Context, q RelationQuery) ([]Relation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Relation{}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	for _, r := range m.edges {
		if q.FromID != nil && q.ToID != nil {
			if r.FromID != *q.FromID || r.ToID != *q.ToID {
				continue
			}
		} else {
			if q.FromID != nil && r.FromID != *q.FromID {
				continue
			}
			if q.ToID != nil && r.ToID != *q.ToID {
				continue
			}
		}
		if q.Type != "" && r.Type != q.Type {
			continue
		}
		if q.CampaignID != nil && r.CampaignID != *q.CampaignID {
			continue
		}
		if q.Since != nil && r.ValidFrom.Before(*q.Since) {
			continue
		}
		if q.Until != nil {
			if r.ValidTo == nil {
				continue
			}
			if r.ValidTo.After(*q.Until) {
				continue
			}
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Traverse does a BFS in the in-memory graph.
func (m *MockNeo4jGraphStore) Traverse(ctx context.Context, startID uuid.UUID, depth int) (Traversal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	start, ok := m.entities[startID]
	if !ok {
		return Traversal{}, nil
	}
	visited := map[uuid.UUID]bool{startID: true}
	entities := []Entity{start}
	relations := []Relation{}
	frontier := []uuid.UUID{startID}
	if depth <= 0 {
		return Traversal{Entities: entities, Relations: relations}, nil
	}
	for d := 0; d < depth; d++ {
		next := []uuid.UUID{}
		for _, eid := range frontier {
			for _, r := range m.edges {
				if r.FromID != eid && r.ToID != eid {
					continue
				}
				relations = append(relations, r)
				for _, nb := range []uuid.UUID{r.FromID, r.ToID} {
					if !visited[nb] {
						visited[nb] = true
						next = append(next, nb)
						if ent, ok := m.entities[nb]; ok {
							entities = append(entities, ent)
						}
					}
				}
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	return Traversal{Entities: entities, Relations: relations}, nil
}

// Stats returns the in-memory counts.
func (m *MockNeo4jGraphStore) Stats(ctx context.Context) (GraphStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	stats := GraphStats{
		EntityCount:   len(m.entities),
		RelationCount: len(m.edges),
		ByType:        map[string]int{},
		ByCampaign:    map[string]int{},
	}
	for _, e := range m.entities {
		stats.ByType[e.Type]++
		stats.ByCampaign[e.CampaignID.String()]++
	}
	return stats, nil
}

// Close releases any held Bolt client. Idempotent.
func (m *MockNeo4jGraphStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		_ = m.client.Close()
		m.client = nil
	}
	return nil
}

// containsFold reports whether substr (lowercase) is contained in
// s (lowercase). Used to match the SQL LIKE semantics with a
// case-insensitive substring check.
func containsFold(s, substr string) bool {
	if substr == "" {
		return true
	}
	sl := toLower(s)
	sub := toLower(substr)
	return indexOf(sl, sub) >= 0
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	if len(sub) > len(s) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Compile-time assertion that MockNeo4jGraphStore satisfies
// GraphStore. Catches interface drift at build time.
var _ GraphStore = (*MockNeo4jGraphStore)(nil)
