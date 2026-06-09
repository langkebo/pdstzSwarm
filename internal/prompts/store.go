package prompts

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNotFound is returned by Get when the requested PromptType
// has never been saved (i.e. the editor is asking about a fresh
// type). The HTTP handler maps this to 404.
var ErrNotFound = errors.New("prompts: not found")

// ErrReadOnly is returned by Set when the store is configured
// as read-only. The HTTP handler maps this to 403.
var ErrReadOnly = errors.New("prompts: store is read-only")

// Store is the persistence interface. Two implementations:
//   - InMemoryStore: process-local, no DB, fast.
//   - (Future) PostgresStore: durable, multi-pod.
//
// The Service holds a Store; tests use InMemoryStore, the
// CLI uses whatever's wired in cmd/serve.
type Store interface {
	Get(ctx context.Context, t PromptType) (*Prompt, error)
	List(ctx context.Context) ([]*Prompt, error)
	Set(ctx context.Context, p *Prompt) error
	Delete(ctx context.Context, t PromptType) error
}

// InMemoryStore is a thread-safe, zero-dep implementation
// suitable for tests and the no-Postgres demo mode. It locks
// per-write; the read path is RWMutex-guarded for the map.
type InMemoryStore struct {
	mu     sync.RWMutex
	data   map[PromptType]*Prompt
	clock  func() time.Time
	readOnly bool
}

// NewInMemoryStore returns an empty InMemoryStore. The clock
// injection is used by tests; pass nil to use time.Now().
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		data:  make(map[PromptType]*Prompt),
		clock: time.Now,
	}
}

// SetReadOnly flips the read-only flag. Used by the "Lock
// prompts" admin toggle.
func (s *InMemoryStore) SetReadOnly(ro bool) {
	s.mu.Lock()
	s.readOnly = ro
	s.mu.Unlock()
}

func (s *InMemoryStore) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

// Get returns a copy of the stored prompt. The copy means
// callers can mutate the returned struct without poisoning
// the in-memory state — important for the editor's
// "preview" workflow.
func (s *InMemoryStore) Get(ctx context.Context, t PromptType) (*Prompt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data[t]
	if !ok {
		return nil, ErrNotFound
	}
	// Shallow copy is enough — Body/Description/Variables are
	// all immutable from the store's perspective.
	cp := *p
	return &cp, nil
}

// List returns a deterministic (alphabetical-by-Type) snapshot
// of all stored prompts. The slice is fresh; callers may sort
// or filter without affecting the store.
func (s *InMemoryStore) List(ctx context.Context) ([]*Prompt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Prompt, 0, len(s.data))
	for _, p := range s.data {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// Set inserts or updates a prompt. Returns ErrReadOnly if
// the store is configured as read-only.
func (s *InMemoryStore) Set(ctx context.Context, p *Prompt) error {
	if p == nil {
		return errors.New("prompts: nil prompt")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return ErrReadOnly
	}
	// Re-scan variables on every save to keep the index in
	// sync with the body — the editor may have hand-edited
	// the body via the in-page textbox and we want the
	// "variables" list to follow the body, not lag behind.
	p.Variables = ScanVariables(p.Body)
	// Bump version + timestamp atomically. Existing
	// records: version is monotonic from 1.
	prev, ok := s.data[p.Type]
	if ok {
		p.Version = prev.Version + 1
	} else {
		p.Version = 1
	}
	p.UpdatedAt = s.now().Unix()
	// Final write: keep a copy, not the caller's pointer.
	cp := *p
	s.data[p.Type] = &cp
	return nil
}

// Delete removes a prompt. Currently unused — but exposed
// for the editor's "Reset to default" button, which deletes
// the user override and re-derives the prompt from the
// embedded template on next read.
func (s *InMemoryStore) Delete(ctx context.Context, t PromptType) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return ErrReadOnly
	}
	delete(s.data, t)
	return nil
}
