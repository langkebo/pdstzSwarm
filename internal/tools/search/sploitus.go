package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SploitusConfig configures the Sploitus exploit-search adapter.
//
// Sploitus (https://sploitus.com) is a free, key-less search engine
// for offensive-security tools — exploits, proof-of-concepts, and
// payloads aggregated from Exploit-DB, Packet Storm, GitHub, and
// the like. It's the recon agent's high-signal source for "is
// there a public exploit for this CVE/technology?" questions.
//
// The provider scrapes the HTML response (no public JSON API). The
// parser is intentionally narrow: Sploitus's HTML is large and
// cluttered with self-promotion, so we only pull the obvious
// exploit-card title/URL/snippet triples. Sploitus sometimes
// rate-limits anonymous scrapers with a captcha; we surface that
// as a regular error so the recon agent can back off.
type SploitusConfig struct {
	// Endpoint defaults to https://sploitus.com/. Sploitus has
	// alternate per-tab URLs (/exploits/, /tools/) but the default
	// `/` page exposes the unified search.
	Endpoint string

	// UserAgent is sent on every request. Sploitus rejects
	// default Go UAs with a captcha.
	UserAgent string

	// MaxPerQuery caps the result count we scrape. Sploitus's
	// HTML holds ~10–20 cards per page; we cap to 20 as the
	// package default.
	MaxPerQuery int

	// HTTPClient allows tests to inject a recorder transport.
	HTTPClient *http.Client
}

// SploitusProvider implements Provider against sploitus.com.
type SploitusProvider struct {
	cfg SploitusConfig
}

// NewSploitusProvider returns a configured Sploitus provider.
// Sploitus is always "configured" — no key required.
func NewSploitusProvider(cfg SploitusConfig) *SploitusProvider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://sploitus.com/"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) Pentest-Swarm-AI/1.0"
	}
	if cfg.MaxPerQuery <= 0 {
		cfg.MaxPerQuery = 20
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &SploitusProvider{cfg: cfg}
}

// Name implements Provider.
func (p *SploitusProvider) Name() string { return "sploitus" }

// IsConfigured implements Provider. Sploitus requires no key.
func (p *SploitusProvider) IsConfigured() bool { return true }

// Search implements Provider. It GETs the search page with `query=`
// form-encoded and parses the exploit cards from the HTML.
func (p *SploitusProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Text == "" {
		return nil, fmt.Errorf("sploitus: empty query")
	}
	max := q.Max
	if max <= 0 || max > p.cfg.MaxPerQuery {
		max = p.cfg.MaxPerQuery
	}

	form := url.Values{}
	form.Set("query", q.Text)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoint+"?"+form.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("sploitus: build request: %w", err)
	}
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sploitus: http: %w", err)
	}
	defer resp.Body.Close()

	// Sploitus serves a captcha wall on rate-limit / suspected
	// abuse. We don't try to solve it; we just bail so the recon
	// agent can rotate to another provider.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("sploitus: rate-limited (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sploitus: unexpected status %d", resp.StatusCode)
	}

	body, err := readAllLimited(resp.Body, 2<<20) // 2 MiB cap
	if err != nil {
		return nil, fmt.Errorf("sploitus: read body: %w", err)
	}
	return parseSploitusHTML(string(body), p.Name(), max), nil
}

// parseSploitusHTML extracts result snippets from the Sploitus
// HTML page. Sploitus renders each hit as a card with class names
// like `exploit-card`, `exploit-card__link` (the title <a>),
// `exploit-card__source` (badge: e.g. "exploitdb"), and a
// description body. The HTML structure has shifted between
// releases, so the parser is defensive: any element that fails to
// match is silently skipped, partial extraction is fine.
func parseSploitusHTML(body, source string, max int) []Result {
	results := make([]Result, 0, max)
	// Walk the body in chunks. We use a simple state machine: every
	// time we see a `exploit-card__link` anchor, we extract its href
	// and inner text as title, then look for the next
	// `exploit-card__description` block as snippet.
	idx := 0
	for len(results) < max {
		anchor := strings.Index(body[idx:], `class="exploit-card__link"`)
		if anchor < 0 {
			// Sploitus renamed these once; try a fallback.
			anchor = strings.Index(body[idx:], `exploit-card__link`)
			if anchor < 0 {
				break
			}
		}
		anchor += idx

		// Find the open <a ...> tag that contains this anchor.
		openStart := strings.LastIndex(body[:anchor], "<a ")
		if openStart < 0 {
			idx = anchor + 1
			continue
		}
		openEnd := strings.Index(body[anchor:], ">")
		if openEnd < 0 {
			break
		}
		openEnd += anchor

		// href="..."
		hrefStart := strings.Index(body[openStart:openEnd], `href="`)
		if hrefStart < 0 {
			idx = openEnd
			continue
		}
		hrefStart += openStart + len(`href="`)
		hrefEnd := strings.Index(body[hrefStart:openEnd], `"`)
		if hrefEnd < 0 {
			idx = openEnd
			continue
		}
		hrefEnd += hrefStart
		rawURL := body[hrefStart:hrefEnd]

		// Title is the inner text of the <a>...</a>.
		closeA := strings.Index(body[openEnd:], "</a>")
		if closeA < 0 {
			break
		}
		closeA += openEnd
		title := strings.TrimSpace(stripTags(body[openEnd+1 : closeA]))
		if title == "" {
			idx = closeA
			continue
		}

		// Snippet: the next description block. Try the canonical
		// class first, then a more lenient match.
		idx = closeA
		snippet := ""
		snipMarker := `class="exploit-card__description"`
		snipStart := strings.Index(body[idx:], snipMarker)
		if snipStart < 0 {
			snipMarker = `exploit-card__description`
			snipStart = strings.Index(body[idx:], snipMarker)
		}
		if snipStart >= 0 {
			snipStart += idx
			// Skip past the marker (plus the closing `>` of the
			// containing tag if present).
			tagEnd := strings.Index(body[snipStart:], ">")
			if tagEnd >= 0 {
				snipStart += tagEnd + 1
			} else {
				snipStart += len(snipMarker)
			}
			snipEnd := strings.Index(body[snipStart:], "</")
			if snipEnd >= 0 {
				snippet = strings.TrimSpace(stripTags(body[snipStart : snipStart+snipEnd]))
			}
			idx = snipStart
		}

		// Normalize URL: Sploitus occasionally wraps links in its
		// own /link/?url= redirect. Decode that.
		realURL := decodeSploitusRedirect(rawURL)
		if realURL == "" {
			idx = openEnd
			continue
		}

		results = append(results, Result{
			Title: title, URL: realURL, Snippet: snippet, Source: source,
		})
	}
	return results
}

// decodeSploitusRedirect reverses Sploitus's /link/?url=<encoded>
// wrapper. Returns empty string if the URL is not parseable; the
// caller then drops the result.
func decodeSploitusRedirect(raw string) string {
	// Already absolute → no redirect.
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if !strings.HasPrefix(raw, "/") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if v := u.Query().Get("url"); v != "" {
		return v
	}
	// Sploitus uses /exploit/?id=… for in-page anchors; skip those.
	if strings.HasPrefix(raw, "/exploit/") {
		return ""
	}
	return raw
}
