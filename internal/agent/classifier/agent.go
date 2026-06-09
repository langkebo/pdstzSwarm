package classifier

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
	"github.com/google/uuid"
)

// ClassifierAgent enriches raw findings with CVE mappings, CVSS scores, and severity.
type ClassifierAgent struct {
	provider      llm.Provider
	fpFilter      *FPFilter
	strict        bool
	onErr         func(error)
	skillInjector *skills.Injector
}

// Option customises ClassifierAgent construction.
type Option func(*ClassifierAgent)

// WithStrict makes any LLM error fatal (Classify returns the error instead
// of silently falling back to heuristic classification).
func WithStrict() Option {
	return func(c *ClassifierAgent) { c.strict = true }
}

// WithErrorSink installs a callback invoked on LLM / parse errors. Useful
// for surfacing degraded-mode warnings to the event stream.
func WithErrorSink(fn func(error)) Option {
	return func(c *ClassifierAgent) { c.onErr = fn }
}

// WithSkillInjector enables community skill recommendations in the
// system prompt for more accurate classification.
func WithSkillInjector(inj *skills.Injector) Option {
	return func(c *ClassifierAgent) { c.skillInjector = inj }
}

// NewClassifierAgent creates a new classifier agent.
func NewClassifierAgent(provider llm.Provider, opts ...Option) *ClassifierAgent {
	c := &ClassifierAgent{
		provider: provider,
		fpFilter: NewFPFilter(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Classify takes raw findings and produces classified, scored, ranked findings.
func (c *ClassifierAgent) Classify(ctx context.Context, campaignID uuid.UUID, rawFindings []pipeline.RawFinding) (*pipeline.ClassifiedFindingSet, error) {
	var classified []pipeline.ClassifiedFinding
	filteredCount := 0

	for _, raw := range rawFindings {
		// Check false positive probability
		if c.fpFilter.ShouldFilter(raw) {
			filteredCount++
			continue
		}

		fpProb := c.fpFilter.Score(raw)

		// Infer attack category from raw finding type/detail when LLM fails
		attackCat := inferAttackCategory(raw.Type, raw.Detail)
		severity := inferSeverity(raw.Type, raw.Detail)

		classified = append(classified, pipeline.ClassifiedFinding{
			ID:                       uuid.New(),
			RawFindingID:             raw.ID,
			CampaignID:               campaignID,
			Title:                    raw.Detail,
			Description:              raw.Detail,
			Severity:                 severity,
			AttackCategory:           attackCat,
			Confidence:               pipeline.ConfidenceUnverified,
			FalsePositiveProbability: fpProb,
			Target:                   raw.Target,
			ClassifiedAt:             time.Now(),
		})
	}

	// Batch classify with LLM (groups of 20)
	batchSize := 20
	for i := 0; i < len(classified); i += batchSize {
		end := i + batchSize
		if end > len(classified) {
			end = len(classified)
		}

		batch := classified[i:end]
		enriched, err := c.classifyBatch(ctx, batch)
		if err != nil {
			if c.strict {
				return nil, fmt.Errorf("classifier batch %d-%d: %w", i, end, err)
			}
			if c.onErr != nil {
				c.onErr(fmt.Errorf("classifier degraded for batch %d-%d: %w", i, end, err))
			}
			// Heuristic classification stands; continue with next batch.
			continue
		}

		// Merge LLM results back
		for j, e := range enriched {
			if i+j < len(classified) {
				classified[i+j].Title = e.Title
				classified[i+j].Description = e.Description
				classified[i+j].CVEIDs = e.CVEIDs
				classified[i+j].CVSSScore = e.CVSSScore
				classified[i+j].CVSSVector = e.CVSSVector
				classified[i+j].Severity = e.Severity
				classified[i+j].AttackCategory = e.AttackCategory
				classified[i+j].Confidence = e.Confidence
				classified[i+j].ChainCandidates = e.ChainCandidates
			}
		}
	}

	// Sort by CVSS score descending
	sort.Slice(classified, func(i, j int) bool {
		return classified[i].CVSSScore > classified[j].CVSSScore
	})

	// Build summary
	bySeverity := make(map[pipeline.Severity]int)
	categoryCount := make(map[string]int)
	for _, f := range classified {
		bySeverity[f.Severity]++
		if f.AttackCategory != "" {
			categoryCount[f.AttackCategory]++
		}
	}

	var topCategories []string
	for cat := range categoryCount {
		topCategories = append(topCategories, cat)
	}
	sort.Slice(topCategories, func(i, j int) bool {
		return categoryCount[topCategories[i]] > categoryCount[topCategories[j]]
	})
	if len(topCategories) > 5 {
		topCategories = topCategories[:5]
	}

	return &pipeline.ClassifiedFindingSet{
		CampaignID: campaignID,
		Findings:   classified,
		Summary: pipeline.ClassificationSummary{
			TotalFindings: len(classified),
			BySeverity:    bySeverity,
			TopCategories: topCategories,
			FilteredAsFP:  filteredCount,
		},
		CreatedAt: time.Now(),
	}, nil
}

// classifierTool is the structured tool schema we ask the LLM to populate.
// Providers that advertise SupportsToolUse() get this path; others fall
// back to the legacy JSON-in-prompt path below.
var classifierTool = llm.Tool{
	Name:        "emit_classified_findings",
	Description: "Emit enriched classifications for each input finding in the same order.",
	Parameters: json.RawMessage(`{
		"type": "object",
		"properties": {
			"findings": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"input_index":      { "type": "integer", "description": "0-based index matching the input array order" },
						"title":            { "type": "string" },
						"description":      { "type": "string" },
						"cve_ids":          { "type": "array", "items": { "type": "string" } },
						"cvss_score":       { "type": "number" },
						"cvss_vector":      { "type": "string" },
						"severity":         { "type": "string", "enum": ["critical","high","medium","low","informational"] },
						"attack_category":  { "type": "string" },
						"confidence":       { "type": "string", "enum": ["high","medium","low","unverified"] }
					},
					"required": ["input_index","title","severity","cvss_score","confidence"]
				}
			}
		},
		"required": ["findings"]
	}`),
}

type classifierToolOutput struct {
	Findings []struct {
		InputIndex     int      `json:"input_index"`
		Title          string   `json:"title"`
		Description    string   `json:"description"`
		CVEIDs         []string `json:"cve_ids"`
		CVSSScore      float64  `json:"cvss_score"`
		CVSSVector     string   `json:"cvss_vector"`
		Severity       string   `json:"severity"`
		AttackCategory string   `json:"attack_category"`
		Confidence     string   `json:"confidence"`
	} `json:"findings"`
}

// classifyBatch sends a batch of findings to the LLM for enrichment.
// When the provider supports tool-use (Claude), we use structured tool
// calls so the response is guaranteed-parseable. Otherwise we fall back
// to JSON-in-prompt.
func (c *ClassifierAgent) classifyBatch(ctx context.Context, findings []pipeline.ClassifiedFinding) ([]pipeline.ClassifiedFinding, error) {
	findingsJSON, _ := json.Marshal(findings)
	userMsg := fmt.Sprintf(
		"Classify and enrich these security findings. Call the emit_classified_findings tool with one entry per input in the same order (input_index starts at 0).\n\n%s",
		string(findingsJSON),
	)

	req := llm.CompletionRequest{
		SystemPrompt:      c.systemPrompt(),
		Messages:          []llm.Message{{Role: "user", Content: userMsg}},
		MaxTokens:         8192,
		Temperature:       0.1,
		CacheSystemPrompt: true,
	}
	if c.provider.SupportsToolUse() {
		req.Tools = []llm.Tool{classifierTool}
	}

	resp, err := c.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("classifier LLM call failed: %w", err)
	}

	// Structured path: prefer tool calls if present.
	if len(resp.ToolCalls) > 0 && resp.ToolCalls[0].Name == classifierTool.Name {
		args := resp.ToolCalls[0].Arguments
		// Repair truncated JSON if needed
		if len(args) > 0 && args[len(args)-1] != '}' && args[len(args)-1] != ']' {
			args = args + "]}"
		}
		var out classifierToolOutput
		if err := json.Unmarshal([]byte(args), &out); err != nil {
			return nil, fmt.Errorf("parsing classifier tool args: %w (raw: %.200s)", err, args)
		}
		enriched := make([]pipeline.ClassifiedFinding, len(findings))
		copy(enriched, findings)
		for _, f := range out.Findings {
			if f.InputIndex < 0 || f.InputIndex >= len(enriched) {
				continue
			}
			enriched[f.InputIndex].Title = f.Title
			enriched[f.InputIndex].Description = f.Description
			enriched[f.InputIndex].CVEIDs = f.CVEIDs
			enriched[f.InputIndex].CVSSScore = f.CVSSScore
			enriched[f.InputIndex].CVSSVector = f.CVSSVector
			enriched[f.InputIndex].Severity = pipeline.Severity(f.Severity)
			enriched[f.InputIndex].AttackCategory = f.AttackCategory
			enriched[f.InputIndex].Confidence = pipeline.Confidence(f.Confidence)
		}
		return enriched, nil
	}

	// Fallback: parse JSON out of the text response.
	content := stripCodeFence(resp.Content)
	var enriched []pipeline.ClassifiedFinding
	if err := json.Unmarshal([]byte(content), &enriched); err != nil {
		return nil, fmt.Errorf("parsing classifier response: %w", err)
	}
	return enriched, nil
}

// systemPrompt returns the base system prompt optionally enriched with
// community skill recommendations when a SkillInjector is configured.
func (c *ClassifierAgent) systemPrompt() string {
	prompt := classifierSystemPrompt
	if c.skillInjector == nil {
		return prompt
	}
	recs := c.skillInjector.ForObjective("classify security findings CVE CVSS CWE ATTACK")
	if len(recs) == 0 {
		return prompt
	}
	return prompt + "\n" + skills.MustInject(recs)
}

const classifierSystemPrompt = `You are a specialised security finding classifier. Your role is to take raw reconnaissance findings (attack surface) and classify them into actionable security findings.

## Classification rules

### 1. CVSS 3.1 scoring
For each finding, compute a CVSS 3.1 vector. Use the FULL vector string, not just the score:
- CVSS:3.1/AV:[N|A|L|P]/AC:[L|H]/PR:[N|L|H]/UI:[N|R]/S:[U|C]/
  C:[N|L|H]/I:[N|L|H]/A:[N|L|H] + optional temporal/environmental suffix

CVSS guidance:
- AV (Attack Vector): N=remote accessible, A=adjacent network, L=local, P=physical
- AC (Attack Complexity): L=simple (no preconditions), H=complex (race condition, user interaction required)
- PR (Privileges Required): N=none, L=low (basic user), H=high (admin)
- C/I/A (Confidentiality/Integrity/Availability): N=none, L=low, H=high

### 2. CWE mapping
Map each finding to the most specific CWE ID:
- CWE-89 (SQLi), CWE-79 (XSS), CWE-918 (SSRF), CWE-22 (Path Traversal), CWE-78 (Command Injection)
- CWE-502 (Deserialization), CWE-287 (Broken Auth), CWE-639 (IDOR/BOLA), CWE-434 (Unrestricted File Upload)
- CWE-200 (Info Disclosure), CWE-611 (XXE), CWE-1336 (SSTI), CWE-1321 (Prototype Pollution)
- CWE-352 (CSRF), CWE-601 (Open Redirect), CWE-862 (Missing Authorization)
- CWE-521 (Weak Password), CWE-307 (No Rate Limiting), CWE-1390 (Weak Credentials)

### 3. MITRE ATT&CK mapping
Map each finding to the most relevant ATT&CK technique:
- T1190 (Exploit Public-Facing Application), T1595 (Active Scanning), T1046 (Network Service Discovery)
- T1078 (Valid Accounts), T1530 (Data from Cloud Storage), T1526 (Cloud Service Discovery)

### 4. Severity classification
- CRITICAL (9.0-10.0): RCE, direct database access, cloud account takeover, full admin bypass
- HIGH (7.0-8.9): SQLi, SSRF to internal/cloud metadata, auth bypass, arbitrary file read, IDOR on sensitive data
- MEDIUM (4.0-6.9): Stored XSS, CSRF on sensitive actions, open redirect, info disclosure of credentials, weak MFA
- LOW (0.1-3.9): Reflected XSS (non-sensitive), missing security headers, verbose error messages, username enumeration
- INFO (0.0): Technology fingerprinting, open ports, standard endpoints

### 5. Confidence scoring
- 0.9-1.0: Direct observation (e.g., SQLi confirmed by error, JWT decoded with alg=none)
- 0.7-0.89: Strong inference (version + known CVE, but no PoC executed)
- 0.5-0.69: Moderate inference (tech stack suggests vuln class, no direct evidence)
- 0.3-0.49: Weak inference (common pattern, significant ambiguity)
- <0.3: Speculative (do not report; use as hypothesis for further testing)

### 6. Subfinding decomposition
A single finding can produce multiple subfindings. For example:
- "Open port 443 with nginx 1.18" → two subfindings: (1) exposed service, (2) version-specific CVE
- "JWT with alg:none allowed" → three subfindings: (1) broken auth (CWE-287), (2) weak JWT config (CWE-1390), (3) privesc path (CWE-269)

### 7. False positive filtering
Flag as likely false positive if:
- CDN/WAF error page disguised as server error (check for Cloudflare/Akamai/AWS headers)
- Wildcard DNS response resolving all subdomains to same IP
- "Open port" on a CDN IP, not the target's origin
- "Exposed file" is part of framework's normal distribution (robots.txt, favicon.ico)
- "Admin panel" is a decoy/honeypot (check for unusual response patterns)

Respond with a JSON array of classified findings. Each finding must have:
title, description, cve_ids, cvss_score, cvss_vector, severity, attack_category, cwe_id, mitre_technique, confidence, chain_candidates, subfindings, false_positive_risk, exploitability, remediation, references`

func stripCodeFence(s string) string {
	s = trimString(s)
	if len(s) > 7 && s[:7] == "```json" {
		s = s[7:]
	} else if len(s) > 3 && s[:3] == "```" {
		s = s[3:]
	}
	if len(s) > 3 && s[len(s)-3:] == "```" {
		s = s[:len(s)-3]
	}
	return trimString(s)
}

func trimString(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\r' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// inferAttackCategory maps a raw finding type and detail to an attack category
// when the LLM classifier is unavailable. This ensures the PathBuilder can
// still generate attack chains from heuristic-classified findings.
func inferAttackCategory(findingType, detail string) string {
	d := strings.ToLower(detail)
	t := strings.ToLower(findingType)

	// Direct type mappings
	switch t {
	case "sqli", "sql_injection":
		return "sqli"
	case "xss", "cross_site_scripting":
		return "xss"
	case "ssrf":
		return "ssrf"
	case "rce", "remote_code_execution", "command_injection":
		return "rce"
	case "lfi", "local_file_inclusion", "path_traversal":
		return "path_traversal"
	case "xxe":
		return "xxe"
	case "idor":
		return "idor"
	case "open_redirect":
		return "open_redirect"
	case "weak_credentials":
		return "weak_credentials"
	}

	// Detail-based inference
	if strings.Contains(d, "sql") && (strings.Contains(d, "injection") || strings.Contains(d, "sqli")) {
		return "sqli"
	}
	if strings.Contains(d, "xss") || strings.Contains(d, "cross-site scripting") {
		return "xss"
	}
	if strings.Contains(d, "ssrf") || strings.Contains(d, "server-side request") {
		return "ssrf"
	}
	if strings.Contains(d, "rce") || strings.Contains(d, "remote code") || strings.Contains(d, "command injection") {
		return "rce"
	}
	if strings.Contains(d, "path traversal") || strings.Contains(d, "lfi") || strings.Contains(d, "directory traversal") {
		return "path_traversal"
	}
	if strings.Contains(d, "xxe") || strings.Contains(d, "xml external") {
		return "xxe"
	}
	if strings.Contains(d, "idor") || strings.Contains(d, "insecure direct object") {
		return "idor"
	}
	if strings.Contains(d, "open redirect") {
		return "open_redirect"
	}
	if strings.Contains(d, "weak password") || strings.Contains(d, "default credential") || strings.Contains(d, "弱密码") {
		return "weak_credentials"
	}
	if strings.Contains(d, "cors") && strings.Contains(d, "misconfig") {
		return "misconfigured_cors"
	}
	if strings.Contains(d, "exposed git") || strings.Contains(d, ".git") {
		return "exposed_git"
	}
	if strings.Contains(d, "outdated") || strings.Contains(d, "old version") {
		return "outdated_software"
	}
	if strings.Contains(d, "technology") || strings.Contains(d, "tech stack") {
		return "info_disclosure"
	}
	if strings.Contains(d, "port") && strings.Contains(d, "open") {
		return "open_port"
	}
	if strings.Contains(d, "subdomain") || strings.Contains(d, "takeover") {
		return "subdomain_takeover"
	}
	if strings.Contains(d, "interesting endpoint") || strings.Contains(d, "admin") || strings.Contains(d, "login") {
		return "info_disclosure"
	}

	return "recon_finding"
}

// inferSeverity maps a raw finding type and detail to a severity level
// when the LLM classifier is unavailable.
func inferSeverity(findingType, detail string) pipeline.Severity {
	d := strings.ToLower(detail)
	t := strings.ToLower(findingType)

	// Critical/High patterns
	if strings.Contains(d, "rce") || strings.Contains(d, "remote code") || strings.Contains(d, "command injection") {
		return pipeline.SeverityCritical
	}
	if strings.Contains(d, "sqli") || strings.Contains(d, "sql injection") {
		return pipeline.SeverityHigh
	}
	if strings.Contains(d, "ssrf") && strings.Contains(d, "internal") {
		return pipeline.SeverityHigh
	}
	if strings.Contains(d, "auth bypass") || strings.Contains(d, "authentication bypass") {
		return pipeline.SeverityHigh
	}
	if t == "xss" || strings.Contains(d, "xss") {
		return pipeline.SeverityHigh
	}

	// Medium patterns
	if strings.Contains(d, "port") && strings.Contains(d, "open") {
		return pipeline.SeverityMedium
	}
	if strings.Contains(d, "technology") || strings.Contains(d, "tech") {
		return pipeline.SeverityLow
	}
	if strings.Contains(d, "subdomain") {
		return pipeline.SeverityMedium
	}

	// Low/Info patterns
	if strings.Contains(d, "interesting endpoint") {
		return pipeline.SeverityLow
	}

	return pipeline.SeverityMedium
}
