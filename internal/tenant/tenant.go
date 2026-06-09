// Package tenant implements the P5+ multi-tenant primitive.
//
// The contract is intentionally minimal: a Tenant is a UUID
// with a name and a plan. TenantStore is the small interface
// callers use to look up / list / create tenants. The ctx
// helpers (NewContext / FromContext / WithTenant) move the
// tenant id through call chains so the auth middleware can
// stamp every request and the storage layer can stamp every
// transaction with `SET LOCAL app.tenant_id`.
//
// Postgres Row-Level Security does the actual isolation
// (see internal/db/migrations/000009_tenants.sql). This
// package is the Go-side glue that keeps the RLS GUC
// consistent across request and tx boundaries.
package tenant

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Tenant is the wire shape for a tenant record.
type Tenant struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is the interface the auth / api packages use. The
// production implementation is the Postgres-backed one; an
// in-memory implementation is provided for tests and the
// single-tenant demo path.
type Store interface {
	Create(ctx context.Context, t *Tenant) error
	Get(ctx context.Context, id uuid.UUID) (*Tenant, error)
	GetByName(ctx context.Context, name string) (*Tenant, error)
	List(ctx context.Context) ([]*Tenant, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// ErrNotFound is returned by Store implementations when the
// id / name doesn't exist.
var ErrNotFound = errors.New("tenant: not found")

// ErrEmptyName is returned by Create when name is empty.
var ErrEmptyName = errors.New("tenant: name is required")

// DefaultTenantID is the placeholder used by the tenant
// migration to absorb pre-existing rows. Operators are
// expected to migrate these rows to a real tenant before
// turning on RLS in production.
var DefaultTenantID = uuid.MustParse("00000000-0000-0000-0000-000000000000")

// ctxKey is the unexported type used as the context key for
// tenant lookups. Using an unexported type prevents
// accidental key collisions with other packages.
type ctxKey struct{}

// NewContext returns a new context that carries t. Downstream
// code can pull it out with FromContext.
func NewContext(ctx context.Context, t *Tenant) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, t)
}

// FromContext returns the tenant attached to ctx, or nil if
// no tenant is set. The auth middleware should attach the
// tenant after a successful session lookup; the storage
// layer should read it back and stamp every tx with
// SET LOCAL app.tenant_id.
func FromContext(ctx context.Context) *Tenant {
	if t, ok := ctx.Value(ctxKey{}).(*Tenant); ok {
		return t
	}
	return nil
}

// IDFromContext is a convenience that returns the tenant's
// UUID, or uuid.Nil if no tenant is set. Used by the storage
// layer.
func IDFromContext(ctx context.Context) uuid.UUID {
	t := FromContext(ctx)
	if t == nil {
		return uuid.Nil
	}
	return t.ID
}

// MustFromContext returns the tenant or panics. Use only in
// code paths that have already proven the request has a
// tenant (e.g. behind the auth middleware).
func MustFromContext(ctx context.Context) *Tenant {
	t := FromContext(ctx)
	if t == nil {
		panic("tenant.MustFromContext: no tenant in context")
	}
	return t
}

// InMemoryStore is a goroutine-safe Store for tests and the
// single-tenant demo path. Not for production use.
type InMemoryStore struct {
	mu   sync.RWMutex
	data map[uuid.UUID]*Tenant
}

// NewInMemoryStore returns an empty InMemoryStore.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{data: make(map[uuid.UUID]*Tenant)}
}

// Create stores a new tenant. Auto-fills ID / CreatedAt if
// empty. Returns ErrEmptyName for empty names.
func (s *InMemoryStore) Create(_ context.Context, t *Tenant) error {
	if t == nil || t.Name == "" {
		return ErrEmptyName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data {
		if existing.Name == t.Name {
			return errors.New("tenant: name already in use")
		}
	}
	now := time.Now().UTC()
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	clone := *t
	s.data[t.ID] = &clone
	return nil
}

// Get returns the tenant by id, or ErrNotFound.
func (s *InMemoryStore) Get(_ context.Context, id uuid.UUID) (*Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	clone := *t
	return &clone, nil
}

// GetByName returns the tenant by name, or ErrNotFound.
func (s *InMemoryStore) GetByName(_ context.Context, name string) (*Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.data {
		if t.Name == name {
			clone := *t
			return &clone, nil
		}
	}
	return nil, ErrNotFound
}

// List returns a copy of every tenant.
func (s *InMemoryStore) List(_ context.Context) ([]*Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Tenant, 0, len(s.data))
	for _, t := range s.data {
		clone := *t
		out = append(out, &clone)
	}
	return out, nil
}

// Delete removes a tenant by id. Returns ErrNotFound if no
// such tenant exists. Refusing to delete DefaultTenantID is
// a safety net: it's referenced by the tenant RLS
// migration's "backfill default" step and by extension by
// every pre-existing row.
func (s *InMemoryStore) Delete(_ context.Context, id uuid.UUID) error {
	if id == DefaultTenantID {
		return errors.New("tenant: cannot delete the default tenant")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return ErrNotFound
	}
	delete(s.data, id)
	return nil
}
