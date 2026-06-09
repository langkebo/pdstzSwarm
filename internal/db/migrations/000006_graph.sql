-- 000006_graph.sql: P4 knowledge graph (Postgres-backed, optional Neo4j alternative)
--
-- The graph layer is the Pentest-Swarm-AI counterpart to ptagent's
-- Graphiti + Neo4j integration (see docs/analysis/ptagent-comparison-report.md
-- §8.3). The ptagent report explicitly recommends deferring Neo4j
-- until cross-task knowledge inheritance is actually needed:
--
--   "I. 用 pgvector + pheromone 已能覆盖大多数场景，仅当需要跨任务
--    知识继承时再引入。"
--
-- Translation: Postgres + pgvector is the default backend, with
-- the graph layer living in the same database. Operators that
-- eventually want Neo4j can:
--
--   1. Build with `//go:build neo4j` and import the official
--      neo4j-go-driver in a separate file (out of scope here);
--   2. The driver implements the BoltClient interface declared in
--      internal/graph/neo4j_store.go;
--   3. NewNeo4jGraphStoreFromBolt(c BoltClient) wires the rest.
--
-- The two tables here are the default schema. They use the
-- existing `vector` extension (already enabled by 000002_pgvector.sql).
-- Embedding width is 384 to match LocalStubEmbedder / the
-- small-embedding-model common case. Operators that want a
-- different width should hand-roll a new migration with
-- internal/graph/pgstore.go Schema(<dim>).
--
-- This migration is idempotent so re-running it is safe.

-- graph_entities: nodes in the knowledge graph
CREATE TABLE IF NOT EXISTS graph_entities (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    type        TEXT NOT NULL,
    label       TEXT NOT NULL,
    campaign_id UUID NOT NULL,
    properties  JSONB NOT NULL DEFAULT '{}'::jsonb,
    embedding   vector(384),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (type, label, campaign_id)
);

CREATE INDEX IF NOT EXISTS idx_graph_entities_campaign
    ON graph_entities(campaign_id, type);

CREATE INDEX IF NOT EXISTS idx_graph_entities_label
    ON graph_entities(campaign_id, label);

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

CREATE INDEX IF NOT EXISTS idx_graph_edges_from
    ON graph_edges(from_id);

CREATE INDEX IF NOT EXISTS idx_graph_edges_to
    ON graph_edges(to_id);

CREATE INDEX IF NOT EXISTS idx_graph_edges_type
    ON graph_edges(type, campaign_id);

CREATE INDEX IF NOT EXISTS idx_graph_edges_validfrom
    ON graph_edges(valid_from DESC);
