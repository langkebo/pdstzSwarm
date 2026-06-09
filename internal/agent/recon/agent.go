package recon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/llm"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/pipeline"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/scope"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/skills"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools"
	"github.com/google/uuid"
)

// ReconAgent orchestrates security tools and analyzes output to build an AttackSurface.
type ReconAgent struct {
	provider      llm.Provider
	coordinator   *tools.Coordinator
	strict        bool
	onErr         func(error)
	skillInjector *skills.Injector
}

// Option customises ReconAgent construction.
type Option func(*ReconAgent)

// WithStrict makes LLM failures fatal instead of returning a partial surface.
func WithStrict() Option {
	return func(r *ReconAgent) { r.strict = true }
}

// WithErrorSink installs a callback for LLM / parse errors. Useful for
// emitting degraded-mode warnings to the event stream.
func WithErrorSink(fn func(error)) Option {
	return func(r *ReconAgent) { r.onErr = fn }
}

// WithSkillInjector enables community skill recommendations in the
// system prompt for more informed analysis.
func WithSkillInjector(inj *skills.Injector) Option {
	return func(r *ReconAgent) { r.skillInjector = inj }
}

// NewReconAgent creates a new recon agent.
func NewReconAgent(provider llm.Provider, coordinator *tools.Coordinator, opts ...Option) *ReconAgent {
	r := &ReconAgent{
		provider:    provider,
		coordinator: coordinator,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ReconPlan defines which tools to run based on target type.
type ReconPlan struct {
	Target    string   `json:"target"`
	ToolOrder []string `json:"tool_order"`
}

// PlanRecon determines which tools to run based on the target type.
func (r *ReconAgent) PlanRecon(target string) ReconPlan {
	plan := ReconPlan{Target: target}

	if isIPTarget(target) {
		// IP target: port scan → probe → scan
		plan.ToolOrder = []string{"naabu", "httpx", "nuclei"}
	} else if isURLTarget(target) {
		// URL target: probe → port scan → crawl → history → scan
		// naabu extracts the host from the URL and scans common ports,
		// giving us a richer attack surface than just the HTTP probe.
		plan.ToolOrder = []string{"naabu", "httpx", "katana", "gau", "nuclei"}
	} else {
		// Domain target: full recon pipeline
		plan.ToolOrder = []string{"subfinder", "dnsx", "naabu", "httpx", "katana", "gau", "nuclei"}
	}

	return plan
}

// Execute runs the recon plan and produces an AttackSurface.
func (r *ReconAgent) Execute(ctx context.Context, plan ReconPlan, scopeDef *scope.ScopeDefinition, campaignID uuid.UUID, opts tools.Options) (*pipeline.AttackSurface, error) {
	// Run tools
	_, resultCh := r.coordinator.RunSelected(ctx, plan.ToolOrder, plan.Target, scopeDef, opts)

	// Collect results as they stream in
	var results []*tools.ToolResult
	for result := range resultCh {
		results = append(results, result)
	}

	// Analyze results with LLM. If the LLM fails (e.g. API key
	// exhausted, rate-limited, network error), fall back to building
	// the attack surface from raw tool output rather than failing
	// the entire campaign.
	surface, err := r.Analyze(ctx, results, campaignID)
	if err != nil {
		if r.onErr != nil {
			r.onErr(fmt.Errorf("LLM analysis failed, using tool-output fallback: %w", err))
		}
		// Build surface from raw tool output as degraded-mode fallback
		fallback := BuildSurfaceFromToolResults(results, plan.Target)
		if fallback != nil && (len(fallback.Hosts) > 0 || len(fallback.Endpoints) > 0 || len(fallback.Technologies) > 0) {
			fallback.CampaignID = campaignID
			fallback.CreatedAt = time.Now()
			return fallback, nil
		}
		// Even with an empty surface, return it rather than failing
		// the campaign — the pipeline can still proceed to
		// classification with whatever partial data exists.
		return &pipeline.AttackSurface{
			Target:     plan.Target,
			CampaignID: campaignID,
			CreatedAt:  time.Now(),
		}, nil
	}

	return surface, nil
}

// Analyze sends tool results to the LLM for structured analysis.
func (r *ReconAgent) Analyze(ctx context.Context, results []*tools.ToolResult, campaignID uuid.UUID) (*pipeline.AttackSurface, error) {
	// Build context from tool results, truncating large outputs to avoid
	// overwhelming the LLM context window (DeepSeek limit: ~64K tokens).
	const maxToolOutputBytes = 60000 // ~15K tokens per tool, safe margin
	var contextBuilder strings.Builder
	successCount := 0
	for _, result := range results {
		if result.Error != nil {
			contextBuilder.WriteString(fmt.Sprintf("Tool: %s (FAILED: %s)\n\n", result.ToolName, result.Error))
			continue
		}
		successCount++
		output := result.RawOutput
		if len(output) > maxToolOutputBytes {
			// Keep first and last portion for context
			half := maxToolOutputBytes / 2
			output = output[:half] + fmt.Sprintf("\n\n... [%d bytes truncated] ...\n\n", len(result.RawOutput)-maxToolOutputBytes) + output[len(result.RawOutput)-half:]
		}
		contextBuilder.WriteString(fmt.Sprintf("Tool: %s (%d bytes)\nOutput:\n%s\n\n", result.ToolName, len(result.RawOutput), output))
	}

	// If no tools succeeded, try to build a surface from parsed findings
	if successCount == 0 {
		surface := BuildSurfaceFromToolResults(results, "")
		if surface != nil && (len(surface.Hosts) > 0 || len(surface.Endpoints) > 0) {
			surface.CampaignID = campaignID
			surface.CreatedAt = time.Now()
			return surface, nil
		}
		// Return empty surface rather than error
		return &pipeline.AttackSurface{
			CampaignID: campaignID,
			CreatedAt:  time.Now(),
		}, nil
	}

	req := llm.CompletionRequest{
		SystemPrompt: r.systemPrompt(""),
		Messages: []llm.Message{
			{
				Role: "user",
				Content: fmt.Sprintf(
					"Analyze the following security tool outputs and produce a structured AttackSurface JSON object.\n\n%s\n\nRespond ONLY with valid JSON matching the AttackSurface schema.",
					contextBuilder.String(),
				),
			},
		},
		MaxTokens:         8192,
		Temperature:       0.1,
		CacheSystemPrompt: true,
	}

	resp, err := r.provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("LLM analysis failed: %w", err)
	}

	// Parse the response
	surface, err := ParseAttackSurface(resp.Content)
	if err != nil {
		// Retry with simplified prompt
		retryReq := llm.CompletionRequest{
			SystemPrompt: "You are a JSON parser. Extract structured data from security tool output. Respond ONLY with valid JSON.",
			Messages: []llm.Message{
				{
					Role: "user",
					Content: fmt.Sprintf(
						"Parse this into an AttackSurface JSON with fields: target, subdomains, hosts, endpoints, technologies.\n\n%s",
						contextBuilder.String(),
					),
				},
			},
			MaxTokens:   8192,
			Temperature: 0,
		}

		retryResp, retryErr := r.provider.Complete(ctx, retryReq)
		if retryErr != nil {
			return nil, fmt.Errorf("retry analysis also failed: %w", retryErr)
		}

		surface, err = ParseAttackSurface(retryResp.Content)
		if err != nil {
			if r.strict {
				return nil, fmt.Errorf("recon parse failed after retry: %w", err)
			}
			if r.onErr != nil {
				r.onErr(fmt.Errorf("recon analysis returned empty surface: %w", err))
			}
			// Fallback: build surface from raw tool output instead of returning empty
			fallback := BuildSurfaceFromToolResults(results, "")
			if fallback != nil && (len(fallback.Hosts) > 0 || len(fallback.Endpoints) > 0 || len(fallback.Technologies) > 0) {
				fallback.CampaignID = campaignID
				fallback.CreatedAt = time.Now()
				return fallback, nil
			}
			// Return partial results rather than error (degraded mode).
			return &pipeline.AttackSurface{
				CampaignID: campaignID,
				CreatedAt:  time.Now(),
			}, nil
		}
	}

	surface.CampaignID = campaignID
	surface.CreatedAt = time.Now()

	return surface, nil
}

// systemPrompt returns the base system prompt optionally enriched with
// community skill recommendations when a SkillInjector is configured.
func (r *ReconAgent) systemPrompt(target string) string {
	prompt := reconSystemPrompt
	if r.skillInjector == nil {
		return prompt
	}
	recs := r.skillInjector.ForTarget(target)
	if len(recs) == 0 {
		return prompt
	}
	return prompt + "\n" + skills.MustInject(recs)
}

const reconSystemPrompt = `You are a specialised security reconnaissance analyst. Your job is to analyse output from security scanning tools and produce a structured attack surface model that downstream agents (classifier, exploit, triage) can reason about.

Given the raw output from tools like subfinder, httpx, nuclei, naabu, katana, dnsx, and gau, you must extract and structure:

## 1. Subdomains
- For each discovered subdomain, record: domain, resolved IP, source tool, and any CNAME chain
- Flag subdomains with dangling CNAMEs (NXDOMAIN / SERVFAIL on the target) — these are takeover candidates
- Flag subdomains that resolve to cloud provider IPs (AWS, GCP, Azure, Cloudflare) — note the provider
- Flag wildcard DNS: if a random subdomain resolves, note that the domain uses wildcard DNS

## 2. Hosts & ports
- For each unique IP, record: all hostnames pointing to it, open ports, service banner per port, and guessed OS
- Flag unusual ports: 6379 (Redis), 27017 (MongoDB), 5432 (PostgreSQL), 9200 (Elasticsearch), 11211 (Memcached), 2375/2376 (Docker)
- Flag services with default credentials potential: FTP (21), SSH (22), Telnet (23), SMB (445), RDP (3389)
- If a port returns an HTTP response, reclassify it as an HTTP endpoint for the endpoint catalog

## 3. Web endpoints
- For each URL, record: method(s) allowed (from OPTIONS or headers), status code, content-type, content-length
- Flag "interesting" endpoints: admin panels (/admin, /wp-admin, /administrator, /manager), API docs (/swagger, /openapi.json, /graphql, /graphiql), debug endpoints (/debug, /phpinfo, /actuator, /.env), file uploads, login pages, registration pages
- Flag endpoints returning 401/403 — these are auth-gated and need auth testing
- Flag endpoints returning 500 — these may indicate exploitable server errors
- Flag endpoints with reflected parameters (check for ?param=value in URL and look for the value in the response)

## 4. Technologies
- Identify: web server (nginx, Apache, IIS, Caddy), app framework (Django, Rails, Laravel, Express, Spring), CMS (WordPress, Drupal, Joomla), JS framework (React, Vue, Angular, Next.js), CDN/WAF (Cloudflare, Akamai, Fastly, AWS CloudFront)
- Record version numbers where available (headers, HTML comments, JS bundles, error pages)
- Flag known-vulnerable versions: e.g., Apache Struts < 2.5.30, WordPress < 5.8.3, Django < 3.2.14
- Flag technologies that suggest specific attack paths: PHP → file inclusion, Java → deserialization, Node.js → prototype pollution, .NET → ViewState

## 5. Interesting anomalies
- Flag any finding that doesn't fit the above categories but is security-relevant: exposed .git directories, backup files (.bak, .old, .swp), config files (.env, config.yml, web.config), database dumps, log files, credentials in JS bundles
- Flag Cloud metadata endpoints: 169.254.169.254 (AWS), metadata.google.internal (GCP), 169.254.169.254/metadata (Azure)
- Flag OOB callback indicators: any hostname that appears to be a Burp Collaborator / interactsh domain

## Output format
Output your analysis as a valid JSON object matching this schema:
{
  "target": "string",
  "subdomains": [{"domain": "string", "ip": "string", "source": "string", "cname": "string", "takeover_risk": bool}],
  "hosts": [{"ip": "string", "hostnames": ["string"], "open_ports": [int], "services": {"port": "service"}, "os": "string", "flags": ["string"]}],
  "endpoints": [{"url": "string", "method": "string", "status_code": int, "content_type": "string", "content_length": int, "interesting": bool, "interesting_reason": "string", "reflected_params": ["string"]}],
  "technologies": {"technology": "version"},
  "anomalies": [{"type": "string", "target": "string", "description": "string"}]
}

## Anti-patterns
- Do not invent data — if the tool output doesn't contain a field, omit it (null/empty in JSON)
- Do not merge two different hosts' data into one entry
- Do not guess OS or service versions if the tool output doesn't provide them
- Do not flag an endpoint as "interesting" without a concrete reason

Respond ONLY with the JSON object. No markdown, no explanation.`

func isIPTarget(target string) bool {
	parts := strings.Split(target, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func isURLTarget(target string) bool {
	return strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")
}
