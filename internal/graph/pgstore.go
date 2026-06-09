package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresGraphStore is the default GraphStore implementation.
// It runs on an existing *pgxpool.Pool (the same one the blackboard
// uses) and adds two tables: graph_entities and graph_edges.
//
// Why Postgres (and not Neo4j)?
//  1. The existing swarm_findings table already has pgvector + a
//     working migration; reusing the same pool means the graph
//     layer adds zero new infrastructure.
//  2. Pheromone + pgvector already cover most cross-task recall
//     scenarios (the ptagent report §8.3 explicitly recommends
//     deferring Neo4j until cross-task inheritance becomes a need).
//  3. Operators can always switch to Neo4j later — the GraphStore
//     interface is the same; only the wiring changes.
//
// Schema is exposed via Schema(dim) so callers can apply the
// migration alongside the existing 00000N_blackboard.sql files.
type PostgresGraphStore struct {
	pool     *pgxpool.Pool
	embedder Embedder
	logger   *slog.Logger
	dim      int
}

// Embedder is the minimum surface PostgresGraphStore needs to attach
// embeddings to outgoing entities. Defined here as a local interface
// to avoid an import cycle on internal/llm. The llm.Embedder
// interface satisfies this contract.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
	ModelName() string
}

// PStoreOption configures a PostgresGraphStore at construction time.
type PStoreOption func(*PostgresGraphStore)

// WithEmbedder attaches an embedder. When set, every AddEntity call
// will compute a vector for (Type, Label) and persist it in the
// graph_entities.embedding pgvector column. When nil, the column
// stays NULL and semantic queries (NearVector/NearK) will skip.
func WithEmbedder(e Embedder) PStoreOption {
	return func(s *PostgresGraphStore) { s.embedder = e }
}

// WithPGStoreLogger attaches a logger. Default is slog.Default().
func WithPGStoreLogger(l *slog.Logger) PStoreOption {
	return func(s *PostgresGraphStore) { s.logger = l }
}

// WithDimensions overrides the embedding dimension. Only useful
// when the operator has hand-rolled a schema with a different
// width; the default (384) matches LocalStubEmbedder.
func WithDimensions(dim int) PStoreOption {
	return func(s *PostgresGraphStore) {
		if dim > 0 {
			s.dim = dim
		}
	}
}

// NewPostgresGraphStore builds a store on the given pool. The pool
// is shared with the blackboard — no separate connection setup is
// required.
func NewPostgresGraphStore(pool *pgxpool.Pool, opts ...PStoreOption) *PostgresGraphStore {
	s := &PostgresGraphStore{
		pool:   pool,
		logger: slog.Default(),
		dim:    DefaultEmbeddingDim,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Schema returns the DDL for the two graph tables. dim is the
// embedding vector width. The default dimension is
// DefaultEmbeddingDim; pass a different value if you've hand-rolled
// a schema with a different width.
//
// The schema is idempotent (CREATE TABLE IF NOT EXISTS) so it can
// be re-applied as part of a migration cycle. Operators that want
// strict version control should embed the DDL into a numbered
// migration file in internal/db/migrations/.
//
// Returns the full DDL as a single string. Multiple statements are
// separated by a semicolon + newline and can be run with
// pool.Exec(ctx, Schema(dim)).
func Schema(dim int) string {
	if dim <= 0 {
		dim = DefaultEmbeddingDim
	}
	return fmt.Sprintf(`
-- graph_entities: nodes in the knowledge graph
CREATE TABLE IF NOT EXISTS graph_entities (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    type        TEXT NOT NULL,
    label       TEXT NOT NULL,
    campaign_id UUID NOT NULL,
    properties  JSONB NOT NULL DEFAULT '{}'::jsonb,
    embedding   vector(%d),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (type, label, campaign_id)
);
CREATE INDEX IF NOT EXISTS idx_graph_entities_campaign ON graph_entities(campaign_id, type);
CREATE INDEX IF NOT EXISTS idx_graph_entities_label    ON graph_entities(campaign_id, label);
CREATE INDEX IF NOT EXISTS idx_graph_entities_embedding
    ON graph_entities USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

-- graph_edges: typed relations between two entities
CREATE TABLE IF NOT EXISTS graph_edges (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    from_id     UUID NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    to_id       UUID NOT NULL REFERENCES graph_entities(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    campaign_id UUID NOT NULL,
    properties  JSONB NOT NULL DEFAULT '{}'::jsonb,
    valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_graph_edges_from      ON graph_edges(from_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_to        ON graph_edges(to_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_type      ON graph_edges(type, campaign_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_validfrom ON graph_edges(valid_from DESC);
`, dim)
}

// EnsureSchema is a convenience for callers that want to apply the
// schema before the first write. Idempotent.
func (s *PostgresGraphStore) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, Schema(s.dim))
	if err != nil {
		return fmt.Errorf("ensure graph schema: %w", err)
	}
	return nil
}

