// Package web gives Route-X the OpenCode web tools: webfetch (read
// a URL) and websearch (search the web). Stdlib only.
//
// Search needs no API key by default: it scrapes DuckDuckGo's
// lightweight HTML endpoint. When EXA_API_KEY is set, Exa's API is
// used instead (better results, metered by Exa).
//
// Fetch refuses private/loopback hosts (SSRF guard): an agent that
// can be told to fetch arbitrary URLs must not be able to reach
// localhost services or cloud metadata endpoints. Tests set
// ROUTE_X_ALLOW_PRIVATE_FETCH=1 to use httptest servers.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// MaxBody is the fetch size cap. Larger pages are truncated with
// a note instead of being refused — a coding agent usually needs
// the beginning of a doc, not all of it.
const MaxBody = 200 << 10

var httpClient = &http.Client{Timeout: 15 * time.Second}

var privateCIDRs = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10",
	"::1/128", "fc00::/7", "fe80::/10",
}

// allowPrivate reports whether the SSRF guard is off (tests only).
func allowPrivate() bool { return os.Getenv("ROUTE_X_ALLOW_PRIVATE_FETCH") == "1" }

// checkPublicHost refuses hosts that resolve to a private or
// loopback address, unless the test escape hatch is set.
func checkPublicHost(rawurl string) error {
	u, err := url.Parse(rawurl)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http(s) URLs are allowed: %q", rawurl)
	}
	if allowPrivate() {
		return nil
	}
	host := u.Hostname()
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip", host)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	for _, cidr := range privateCIDRs {
		_, network, _ := net.ParseCIDR(cidr)
		for _, ip := range ips {
			if network.Contains(ip) {
				return fmt.Errorf("refused private host %s (%s)", host, ip)
			}
		}
	}
	return nil
}

// Fetch downloads url and returns its text. HTML is stripped to
// readable text; other content is returned as-is. Bodies over
// MaxBody are truncated with a note.
func Fetch(rawurl string) (string, error) {
	return fetchWith(httpClient, rawurl)
}

func fetchWith(client *http.Client, rawurl string) (string, error) {
	if err := checkPublicHost(rawurl); err != nil {
		return "", err
	}
	req, err := http.NewRequest("GET", rawurl, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Route-X/1.0 (coding agent; +https://github.com/dbrok89-alt/Route-X-CLI)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("fetch %s: HTTP %d", rawurl, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return "", err
	}
	truncated := len(data) > MaxBody
	if truncated {
		data = data[:MaxBody]
	}
	text := string(data)
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		text = stripHTML(text)
	}
	if truncated {
		text += "\n[truncated: response exceeded 200 KiB]"
	}
	return text, nil
}

// stripHTML removes tags and decodes the common entities.
func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteByte(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	text := b.String()
	for _, pair := range [][2]string{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&nbsp;", " "},
	} {
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}
	text = regexp.MustCompile(`[ \t]+`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\n\s*\n+`).ReplaceAllString(text, "\n")
	return strings.TrimSpace(text)
}

// SearchResult is one web search hit.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Searcher runs web searches, via Exa when an API key is set,
// otherwise via DuckDuckGo's HTML endpoint (no key needed).
// Base URLs are fields so tests can point at a mock server.
type Searcher struct {
	HTTP       *http.Client
	ExaAPIKey  string
	ExaBaseURL string
	DDGBaseURL string
}

func (s *Searcher) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return httpClient
}

func (s *Searcher) exaBase() string {
	if s.ExaBaseURL != "" {
		return s.ExaBaseURL
	}
	return "https://api.exa.ai"
}

func (s *Searcher) ddgBase() string {
	if s.DDGBaseURL != "" {
		return s.DDGBaseURL
	}
	return "https://html.duckduckgo.com"
}

// Search returns up to 5 results for query.
func (s *Searcher) Search(ctx context.Context, query string) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if s.ExaAPIKey != "" {
		return s.searchExa(ctx, query)
	}
	return s.searchDDG(ctx, query)
}

func (s *Searcher) searchExa(ctx context.Context, query string) ([]SearchResult, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "num_results": 5})
	req, err := http.NewRequestWithContext(ctx, "POST", s.exaBase()+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.ExaAPIKey)
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("exa search: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Text  string `json:"text"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, r := range out.Results {
		snippet := r.Text
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		results = append(results, SearchResult{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	return results, nil
}

var (
	ddgLinkRe    = regexp.MustCompile(`<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)
)

func (s *Searcher) searchDDG(ctx context.Context, query string) ([]SearchResult, error) {
	endpoint := s.ddgBase() + "/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Route-X/1.0)")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("duckduckgo search: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	html := string(data)
	links := ddgLinkRe.FindAllStringSubmatch(html, -1)
	snippets := ddgSnippetRe.FindAllStringSubmatch(html, -1)
	var results []SearchResult
	for i, m := range links {
		if len(results) >= 5 {
			break
		}
		snippet := ""
		if i < len(snippets) {
			snippet = strings.TrimSpace(stripHTML(snippets[i][1]))
		}
		results = append(results, SearchResult{
			Title:   strings.TrimSpace(stripHTML(m[2])),
			URL:     strings.TrimSpace(m[1]),
			Snippet: snippet,
		})
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no results (DuckDuckGo may have blocked the request)")
	}
	return results, nil
}
