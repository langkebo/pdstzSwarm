package prompts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrInvalidType is returned by the Service for any unknown
// PromptType. The HTTP layer maps this to 400.
var ErrInvalidType = errors.New("prompts: invalid type")

// ErrBodyEmpty is returned when the body is empty or
// whitespace-only. We reject empties so the editor can't
// accidentally save a prompt that would render as a blank
// system message.
var ErrBodyEmpty = errors.New("prompts: body is empty")

// Service is the business-logic layer that the API handler
// sits on top of. The Service composes the Store (persistence)
// and a "default source" — the embedded templates under
// internal/agent/prompts/templates/.
//
// Read path (Get): if the type has an override in the store,
// return that; otherwise derive from the embedded default
// (i.e. the file in internal/agent/prompts/templates). This
// is what makes the editor's "Reset to default" button
// a one-call operation: delete the override, and the next
// Get returns the embedded body.
//
// Write path (Set): validate → store.
type Service struct {
	store   Store
	defaults DefaultsSource
	clock   func() time.Time
	mu      sync.Mutex
}

// DefaultsSource is the abstraction over the embedded
// templates. We isolate it so tests can inject canned
// defaults without a real go:embed.
type DefaultsSource interface {
	DefaultBody(t PromptType) (string, bool)
}

// NewService returns a Service. defaults may be nil — in
// that case unknown types return ErrNotFound on first read,
// but explicit store-loaded entries still work. The clock
// is for tests; pass nil for time.Now().
func NewService(store Store, defaults DefaultsSource) *Service {
	return &Service{
		store:   store,
		defaults: defaults,
		clock:   time.Now,
	}
}

// List returns the *complete* set of 35 prompts, including
// the ones that don't have an override yet. The override
// status is encoded in Version == 0 (no override) vs
// Version >= 1 (override present).
//
// Always returns exactly len(AllPromptTypes) entries. The
// editor's "X / 35 customized" badge uses this invariant.
func (s *Service) List(ctx context.Context) ([]*Prompt, error) {
	stored, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	byType := make(map[PromptType]*Prompt, len(stored))
	for _, p := range stored {
		byType[p.Type] = p
	}

	out := make([]*Prompt, 0, len(AllPromptTypes))
	for _, t := range AllPromptTypes {
		if p, ok := byType[t]; ok {
			out = append(out, p)
			continue
		}
		// No override — synthesize a "default" entry whose
		// Version is 0 and whose Body is the embedded
		// default (or empty if no default source is wired).
		p := &Prompt{Type: t, Description: Description(t)}
		if s.defaults != nil {
			if body, ok := s.defaults.DefaultBody(t); ok {
				p.Body = body
			}
		}
		p.Variables = ScanVariables(p.Body)
		out = append(out, p)
	}
	return out, nil
}

// Get returns the effective prompt for t. If the store has
// an override, that's returned; otherwise the embedded
// default; otherwise ErrNotFound (and the HTTP layer maps
// that to a synthesized response with body="").
func (s *Service) Get(ctx context.Context, t PromptType) (*Prompt, error) {
	if !ValidType(t) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidType, t)
	}
	if p, err := s.store.Get(ctx, t); err == nil {
		return p, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	// Fall back to the embedded default.
	if s.defaults != nil {
		if body, ok := s.defaults.DefaultBody(t); ok {
			return &Prompt{
				Type:        t,
				Body:        body,
				Description: Description(t),
				Variables:   ScanVariables(body),
				Version:     0,
			}, nil
		}
	}
	return nil, ErrNotFound
}

// Set validates the body, scans variables, bumps version,
// and writes to the store. The actor argument is recorded
// on the prompt's UpdatedBy field; pass "" for "system".
func (s *Service) Set(ctx context.Context, t PromptType, body, actor string) (*Prompt, error) {
	if !ValidType(t) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidType, t)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, ErrBodyEmpty
	}
	// Sanity-cap: a 256 KiB prompt is well above any
	// real-world offensive-security template. Anything
	// larger is almost certainly a paste-bomb.
	if len(body) > 256*1024 {
		return nil, fmt.Errorf("prompts: body too large (%d bytes)", len(body))
	}
	p := &Prompt{
		Type:        t,
		Body:        body,
		Description: Description(t),
		UpdatedBy:   actor,
	}
	if err := s.store.Set(ctx, p); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, t)
}

// Reset deletes the override, leaving the embedded default
// in effect on the next Get. Used by the editor's "Reset to
// default" button.
func (s *Service) Reset(ctx context.Context, t PromptType) error {
	if !ValidType(t) {
		return fmt.Errorf("%w: %q", ErrInvalidType, t)
	}
	return s.store.Delete(ctx, t)
}
