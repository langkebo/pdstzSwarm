// Package blackboard implements the stigmergic shared state for the swarm.
//
// The blackboard replaces the sequential 5-phase runner. Agents read and write
// findings tagged with a type; their trigger predicates wake them when
// relevant state appears. Pheromone weights decay over time so the swarm
// naturally prioritises recent, high-signal findings and lets stale paths die.
package blackboard

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FindingEmbedder is the minimum surface PostgresBoard needs to attach
// embeddings to outgoing findings. Defined here as a local interface to
// avoid an import cycle on internal/llm. The llm.Embedder interface
// satisfies this contract.
type FindingEmbedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
	ModelName() string
}

// PostgresBoard is the durable Postgres-backed blackboard.
// All writes are transactional; reads compute pheromone at query time via
// the swarm_pheromone SQL function.
type PostgresBoard struct {
	pool *pgxpool.Pool

	// Subscribe uses a poll loop since we don't want to take a hard
	// dependency on LISTEN/NOTIFY here. pollInterval controls the cadence.
	pollInterval time.Duration

	// Subscribers are tracked so Close can tear them down cleanly.
	subs struct {
		sync.Mutex
		ch []chan Finding
	}

	// embedder is an optional hook. When non-nil, PostgresBoard.Write
	// computes an embedding for the finding's textual summary and
	// persists it alongside the row. The hook is best-effort: an
	// embedding failure is logged and the row is still inserted with
	// a NULL vector, so the writing path never breaks the agent loop
	// because the embedding API is down.
	embedderMu sync.RWMutex
	embedder   FindingEmbedder
	hooksMu    sync.RWMutex
	hooks      *HookRegistry
}

// NewPostgresBoard creates a new blackboard backed by the given pool.
func NewPostgresBoard(pool *pgxpool.Pool) *PostgresBoard {
	return &PostgresBoard{
		pool:         pool,
		pollInterval: 500 * time.Millisecond,
		hooks:        NewHookRegistry(),
	}
}

// AddEmbedHook registers an EmbedHook to be invoked from Write. The
// hook fires in addition to (not instead of) the legacy SetEmbedder
// path, so the two surfaces can coexist. Multiple hooks are invoked
// in registration order; any single failure is logged but does not
// abort the others.
func (b *PostgresBoard) AddEmbedHook(h EmbedHook) {
	b.hooksMu.Lock()
	if b.hooks == nil {
		b.hooks = NewHookRegistry()
	}
	b.hooksMu.Unlock()
	b.hooks.Add(h)
}

// GraphHook is the minimum surface PostgresBoard needs to invoke
// a knowledge-graph hook from Write. The contract matches
// internal/graph.GraphHook.OnWrite exactly. Declared here as a
// local interface to avoid an import cycle on internal/graph —
// the graph package imports blackboard (for Finding), so the
// dependency must point the other way.
//
// Operators that want the graph layer wire a graph.GraphHook via
// AddGraphHook. The interface is structural: any value with an
// OnWrite method that accepts a *Finding and returns an error
// satisfies it, so unit tests can pass mocks freely.
type GraphHook interface {
	OnWrite(ctx context.Context, f *Finding) error
}

// AddGraphHook registers a GraphHook to be invoked from Write. It
// runs AFTER the EmbedHook chain (so the finding's Embedding is
// already populated when the graph hook extracts entities) and is
// best-effort: errors are logged and silently dropped so a
// transient graph-store failure never blocks the writing path.
func (b *PostgresBoard) AddGraphHook(h GraphHook) {
	b.hooksMu.Lock()
	if b.hooks == nil {
		b.hooks = NewHookRegistry()
	}
	b.hooksMu.Unlock()
	b.hooks.AddGraph(h)
}

