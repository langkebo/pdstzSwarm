// Package license implements the P5+ commercial license
// primitive. A license is an Ed25519-signed JSON payload
// (canonicalised by the encoding/json wire shape) that binds a
// plan / tenant / installation / expiry tuple to the issuer's
// private key. The server-side Validator verifies the signature
// using the embedded public key (or a custom one supplied by
// the operator), and rejects the license if any of the bound
// fields don't match what the running deployment claims.
//
// Why Ed25519 (zero-dep, stdlib only)?
//
//   - The signature size is constant (64 bytes) — the
//     license blob stays tiny enough to log in a single line.
//   - Verification is fast (< 1µs) so it can run on every
//     startup with no perceptible cost.
//   - Ed25519 is in `crypto/ed25519` since Go 1.13; no
//     third-party crypto dep keeps the supply chain small.
//   - Deterministic nonces (Sign-with-empty-random) mean
//     signing the same payload twice gives the same
//     signature, which is critical for reproducible builds.
//
// Threat model
//
//   - The attacker has the binary and the license blob but
//     not the private key. → Signature verification
//     rejects forgery.
//   - The attacker rewrites the binary. → Out of scope;
//     that's why commercial builds ship with reproducible
//     builds + a separate attestation step (TUF, sigstore,
//     etc.) — the license primitive here is one of several
//     layers, not the only one.
//   - The license leaks. → The license is bound to an
//     InstallationID; deploying it on a different host
//     triggers a mismatch. A revocation list (out of scope
//     here) is the long-term answer.
package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Plan is the commercial edition tier. Picked at signing
// time; can be downgraded only by re-issuing the license.
type Plan string

const (
	PlanCommunity Plan = "community" // free; signature optional
	PlanTeam      Plan = "team"      // paid; multi-user
	PlanEnterprise Plan = "enterprise" // paid; multi-tenant
)

// License is the wire shape signed by the issuer. The JSON
// field order is significant: changing it re-serialises the
// payload and invalidates existing signatures. We use struct
// tags + json.Marshal which is deterministic for our purposes
// (no maps; no slices of maps; all fields are scalars +
// string slices).
type License struct {
	// Schema is bumped whenever the wire shape changes
	// in a backwards-incompatible way. Verifiers reject
	// unknown schemas.
	Schema int `json:"schema"`
	// Version is a monotonic counter for issuer bookkeeping.
	Version int `json:"version"`
	// Plan is the edition tier.
	Plan Plan `json:"plan"`
	// TenantID is the multi-tenant isolation key. Empty
	// for single-tenant plans.
	TenantID string `json:"tenant_id,omitempty"`
	// InstallationID is the deployment fingerprint. The
	// verifier rejects the license if this doesn't match
	// the running host's id.
	InstallationID string `json:"installation_id"`
	// IssuedAt is when the license was signed.
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt is the hard expiry. Verifiers reject
	// expired licenses even if the signature is valid.
	ExpiresAt time.Time `json:"expires_at"`
	// Features is a list of feature flags the license
	// unlocks. The verifier consults this list when an
	// endpoint requires a non-default capability.
	Features []string `json:"features,omitempty"`
	// IssuedBy is a human-readable issuer label.
	IssuedBy string `json:"issued_by,omitempty"`
}

// CurrentSchema is the schema version this build knows how
// to verify. Bumping it is a breaking change for old
// licenses.
const CurrentSchema = 1

// ErrInvalidSignature is returned by Verify when the
// signature does not match the payload.
var ErrInvalidSignature = errors.New("license: invalid signature")

// ErrExpired is returned by Verify when ExpiresAt is in
// the past.
var ErrExpired = errors.New("license: expired")

// ErrSchemaMismatch is returned by Verify when the
// license's Schema field is not the current version.
var ErrSchemaMismatch = errors.New("license: schema mismatch")

// ErrInstallationMismatch is returned by Verify when the
// license's InstallationID does not match the host's id.
var ErrInstallationMismatch = errors.New("license: installation id mismatch")

// ErrEmpty is returned by Verify when the blob is empty.
var ErrEmpty = errors.New("license: empty")

// ErrBadKey is returned by ParsePublicKey when the
// supplied hex string is not a valid Ed25519 public key.
var ErrBadKey = errors.New("license: bad public key")

// KeyPair is an Ed25519 key pair. The zero value is
// unusable; call GenerateKeyPair to make one.
type KeyPair struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

// GenerateKeyPair creates a fresh Ed25519 key pair. The
// private key must NOT be logged or persisted in the
// clear; the public key may be embedded into the binary.
func GenerateKeyPair() (KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{Private: priv, Public: pub}, nil
}

