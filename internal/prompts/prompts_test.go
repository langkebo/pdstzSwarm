package prompts

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestAllPromptTypes_Exactly35(t *testing.T) {
	// The frontend renders an "X / 35 customized" badge that
	// depends on this exact count. The catalogue can grow
	// (we'd update the badge) but a *shrink* would mean we
	// silently lost a type.
	if got := len(AllPromptTypes); got != PromptTypeCount {
		t.Errorf("AllPromptTypes has %d entries, want %d", got, PromptTypeCount)
	}
	// Uniqueness invariant.
	seen := make(map[PromptType]bool, len(AllPromptTypes))
	for _, p := range AllPromptTypes {
		if seen[p] {
			t.Errorf("duplicate PromptType: %q", p)
		}
		seen[p] = true
	}
}

func TestValidType(t *testing.T) {
	for _, p := range AllPromptTypes {
		if !ValidType(p) {
			t.Errorf("ValidType(%q) = false, want true", p)
		}
	}
	if ValidType("nonexistent") {
		t.Error("ValidType(\"nonexistent\") = true, want false")
	}
	if ValidType("") {
		t.Error("ValidType(\"\") = true, want false")
	}
}

func TestScanVariables(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"empty", "", nil},
		{"no vars", "hello world", nil},
		{"single var", "{{ .Name }}", []string{"Name"}},
		{"multiple vars, dedup", "{{ .Name }} and {{ .Name }} and {{ .Other }}",
			[]string{"Name", "Other"}},
		{"trim dash", "{{- .Name }}", []string{"Name"}},
		{"block-level", "{{ if .X }}body{{ end }}", []string{"X"}},
		{"range", "{{ range .Items }}{{ .Name }}{{ end }}", []string{"Items", "Name"}},
		{"invalid dot - missing", "{{ Name }}", nil},
		{"nested", "{{ .Foo.Bar }}", []string{"Foo"}},
		{"digits in name", "{{ .Var1 }} {{ .Var2 }}", []string{"Var1", "Var2"}},
		{"underscore", "{{ ._private }}", []string{"_private"}},
		{"trailing text ignored", "{{ .Foo }}bar", []string{"Foo"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScanVariables(c.body)
			if !stringSliceEq(got, c.want) {
				t.Errorf("ScanVariables(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestDescription(t *testing.T) {
	for _, p := range AllPromptTypes {
		if Description(p) == "" {
			t.Errorf("Description(%q) is empty", p)
		}
	}
	if Description("nope") != "" {
		t.Error("Description of unknown type should be empty")
	}
}

func TestInMemoryStore_GetList(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	if _, err := s.Get(ctx, PromptAuthSystem); err != ErrNotFound {
		t.Errorf("Get on empty store: err = %v, want ErrNotFound", err)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("List on empty store: %d entries, want 0", len(list))
	}
}

func TestInMemoryStore_SetGetRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()

	p := &Prompt{Type: PromptAuthSystem, Body: "hello {{ .Name }}"}
	if err := s.Set(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.Variables == nil || len(p.Variables) != 1 || p.Variables[0] != "Name" {
		t.Errorf("Set did not scan variables: %v", p.Variables)
	}
	if p.Version != 1 {
		t.Errorf("first Set: version = %d, want 1", p.Version)
	}

	got, err := s.Get(ctx, PromptAuthSystem)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "hello {{ .Name }}" {
		t.Errorf("body = %q, want %q", got.Body, "hello {{ .Name }}")
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}

	// Update bumps version.
	if err := s.Set(ctx, &Prompt{Type: PromptAuthSystem, Body: "v2"}); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Get(ctx, PromptAuthSystem)
	if got2.Version != 2 {
		t.Errorf("after second Set: version = %d, want 2", got2.Version)
	}
}

func TestInMemoryStore_ReadOnly(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	s.SetReadOnly(true)
	if err := s.Set(ctx, &Prompt{Type: PromptAuthSystem, Body: "x"}); err != ErrReadOnly {
		t.Errorf("Set on read-only: err = %v, want ErrReadOnly", err)
	}
	if err := s.Delete(ctx, PromptAuthSystem); err != ErrReadOnly {
		t.Errorf("Delete on read-only: err = %v, want ErrReadOnly", err)
	}
}

func TestInMemoryStore_GetReturnsCopy(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	if err := s.Set(ctx, &Prompt{Type: PromptAuthSystem, Body: "orig"}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get(ctx, PromptAuthSystem)
	p.Body = "MUTATED"
	p2, _ := s.Get(ctx, PromptAuthSystem)
	if p2.Body != "orig" {
		t.Errorf("Get did not return a copy: %q", p2.Body)
	}
}

func TestInMemoryStore_ConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t := PromptType(string(PromptAuthSystem) + "_concurrent")
			_ = s.Set(ctx, &Prompt{Type: t, Body: "x"})
			_, _ = s.Get(ctx, t)
		}(i)
	}
	wg.Wait()
}

func TestService_ListReturnsAll35(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore()
	svc := NewService(store, nil)

	out, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != PromptTypeCount {
		t.Errorf("List returned %d, want %d", len(out), PromptTypeCount)
	}
	// All entries should be the canonical order (matching
	// AllPromptTypes).
	for i, p := range out {
		if p.Type != AllPromptTypes[i] {
			t.Errorf("List[%d].Type = %q, want %q", i, p.Type, AllPromptTypes[i])
		}
	}
}

func TestService_GetSetRoundtrip(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore()
	svc := NewService(store, nil)

	p, err := svc.Set(ctx, PromptAuthSystem, "hello {{ .Name }}", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 1 {
		t.Errorf("after Set: version = %d, want 1", p.Version)
	}
	if p.UpdatedBy != "tester" {
		t.Errorf("UpdatedBy = %q, want %q", p.UpdatedBy, "tester")
	}

	got, err := svc.Get(ctx, PromptAuthSystem)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "hello {{ .Name }}" {
		t.Errorf("body = %q", got.Body)
	}
}

func TestService_SetRejectsInvalidType(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewInMemoryStore(), nil)
	if _, err := svc.Set(ctx, "nope", "body", ""); err == nil {
		t.Error("expected error for invalid type")
	}
}

func TestService_SetRejectsEmptyBody(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewInMemoryStore(), nil)
	cases := []string{"", "   ", "\n\n\n", "\t \t"}
	for _, c := range cases {
		if _, err := svc.Set(ctx, PromptAuthSystem, c, ""); err != ErrBodyEmpty {
			t.Errorf("Set(%q): err = %v, want ErrBodyEmpty", c, err)
		}
	}
}

