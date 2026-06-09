// Package mobile provides an LLM-powered mobile application security analysis
// agent that covers Android (APK) and iOS (IPA) static and dynamic analysis.
package mobile

import (
	"context"
	"fmt"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
)

// Agent analyses mobile applications (APK/IPA) for security vulnerabilities
// using LLM reasoning augmented with community mobile-security skills.
type Agent struct {
	provider      llm.Provider
	skillInjector *skills.Injector
}

// New creates a mobile-security agent backed by the given LLM provider.
func New(provider llm.Provider) *Agent {
	return &Agent{provider: provider}
}

// WithSkillInjector enables community skill recommendations in the system
// prompt for more comprehensive mobile security analysis.
func (a *Agent) WithSkillInjector(inj *skills.Injector) {
	a.skillInjector = inj
}

// AnalysisResult holds the structured output of a mobile app security review.
type AnalysisResult struct {
	AppName           string            `json:"app_name"`
	PackageName       string            `json:"package_name"`
	Platform          string            `json:"platform"`
	Permissions       []PermissionRisk  `json:"permissions"`
	ExportedComponents []ExportedComponent `json:"exported_components"`
	Secrets           []SecretFinding   `json:"secrets"`
	NetworkIssues     []NetworkIssue    `json:"network_issues"`
	StorageIssues     []StorageIssue    `json:"storage_issues"`
	CryptoIssues      []CryptoIssue     `json:"crypto_issues"`
	Protections       []ProtectionGap   `json:"protections"`
	RiskLevel         string            `json:"risk_level"`
	Summary           string            `json:"summary"`
}

// PermissionRisk describes a dangerous or misused permission.
type PermissionRisk struct {
	Permission string `json:"permission"`
	Risk       string `json:"risk"`
	Justification string `json:"justification,omitempty"`
}

// ExportedComponent describes an exported Android component or iOS URL scheme.
type ExportedComponent struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	IntentFilters []string `json:"intent_filters,omitempty"`
	Risk          string `json:"risk"`
}

// SecretFinding describes a hardcoded secret found in the app.
type SecretFinding struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Location string `json:"location"`
	Severity string `json:"severity"`
}