// Close is a no-op for the PostgresGraphStore: the pool is owned
// by the caller. Provided for interface symmetry with future
// implementations.
func (s *PostgresGraphStore) Close() error { return nil }

// AddEntity upserts an entity. Same (type, label, campaign_id)
// triple returns the same row. Returns the canonical UUID.
func (s *PostgresGraphStore) AddEntity(ctx context.Context, e Entity) (uuid.UUID, error) {
	if e.Type == "" {
		return uuid.Nil, fmt.Errorf("AddEntity: type is required")
	}
	if e.Label == "" {
		return uuid.Nil, fmt.Errorf("AddEntity: label is required")
	}
	if e.CampaignID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("AddEntity: campaign_id is required")
	}

	vec := s.embedIfPossible(ctx, e.Type, e.Label, e.Properties)

	props := e.Properties
	if props == nil {
		props = map[string]any{}
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return uuid.Nil, fmt.Errorf("AddEntity: marshal properties: %w", err)
	}

	createdAt := e.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	var id uuid.UUID
	err = s.pool.QueryRow(ctx,
		`INSERT INTO graph_entities (type, label, campaign_id, properties, embedding, created_at)
		 VALUES ($1, $2, $3, $4::jsonb, $5, $6)
		 ON CONFLICT (type, label, campaign_id) DO UPDATE
		   SET properties = COALESCE(EXCLUDED.properties, graph_entities.properties),
		       embedding  = COALESCE(EXCLUDED.embedding,  graph_entities.embedding)
		 RETURNING id`,
		e.Type, e.Label, e.CampaignID, string(propsJSON), embeddingArg(vec), createdAt,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("AddEntity: upsert: %w", err)
	}
	return id, nil
}

// AddRelation upserts a relation. The unique key is
// (from_id, to_id, type, campaign_id) so a "CVE-1 AFFECTS Host-A"
// fact seen twice produces a single row, with the second write
// updating valid_to / properties.
func (s *PostgresGraphStore) AddRelation(ctx context.Context, r Relation) (uuid.UUID, error) {
	if r.FromID == uuid.Nil || r.ToID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("AddRelation: from_id and to_id are required")
	}
	if r.Type == "" {
		return uuid.Nil, fmt.Errorf("AddRelation: type is required")
	}
	if r.CampaignID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("AddRelation: campaign_id is required")
	}

	props := r.Properties
	if props == nil {
		props = map[string]any{}
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return uuid.Nil, fmt.Errorf("AddRelation: marshal properties: %w", err)
	}

	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	validFrom := r.ValidFrom
	if validFrom.IsZero() {
		validFrom = createdAt
	}

	var id uuid.UUID
	err = s.pool.QueryRow(ctx,
		`INSERT INTO graph_edges (from_id, to_id, type, campaign_id, properties, valid_from, valid_to, created_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		 ON CONFLICT DO NOTHING
		 RETURNING id`,
		r.FromID, r.ToID, r.Type, r.CampaignID, string(propsJSON), validFrom, r.ValidTo, createdAt,
	).Scan(&id)
	if err != nil {
		// The ON CONFLICT DO NOTHING path means a duplicate
		// insert returns ErrNoRows. Re-query to find the existing
		// row's id.
		if err == pgx.ErrNoRows {
			err = s.pool.QueryRow(ctx,
				`SELECT id FROM graph_edges
				 WHERE from_id = $1 AND to_id = $2 AND type = $3 AND campaign_id = $4`,
				r.FromID, r.ToID, r.Type, r.CampaignID,
			).Scan(&id)
			if err != nil {
				return uuid.Nil, fmt.Errorf("AddRelation: locate existing: %w", err)
			}
			return id, nil
		}
		return uuid.Nil, fmt.Errorf("AddRelation: upsert: %w", err)
	}
	return id, nil
}

