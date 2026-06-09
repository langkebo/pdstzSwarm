package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SEARXNGConfig configures the SEARXNG meta-search adapter.
//
// SEARXNG is a self-hostable meta-search engine; many public
// instances are also available. The provider is a thin client over
// the instance's `/search?format=json` endpoint, which returns a
// flat list of {title, url, content} entries aggregated from
// upstream engines (Google, Bing, DuckDuckGo, …).
//
// The provider is configured if Endpoint is set; no API key is
// required for most public instances. Some instances expose a
// Bearer-key gate — pass it as APIKey to use the Authorization
// header.
type SEARXNGConfig struct {
	// Endpoint is the SEARXNG base URL, e.g. "https://searx.be".
	// Required. We append "/search" + the form-encoded query.
	Endpoint string

	// APIKey, if set, is sent as a Bearer token. Most public
	// instances don't require this; private instances often do.
	APIKey string

	// User-Agent is sent on every request. Some instances refuse
	// default Go UAs.
	UserAgent string

	// MaxPerQuery caps the result count we accept from SEARXNG.
	// SEARXNG's own default is 10; we mirror that as the package
	// default.
	MaxPerQuery int

	// Timeout, if non-zero, overrides the per-call HTTP timeout.
	// Default is DefaultTimeout.
	Timeout time.Duration

	// HTTPClient allows tests to inject a recorder transport.
	HTTPClient *http.Client
}

// SEARXNGProvider implements Provider against a SEARXNG instance's
// JSON API.
type SEARXNGProvider struct {
	cfg SEARXNGConfig
}

// NewSEARXNGProvider returns a configured SEARXNG provider. The
// returned provider is "configured" when Endpoint is non-empty;
// APIKey is optional.
func NewSEARXNGProvider(cfg SEARXNGConfig) *SEARXNGProvider {
	if cfg.MaxPerQuery <= 0 {
		cfg.MaxPerQuery = 10
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) Pentest-Swarm-AI/1.0"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &SEARXNGProvider{cfg: cfg}
}

// Name implements Provider.
func (p *SEARXNGProvider) Name() string { return "searxng" }

// IsConfigured implements Provider. SEARXNG only needs an Endpoint.
// APIKey is optional for authenticated instances.
func (p *SEARXNGProvider) IsConfigured() bool { return strings.TrimSpace(p.cfg.Endpoint) != "" }

// Search implements Provider. It GETs `/search?format=json&q=…&limit=…`
// and decodes the result list.
//
// Errors from the upstream are wrapped with a "searxng:" prefix so
// callers can route on provider. We do NOT treat 5xx as a hard
// error: SEARXNG instances are independently run and frequently
// flaky; the caller (recon agent) is expected to fan out across
// multiple providers and pick the most successful.
func (p *SEARXNGProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("searxng: endpoint not set")
	}
	if q.Text == "" {
		return nil, fmt.Errorf("searxng: empty query")
	}
	max := q.Max
	if max <= 0 || max > p.cfg.MaxPerQuery {
		max = p.cfg.MaxPerQuery
	}

	endpoint := strings.TrimRight(p.cfg.Endpoint, "/") + "/search"
	form := url.Values{}
	form.Set("q", q.Text)
	form.Set("format", "json")
	form.Set("limit", fmt.Sprintf("%d", max))
	// language=all keeps results language-agnostic; SEARXNG otherwise
	// uses the Accept-Language header which is empty for our Go UA.
	form.Set("language", "all")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+form.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("searxng: build request: %w", err)
	}
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("searxng: auth failed (status %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("searxng: status %d: %s", resp.StatusCode, string(raw))
	}

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
			Engine  string `json:"engine"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("searxng: decode: %w", err)
	}

	out := make([]Result, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.URL == "" {
			continue
		}
		// SEARXNG strips tracking redirects by default; URL should
		// already be the real one. Defensively reject relative URLs
		// just in case.
		if !strings.HasPrefix(r.URL, "http://") && !strings.HasPrefix(r.URL, "https://") {
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
