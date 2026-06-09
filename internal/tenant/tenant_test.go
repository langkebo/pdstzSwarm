package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestInMemoryStore_CreateAndGet(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	want := &Tenant{Name: "acme", Plan: "team"}
	if err := s.Create(ctx, want); err != nil {
		t.Fatal(err)
	}
	if want.ID == uuid.Nil {
		t.Errorf("Create did not assign ID")
	}
	if want.CreatedAt.IsZero() {
		t.Errorf("Create did not assign CreatedAt")
	}
	got, err := s.Get(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
}

func TestInMemoryStore_GetByName(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Tenant{Name: "acme"})
	got, err := s.GetByName(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "acme" {
		t.Errorf("Name = %q, want acme", got.Name)
	}
	if _, err := s.GetByName(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing → %v, want ErrNotFound", err)
	}
}

func TestInMemoryStore_EmptyName(t *testing.T) {
	s := NewInMemoryStore()
	if err := s.Create(context.Background(), &Tenant{Name: ""}); !errors.Is(err, ErrEmptyName) {
		t.Errorf("empty → %v, want ErrEmptyName", err)
	}
	if err := s.Create(context.Background(), nil); !errors.Is(err, ErrEmptyName) {
		t.Errorf("nil → %v, want ErrEmptyName", err)
	}
}

func TestInMemoryStore_DuplicateName(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Tenant{Name: "acme"})
	if err := s.Create(ctx, &Tenant{Name: "acme"}); err == nil {
		t.Errorf("duplicate name accepted")
	}
}

func TestInMemoryStore_List(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Tenant{Name: "a"})
	_ = s.Create(ctx, &Tenant{Name: "b"})
	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestInMemoryStore_Delete(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	tt := &Tenant{Name: "tmp"}
	_ = s.Create(ctx, tt)
	if err := s.Delete(ctx, tt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, tt.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("post-delete Get → %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, tt.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete → %v, want ErrNotFound", err)
	}
}

func TestInMemoryStore_CannotDeleteDefault(t *testing.T) {
	s := NewInMemoryStore()
	if err := s.Delete(context.Background(), DefaultTenantID); err == nil {
		t.Errorf("default tenant deletion accepted")
	}
}

func TestContext_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if FromContext(ctx) != nil {
		t.Errorf("empty ctx should yield nil tenant")
	}
	want := &Tenant{ID: uuid.New(), Name: "x"}
	ctx = NewContext(ctx, want)
	got := FromContext(ctx)
	if got == nil || got.ID != want.ID {
		t.Errorf("FromContext = %v, want %v", got, want)
	}
	if IDFromContext(ctx) != want.ID {
		t.Errorf("IDFromContext mismatch")
	}
}

func TestMustFromContext_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic, got none")
		}
	}()
	MustFromContext(context.Background())
}

func TestNewContext_NilTenantIsNoop(t *testing.T) {
	ctx := NewContext(context.Background(), nil)
	if FromContext(ctx) != nil {
		t.Errorf("NewContext with nil tenant should not stamp")
	}
}