// NetworkIssue describes a network-related security concern.
type NetworkIssue struct {
	Issue       string `json:"issue"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// StorageIssue describes insecure local data storage.
type StorageIssue struct {
	Location    string `json:"location"`
	DataType    string `json:"data_type"`
	Encryption  string `json:"encryption"`
	Risk        string `json:"risk"`
}

// CryptoIssue describes weak cryptographic practices.
type CryptoIssue struct {
	Algorithm   string `json:"algorithm"`
	Usage       string `json:"usage"`
	Risk        string `json:"risk"`
}

// ProtectionGap describes a missing or bypassable runtime protection.
type ProtectionGap struct {
	Control string `json:"control"`
	Status  string `json:"status"`
	Bypass  string `json:"bypass,omitempty"`
}

// Analyze performs LLM-based mobile security analysis on the provided
// static/dynamic analysis tool output.
func (a *Agent) Analyze(ctx context.Context, appName string, toolOutput string) (*AnalysisResult, error) {
	req := llm.CompletionRequest{
		SystemPrompt: a.systemPrompt(appName),
		Messages: []llm.Message{
			{
				Role: "user",
				Content: fmt.Sprintf(
					"Analyze the following mobile app security tool output for %q:\n\n%s\n\nProduce a structured security analysis in JSON format.",
					appName, toolOutput,
				),
			},
		},
		MaxTokens:         8192,
		Temperature:       0.1,
		CacheSystemPrompt: true,
	}

	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mobile security analysis failed: %w", err)
	}

	var result AnalysisResult
	if err := parseJSON(resp.Content, &result); err != nil {
		return nil, fmt.Errorf("parse analysis result: %w", err)
	}
	return &result, nil
}

// systemPrompt returns the base system prompt optionally enriched with
// community mobile-security skill recommendations.
func (a *Agent) systemPrompt(app string) string {
	prompt := mobileSystemPrompt
	if a.skillInjector == nil {
		return prompt
	}
	recs := a.skillInjector.ForObjective(app)
	if len(recs) == 0 {
		return prompt
	}
	return prompt + "\n" + skills.MustInject(recs)
}

const mobileSystemPrompt = `You are an expert mobile application security analyst. Analyse Android (APK) and iOS (IPA) application output from tools like MobSF, APKTool, jadx, objection, Frida, and Objection to produce a structured security assessment.

## Analysis Framework

### 1. Permission Analysis (Android)
- Flag dangerous permissions: CAMERA, RECORD_AUDIO, READ_CONTACTS, READ_SMS, ACCESS_FINE_LOCATION, READ_EXTERNAL_STORAGE
- Check for signatureOrSystem permissions that should not be present
- Flag custom permissions with protectionLevel="normal" that guard sensitive actions

### 2. Exported Components
- Activities, Services, BroadcastReceivers, ContentProviders with exported=true and no permission
- Components handling Intents without input validation
- ContentProviders with read/write permissions missing
- iOS: URL schemes, app extensions, shared containers, App Groups

### 3. Hardcoded Secrets
- API keys, OAuth tokens, AWS/GCP/Azure credentials
- HMAC secrets, JWT signing keys, encryption keys
- Internal hostnames, staging URLs, developer credentials
- Firebase / Google Services JSON with exposed project IDs

### 4. Network Security
- Cleartext HTTP traffic (android:usesCleartextTraffic, NSAppTransportSecurity)
- Certificate pinning absence or weak implementations
- Self-signed certificates or custom TrustManagers
- WebView JavaScript enabled with file access (setJavaScriptEnabled + setAllowFileAccess)

### 5. Local Storage
- SharedPreferences in MODE_WORLD_READABLE / MODE_WORLD_WRITEABLE
- SQLite databases without encryption
- Sensitive data in WebView localStorage / cookies
- iOS: NSUserDefaults, plist files in Documents directory, Realm databases

### 6. Cryptography
- Hardcoded IVs, static salts, predictable PRNG seeds (java.util.Random)
- ECB mode encryption, DES/3DES usage, MD5/SHA1 for password hashing
- Keys derived from device identifiers (IMEI, Android ID, UDID)
- Custom crypto implementations (avoid — flag them)

### 7. Runtime Protections
- Root/jailbreak detection: presence, bypass difficulty
- Emulator detection: build.prop checks, QEMU pipe detection
- Certificate pinning: TrustKit (iOS), OkHttp CertificatePinner (Android)
- Anti-debugging: ptrace checks, isDebuggerConnected(), DYLD_INSERT_LIBRARIES
- Code obfuscation: ProGuard/R8 mappings, Swift name mangling, string encryption

## Output Format
Respond ONLY with a valid JSON object:
{
  "app_name": "string",
  "package_name": "string",
  "platform": "android|ios",
  "permissions": [{"permission": "string", "risk": "string", "justification": "string"}],
  "exported_components": [{"name": "string", "type": "activity|service|receiver|provider|url_scheme", "intent_filters": ["string"], "risk": "string"}],
  "secrets": [{"type": "api_key|token|password|certificate", "value": "REDACTED", "location": "string", "severity": "LOW|MEDIUM|HIGH|CRITICAL"}],
  "network_issues": [{"issue": "string", "description": "string", "severity": "LOW|MEDIUM|HIGH|CRITICAL"}],
  "storage_issues": [{"location": "string", "data_type": "string", "encryption": "none|partial|full", "risk": "string"}],
  "crypto_issues": [{"algorithm": "string", "usage": "string", "risk": "string"}],
  "protections": [{"control": "string", "status": "present|absent|bypassable", "bypass": "string"}],
  "risk_level": "LOW|MEDIUM|HIGH|CRITICAL",
  "summary": "one-paragraph executive summary"
}

## Anti-patterns
- Never include actual secret values — replace with "REDACTED"
- Do not flag permissions the app legitimately needs for its core functionality without justification
- Platform-specific checks: do not report Android-only issues on iOS output`