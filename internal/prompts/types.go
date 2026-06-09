// Package prompts is the prompt-template management surface for
// P5+ 提示词编辑. It exposes the 35 PromptType catalogue, an
// in-memory + Postgres-backed Store, and a Service that the
// /api/v1/prompts/{type} endpoints sit on top of.
//
// The 35 types are the canonical ptagent-parity set, mirroring
// ptagent's PromptTemplate library (Recon / Classifier /
// Exploit / Report × 5 categories × 2 modes, plus a
// Strict-mode and a few cross-cutting helpers). The catalogue
// is a constant array, not generated, so:
//   - adding a new type is a one-line append + gofmt
//   - the validation in the editor can iterate it cheaply
//   - tests can assert the count exactly (>= 30, <= 40)
package prompts

import (
	"sort"
)

// PromptType is a single canonical prompt-template slot. Each
// type maps to one .tmpl file under internal/agent/prompts/
// templates/ at runtime; if the editor overwrites a type with
// a different name, the system prompt would break for that
// agent role — that's why the editor disables the "name" field
// and only allows editing the body.
type PromptType string

// AllPromptTypes is the canonical, alphabetically-sorted list
// of supported prompt slots. The order here is the order shown
// in the editor sidebar and the order returned by
// Service.List. Keep this sorted.
var AllPromptTypes = []PromptType{
	// Auth category (5)
	PromptAuthExploitStrict,
	PromptAuthExploit,
	PromptAuthReconStrict,
	PromptAuthRecon,
	PromptAuthSystem,

	// API category (5)
	PromptAPIExploitStrict,
	PromptAPIExploit,
	PromptAPIReconStrict,
	PromptAPIRecon,
	PromptAPISystem,

	// Web category (5)
	PromptWebExploitStrict,
	PromptWebExploit,
	PromptWebReconStrict,
	PromptWebRecon,
	PromptWebSystem,

	// Cloud category (5)
	PromptCloudExploitStrict,
	PromptCloudExploit,
	PromptCloudReconStrict,
	PromptCloudRecon,
	PromptCloudSystem,

	// Cross-cutting / role-scope (10)
	PromptClassifierStrict,
	PromptClassifier,
	PromptReportStrict,
	PromptReport,
	PromptTriageStrict,
	PromptTriage,
	PromptOrchestratorStrict,
	PromptOrchestrator,
	PromptSummarizerStrict,
	PromptSummarizer,

	// Validation / meta (5)
	PromptRefusalDetector,
	PromptFinalizeStrict,
	PromptFinalize,
	PromptScopeGuardStrict,
	PromptScopeGuard,

	// Generic aliases (3) — map to web-category defaults for
	// backward compatibility with clients that use bare "recon",
	// "exploit", "report" as type identifiers.
	PromptRecon,
	PromptExploit,
	PromptReportAlias,
}

// Constants — the 35 PromptType slots. Grouped by role for
// readability; the canonical list is the slice above.
const (
	// Auth (5)
	PromptAuthSystem         PromptType = "auth_system"
	PromptAuthRecon          PromptType = "auth_recon"
	PromptAuthReconStrict    PromptType = "auth_recon_strict"
	PromptAuthExploit        PromptType = "auth_exploit"
	PromptAuthExploitStrict  PromptType = "auth_exploit_strict"

	// API (5)
	PromptAPISystem         PromptType = "api_system"
	PromptAPIRecon          PromptType = "api_recon"
	PromptAPIReconStrict    PromptType = "api_recon_strict"
	PromptAPIExploit        PromptType = "api_exploit"
	PromptAPIExploitStrict  PromptType = "api_exploit_strict"

	// Web (5)
	PromptWebSystem         PromptType = "web_system"
	PromptWebRecon          PromptType = "web_recon"
	PromptWebReconStrict    PromptType = "web_recon_strict"
	PromptWebExploit        PromptType = "web_exploit"
	PromptWebExploitStrict  PromptType = "web_exploit_strict"

	// Cloud (5)
	PromptCloudSystem         PromptType = "cloud_system"
	PromptCloudRecon          PromptType = "cloud_recon"
	PromptCloudReconStrict    PromptType = "cloud_recon_strict"
	PromptCloudExploit        PromptType = "cloud_exploit"
	PromptCloudExploitStrict  PromptType = "cloud_exploit_strict"

	// Cross-cutting (10)
	PromptClassifier         PromptType = "classifier"
	PromptClassifierStrict   PromptType = "classifier_strict"
	PromptReport             PromptType = "report"
	PromptReportStrict       PromptType = "report_strict"
	PromptTriage             PromptType = "triage"
	PromptTriageStrict       PromptType = "triage_strict"
	PromptOrchestrator       PromptType = "orchestrator"
	PromptOrchestratorStrict PromptType = "orchestrator_strict"
	PromptSummarizer         PromptType = "summarizer"
	PromptSummarizerStrict   PromptType = "summarizer_strict"

	// Meta (5)
	PromptRefusalDetector  PromptType = "refusal_detector"
	PromptFinalize         PromptType = "finalize"
	PromptFinalizeStrict   PromptType = "finalize_strict"
	PromptScopeGuard       PromptType = "scope_guard"
	PromptScopeGuardStrict PromptType = "scope_guard_strict"

	// Generic aliases (3) — map to web-category defaults
	PromptRecon        PromptType = "recon"
	PromptExploit      PromptType = "exploit"
	PromptReportAlias  PromptType = "report_alias"
)

