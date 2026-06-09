package cli

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/license"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// licenseCmd is the parent for the license subcommands
// (generate / keygen / show / verify). Mounted under the
// top-level "pentestswarm" CLI alongside "serve", "mcp",
// "login", etc.
var licenseCmd = &cobra.Command{
	Use:   "license",
	Short: "Manage commercial licenses (P5+ commercial)",
	Long: `Generate, inspect, and verify Ed25519-signed commercial
licenses. The license is a signed JSON blob that the server
consults on startup; the issuing party's private key is held
externally and never shipped with the binary.`,
}

var licenseGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Sign a new license (requires issuer private key)",
	RunE:  runLicenseGenerate,
}

var licenseKeygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "Generate an Ed25519 key pair for license signing",
	RunE:  runLicenseKeygen,
}

var licenseShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the active license summary",
	RunE:  runLicenseShow,
}

var licenseVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify a license against a public key",
	RunE:  runLicenseVerify,
}

var (
	licPlan          string
	licDays          int
	licTenant        string
	licInstall       string
	licFeatures      []string
	licIssuer        string
	licIssuerKeyHex  string
	licIssuerKeyFile string
	licOutput        string

	licKeyOut     string
	licKeyOutPriv string

	licShowFile string

	licVerifyKeyHex  string
	licVerifyKeyFile string
	licVerifyFile    string
	licVerifyInstall string
)

func init() {
	licenseGenerateCmd.Flags().StringVar(&licPlan, "plan", "team", "license plan: community, team, enterprise")
	licenseGenerateCmd.Flags().IntVar(&licDays, "days", 365, "validity in days from now")
	licenseGenerateCmd.Flags().StringVar(&licTenant, "tenant", "", "tenant id (multi-tenant plans)")
	licenseGenerateCmd.Flags().StringVar(&licInstall, "installation", "", "installation id (random uuid if empty)")
	licenseGenerateCmd.Flags().StringSliceVar(&licFeatures, "feature", nil, "feature to unlock (repeatable)")
	licenseGenerateCmd.Flags().StringVar(&licIssuer, "issuer", "", "issuer label (e.g. acme-licensing)")
	licenseGenerateCmd.Flags().StringVar(&licIssuerKeyHex, "private-key-hex", "", "Ed25519 private key (hex)")
	licenseGenerateCmd.Flags().StringVar(&licIssuerKeyFile, "private-key-file", "", "read private key (hex) from this file")
	licenseGenerateCmd.Flags().StringVarP(&licOutput, "output", "o", "", "write blob to file (default: stdout)")

	licenseKeygenCmd.Flags().StringVar(&licKeyOut, "public-out", "license.pub.hex", "output file for the public key (hex)")
	licenseKeygenCmd.Flags().StringVar(&licKeyOutPriv, "private-out", "license.priv.hex", "output file for the private key (hex)")

	licenseShowCmd.Flags().StringVar(&licShowFile, "file", "", "license file to read (default: $PENTESTSWARM_LICENSE_FILE or installation cache)")

	licenseVerifyCmd.Flags().StringVar(&licVerifyKeyHex, "public-key-hex", "", "Ed25519 public key (hex)")
	licenseVerifyCmd.Flags().StringVar(&licVerifyKeyFile, "public-key-file", "", "file with hex public key")
	licenseVerifyCmd.Flags().StringVar(&licVerifyFile, "file", "", "license file (default: stdin)")
	licenseVerifyCmd.Flags().StringVar(&licVerifyInstall, "installation", "", "expected installation id (default: skip check)")

	licenseCmd.AddCommand(licenseGenerateCmd)
	licenseCmd.AddCommand(licenseKeygenCmd)
	licenseCmd.AddCommand(licenseShowCmd)
	licenseCmd.AddCommand(licenseVerifyCmd)

	rootCmd.AddCommand(licenseCmd)
}