// SetEmbedder attaches an embeddings hook to the board. The hook is
// invoked from Write to compute a vector for each finding's textual
// summary and persist it in the pgvector column. Passing nil detaches
// the hook and returns to the pre-P3-2 behaviour of always inserting
// NULL vectors. Safe to call at any point during the board's lifetime
// (the embedder pointer is read under an RWMutex).
func (b *PostgresBoard) SetEmbedder(e FindingEmbedder) {
	b.embedderMu.Lock()
	b.embedder = e
	b.embedderMu.Unlock()
}

// embedderSnapshot returns the current embedder hook (may be nil).
// Reading under RLock is the cheap fast-path; the hook is read on every
// Write so the lock is intentionally narrow.
func (b *PostgresBoard) embedderSnapshot() FindingEmbedder {
	b.embedderMu.RLock()
	defer b.embedderMu.RUnlock()
	return b.embedder
}

// computeFindingEmbedding builds the textual summary used as the
// embedding input and asks the embedder for a vector. The summary is
// the title plus the first 500 bytes of the Data blob (interpreted as
// a JSON or text payload). Returns (nil, nil) when no embedder is
// attached — callers must tolerate nil.
func (b *PostgresBoard) computeFindingEmbedding(ctx context.Context, f Finding) []float32 {
	e := b.embedderSnapshot()
	if e == nil {
		return nil
	}
	text := buildFindingText(f, 500)
	if text == "" {
		return nil
	}
	vecs, err := e.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return nil // best-effort: degrade to NULL embedding on failure
	}
	return vecs[0]
}