// PromptTypeCount is the runtime-truth count, exposed for the
// frontend's "X / 38" indicator and tests.
const PromptTypeCount = 38

// Prompt is a single template instance: a body string + the
// metadata the editor needs to render correctly. Variables
// declared via {{ .Variable }} are scanned at save time and
// reported back to the frontend so Monaco's variable-suggest
// box can populate.
type Prompt struct {
	Type        PromptType `json:"type"`
	Body        string     `json:"body"`
	Description string     `json:"description,omitempty"`
	Variables   []string   `json:"variables,omitempty"`
	Version     int        `json:"version"`     // monotonically increments on each save
	UpdatedAt   int64      `json:"updated_at"`  // unix seconds
	UpdatedBy   string     `json:"updated_by"`  // username or "system"
}

// ValidType reports whether t is one of the 35 known types.
// Used by the API handler to reject unknown types with 400.
func ValidType(t PromptType) bool {
	// The slice is sorted; binary search would be O(log n) but
	// the constant is 35, so a linear scan is fine and avoids
	// subtle bugs around ordering invariants.
	for _, p := range AllPromptTypes {
		if p == t {
			return true
		}
	}
	return false
}

// ScanVariables extracts the set of `{{ .Foo }}` variable
// references from a prompt body. Used at save time to populate
// the prompt's Variables slice, and (transitively) to populate
// Monaco's completion list.
//
// This is a *permissive* parser: it returns the names of any
// `.Identifier` token inside an action block, even if the
// surrounding template is malformed. The downstream
// go-template parser will still error on parse — but with the
// variables list pre-populated, the editor can highlight them
// in the UI.
//
// Handles:
//
//   - {{ .Name }}                      → ["Name"]
//   - {{- .Name }} / {{ .Name -}}      → ["Name"]   (whitespace trim)
//   - {{ if .X }} … {{ end }}          → ["X"]      (block-level refs)
//   - {{ range .Items }}{{ .Name }}{{end}} → ["Items","Name"]
//   - {{ .Foo.Bar }}                   → ["Foo"]    (only top-level)
func ScanVariables(body string) []string {
	seen := make(map[string]struct{})
	var out []string
	i := 0
	for i < len(body)-3 {
		if body[i] != '{' || body[i+1] != '{' {
			i++
			continue
		}
		// Find the closing `}}`.
		j := i + 2
		for j < len(body)-1 && !(body[j] == '}' && body[j+1] == '}') {
			j++
		}
		if j >= len(body)-1 {
			break
		}
		inner := body[i+2 : j]
		// Scan every `.Identifier` token in the block, not
		// just the one at the front. This catches both
		// action-level (`{{ .Name }}`) and block-level
		// (`{{ if .X }}…{{ end }}`) usages.
		extractDots(inner, seen, &out)
		i = j + 2
	}
	sort.Strings(out)
	return out
}