func runLicenseGenerate(cmd *cobra.Command, args []string) error {
	priv, err := loadIssuerPrivateKey()
	if err != nil {
		return err
	}
	install := licInstall
	if install == "" {
		install = uuid.NewString()
	}
	l := license.License{
		Schema:         license.CurrentSchema,
		Version:        1,
		Plan:           license.Plan(licPlan),
		TenantID:       licTenant,
		InstallationID: install,
		IssuedAt:       time.Now().UTC(),
		ExpiresAt:      time.Now().Add(time.Duration(licDays) * 24 * time.Hour).UTC(),
		Features:       licFeatures,
		IssuedBy:       licIssuer,
	}
	blob, err := l.Sign(priv)
	if err != nil {
		return err
	}
	if licOutput != "" {
		if err := os.WriteFile(licOutput, []byte(blob+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "license written to %s\n", licOutput)
	} else {
		fmt.Println(blob)
	}
	return nil
}

// loadIssuerPrivateKey resolves the private key from one of
// the two flags, with the explicit hex value winning.
func loadIssuerPrivateKey() (ed25519.PrivateKey, error) {
	if licIssuerKeyHex != "" {
		return license.ParsePrivateKeyHex(licIssuerKeyHex)
	}
	if licIssuerKeyFile != "" {
		b, err := os.ReadFile(licIssuerKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read private key file: %w", err)
		}
		return license.ParsePrivateKeyHex(trim(string(b)))
	}
	return nil, fmt.Errorf("license generate: --private-key-hex or --private-key-file is required")
}

func runLicenseKeygen(cmd *cobra.Command, args []string) error {
	kp, err := license.GenerateKeyPair()
	if err != nil {
		return err
	}
	if err := os.WriteFile(licKeyOut, []byte(kp.PublicKeyHex()+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(licKeyOutPriv, []byte(kp.PrivateKeyHex()+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "public key  -> %s (chmod 0644)\n", licKeyOut)
	fmt.Fprintf(os.Stderr, "private key -> %s (chmod 0600, keep secret)\n", licKeyOutPriv)
	return nil
}

func runLicenseShow(cmd *cobra.Command, args []string) error {
	blob, err := readLicenseForShow(licShowFile)
	if err != nil {
		return err
	}
	if blob == "" {
		fmt.Fprintln(os.Stderr, "no license configured (OSS / demo build)")
		return nil
	}
	// We don't have the public key here; show the raw
	// blob's parsed payload without verifying. The
	// `verify` subcommand does the cryptographic check.
	payload, _, err := splitLicenseBlob(blob)
	if err != nil {
		return err
	}
	var l license.License
	if err := json.Unmarshal(payload, &l); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(l)
}

func runLicenseVerify(cmd *cobra.Command, args []string) error {
	pub, err := loadVerifierKey()
	if err != nil {
		return err
	}
	blob, err := readLicenseForVerify(licVerifyFile)
	if err != nil {
		return err
	}
	l, err := license.Verify(blob, licVerifyInstall, pub, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "VERIFY FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "OK: plan=%s tenant=%s install=%s expires=%s features=%v\n",
		l.Plan, l.TenantID, l.InstallationID, l.ExpiresAt.Format(time.RFC3339), l.Features)
	return nil
}

func loadVerifierKey() (ed25519.PublicKey, error) {
	if licVerifyKeyHex != "" {
		return license.ParsePublicKeyHex(licVerifyKeyHex)
	}
	if licVerifyKeyFile != "" {
		b, err := os.ReadFile(licVerifyKeyFile)
		if err != nil {
			return nil, err
		}
		return license.ParsePublicKeyHex(trim(string(b)))
	}
	return nil, fmt.Errorf("verify: --public-key-hex or --public-key-file is required")
}

// readLicenseForShow picks the source path with the
// following precedence: --file, $PENTESTSWARM_LICENSE_FILE,
// then the on-disk cache (next to the installation id
// file). Returns "" + nil when no source exists, so the
// caller can print the "no license" message and exit 0.
func readLicenseForShow(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return trim(string(b)), nil
	}
	if env := os.Getenv("PENTESTSWARM_LICENSE_FILE"); env != "" {
		b, err := os.ReadFile(env)
		if err != nil {
			return "", err
		}
		return trim(string(b)), nil
	}
	cache, err := defaultInstallationCachePath()
	if err != nil || cache == "" {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(cache), "license"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return trim(string(b)), nil
}

func readLicenseForVerify(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return trim(string(b)), nil
	}
	var buf [4096]byte
	n, _ := os.Stdin.Read(buf[:])
	return trim(string(buf[:n])), nil
}

func splitLicenseBlob(blob string) (payload []byte, sig []byte, err error) {
	for i := 0; i < len(blob); i++ {
		if blob[i] == '.' {
			return []byte(blob[:i]), []byte(blob[i+1:]), nil
		}
	}
	return nil, nil, fmt.Errorf("license: malformed blob (no '.' separator)")
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// defaultInstallationCachePath returns the path of the
// installation id cache, or "" if the home directory is
// unknown. The directory is created on first use.
func defaultInstallationCachePath() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, ".pentestswarm", "installation_id")
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", err
	}
	return full, nil
}

// installIDFromCache returns the persisted installation id,
// generating and persisting a fresh one if the cache is
// missing. The id is the lowercase hex of the SHA-256 of a
// uuid v4; this gives us a stable, opaque-looking id with
// negligible collision probability while making the cache
// file itself short. We hash the uuid (rather than storing
// the raw uuid) so a file-leak doesn't immediately expose
// the live id — a minor defence in depth on top of the
// Ed25519 binding.
func installIDFromCache() (string, error) {
	path, err := defaultInstallationCachePath()
	if err != nil {
		return "", err
	}
	if b, err := os.ReadFile(path); err == nil {
		s := trim(string(b))
		if s != "" {
			return s, nil
		}
	}
	id := uuid.NewString()
	h := sha256.Sum256([]byte(id))
	encoded := hex.EncodeToString(h[:])
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return "", err
	}
	return encoded, nil
}

