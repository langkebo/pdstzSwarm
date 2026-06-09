package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// PerplexityConfig configures the Perplexity AI search adapter.
//
// Perplexity exposes a Sonar family of models behind an OpenAI-
// compatible chat completions endpoint. The /search endpoint was
// deprecated in 2025; current best practice is to POST
// /chat/completions with `search_mode=sonar` and read the answer +
// citations from the response.
//
// For our purposes we want the citations as Result rows. The
// `search_results` field on the response carries the upstream
// references; the assistant message is folded into a synthesized
// Result with the citation list as its snippet (one URL per line).
type PerplexityConfig struct {
	// APIKey is the Perplexity API key. Required.
	APIKey string

	// Endpoint defaults to https://api.perplexity.ai/chat/completions.
	Endpoint string

	// Model is the Sonar model name. Defaults to "sonar" (the
	// cheapest citation-quality model). Set to "sonar-pro" for
	// higher-quality retrieval at higher cost.
	Model string

	// MaxPerQuery caps the result count we accept from Perplexity.
	// Perplexity returns up to ~10 search_results; we mirror that
	// as the package default.
	MaxPerQuery int

	// HTTPClient allows tests to inject a recorder transport.
	HTTPClient *http.Client
}

// PerplexityProvider implements Provider against the Perplexity
// chat-completions API in sonar mode.
type PerplexityProvider struct {
	cfg PerplexityConfig
}

// NewPerplexityProvider returns a configured Perplexity provider.
// The returned provider is "configured" only when APIKey is set.
func NewPerplexityProvider(cfg PerplexityConfig) *PerplexityProvider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.perplexity.ai/chat/completions"
	}
	if cfg.Model == "" {
		cfg.Model = "sonar"
	}
	if cfg.MaxPerQuery <= 0 {
		cfg.MaxPerQuery = 10
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &PerplexityProvider{cfg: cfg}
}

// Name implements Provider.
func (p *PerplexityProvider) Name() string { return "perplexity" }

// IsConfigured implements Provider.
func (p *PerplexityProvider) IsConfigured() bool { return strings.TrimSpace(p.cfg.APIKey) != "" }

// Search implements Provider.
//
// We POST a single-message chat completion with `search_mode=sonar`
// and a system prompt that asks for a concise answer with
// citations. The response carries two relevant fields:
//
//   - choices[0].message.content — the assistant's answer text
//   - search_results[]           — the upstream URL/citation list
//
// We promote the citations to Result rows and ignore the answer
// text (it's free-form prose, not a URL the recon agent can
// follow). If `search_results` is empty, we fall back to a single
// Result with the answer as the snippet so the recon agent at
// least sees a non-empty payload (a known Perplexity behavior when
// the query is too narrow).
func (p *PerplexityProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("perplexity: api_key not set")
	}
	if q.Text == "" {
		return nil, fmt.Errorf("perplexity: empty query")
	}
	max := q.Max
	if max <= 0 || max > p.cfg.MaxPerQuery {
		max = p.cfg.MaxPerQuery
	}

	body, err := json.Marshal(map[string]any{
		"model": p.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a security reconnaissance search assistant. Provide concise, citation-backed answers about exploits, CVEs, and exposed services."},
			{"role": "user", "content": q.Text},
		},
		"search_mode":   "sonar",
		"return_citations": true,
		"return_related_questions": false,
	})
	if err != nil {
		return nil, fmt.Errorf("perplexity: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("perplexity: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perplexity: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("perplexity: auth failed (status %d) — check api_key", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("perplexity: rate-limited (status 429)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("perplexity: status %d: %s", resp.StatusCode, string(raw))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		SearchResults []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Date  string `json:"date"`
		} `json:"search_results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("perplexity: decode: %w", err)
	}

	out := make([]Result, 0, len(parsed.SearchResults))
	for _, r := range parsed.SearchResults {
		if r.URL == "" {
			continue
		}
		// Title may be empty for some citations; fall back to the
		// URL host as a human-readable label.
		title := r.Title
		if title == "" {
			title = r.URL
		}
		out = append(out, Result{
			Title:   title,
			URL:     r.URL,
			Snippet: r.Date,
			Source:  p.Name(),
		})
		if len(out) >= max {
			break
		}
	}
	// Fallback: empty citations — synthesize a single Result carrying
	// the assistant text. The recon agent can still derive value
	// from the prose even if there are no followable URLs.
	if len(out) == 0 && len(parsed.Choices) > 0 {
		answer := strings.TrimSpace(parsed.Choices[0].Message.Content)
		if answer != "" {
			out = append(out, Result{
				Title:   q.Text,
				URL:     "perplexity://answer",
				Snippet: truncateForSnippet(answer, 800),
				Source:  p.Name(),
			})
		}
	}
	return out, nil
}

// truncateForSnippet clamps the synthesized snippet to a sane
// length to keep the WS payload bounded.
func truncateForSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
