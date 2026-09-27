package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SearchConfig selects the search backend ("web.search" in settings).
type SearchConfig struct {
	Provider  string `json:"provider,omitempty"` // brave, tavily, searxng
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	URL       string `json:"url,omitempty"` // SearXNG instance or API base override
	Disabled  bool   `json:"disabled,omitempty"`
}

type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Searcher runs web searches.
type Searcher interface {
	Name() string
	Search(ctx context.Context, query string, n int) ([]Result, error)
}

// NewSearcher picks a backend from config or, failing that, from the
// environment (BRAVE_API_KEY, TAVILY_API_KEY, SEARXNG_URL). It returns
// nil when none is available.
func NewSearcher(cfg SearchConfig) (Searcher, error) {
	if cfg.Disabled {
		return nil, nil
	}
	key := cfg.APIKey
	if key == "" && cfg.APIKeyEnv != "" {
		key = os.Getenv(cfg.APIKeyEnv)
	}
	provider := cfg.Provider
	if provider == "" {
		switch {
		case os.Getenv("BRAVE_API_KEY") != "":
			provider = "brave"
		case os.Getenv("TAVILY_API_KEY") != "":
			provider = "tavily"
		case os.Getenv("SEARXNG_URL") != "":
			provider = "searxng"
		default:
			return nil, nil
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	switch provider {
	case "brave":
		if key == "" {
			key = os.Getenv("BRAVE_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("brave search needs BRAVE_API_KEY")
		}
		base := cfg.URL
		if base == "" {
			base = "https://api.search.brave.com/res/v1/web/search"
		}
		return &brave{key: key, url: base, client: client}, nil
	case "tavily":
		if key == "" {
			key = os.Getenv("TAVILY_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("tavily search needs TAVILY_API_KEY")
		}
		base := cfg.URL
		if base == "" {
			base = "https://api.tavily.com/search"
		}
		return &tavily{key: key, url: base, client: client}, nil
	case "searxng":
		base := cfg.URL
		if base == "" {
			base = os.Getenv("SEARXNG_URL")
		}
		if base == "" {
			return nil, fmt.Errorf("searxng search needs a url (or SEARXNG_URL)")
		}
		return &searxng{url: strings.TrimRight(base, "/"), client: client}, nil
	}
	return nil, fmt.Errorf("unknown search provider %q (brave, tavily, searxng)", provider)
}

type brave struct {
	key, url string
	client   *http.Client
}

func (b *brave) Name() string { return "brave" }

func (b *brave) Search(ctx context.Context, query string, n int) ([]Result, error) {
	u := b.url + "?" + url.Values{"q": {query}, "count": {fmt.Sprint(n)}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.key)
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := doJSON(b.client, req, &out); err != nil {
		return nil, err
	}
	var rs []Result
	for _, r := range out.Web.Results {
		rs = append(rs, Result{Title: r.Title, URL: r.URL, Snippet: stripTags(r.Description)})
	}
	return rs, nil
}

type tavily struct {
	key, url string
	client   *http.Client
}

func (t *tavily) Name() string { return "tavily" }

func (t *tavily) Search(ctx context.Context, query string, n int) ([]Result, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "max_results": n, "search_depth": "basic"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.key)
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := doJSON(t.client, req, &out); err != nil {
		return nil, err
	}
	var rs []Result
	for _, r := range out.Results {
		rs = append(rs, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return rs, nil
}

type searxng struct {
	url    string
	client *http.Client
}

func (s *searxng) Name() string { return "searxng" }

func (s *searxng) Search(ctx context.Context, query string, n int) ([]Result, error) {
	u := s.url + "/search?" + url.Values{"q": {query}, "format": {"json"}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := doJSON(s.client, req, &out); err != nil {
		return nil, fmt.Errorf("%w (is the JSON format enabled on the SearXNG instance?)", err)
	}
	var rs []Result
	for i, r := range out.Results {
		if i == n {
			break
		}
		rs = append(rs, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return rs, nil
}

func doJSON(c *http.Client, req *http.Request, out any) error {
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("search API returned %s: %s", resp.Status, msg)
	}
	return json.Unmarshal(body, out)
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