// bootstrapLicense is the boot-time gate. It is intentionally
// non-fatal for the empty-license case (OSS / demo build,
// matches ptagent's "no license" path). When LicenseKey is
// set we:
//  1. Resolve the public key (config > demo key)
//  2. Read or generate the per-host installation id
//  3. Verify the license blob against the public key + the
//     installation id + the current time
//  4. Print a one-line status to stderr so the operator
//     can confirm the deployment is commercial-enabled
//
// On any verification failure we return a non-nil error so
// the cmd exits non-zero; running with an invalid license
// would silently downgrade features and create support
// headaches.
func bootstrapLicense(cfg config.CommercialConfig, w io.Writer) error {
	if cfg.LicenseKey == "" {
		fmt.Fprintln(w, "license: none (OSS / demo build)")
		return nil
	}
	pub, err := resolvePublicKey(cfg.PublicKey)
	if err != nil {
		return err
	}
	installID, err := installIDFromCache()
	if err != nil {
		return fmt.Errorf("install id: %w", err)
	}
	l, err := license.Verify(cfg.LicenseKey, installID, pub, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "license: plan=%s tenant=%s install=%s expires=%s features=%v fingerprint=%s\n",
		l.Plan, l.TenantID, l.InstallationID, l.ExpiresAt.Format(time.RFC3339), l.Features, l.Fingerprint())
	return nil
}

// resolvePublicKey picks the public key in priority order:
//  1. cfg.PublicKey (operator-supplied)
//  2. The on-disk key file at $PENTESTSWARM_LICENSE_PUB or
//     ~/.pentestswarm/license.pub.hex
//  3. The built-in demo public key (allows a fresh
//     checkout to verify a freshly generated license)
//
// Anything else is an error.
func resolvePublicKey(hexKey string) (ed25519.PublicKey, error) {
	if hexKey != "" {
		return license.ParsePublicKeyHex(hexKey)
	}
	if env := os.Getenv("PENTESTSWARM_LICENSE_PUB"); env != "" {
		if b, err := os.ReadFile(env); err == nil {
			return license.ParsePublicKeyHex(trim(string(b)))
		}
	}
	dir, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(dir, ".pentestswarm", "license.pub.hex")
		if b, err := os.ReadFile(path); err == nil {
			if pub, perr := license.ParsePublicKeyHex(trim(string(b))); perr == nil {
				return pub, nil
			}
		}
	}
	return nil, fmt.Errorf("license: no public key configured (set commercial.public_key, $PENTESTSWARM_LICENSE_PUB, or ~/.pentestswarm/license.pub.hex)")
}