// Write inserts a new finding. The assigned ID is returned even if the
// caller provides one — the DB authoritatively assigns IDs.
func (b *PostgresBoard) Write(ctx context.Context, f Finding, opts ...WriteOption) (uuid.UUID, error) {
	o := writeOpts{
		pheromoneBase: 1.0,
		halfLifeSec:   3600,
	}
	// Honour the Finding fields if already set (explicit wins).
	if f.PheromoneBase != 0 {
		o.pheromoneBase = f.PheromoneBase
	}
	if f.HalfLifeSec != 0 {
		o.halfLifeSec = f.HalfLifeSec
	}
	for _, opt := range opts {
		opt(&o)
	}

	if f.CampaignID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("finding requires campaign_id")
	}
	if f.Type == "" {
		return uuid.Nil, fmt.Errorf("finding requires type")
	}
	if f.AgentName == "" {
		return uuid.Nil, fmt.Errorf("finding requires agent_name")
	}
	if len(f.Data) == 0 {
		// Default to an empty JSON object so the jsonb column is well-formed.
		f.Data = []byte(`{}`)
	}

	// Compute the embedding through the optional hook, then merge with
	// any explicit WithEmbedding(...) override. The hook is best-effort:
	// a failure or absent embedder is silently treated as "no vector",
	// and the row is still inserted (with a NULL embedding).
	if o.embedding == nil {
		if vec := b.computeFindingEmbedding(ctx, f); vec != nil {
			o.embedding = vec
		}
	}

	// Fire any registered EmbedHook implementations (e.g. AutoEmbedHook).
	// A hook may overwrite the embedding with a richer derivation, or
	// populate it for the first time when the legacy SetEmbedder path
	// was not used. Any hook error is collected and silently dropped
	// so the writing path remains best-effort.
	if b.hooks != nil {
		_ = b.hooks.Invoke(ctx, &f)
		if o.embedding == nil && len(f.Embedding) > 0 {
			// At least one hook produced a vector — prefer it.
			o.embedding = f.Embedding
		}
	}

	// Fire any registered GraphHook implementations (the P4
	// knowledge-graph layer). Runs AFTER the embed-hook chain so
	// the finding's Embedding is already populated. Errors are
	// collected and dropped — the writing path is best-effort.
	if b.hooks != nil {
		_ = b.hooks.InvokeGraph(ctx, &f)
	}

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var id uuid.UUID
	err = tx.QueryRow(ctx,
		`INSERT INTO swarm_findings
		 (campaign_id, agent_name, finding_type, target, data,
		  pheromone_base, half_life_sec, embedding)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		 RETURNING id`,
		f.CampaignID, f.AgentName, string(f.Type), f.Target,
		string(f.Data),
		o.pheromoneBase, o.halfLifeSec, embeddingArg(o.embedding),
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert finding: %w", err)
	}

	if o.supersedes != nil {
		_, err = tx.Exec(ctx,
			`UPDATE swarm_findings SET superseded_by = $1 WHERE id = $2`,
			id, *o.supersedes,
		)
		if err != nil {
			return uuid.Nil, fmt.Errorf("supersede: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// Query returns findings matching the predicate, newest first.
func (b *PostgresBoard) Query(ctx context.Context, p Predicate) ([]Finding, error) {
	where := []string{"superseded_by IS NULL"}
	args := []any{}
	i := 1

	if len(p.Types) > 0 {
		types := make([]string, len(p.Types))
		for j, t := range p.Types {
			types[j] = string(t)
		}
		where = append(where, fmt.Sprintf("finding_type = ANY($%d)", i))
		args = append(args, types)
		i++
	}
	if p.TargetPrefix != "" {
		where = append(where, fmt.Sprintf("target LIKE $%d", i))
		args = append(args, p.TargetPrefix+"%")
		i++
	}
	if p.SinceID != uuid.Nil {
		where = append(where, fmt.Sprintf(
			"created_at > (SELECT created_at FROM swarm_findings WHERE id = $%d)", i))
		args = append(args, p.SinceID)
		i++
	}
	if p.MinPheromone > 0 {
		where = append(where, fmt.Sprintf(
			"swarm_pheromone(pheromone_base, half_life_sec, EXTRACT(EPOCH FROM (NOW() - created_at))) >= $%d",
			i))
		args = append(args, p.MinPheromone)
		i++
	}

	limit := "100"
	if p.Limit > 0 {
		limit = fmt.Sprintf("%d", p.Limit)
	}

	q := fmt.Sprintf(`
		SELECT id, campaign_id, agent_name, finding_type, target, data,
		       pheromone_base, half_life_sec, superseded_by, created_at,
		       swarm_pheromone(pheromone_base, half_life_sec,
		                        EXTRACT(EPOCH FROM (NOW() - created_at))) AS pheromone
		FROM swarm_findings
		WHERE %s
		ORDER BY created_at DESC
		LIMIT %s
	`, strings.Join(where, " AND "), limit)

	rows, err := b.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		var f Finding
		var ftype string
		var data []byte
		if err := rows.Scan(
			&f.ID, &f.CampaignID, &f.AgentName, &ftype, &f.Target, &data,
			&f.PheromoneBase, &f.HalfLifeSec, &f.SupersededBy, &f.CreatedAt,
			&f.Pheromone,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		f.Type = FindingType(ftype)
		f.Data = data
		out = append(out, f)
	}
	return out, rows.Err()
}

// Subscribe polls for new findings at the configured interval.
// Delivery is at-least-once; use CommitCursor for exactly-once semantics.
func (b *PostgresBoard) Subscribe(ctx context.Context, p Predicate) (<-chan Finding, error) {
	ch := make(chan Finding, 32)

	b.subs.Lock()
	b.subs.ch = append(b.subs.ch, ch)
	b.subs.Unlock()

	go func() {
		defer close(ch)
		ticker := time.NewTicker(b.pollInterval)
		defer ticker.Stop()

		cursor := p.SinceID
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			probe := p
			probe.SinceID = cursor
			findings, err := b.Query(ctx, probe)
			if err != nil {
				continue
			}
			// Newest-first from Query — iterate oldest-first for delivery.
			for j := len(findings) - 1; j >= 0; j-- {
				select {
				case <-ctx.Done():
					return
				case ch <- findings[j]:
					cursor = findings[j].ID
				}
			}
		}
	}()
	return ch, nil
}

// Cursor returns the last committed cursor for an agent.
func (b *PostgresBoard) Cursor(ctx context.Context, campaignID uuid.UUID, agentName string) (uuid.UUID, error) {
	var id uuid.UUID
	err := b.pool.QueryRow(ctx,
		`SELECT last_seen_id FROM swarm_agent_cursors WHERE campaign_id = $1 AND agent_name = $2`,
		campaignID, agentName,
	).Scan(&id)
	if err == pgx.ErrNoRows {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read cursor: %w", err)
	}
	return id, nil
}

// CommitCursor upserts the cursor for an agent.
func (b *PostgresBoard) CommitCursor(ctx context.Context, campaignID uuid.UUID, agentName string, findingID uuid.UUID) error {
	_, err := b.pool.Exec(ctx,
		`INSERT INTO swarm_agent_cursors (campaign_id, agent_name, last_seen_id, last_seen_at)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (campaign_id, agent_name)
		 DO UPDATE SET last_seen_id = EXCLUDED.last_seen_id, last_seen_at = EXCLUDED.last_seen_at`,
		campaignID, agentName, findingID,
	)
	if err != nil {
		return fmt.Errorf("commit cursor: %w", err)
	}
	return nil
}

// Pheromone returns the current decayed weight of a single finding.
func (b *PostgresBoard) Pheromone(ctx context.Context, findingID uuid.UUID) (float64, error) {
	var p float64
	err := b.pool.QueryRow(ctx,
		`SELECT swarm_pheromone(pheromone_base, half_life_sec,
		                       EXTRACT(EPOCH FROM (NOW() - created_at)))
		 FROM swarm_findings WHERE id = $1`,
		findingID,
	).Scan(&p)
	if err != nil {
		return 0, fmt.Errorf("pheromone: %w", err)
	}
	return p, nil
}

// Supersede marks oldID as superseded by newID.
func (b *PostgresBoard) Supersede(ctx context.Context, oldID, newID uuid.UUID) error {
	_, err := b.pool.Exec(ctx,
		`UPDATE swarm_findings SET superseded_by = $1 WHERE id = $2`,
		newID, oldID,
	)
	if err != nil {
		return fmt.Errorf("supersede: %w", err)
	}
	return nil
}

// Budget reads the current budget for a campaign, creating a default row if absent.
func (b *PostgresBoard) Budget(ctx context.Context, campaignID uuid.UUID) (Budget, error) {
	var bud Budget
	bud.CampaignID = campaignID
	err := b.pool.QueryRow(ctx,
		`INSERT INTO swarm_budgets (campaign_id) VALUES ($1)
		 ON CONFLICT (campaign_id) DO UPDATE SET updated_at = NOW()
		 RETURNING max_agent_hours, max_tokens, agent_hours_used, tokens_used`,
		campaignID,
	).Scan(&bud.MaxAgentHours, &bud.MaxTokens, &bud.AgentHoursUsed, &bud.TokensUsed)
	if err != nil {
		return bud, fmt.Errorf("budget: %w", err)
	}
	return bud, nil
}

// UpdateBudget increments usage counters atomically.
func (b *PostgresBoard) UpdateBudget(ctx context.Context, campaignID uuid.UUID, deltaHours float64, deltaTokens int64) error {
	_, err := b.pool.Exec(ctx,
		`UPDATE swarm_budgets
		 SET agent_hours_used = agent_hours_used + $1,
		     tokens_used = tokens_used + $2,
		     updated_at = NOW()
		 WHERE campaign_id = $3`,
		deltaHours, deltaTokens, campaignID,
	)
	if err != nil {
		return fmt.Errorf("update budget: %w", err)
	}
	return nil
}

// SetBudgetLimits overrides the caps for a campaign.
func (b *PostgresBoard) SetBudgetLimits(ctx context.Context, campaignID uuid.UUID, maxHours float64, maxTokens int64) error {
	_, err := b.pool.Exec(ctx,
		`INSERT INTO swarm_budgets (campaign_id, max_agent_hours, max_tokens)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (campaign_id) DO UPDATE
		   SET max_agent_hours = EXCLUDED.max_agent_hours,
		       max_tokens = EXCLUDED.max_tokens,
		       updated_at = NOW()`,
		campaignID, maxHours, maxTokens,
	)
	if err != nil {
		return fmt.Errorf("set budget limits: %w", err)
	}
	return nil
}

// AgentBudget reads (or creates-with-defaults) the per-agent budget row.
func (b *PostgresBoard) AgentBudget(ctx context.Context, campaignID uuid.UUID, agent string) (AgentBudget, error) {
	var bud AgentBudget
	bud.CampaignID = campaignID
	bud.Agent = agent
	err := b.pool.QueryRow(ctx,
		`INSERT INTO swarm_agent_budgets (campaign_id, agent_name)
		 VALUES ($1, $2)
		 ON CONFLICT (campaign_id, agent_name) DO UPDATE SET updated_at = NOW()
		 RETURNING max_tokens, warn_at_tokens, tokens_used, warned`,
		campaignID, agent,
	).Scan(&bud.MaxTokens, &bud.WarnAtTokens, &bud.TokensUsed, &bud.Warned)
	if err != nil {
		return bud, fmt.Errorf("agent budget: %w", err)
	}
	return bud, nil
}

// ChargeAgent increments tokens_used and flips Warned when the soft
// threshold is first crossed. Atomic via a single UPDATE.
func (b *PostgresBoard) ChargeAgent(ctx context.Context, campaignID uuid.UUID, agent string, tokens int64) error {
	// Ensure the row exists first.
	if _, err := b.AgentBudget(ctx, campaignID, agent); err != nil {
		return err
	}
	_, err := b.pool.Exec(ctx,
		`UPDATE swarm_agent_budgets
		   SET tokens_used = tokens_used + $3,
		       warned = warned OR (tokens_used + $3 >= warn_at_tokens),
		       updated_at = NOW()
		 WHERE campaign_id = $1 AND agent_name = $2`,
		campaignID, agent, tokens,
	)
	if err != nil {
		return fmt.Errorf("charge agent: %w", err)
	}
	return nil
}

// SetAgentBudget overrides the caps (and clears Warned if usage is now
// below the new soft threshold).
func (b *PostgresBoard) SetAgentBudget(ctx context.Context, campaignID uuid.UUID, agent string, maxTokens, warnAt int64) error {
	_, err := b.pool.Exec(ctx,
		`INSERT INTO swarm_agent_budgets (campaign_id, agent_name, max_tokens, warn_at_tokens)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (campaign_id, agent_name) DO UPDATE
		   SET max_tokens = EXCLUDED.max_tokens,
		       warn_at_tokens = EXCLUDED.warn_at_tokens,
		       warned = swarm_agent_budgets.tokens_used >= EXCLUDED.warn_at_tokens,
		       updated_at = NOW()`,
		campaignID, agent, maxTokens, warnAt,
	)
	if err != nil {
		return fmt.Errorf("set agent budget: %w", err)
	}
	return nil
}

// embeddingArg converts a float32 vector into the string literal pgvector accepts.
// Returns nil (which pgx will send as NULL) for an empty slice.
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

// buildFindingText assembles the textual summary used as the embedding
// input. The summary prefers the Finding's Target and AgentName as
// high-signal anchors and falls back to a prefix of the JSON Data blob.
// maxDataBytes caps the Data slice so a giant finding payload doesn't
// blow the embedder's input limit.
func buildFindingText(f Finding, maxDataBytes int) string {
	var b strings.Builder
	if f.Target != "" {
		b.WriteString(f.Target)
		b.WriteString(" | ")
	}
	if f.AgentName != "" {
		b.WriteString(f.AgentName)
		b.WriteString(" | ")
	}
	b.WriteString(string(f.Type))
	b.WriteString(": ")
	if len(f.Data) > 0 {
		data := f.Data
		if len(data) > maxDataBytes {
			data = data[:maxDataBytes]
		}
		b.Write(data)
	}
	return b.String()
}