// extractDots walks the inner content of a `{{ ... }}` block
// and records each *top-level* `.Identifier` token. A
// "top-level" token is one whose leading `.` is not itself
// preceded by an identifier character — i.e. `.Foo` in
// `.Foo.Bar` is top-level, but `.Bar` is a field of `.Foo`
// and is not recorded.
//
// The `if`/`range`/`with`/`end` keywords are not skipped
// explicitly — they just don't contain a dot, so they're
// naturally ignored.
func extractDots(s string, seen map[string]struct{}, out *[]string) {
	for k := 0; k < len(s)-1; k++ {
		if s[k] != '.' {
			continue
		}
		// Top-level test: a `.` is a top-level access only
		// if the char before it is not an identifier byte
		// (or this is the first char of the block). If the
		// preceding char is alphanum/underscore, then this
		// `.` is a field access on a parent variable.
		if k > 0 && isIdentByte(s[k-1]) {
			continue
		}
		// `.` is followed by an identifier run; consume
		// [A-Za-z_][A-Za-z0-9_]*.
		name := ""
		for m := k + 1; m < len(s) && isIdentByte(s[m]); m++ {
			name += string(s[m])
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			*out = append(*out, name)
		}
	}
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// Description returns a one-line human description for a
// prompt type. Used by the editor's "info" tooltip and by
// the GET /api/v1/prompts endpoint.
func Description(t PromptType) string {
	switch t {
	// Auth
	case PromptAuthSystem:
		return "Auth-category agent system prompt — used by auth_recon / auth_exploit."
	case PromptAuthRecon, PromptAuthReconStrict:
		return "Auth recon: enumerate login forms, OAuth endpoints, JWT parsing, password-reset flows."
	case PromptAuthExploit, PromptAuthExploitStrict:
		return "Auth exploit: IDOR, JWT confusion, OAuth state-bypass, 2FA bypass, password-reset poisoning."
	// API
	case PromptAPISystem:
		return "API-category system prompt — used by api_recon / api_exploit."
	case PromptAPIRecon, PromptAPIReconStrict:
		return "API recon: introspect GraphQL, enumerate REST verbs, follow .well-known/openapi.json."
	case PromptAPIExploit, PromptAPIExploitStrict:
		return "API exploit: BOLA, BOPLA, broken auth, mass assignment, rate-limit bypass."
	// Web
	case PromptWebSystem:
		return "Web-category system prompt — used by web_recon / web_exploit."
	case PromptWebRecon, PromptWebReconStrict:
		return "Web recon: fingerprint stack, follow sitemap, enumerate endpoints from JS bundles."
	case PromptWebExploit, PromptWebExploitStrict:
		return "Web exploit: XSS, SSRF, deserialization, smuggling, prototype pollution."
	// Cloud
	case PromptCloudSystem:
		return "Cloud-category system prompt — used by cloud_recon / cloud_exploit."
	case PromptCloudRecon, PromptCloudReconStrict:
		return "Cloud recon: enumerate S3 / GCS / Azure blobs, scan for metadata endpoints."
	case PromptCloudExploit, PromptCloudExploitStrict:
		return "Cloud exploit: SSRF → 169.254.169.254, KMS, IAM enumeration, lambda invocation."
	// Cross-cutting
	case PromptClassifier, PromptClassifierStrict:
		return "Classifier: take a raw finding and assign OWASP class + severity + CVSS."
	case PromptReport, PromptReportStrict:
		return "Report: render a polished Markdown finding from a ClassifiedFinding + indicator data."
	case PromptTriage, PromptTriageStrict:
		return "Triage: decide which agent role should pick up the next blackboard item."
	case PromptOrchestrator, PromptOrchestratorStrict:
		return "Orchestrator: per-step agent selection, scope guard, and pheromone decay."
	case PromptSummarizer, PromptSummarizerStrict:
		return "Summarizer: compress agent context to fit the model's context window."
	// Meta
	case PromptRefusalDetector:
		return "Refusal detector: classify a model response as PROCEED / REFUSAL / UNCLEAR."
	case PromptFinalize, PromptFinalizeStrict:
		return "Finalize: wrap a finding payload with all required audit metadata."
	case PromptScopeGuard, PromptScopeGuardStrict:
		return "Scope guard: validate a candidate target against the engagement scope manifest."
	// Generic aliases
	case PromptRecon:
		return "Generic recon prompt — alias for web_recon."
	case PromptExploit:
		return "Generic exploit prompt — alias for web_exploit."
	case PromptReportAlias:
		return "Generic report prompt — alias for report."
	}
	return ""
}
