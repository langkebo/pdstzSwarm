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

// TavilyConfig configures the Tavily AI search adapter.
type TavilyConfig struct {
	// APIKey is the Tavily API key. Required.
	APIKey string

	// Endpoint defaults to https://api.tavily.com/search.
	Endpoint string

	// MaxPerQuery caps the result count we accept from Tavily. Tavily
	// itself caps at 20; we mirror that as the package default.
	MaxPerQuery int

	// HTTPClient allows tests to inject a recorder transport.
	HTTPClient *http.Client
}

// TavilyProvider implements Provider against the Tavily API.
type TavilyProvider struct {
	cfg TavilyConfig
}

// NewTavilyProvider returns a configured Tavily provider. The
// returned provider is "configured" only when APIKey is set, allowing
// the recon agent to skip it without a hard error.
func NewTavilyProvider(cfg TavilyConfig) *TavilyProvider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.tavily.com/search"
	}
	if cfg.MaxPerQuery <= 0 {
		cfg.MaxPerQuery = 20
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &TavilyProvider{cfg: cfg}
}

// Name implements Provider.
func (p *TavilyProvider) Name() string { return "tavily" }

// IsConfigured implements Provider.
func (p *TavilyProvider) IsConfigured() bool { return strings.TrimSpace(p.cfg.APIKey) != "" }

// Search implements Provider.
func (p *TavilyProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("tavily: api_key not set")
	}
	if q.Text == "" {
		return nil, fmt.Errorf("tavily: empty query")
	}
	max := q.Max
	if max <= 0 || max > p.cfg.MaxPerQuery {
		max = p.cfg.MaxPerQuery
	}

	body, err := json.Marshal(map[string]any{
		"api_key":             p.cfg.APIKey,
		"query":               q.Text,
		"max_results":         max,
		"include_answer":      false,
		"include_raw_content": false,
	})
	if err != nil {
		return nil, fmt.Errorf("tavily: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("tavily: auth failed (status %d) — check api_key", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("tavily: status %d: %s", resp.StatusCode, string(raw))
	}

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("tavily: decode: %w", err)
	}

	out := make([]Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.URL == "" {
			continue
		}
		out = append(out, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
			Source:  p.Name(),
		})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}