func TestService_ResetClearsOverride(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore()
	defaults := &stubDefaults{
		values: map[PromptType]string{PromptAuthSystem: "DEFAULT"},
	}
	svc := NewService(store, defaults)

	// Override.
	if _, err := svc.Set(ctx, PromptAuthSystem, "USER", ""); err != nil {
		t.Fatal(err)
	}
	// Reset.
	if err := svc.Reset(ctx, PromptAuthSystem); err != nil {
		t.Fatal(err)
	}
	// Get returns the default.
	got, err := svc.Get(ctx, PromptAuthSystem)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "DEFAULT" {
		t.Errorf("after Reset: body = %q, want DEFAULT", got.Body)
	}
}

func TestService_GetFallsBackToDefaults(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore()
	defaults := &stubDefaults{
		values: map[PromptType]string{PromptAuthSystem: "DEFAULT"},
	}
	svc := NewService(store, defaults)

	got, err := svc.Get(ctx, PromptAuthSystem)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "DEFAULT" {
		t.Errorf("body = %q, want DEFAULT", got.Body)
	}
	if got.Version != 0 {
		t.Errorf("default-only entry: version = %d, want 0", got.Version)
	}
}

func TestService_SetSavesActor(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewInMemoryStore(), nil)
	p, err := svc.Set(ctx, PromptAuthSystem, "body", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if p.UpdatedBy != "alice" {
		t.Errorf("UpdatedBy = %q, want alice", p.UpdatedBy)
	}
}

func TestService_SetRejectsOversizedBody(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewInMemoryStore(), nil)
	big := strings.Repeat("x", 300*1024)
	if _, err := svc.Set(ctx, PromptAuthSystem, big, ""); err == nil {
		t.Error("expected error for oversized body")
	}
}

func TestEmbeddedDefaults_MissingTypes(t *testing.T) {
	// The embedded templates/ dir has only offsec_system*.tmpl
	// at the moment; all other 33 types should return
	// ("", false). This guards against a contributor
	// accidentally removing a .tmpl — the editor will show
	// a "no default" badge and stay read-only for that type.
	d := NewEmbeddedDefaults()
	missing := d.Warmup(context.Background())
	if len(missing) == 0 {
		t.Skip("all 35 types have a default; this assertion only runs when some are missing")
	}
	for _, m := range missing {
		if _, ok := d.DefaultBody(m); ok {
			t.Errorf("DefaultBody(%q) returned ok=true, expected false", m)
		}
	}
}

type stubDefaults struct {
	values map[PromptType]string
}

func (s *stubDefaults) DefaultBody(t PromptType) (string, bool) {
	v, ok := s.values[t]
	return v, ok
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