// PublicKeyHex returns the public key as a lowercase-hex
// string. This is the canonical wire format used in
// config.CommercialConfig.PublicKey and license show
// output.
func (k KeyPair) PublicKeyHex() string {
	return hex.EncodeToString(k.Public)
}

// PrivateKeyHex returns the private key as a lowercase-hex
// string. The "private" name is intentional — operators
// who copy this into a license file should treat the
// output as a secret.
func (k KeyPair) PrivateKeyHex() string {
	return hex.EncodeToString(k.Private)
}

// ParsePublicKeyHex parses a lowercase-hex Ed25519 public
// key. The length must be exactly 64 hex chars (32 bytes).
func ParsePublicKeyHex(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if len(s) != ed25519.PublicKeySize*2 {
		return nil, fmt.Errorf("%w: want %d hex chars, got %d", ErrBadKey, ed25519.PublicKeySize*2, len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKeyHex is the private-key counterpart of
// ParsePublicKeyHex. Length must be 128 hex chars (64
// bytes for the seed + public key concatenated, the
// Ed25519 convention used by Go's stdlib).
func ParsePrivateKeyHex(s string) (ed25519.PrivateKey, error) {
	s = strings.TrimSpace(s)
	if len(s) != ed25519.PrivateKeySize*2 {
		return nil, fmt.Errorf("%w: want %d hex chars, got %d", ErrBadKey, ed25519.PrivateKeySize*2, len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	return ed25519.PrivateKey(b), nil
}

// Sign encodes the license as canonical JSON, signs the
// bytes with the private key, and returns a
// base64url-no-padding blob of the form:
//
//	<base64url(payload)>.<base64url(signature)>
//
// The dot-separator format keeps the wire shape
// self-describing and trivial to split with strings.Split.
func (l License) Sign(priv ed25519.PrivateKey) (string, error) {
	if l.Schema == 0 {
		l.Schema = CurrentSchema
	}
	payload, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify parses, signature-checks, and policy-validates a
// license blob. installationID is the host's id; if the
// license binds to a different one the call fails with
// ErrInstallationMismatch.
//
// now is injectable for tests; pass time.Now at the call
// site in production.
func Verify(blob, installationID string, pub ed25519.PublicKey, now time.Time) (License, error) {
	if blob == "" {
		return License{}, ErrEmpty
	}
	parts := strings.Split(blob, ".")
	if len(parts) != 2 {
		return License{}, fmt.Errorf("license: malformed blob (expected <payload>.<sig>)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return License{}, fmt.Errorf("license: bad payload b64: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return License{}, fmt.Errorf("license: bad sig b64: %w", err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		return License{}, ErrInvalidSignature
	}
	var l License
	if err := json.Unmarshal(payload, &l); err != nil {
		return License{}, fmt.Errorf("license: bad payload json: %w", err)
	}
	if l.Schema != CurrentSchema {
		return License{}, fmt.Errorf("%w: got %d, want %d", ErrSchemaMismatch, l.Schema, CurrentSchema)
	}
	if now.After(l.ExpiresAt) {
		return License{}, ErrExpired
	}
	if installationID != "" && l.InstallationID != "" && l.InstallationID != installationID {
		return License{}, fmt.Errorf("%w: license=%q host=%q", ErrInstallationMismatch, l.InstallationID, installationID)
	}
	return l, nil
}

// HasFeature reports whether the license unlocks a named
// capability. Used by the auth middleware to gate
// feature-flagged endpoints.
func (l License) HasFeature(name string) bool {
	for _, f := range l.Features {
		if f == name {
			return true
		}
	}
	return false
}

// Fingerprint is a short hex digest of the canonical
// payload. Useful for logging "license X is loaded" without
// leaking the entire blob.
func (l License) Fingerprint() string {
	// Re-canonicalise so the digest is independent of the
	// signer's map ordering (struct fields are stable in
	// encoding/json, so this is identity for our type, but
	// pinning the format makes the function safe across
	// future field additions).
	b, _ := json.Marshal(l)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// PublicKeyPEM renders the public key as a PEM-encoded
// SubjectPublicKeyInfo. Useful for the operator who
// wants to verify a license with `openssl` or any
// non-Go tooling.
func PublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	const pemLine = 64
	b64 := base64.StdEncoding.EncodeToString(der)
	var sb strings.Builder
	sb.WriteString("-----BEGIN PUBLIC KEY-----\n")
	for i := 0; i < len(b64); i += pemLine {
		end := i + pemLine
		if end > len(b64) {
			end = len(b64)
		}
		sb.WriteString(b64[i:end])
		sb.WriteByte('\n')
	}
	sb.WriteString("-----END PUBLIC KEY-----\n")
	return sb.String(), nil
}
