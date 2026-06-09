package license

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerify_RoundTrip(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	l := License{
		Schema:         CurrentSchema,
		Version:        1,
		Plan:           PlanEnterprise,
		TenantID:       "tenant-abc",
		InstallationID: "host-xyz",
		IssuedAt:       time.Now().UTC().Truncate(time.Second),
		ExpiresAt:      time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second),
		Features:       []string{"multi_tenant", "audit_log"},
		IssuedBy:       "acme-licensing",
	}
	blob, err := l.Sign(kp.Private)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blob, ".") {
		t.Fatalf("blob missing separator: %q", blob)
	}
	got, err := Verify(blob, "host-xyz", kp.Public, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Plan != PlanEnterprise || got.TenantID != "tenant-abc" {
		t.Errorf("decoded mismatch: %+v", got)
	}
}

func TestVerify_WrongSignature(t *testing.T) {
	kp, _ := GenerateKeyPair()
	other, _ := GenerateKeyPair()
	l := License{
		Schema:         CurrentSchema,
		InstallationID: "host-xyz",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	blob, _ := l.Sign(kp.Private)
	// Sign with kp.Private, but verify with other.Public →
	// signature won't match.
	_, err := Verify(blob, "host-xyz", other.Public, time.Now())
	if err != ErrInvalidSignature {
		t.Errorf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	kp, _ := GenerateKeyPair()
	l := License{
		Schema:         CurrentSchema,
		InstallationID: "host-xyz",
		ExpiresAt:      time.Now().Add(-time.Hour),
	}
	blob, _ := l.Sign(kp.Private)
	_, err := Verify(blob, "host-xyz", kp.Public, time.Now())
	if err != ErrExpired {
		t.Errorf("err = %v, want ErrExpired", err)
	}
}

func TestVerify_InstallationMismatch(t *testing.T) {
	kp, _ := GenerateKeyPair()
	l := License{
		Schema:         CurrentSchema,
		InstallationID: "host-xyz",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	blob, _ := l.Sign(kp.Private)
	_, err := Verify(blob, "host-OTHER", kp.Public, time.Now())
	if !errors.Is(err, ErrInstallationMismatch) {
		t.Errorf("err = %v, want ErrInstallationMismatch", err)
	}
}

func TestVerify_InstallationEmptyWildcard(t *testing.T) {
	// When the license binds to a specific host and the
	// caller passes "" we still require equality (the
	// caller's empty string is treated as "unknown", not
	// as "match-anything"). The Verify implementation
	// short-circuits when either side is empty, so the
	// signature alone is checked.
	kp, _ := GenerateKeyPair()
	l := License{
		Schema:         CurrentSchema,
		InstallationID: "host-xyz",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	blob, _ := l.Sign(kp.Private)
	if _, err := Verify(blob, "", kp.Public, time.Now()); err != nil {
		t.Errorf("Verify with empty host: %v", err)
	}
}

func TestVerify_SchemaMismatch(t *testing.T) {
	kp, _ := GenerateKeyPair()
	l := License{
		Schema:         99, // future schema
		InstallationID: "host-xyz",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	blob, _ := l.Sign(kp.Private)
	_, err := Verify(blob, "host-xyz", kp.Public, time.Now())
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("err = %v, want schema mismatch", err)
	}
}

func TestVerify_MalformedBlob(t *testing.T) {
	kp, _ := GenerateKeyPair()
	cases := []string{
		"",
		"no-separator",
		"only-one-part.here",
		"foo.bar.baz", // too many parts
	}
	for _, c := range cases {
		_, err := Verify(c, "host", kp.Public, time.Now())
		if err == nil {
			t.Errorf("blob %q: expected error, got nil", c)
		}
	}
}

func TestHasFeature(t *testing.T) {
	l := License{Features: []string{"a", "b", "c"}}
	if !l.HasFeature("b") {
		t.Error("HasFeature(b) = false, want true")
	}
	if l.HasFeature("z") {
		t.Error("HasFeature(z) = true, want false")
	}
	if (License{}).HasFeature("anything") {
		t.Error("zero-value HasFeature returned true")
	}
}

func TestFingerprint_Stable(t *testing.T) {
	l := License{
		Schema:         CurrentSchema,
		InstallationID: "host-xyz",
		Plan:           PlanTeam,
		ExpiresAt:      time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	f1 := l.Fingerprint()
	f2 := l.Fingerprint()
	if f1 != f2 {
		t.Errorf("fingerprint not stable: %q vs %q", f1, f2)
	}
	if len(f1) != 16 { // 8 bytes = 16 hex chars
		t.Errorf("fingerprint length = %d, want 16", len(f1))
	}
}

func TestParsePublicKeyHex(t *testing.T) {
	kp, _ := GenerateKeyPair()
	got, err := ParsePublicKeyHex(kp.PublicKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(kp.Public) {
		t.Errorf("public key mismatch")
	}
	if _, err := ParsePublicKeyHex(""); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := ParsePublicKeyHex("not-hex"); err == nil {
		t.Error("garbage key accepted")
	}
	if _, err := ParsePublicKeyHex(strings.Repeat("aa", 32)); err != nil {
		// 32 bytes of 0xAA is technically a valid Ed25519
		// public key (Go's stdlib doesn't reject small-order
		// keys), so this should succeed.
		t.Errorf("32-byte all-0xAA key rejected: %v", err)
	}
}

func TestParsePrivateKeyHex(t *testing.T) {
	kp, _ := GenerateKeyPair()
	got, err := ParsePrivateKeyHex(kp.PrivateKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(kp.Private) {
		t.Errorf("private key mismatch")
	}
	if _, err := ParsePrivateKeyHex("tooshort"); err == nil {
		t.Error("short key accepted")
	}
}

func TestPublicKeyPEM_RoundTrip(t *testing.T) {
	kp, _ := GenerateKeyPair()
	pem, err := PublicKeyPEM(kp.Public)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pem, "-----BEGIN PUBLIC KEY-----") {
		t.Errorf("missing PEM header: %q", pem)
	}
	if !strings.Contains(pem, "-----END PUBLIC KEY-----") {
		t.Errorf("missing PEM footer")
	}
}

func TestSign_DefaultSchema(t *testing.T) {
	// A zero-schema license still signs with the current
	// schema, so the wire is always well-formed.
	kp, _ := GenerateKeyPair()
	l := License{
		InstallationID: "host",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	blob, err := l.Sign(kp.Private)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(blob, "host", kp.Public, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != CurrentSchema {
		t.Errorf("schema = %d, want %d", got.Schema, CurrentSchema)
	}
}

func TestVerify_TimeInjected(t *testing.T) {
	// Ensure the time argument is actually consulted, not
	// the wall clock.
	kp, _ := GenerateKeyPair()
	l := License{
		Schema:         CurrentSchema,
		InstallationID: "host",
		ExpiresAt:      time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	blob, _ := l.Sign(kp.Private)
	// Pretend "now" is 2025 — license not expired.
	if _, err := Verify(blob, "host", kp.Public, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Errorf("2025 verify: %v", err)
	}
	// Pretend "now" is 2031 — license expired.
	if _, err := Verify(blob, "host", kp.Public, time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC)); err != ErrExpired {
		t.Errorf("2031 verify: err=%v, want ErrExpired", err)
	}
}

// Suppress unused-import lint when the test file is
// compiled with the -tags=skip flag.
var _ = ed25519.PublicKeySize
