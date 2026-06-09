package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the service.
var (
	ErrNotFound = errors.New("reports: report not found")
	ErrNoInputs = errors.New("reports: cannot build without a campaign id and at least one finding source")
)

// Store persists Reports. When pool is non-nil it uses PostgreSQL;
// otherwise it falls back to an in-memory map (lost on restart).
type Store struct {
	pool *pgxpool.Pool

	// In-memory fallback when pool is nil
	mu      sync.RWMutex
	records map[uuid.UUID]*Report
	byCamp  map[uuid.UUID][]*Report // campaign_id → reports
}

// NewStore creates a report store backed by PostgreSQL.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, records: make(map[uuid.UUID]*Report), byCamp: make(map[uuid.UUID][]*Report)}
}

// NewMemoryStore creates a report store backed by an in-memory map.
// This is used when PostgreSQL is not available (e.g. development or
// demo deployments). Reports are lost on process restart.
func NewMemoryStore() *Store {
	return &Store{pool: nil, records: make(map[uuid.UUID]*Report), byCamp: make(map[uuid.UUID][]*Report)}
}

func (s *Store) isMemory() bool { return s.pool == nil }

// Insert persists a fully-built Report.
func (s *Store) Insert(ctx context.Context, r *Report) error {
	if s.isMemory() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.records[r.ID] = r
		s.byCamp[r.CampaignID] = append(s.byCamp[r.CampaignID], r)
		return nil
	}

	sectionsJSON, err := json.Marshal(r.Sections)
	if err != nil {
		return fmt.Errorf("marshaling sections: %w", err)
	}
	summaryJSON, err := json.Marshal(r.Summary)
	if err != nil {
		return fmt.Errorf("marshaling summary: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO reports
			(id, campaign_id, title, status, format, markdown, summary, sections, byte_size, error_message, created_at, completed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12)
	`,
		r.ID, r.CampaignID, r.Title, r.Status, r.Format, r.Markdown,
		summaryJSON, sectionsJSON, r.ByteSize, r.ErrorMessage,
		r.CreatedAt, r.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting report: %w", err)
	}
	return nil
}

// Get returns a Report by id.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Report, error) {
	if s.isMemory() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		r, ok := s.records[id]
		if !ok {
			return nil, ErrNotFound
		}
		return r, nil
	}

	row := s.pool.QueryRow(ctx, `
		SELECT id, campaign_id, title, status, format, markdown, summary, sections,
		       byte_size, COALESCE(error_message, ''), created_at, completed_at
		FROM reports WHERE id = $1
	`, id)

	r := &Report{}
	var summaryJSON, sectionsJSON []byte
	if err := row.Scan(
		&r.ID, &r.CampaignID, &r.Title, &r.Status, &r.Format, &r.Markdown,
		&summaryJSON, &sectionsJSON, &r.ByteSize, &r.ErrorMessage,
		&r.CreatedAt, &r.CompletedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("getting report: %w", err)
	}
	if err := json.Unmarshal(summaryJSON, &r.Summary); err != nil {
		return nil, fmt.Errorf("unmarshaling summary: %w", err)
	}
	if err := json.Unmarshal(sectionsJSON, &r.Sections); err != nil {
		return nil, fmt.Errorf("unmarshaling sections: %w", err)
	}
	return r, nil
}

// ListByCampaign returns reports for a campaign, newest first.
func (s *Store) ListByCampaign(ctx context.Context, campaignID uuid.UUID, limit int) ([]*Report, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if s.isMemory() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := make([]*Report, 0)
		for _, r := range s.byCamp[campaignID] {
			out = append(out, r)
			if len(out) >= limit {
				break
			}
		}
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, campaign_id, title, status, format, byte_size,
		       COALESCE(error_message, ''), created_at, completed_at
		FROM reports WHERE campaign_id = $1
		ORDER BY created_at DESC LIMIT $2
	`, campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying reports: %w", err)
	}
	defer rows.Close()

	var out []*Report
	for rows.Next() {
		r := &Report{}
		if err := rows.Scan(
			&r.ID, &r.CampaignID, &r.Title, &r.Status, &r.Format,
			&r.ByteSize, &r.ErrorMessage, &r.CreatedAt, &r.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning report row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRecent returns the most recent reports across all campaigns.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]*Report, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if s.isMemory() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := make([]*Report, 0, len(s.records))
		for _, r := range s.records {
			out = append(out, r)
		}
		if len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, campaign_id, title, status, format, byte_size,
		       COALESCE(error_message, ''), created_at, completed_at
		FROM reports ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("querying recent reports: %w", err)
	}
	defer rows.Close()

	var out []*Report
	for rows.Next() {
		r := &Report{}
		if err := rows.Scan(
			&r.ID, &r.CampaignID, &r.Title, &r.Status, &r.Format,
			&r.ByteSize, &r.ErrorMessage, &r.CreatedAt, &r.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning report row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes a report.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	if s.isMemory() {
		s.mu.Lock()
		defer s.mu.Unlock()
		r, ok := s.records[id]
		if !ok {
			return ErrNotFound
		}
		delete(s.records, id)
		if list, ok := s.byCamp[r.CampaignID]; ok {
			for i, rr := range list {
				if rr.ID == id {
					s.byCamp[r.CampaignID] = append(list[:i], list[i+1:]...)
					break
				}
			}
		}
		return nil
	}

	tag, err := s.pool.Exec(ctx, `DELETE FROM reports WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting report: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Service ---

// Service is the high-level facade. It owns a Store plus a Builder and
// is the type the API layer depends on.
type Service struct {
	store  *Store
	builder *Builder
	// Now allows tests to inject a deterministic clock.
	Now func() time.Time
}

// NewService wires a store and builder together. Either may be nil —
// a nil store means the service operates as a "build only" facade
// (useful for tests and for legacy / Postgres-less deployments where
// reports are returned to the caller but not persisted).
func NewService(store *Store, builder *Builder) *Service {
	if builder == nil {
		builder = NewBuilder()
	}
	return &Service{store: store, builder: builder, Now: time.Now}
}

// GenerateForCampaign renders and persists a report from the given
// input. The input is expected to be a complete BuildInput (campaign +
// findings already snapshotted by the caller).
//
// When the service was constructed with a nil store, this method
// returns the built report without persisting; the caller is
// responsible for storage (e.g. writing to disk or shipping over the
// wire). When a store is present, the report is inserted and a copy
// with the assigned id is returned.
func (s *Service) GenerateForCampaign(ctx context.Context, in BuildInput) (*Report, error) {
	if in.Campaign.ID == uuid.Nil {
		return nil, ErrNoInputs
	}
	if in.GeneratedAt.IsZero() {
		in.GeneratedAt = s.now()
	}
	if in.Generator == "" {
		in.Generator = "pentest-swarm-ai"
	}

	r, err := s.builder.Build(in)
	if err != nil {
		return nil, fmt.Errorf("building report: %w", err)
	}
	if s.store != nil {
		if err := s.store.Insert(ctx, r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Generate returns a built report without persistence. The store is
// consulted only for read paths; if it's nil the report is purely
// transient.
func (s *Service) Generate(in BuildInput) (*Report, error) {
	if in.Campaign.ID == uuid.Nil {
		return nil, ErrNoInputs
	}
	if in.GeneratedAt.IsZero() {
		in.GeneratedAt = s.now()
	}
	if in.Generator == "" {
		in.Generator = "pentest-swarm-ai"
	}
	return s.builder.Build(in)
}

// Get returns a stored report by id.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Report, error) {
	return s.store.Get(ctx, id)
}

// ListByCampaign proxies to the store.
func (s *Service) ListByCampaign(ctx context.Context, campaignID uuid.UUID, limit int) ([]*Report, error) {
	return s.store.ListByCampaign(ctx, campaignID, limit)
}

// ListRecent proxies to the store.
func (s *Service) ListRecent(ctx context.Context, limit int) ([]*Report, error) {
	return s.store.ListRecent(ctx, limit)
}

// Delete removes a report.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.store.Delete(ctx, id)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