// AddEpisode extracts entities / relations from ep via the supplied
// extractor, then writes them in a single transaction. The extractor
// returns entities and relations with empty IDs — AddEpisode
// resolves the (type, label) pair to a UUID for each entity, then
// resolves relations by looking up the (from_label, to_label) pair
// in the just-written entity set.
//
// Any error from the extractor is surfaced; the transaction is
// not started. An empty extractor result (no entities, no relations)
// is a successful no-op.
func (s *PostgresGraphStore) AddEpisode(ctx context.Context, ep Episode, extractor EntityExtractor) error {
	if extractor == nil {
		return fmt.Errorf("AddEpisode: extractor is required")
	}
	ents, rels, err := extractor.Extract(ctx, ep)
	if err != nil {
		return fmt.Errorf("AddEpisode: extract: %w", err)
	}
	if len(ents) == 0 && len(rels) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("AddEpisode: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Map (type, label) → UUID within this transaction.
	entID := map[string]uuid.UUID{}
	for _, e := range ents {
		k := e.Type + "\x00" + e.Label
		if _, ok := entID[k]; ok {
			continue
		}
		vec := s.embedIfPossible(ctx, e.Type, e.Label, e.Properties)
		props := e.Properties
		if props == nil {
			props = map[string]any{}
		}
		propsJSON, _ := json.Marshal(props)
		createdAt := e.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}

		var id uuid.UUID
		err := tx.QueryRow(ctx,
			`INSERT INTO graph_entities (type, label, campaign_id, properties, embedding, created_at)
			 VALUES ($1, $2, $3, $4::jsonb, $5, $6)
			 ON CONFLICT (type, label, campaign_id) DO UPDATE
			   SET properties = COALESCE(EXCLUDED.properties, graph_entities.properties)
			 RETURNING id`,
			e.Type, e.Label, e.CampaignID, string(propsJSON), embeddingArg(vec), createdAt,
		).Scan(&id)
		if err != nil {
			return fmt.Errorf("AddEpisode: upsert entity %s/%s: %w", e.Type, e.Label, err)
		}
		entID[k] = id
	}

	// Relations: resolve labels to UUIDs.
	for _, r := range rels {
		fromLabel, _ := r.Properties["from_label"].(string)
		toLabel, _ := r.Properties["to_label"].(string)
		fromType, toType := "", ""
		// Heuristic: if the relation's Properties carries a "from_type"
		// / "to_type" key (set by some extractors) use it; otherwise
		// look up across all known (type, label) pairs in this batch.
		if v, ok := r.Properties["from_type"].(string); ok {
			fromType = v
		}
		if v, ok := r.Properties["to_type"].(string); ok {
			toType = v
		}
		fromID := resolveID(entID, fromType, fromLabel)
		toID := resolveID(entID, toType, toLabel)
		if fromID == uuid.Nil || toID == uuid.Nil {
			s.logger.Debug("AddEpisode: drop dangling relation",
				slog.String("from", fromLabel), slog.String("to", toLabel),
				slog.String("type", r.Type))
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
		propsJSON, _ := json.Marshal(props)
		createdAt := r.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		validFrom := r.ValidFrom
		if validFrom.IsZero() {
			validFrom = createdAt
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO graph_edges (from_id, to_id, type, campaign_id, properties, valid_from, valid_to, created_at)
			 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
			 ON CONFLICT DO NOTHING`,
			fromID, toID, r.Type, r.CampaignID, string(propsJSON), validFrom, r.ValidTo, createdAt,
		)
		if err != nil {
			return fmt.Errorf("AddEpisode: upsert edge: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("AddEpisode: commit: %w", err)
	}
	return nil
}

// resolveID looks up an entity by (type, label). When the type is
// empty (the LLM extractor doesn't always populate it), it falls
// back to the first match by label alone. Returns uuid.Nil if the
// entity isn't in the batch — the caller drops the dangling edge.
func resolveID(entID map[string]uuid.UUID, typ, label string) uuid.UUID {
	if typ != "" {
		if id, ok := entID[typ+"\x00"+label]; ok {
			return id
		}
	}
	for k, id := range entID {
		i := strings.Index(k, "\x00")
		if i < 0 {
			continue
		}
		if k[i+1:] == label {
			return id
		}
	}
	return uuid.Nil
}

// QueryEntities returns entities matching the query. Filters are
// AND-combined. NearVector / NearK together enable pgvector cosine
// search; either alone is a no-op.
func (s *PostgresGraphStore) QueryEntities(ctx context.Context, q EntityQuery) ([]Entity, error) {
	var (
		where []string
		args  []any
		i     = 1
	)
	if q.Type != "" {
		where = append(where, fmt.Sprintf("type = $%d", i))
		args = append(args, q.Type)
		i++
	}
	if q.LabelLike != "" {
		where = append(where, fmt.Sprintf("label LIKE $%d", i))
		args = append(args, q.LabelLike)
		i++
	}
	if q.CampaignID != nil {
		where = append(where, fmt.Sprintf("campaign_id = $%d", i))
		args = append(args, *q.CampaignID)
		i++
	}
	useVector := len(q.NearVector) > 0 && q.NearK > 0
	if useVector {
		// Append the vector as a pgvector literal and use <=> for
		// cosine distance. ORDER BY distance and LIMIT to K.
		args = append(args, embeddingArg(q.NearVector))
		vecIdx := i
		i++
		where = append(where, "embedding IS NOT NULL")
		limit := q.NearK
		if limit <= 0 {
			limit = 10
		}
		sql := fmt.Sprintf(`
			SELECT id, type, label, properties, campaign_id, created_at, embedding
			FROM graph_entities
			WHERE %s
			ORDER BY embedding <=> $%d
			LIMIT %d
		`, strings.Join(where, " AND "), vecIdx, limit)
		rows, err := s.pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, fmt.Errorf("QueryEntities: %w", err)
		}
		defer rows.Close()
		return scanEntities(rows)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	sql := fmt.Sprintf(`
		SELECT id, type, label, properties, campaign_id, created_at, embedding
		FROM graph_entities
		%sORDER BY created_at DESC
		LIMIT %d
	`, whereClause(where), limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("QueryEntities: %w", err)
	}
	defer rows.Close()
	return scanEntities(rows)
}

// QueryRelations returns relations matching the query. FromID and
// ToID are independent (an edge is matched if EITHER side matches
// unless both are set, in which case BOTH must match). Since / Until
// apply to (ValidFrom, ValidTo) and the AND of those is the
// conventional point-in-time semantics.
func (s *PostgresGraphStore) QueryRelations(ctx context.Context, q RelationQuery) ([]Relation, error) {
	var (
		where []string
		args  []any
		i     = 1
	)
	if q.FromID != nil && q.ToID != nil {
		where = append(where, fmt.Sprintf("from_id = $%d AND to_id = $%d", i, i+1))
		args = append(args, *q.FromID, *q.ToID)
		i += 2
	} else {
		if q.FromID != nil {
			where = append(where, fmt.Sprintf("from_id = $%d", i))
			args = append(args, *q.FromID)
			i++
		}
		if q.ToID != nil {
			where = append(where, fmt.Sprintf("to_id = $%d", i))
			args = append(args, *q.ToID)
			i++
		}
	}
	if q.Type != "" {
		where = append(where, fmt.Sprintf("type = $%d", i))
		args = append(args, q.Type)
		i++
	}
	if q.CampaignID != nil {
		where = append(where, fmt.Sprintf("campaign_id = $%d", i))
		args = append(args, *q.CampaignID)
		i++
	}
	if q.Since != nil {
		where = append(where, fmt.Sprintf("valid_from >= $%d", i))
		args = append(args, *q.Since)
		i++
	}
	if q.Until != nil {
		// Edges that have closed (valid_to IS NOT NULL AND valid_to <= Until)
		// OR are still open (valid_to IS NULL).
		where = append(where, fmt.Sprintf("(valid_to IS NULL OR valid_to <= $%d)", i))
		args = append(args, *q.Until)
		i++
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	sql := fmt.Sprintf(`
		SELECT id, from_id, to_id, type, properties, campaign_id, valid_from, valid_to, created_at
		FROM graph_edges
		%sORDER BY created_at DESC
		LIMIT %d
	`, whereClause(where), limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("QueryRelations: %w", err)
	}
	defer rows.Close()
	return scanRelations(rows)
}

// Traverse does a BFS from startID up to depth hops, returning
// every reachable entity and the edges traversed. Self-edges are
// included. The result is deduplicated by ID. depth ≤ 0 returns
// just the starting entity.
func (s *PostgresGraphStore) Traverse(ctx context.Context, startID uuid.UUID, depth int) (Traversal, error) {
	if startID == uuid.Nil {
		return Traversal{}, fmt.Errorf("Traverse: start_id is required")
	}
	if depth < 0 {
		depth = 0
	}

	visited := map[uuid.UUID]bool{startID: true}
	visitedEdges := map[uuid.UUID]bool{}
	entities := []Entity{}
	relations := []Relation{}

	// Pull the start entity to anchor the result.
	rows, err := s.pool.Query(ctx,
		`SELECT id, type, label, properties, campaign_id, created_at, embedding
		 FROM graph_entities WHERE id = $1`, startID)
	if err != nil {
		return Traversal{}, fmt.Errorf("Traverse: pull start: %w", err)
	}
	ents, err := scanEntities(rows)
	rows.Close()
	if err != nil {
		return Traversal{}, err
	}
	if len(ents) == 0 {
		return Traversal{}, nil
	}
	entities = append(entities, ents[0])

	frontier := []uuid.UUID{startID}
	for d := 0; d < depth; d++ {
		if len(frontier) == 0 {
			break
		}
		// Pull all edges in either direction from the current frontier.
		rows, err := s.pool.Query(ctx,
			`SELECT id, from_id, to_id, type, properties, campaign_id, valid_from, valid_to, created_at
			 FROM graph_edges
			 WHERE from_id = ANY($1) OR to_id = ANY($1)`, frontier)
		if err != nil {
			return Traversal{}, fmt.Errorf("Traverse: pull edges: %w", err)
		}
		edges, err := scanRelations(rows)
		rows.Close()
		if err != nil {
			return Traversal{}, err
		}

		next := []uuid.UUID{}
		for _, e := range edges {
			if !visitedEdges[e.ID] {
				visitedEdges[e.ID] = true
				relations = append(relations, e)
			}
			for _, neighbor := range []uuid.UUID{e.FromID, e.ToID} {
				if !visited[neighbor] {
					visited[neighbor] = true
					next = append(next, neighbor)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		// Pull the newly-discovered entities in one round trip.
		rows, err = s.pool.Query(ctx,
			`SELECT id, type, label, properties, campaign_id, created_at, embedding
			 FROM graph_entities WHERE id = ANY($1)`, next)
		if err != nil {
			return Traversal{}, fmt.Errorf("Traverse: pull entities: %w", err)
		}
		moreEnts, err := scanEntities(rows)
		rows.Close()
		if err != nil {
			return Traversal{}, err
		}
		entities = append(entities, moreEnts...)
		frontier = next
	}

	return Traversal{Entities: entities, Relations: relations}, nil
}

// Stats returns aggregate counts and breakdowns for dashboards.
func (s *PostgresGraphStore) Stats(ctx context.Context) (GraphStats, error) {
	stats := GraphStats{
		ByType:     map[string]int{},
		ByCampaign: map[string]int{},
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM graph_entities`,
	).Scan(&stats.EntityCount); err != nil {
		return stats, fmt.Errorf("Stats: entity count: %w", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM graph_edges`,
	).Scan(&stats.RelationCount); err != nil {
		return stats, fmt.Errorf("Stats: relation count: %w", err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT type, COUNT(*) FROM graph_entities GROUP BY type`)
	if err != nil {
		return stats, fmt.Errorf("Stats: by-type: %w", err)
	}
	for rows.Next() {
		var t string
		var c int
		if err := rows.Scan(&t, &c); err != nil {
			rows.Close()
			return stats, fmt.Errorf("Stats: scan by-type: %w", err)
		}
		stats.ByType[t] = c
	}
	rows.Close()

	rows, err = s.pool.Query(ctx,
		`SELECT campaign_id, COUNT(*) FROM graph_entities GROUP BY campaign_id`)
	if err != nil {
		return stats, fmt.Errorf("Stats: by-campaign: %w", err)
	}
	for rows.Next() {
		var id uuid.UUID
		var c int
		if err := rows.Scan(&id, &c); err != nil {
			rows.Close()
			return stats, fmt.Errorf("Stats: scan by-campaign: %w", err)
		}
		stats.ByCampaign[id.String()] = c
	}
	rows.Close()
	return stats, nil
}

// --- internals --------------------------------------------------------------

// embedIfPossible computes a vector for (Type, Label) when an
// embedder is attached. The properties bag is included in the
// embed input so two entities with the same (type, label) but
// different properties end up with different vectors.
func (s *PostgresGraphStore) embedIfPossible(ctx context.Context, typ, label string, props map[string]any) []float32 {
	if s.embedder == nil {
		return nil
	}
	input := typ + " | " + label
	if len(props) > 0 {
		if b, err := json.Marshal(props); err == nil {
			input += " | " + string(b)
		}
	}
	vecs, err := s.embedder.Embed(ctx, []string{input})
	if err != nil || len(vecs) == 0 {
		s.logger.Debug("graph entity embed failed",
			slog.String("type", typ), slog.String("label", label),
			slog.String("err", errString(err)))
		return nil
	}
	return vecs[0]
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// embeddingArg converts a float32 vector into the string literal
// pgvector accepts. Returns nil for an empty slice.
func embeddingArg(v []float32) any {
	if len(v) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%g", x)
	}
	sb.WriteByte(']')
	return sb.String()
}

// whereClause returns "WHERE x AND y" or "" for an empty slice.
func whereClause(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(parts, " AND ") + " "
}

// scanEntities materialises rows from a graph_entities SELECT.
func scanEntities(rows pgx.Rows) ([]Entity, error) {
	var out []Entity
	for rows.Next() {
		var e Entity
		var propsRaw []byte
		var vec []float32
		if err := rows.Scan(&e.ID, &e.Type, &e.Label, &propsRaw, &e.CampaignID, &e.CreatedAt, &vec); err != nil {
			return nil, fmt.Errorf("scan entity: %w", err)
		}
		if len(propsRaw) > 0 {
			_ = json.Unmarshal(propsRaw, &e.Properties)
		}
		if len(vec) > 0 {
			e.Embedding = vec
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scanRelations materialises rows from a graph_edges SELECT.
func scanRelations(rows pgx.Rows) ([]Relation, error) {
	var out []Relation
	for rows.Next() {
		var r Relation
		var propsRaw []byte
		if err := rows.Scan(&r.ID, &r.FromID, &r.ToID, &r.Type, &propsRaw,
			&r.CampaignID, &r.ValidFrom, &r.ValidTo, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan relation: %w", err)
		}
		if len(propsRaw) > 0 {
			_ = json.Unmarshal(propsRaw, &r.Properties)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
