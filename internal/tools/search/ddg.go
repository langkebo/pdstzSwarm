package search

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DDGConfig configures the DuckDuckGo HTML-scraping provider. The
// free /lite endpoint requires no API key but is rate-limited;
// callers should respect HTTP 202 (rate-limited) by backing off.
type DDGConfig struct {
	// Endpoint defaults to https://html.duckduckgo.com/html/ which
	// returns the legacy HTML page (no JS). The "/lite/" variant is
	// even simpler but is sometimes down.
	Endpoint string

	// UserAgent is sent on every request. DDG blocks default Go UAs.
	UserAgent string

	// MaxPerQuery caps the result count we scrape. Set on the Query
	// for per-call control; this is the package-level ceiling.
	MaxPerQuery int

	// HTTPClient allows tests to inject a recorder transport.
	HTTPClient *http.Client
}

// DDGProvider implements Provider against DuckDuckGo's HTML endpoint.
type DDGProvider struct {
	cfg DDGConfig
}

// NewDDGProvider returns a configured DDG provider. The returned
// provider is always "configured" — no key required.
func NewDDGProvider(cfg DDGConfig) *DDGProvider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://html.duckduckgo.com/html/"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) Pentest-Swarm-AI/1.0"
	}
	if cfg.MaxPerQuery <= 0 {
		cfg.MaxPerQuery = 10
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &DDGProvider{cfg: cfg}
}

// Name implements Provider.
func (p *DDGProvider) Name() string { return "ddg" }

// IsConfigured implements Provider. DDG requires no key, so it's
// always configured.
func (p *DDGProvider) IsConfigured() bool { return true }

// Search implements Provider. It issues a single GET with a `q` form
// parameter, then parses out result anchors from the HTML. The parser
// is intentionally narrow — DDG's HTML is large and full of
// self-promotion; we only extract the obvious result snippets.
func (p *DDGProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Text == "" {
		return nil, fmt.Errorf("ddg: empty query")
	}
	max := q.Max
	if max <= 0 || max > p.cfg.MaxPerQuery {
		max = p.cfg.MaxPerQuery
	}

	form := url.Values{}
	form.Set("q", q.Text)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoint+"?"+form.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("ddg: build request: %w", err)
	}
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ddg: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		// 202 is DDG's "anomaly" / rate-limit response. Treat as a
		// transient failure so the caller can back off.
		return nil, fmt.Errorf("ddg: rate-limited (HTTP 202)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ddg: unexpected status %d", resp.StatusCode)
	}

	body, err := readAllLimited(resp.Body, 1<<20) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("ddg: read body: %w", err)
	}
	return parseDDGHTML(string(body), p.Name(), max), nil
}

// parseDDGHTML extracts result snippets from the legacy DDG HTML
// page. It looks for the well-known `.result__a` (title), `.result__url`
// (URL), and `.result__snippet` patterns. Any element that fails to
// parse is silently skipped — partial extraction is fine.
func parseDDGHTML(body, source string, max int) []Result {
	results := make([]Result, 0, max)
	idx := 0
	for len(results) < max {
		needle := `class="result__a"`
		anchor := strings.Index(body[idx:], needle)
		if anchor < 0 {
			break
		}
		anchor += idx // make absolute

		// Walk left to find the <a ...> open tag for this anchor.
		openStart := strings.LastIndex(body[:anchor], "<a ")
		if openStart < 0 {
			idx = anchor + len(needle)
			continue
		}

		// Find the closing > of the <a> open tag (i.e. the end of
		// "<a class=\"result__a\" href=\"...\">").
		openEnd := strings.Index(body[anchor:], ">")
		if openEnd < 0 {
			break
		}
		openEnd += anchor

		// Extract href="..." from inside the open tag.
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

		// Title text is the inner text of the <a>…</a>.
		closeA := strings.Index(body[openEnd:], "</a>")
		if closeA < 0 {
			break
		}
		closeA += openEnd
		title := strings.TrimSpace(stripTags(body[openEnd+1 : closeA]))

		// URL: DDG wraps real URLs in a /l/?uddg= redirect, so decode.
		realURL := decodeDDGRedirect(body[hrefStart:hrefEnd])

		// Snippet is the next .result__snippet block.
		idx = closeA
		snipStart := strings.Index(body[idx:], `class="result__snippet"`)
		snippet := ""
		if snipStart >= 0 {
			snipStart += idx
			snipGT := strings.Index(body[snipStart:], ">")
			if snipGT >= 0 {
				snipGT += snipStart
				snipEnd := strings.Index(body[snipGT:], "</")
				if snipEnd >= 0 {
					snippet = strings.TrimSpace(stripTags(body[snipGT+1 : snipGT+snipEnd]))
				}
			}
			idx = snipStart
		}

		if title != "" && realURL != "" {
			results = append(results, Result{
				Title: title, URL: realURL, Snippet: snippet, Source: source,
			})
		}
	}
	return results
}

// decodeDDGRedirect reverses DDG's /l/?uddg=<encoded> wrapper.
func decodeDDGRedirect(raw string) string {
	if !strings.HasPrefix(raw, "/") {
		return raw
	}
	if !strings.Contains(raw, "uddg=") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if v := u.Query().Get("uddg"); v != "" {
		return v
	}
	return raw
}

// stripTags removes anything between < and >. Sufficient for the
// fields we care about (no nested tags expected in DDG titles or
// snippets); faster than a real HTML parser and avoids pulling in a
// dependency for one page layout.
func stripTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '<' {
			if j := strings.IndexByte(s[i:], '>'); j >= 0 {
				i += j + 1
				continue
			}
			break
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// indexAfter returns the next index ≥ from where needle appears, or
// -1. Used to advance the parser between result rows.
func indexAfter(haystack, needle string, from int) int {
	if from >= len(haystack) {
		return -1
	}
	return strings.Index(haystack[from:], needle)
}

// Avoid time import linter issues when only used in the future.
var _ = time.Second
