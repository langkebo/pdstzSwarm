package cli

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/license"
)

func TestBootstrapLicense_EmptyIsDemoBuild(t *testing.T) {
	var buf bytes.Buffer
	if err := bootstrapLicense(config.CommercialConfig{}, &buf); err != nil {
		t.Fatalf("empty license should be OK: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("OSS / demo build")) {
		t.Errorf("expected demo-build notice, got %q", buf.String())
	}
}

func TestBootstrapLicense_Valid(t *testing.T) {
	// Generate a key pair, sign a license, point
	// bootstrapLicense at the public key.
	kp, _ := license.GenerateKeyPair()
	l := license.License{
		Schema:         license.CurrentSchema,
		Plan:           license.PlanTeam,
		InstallationID: "", // empty binding: matches any host
		IssuedAt:       time.Now().UTC(),
		ExpiresAt:      time.Now().Add(24 * time.Hour).UTC(),
		Features:       []string{"multi_tenant"},
	}
	blob, _ := l.Sign(kp.Private)
	// Persist the public key to a tmp file the resolver
	// will pick up.
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "license.pub.hex")
	if err := os.WriteFile(pubPath, []byte(kp.PublicKeyHex()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Override HOME for installIDFromCache (uses ~/.pentestswarm).
	t.Setenv("HOME", dir)
	t.Setenv("PENTESTSWARM_LICENSE_PUB", pubPath)

	var buf bytes.Buffer
	err := bootstrapLicense(config.CommercialConfig{LicenseKey: blob}, &buf)
	if err != nil {
		t.Fatalf("bootstrapLicense: %v (log: %s)", err, buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("plan=team")) {
		t.Errorf("expected plan=team in log, got %q", buf.String())
	}
}

func TestBootstrapLicense_BadSignature(t *testing.T) {
	kp, _ := license.GenerateKeyPair()
	other, _ := license.GenerateKeyPair()
	l := license.License{
		Schema:         license.CurrentSchema,
		InstallationID: "host-abc",
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	blob, _ := l.Sign(kp.Private)
	// Use OTHER's public key → verification fails.
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "license.pub.hex")
	if err := os.WriteFile(pubPath, []byte(other.PublicKeyHex()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("PENTESTSWARM_LICENSE_PUB", pubPath)

	var buf bytes.Buffer
	if err := bootstrapLicense(config.CommercialConfig{LicenseKey: blob}, &buf); err == nil {
		t.Errorf("bad signature should error")
	}
}

func TestBootstrapLicense_NoKey(t *testing.T) {
	// License is set but no public key is reachable
	// (no env, no file, no config).
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PENTESTSWARM_LICENSE_PUB", "")
	var buf bytes.Buffer
	err := bootstrapLicense(config.CommercialConfig{LicenseKey: "junk.junk"}, &buf)
	if err == nil {
		t.Errorf("missing public key should error")
	}
}

func TestTrim(t *testing.T) {
	cases := map[string]string{
		"hello\n":    "hello",
		"hello\r\n":  "hello",
		"hello  ":    "hello",
		"hello":      "hello",
		"":           "",
		"\n\n\n":     "",
	}
	for in, want := range cases {
		if got := trim(in); got != want {
			t.Errorf("trim(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadIssuerPrivateKey_BothEmpty(t *testing.T) {
	licIssuerKeyHex = ""
	licIssuerKeyFile = ""
	if _, err := loadIssuerPrivateKey(); err == nil {
		t.Errorf("expected error when no key supplied")
	}
}

func TestLoadIssuerPrivateKey_FromFile(t *testing.T) {
	kp, _ := license.GenerateKeyPair()
	path := filepath.Join(t.TempDir(), "priv.hex")
	if err := os.WriteFile(path, []byte(kp.PrivateKeyHex()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	licIssuerKeyHex = ""
	licIssuerKeyFile = path
	defer func() { licIssuerKeyFile = "" }()
	got, err := loadIssuerPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(kp.Private) {
		t.Errorf("loaded private key mismatch")
	}
}

func TestLoadVerifierKey_FromFile(t *testing.T) {
	kp, _ := license.GenerateKeyPair()
	path := filepath.Join(t.TempDir(), "pub.hex")
	if err := os.WriteFile(path, []byte(kp.PublicKeyHex()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	licVerifyKeyHex = ""
	licVerifyKeyFile = path
	defer func() { licVerifyKeyFile = "" }()
	got, err := loadVerifierKey()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(kp.Public) {
		t.Errorf("loaded public key mismatch")
	}
}

func TestSplitLicenseBlob(t *testing.T) {
	payload, sig, err := splitLicenseBlob("aaa.bbb")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "aaa" || string(sig) != "bbb" {
		t.Errorf("split mismatch: %q / %q", payload, sig)
	}
	if _, _, err := splitLicenseBlob("nodot"); err == nil {
		t.Errorf("expected error for missing separator")
	}
}

// Sanity: make sure ed25519.PublicKey is what we think it
// is. The cli test only uses Equal().
var _ ed25519.PublicKey = ed25519.PublicKey{}
