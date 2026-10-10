package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const page = `<!doctype html><html><head><title> Go  Docs </title><script>var tracking=1</script><style>.x{}</style></head>
<body>
<header><a href="/">Home</a> | <a href="/blog">Blog</a></header>
<nav><ul><li>Menu item one</li></ul></nav>
<div role="navigation">Sidebar links</div>
<main>
  <h1>Effective Go</h1>
  <p>Go is <strong>expressive</strong>. See <a href="/doc/faq">the FAQ</a>, <a href="#usage">usage</a> and <a href="spec">the spec</a>.</p>
  <img src="/icon.svg"><img src="/gopher.png" alt="Gopher">
  <pre><code>func main() {}</code></pre>
  <div aria-hidden="true">decorative</div>
</main>
<footer>Copyright footer</footer>
</body></html>`

func run(t *testing.T, tl FetchTool, in string) (string, bool) {
	t.Helper()
	r := tl.Run(context.Background(), nil, json.RawMessage(in))
	return r.Content, r.IsError
}

func TestFetchHTML(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/doc":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, page)
		case "/data.json":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"ok":true}`)
		case "/img":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte{0x89, 'P', 'N', 'G'})
		case "/moved":
			http.Redirect(w, r, "/doc", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tl := FetchTool{F: NewFetcher()}

	out, isErr := run(t, tl, `{"url":"`+srv.URL+`/doc"}`)
	if isErr {
		t.Fatal(out)
	}
	for _, want := range []string{"Title: Go Docs", "# Effective Go", "**expressive**", "[the FAQ](" + srv.URL + "/doc/faq)", "[usage](" + srv.URL + "/doc#usage)", "[the spec](" + srv.URL + "/spec)", "![Gopher](" + srv.URL + "/gopher.png)", "func main() {}", "<web_content"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, noise := range []string{"tracking", "Menu item", "Sidebar links", "Copyright footer", "decorative", "Blog", "icon.svg"} {
		if strings.Contains(out, noise) {
			t.Errorf("boilerplate %q should be stripped:\n%s", noise, out)
		}
	}

	// Cached: fetching again doesn't hit the server.
	before := hits.Load()
	run(t, tl, `{"url":"`+srv.URL+`/doc"}`)
	if hits.Load() != before {
		t.Error("second fetch should come from the cache")
	}

	if out, _ := run(t, tl, `{"url":"`+srv.URL+`/data.json"}`); !strings.Contains(out, `{"ok":true}`) {
		t.Errorf("json: %s", out)
	}
	if out, isErr := run(t, tl, `{"url":"`+srv.URL+`/img"}`); !isErr || !strings.Contains(out, "unsupported content type") {
		t.Errorf("image: %s", out)
	}
	if out, _ := run(t, tl, `{"url":"`+srv.URL+`/missing"}`); !strings.Contains(out, "HTTP 404") {
		t.Errorf("404: %s", out)
	}
	if out, _ := run(t, tl, `{"url":"`+srv.URL+`/moved"}`); !strings.Contains(out, "# Effective Go") {
		t.Errorf("same-host redirect should be followed: %s", out)
	}
	if out, isErr := run(t, tl, `{"url":"ftp://example.com/x"}`); !isErr || !strings.Contains(out, "only http and https") {
		t.Errorf("scheme: %s", out)
	}
}

func TestFetchCacheExpiresAndHonorsEntryLimit(t *testing.T) {
	f := NewFetcher()
	now := time.Now()
	f.mu.Lock()
	for i := 0; i < maxCacheEntries; i++ {
		key := fmt.Sprintf("https://example.test/%03d", i)
		f.storeCache(key, Page{URL: key, Text: "body"}, now)
		entry := f.cache[key]
		entry.lastUsed = now.Add(time.Duration(i) * time.Second)
		f.cache[key] = entry
	}
	// Make the newest page most recently used, then force one more insertion.
	newest := "https://example.test/127"
	entry := f.cache[newest]
	entry.lastUsed = now.Add(time.Hour)
	f.cache[newest] = entry
	f.storeCache("https://example.test/new", Page{Text: "new"}, now.Add(time.Second))
	if len(f.cache) != maxCacheEntries {
		t.Fatalf("cache entries = %d, want %d", len(f.cache), maxCacheEntries)
	}
	if _, ok := f.cache["https://example.test/000"]; ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if _, ok := f.cache[newest]; !ok {
		t.Fatal("most recently used entry was evicted")
	}
	if f.cacheBytes > maxCacheBytes {
		t.Fatalf("cache bytes = %d, limit %d", f.cacheBytes, maxCacheBytes)
	}
	f.pruneCache(now.Add(20 * time.Minute))
	if len(f.cache) != 0 || f.cacheBytes != 0 {
		t.Fatalf("expired entries remain: entries=%d bytes=%d", len(f.cache), f.cacheBytes)
	}
	f.mu.Unlock()
}

func TestFetchCacheRejectsOversizedEntry(t *testing.T) {
	f := NewFetcher()
	f.mu.Lock()
	f.storeCache("https://example.test/large", Page{Text: strings.Repeat("x", maxCacheBytes+1)}, time.Now())
	f.mu.Unlock()
	if len(f.cache) != 0 || f.cacheBytes != 0 {
		t.Fatalf("oversized entry was cached: entries=%d bytes=%d", len(f.cache), f.cacheBytes)
	}
}

func TestFetchCacheHonorsByteLimit(t *testing.T) {
	f := NewFetcher()
	f.mu.Lock()
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("https://example.test/large-%d", i)
		f.storeCache(key, Page{Text: strings.Repeat("x", 2<<20)}, time.Now())
	}
	got := f.cacheBytes
	entries := len(f.cache)
	f.mu.Unlock()
	if got > maxCacheBytes || entries >= 10 {
		t.Fatalf("byte cap not enforced: bytes=%d entries=%d", got, entries)
	}
}

func TestPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("a", 50)+strings.Repeat("b", 50))
	}))
	defer srv.Close()
	tl := FetchTool{F: NewFetcher()}
	out, _ := run(t, tl, `{"url":"`+srv.URL+`","max_length":50}`)
	if !strings.Contains(out, strings.Repeat("a", 50)+"\n</web_content>") || !strings.Contains(out, "start=50") {
		t.Errorf("first chunk:\n%s", out)
	}
	out, _ = run(t, tl, `{"url":"`+srv.URL+`","start":50,"max_length":50}`)
	if !strings.Contains(out, strings.Repeat("b", 50)) || strings.Contains(out, "start=") {
		t.Errorf("second chunk:\n%s", out)
	}
}

func TestCrossHostRedirectIsReported(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "should not be fetched")
	}))
	defer target.Close()
	// "localhost" and "127.0.0.1" are different hosts for permission purposes.
	other := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other+"/landing", http.StatusMovedPermanently)
	}))
	defer src.Close()
	out, isErr := run(t, FetchTool{F: NewFetcher()}, `{"url":"`+src.URL+`"}`)
	if isErr || !strings.Contains(out, "redirected (HTTP 301) to a different host") || !strings.Contains(out, other+"/landing") || strings.Contains(out, "should not be fetched") {
		t.Errorf("cross-host redirect:\n%s", out)
	}
}

func TestWWWRedirectNeedsSeparatePermission(t *testing.T) {
	f := NewFetcher()
	from, _ := http.NewRequest(http.MethodGet, "https://www.example.com/start", nil)
	to, _ := http.NewRequest(http.MethodGet, "https://example.com/next", nil)
	if err := f.client.CheckRedirect(to, []*http.Request{from}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("www to apex redirect was followed: %v", err)
	}
}

func TestBlockedAddresses(t *testing.T) {
	f := NewFetcher()
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://[fe80::1]/", "http://0.0.0.0:9/"} {
		if _, err := f.Fetch(context.Background(), u); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: want blocked, got %v", u, err)
		}
	}
}

func TestSearchBackends(t *testing.T) {
	var gotReq *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch r.URL.Path {
		case "/brave":
			io.WriteString(w, `{"web":{"results":[{"title":"Go","url":"https://go.dev","description":"The <strong>Go</strong> language"}]}}`)
		case "/tavily":
			io.WriteString(w, `{"results":[{"title":"Tav","url":"https://t.example","content":"tavily snippet"}]}`)
		case "/search":
			io.WriteString(w, `{"results":[{"title":"S1","url":"https://s1","content":"c1"},{"title":"S2","url":"https://s2","content":"c2"}]}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	b, _ := NewSearcher(SearchConfig{Provider: "brave", APIKey: "bk", URL: srv.URL + "/brave"})
	rs, err := b.Search(ctx, "golang generics", 5)
	if err != nil || len(rs) != 1 || rs[0].Snippet != "The Go language" {
		t.Fatalf("brave: %+v %v", rs, err)
	}
	if gotReq.Header.Get("X-Subscription-Token") != "bk" || gotReq.URL.Query().Get("q") != "golang generics" || gotReq.URL.Query().Get("count") != "5" {
		t.Errorf("brave request: %v %v", gotReq.Header, gotReq.URL)
	}

	tv, _ := NewSearcher(SearchConfig{Provider: "tavily", APIKey: "tk", URL: srv.URL + "/tavily"})
	rs, err = tv.Search(ctx, "q", 3)
	if err != nil || rs[0].Title != "Tav" || gotReq.Header.Get("Authorization") != "Bearer tk" || !strings.Contains(gotBody, `"max_results":3`) {
		t.Errorf("tavily: %+v %v %s", rs, err, gotBody)
	}

	sx, _ := NewSearcher(SearchConfig{Provider: "searxng", URL: srv.URL})
	rs, _ = sx.Search(ctx, "q", 1)
	if len(rs) != 1 || gotReq.URL.Query().Get("format") != "json" {
		t.Errorf("searxng: %+v", rs)
	}

	// Output formatting and site filter.
	out := SearchTool{S: b}.Run(ctx, nil, json.RawMessage(`{"query":"generics","site":"go.dev"}`))
	if !strings.Contains(out.Content, "1. Go\n   https://go.dev\n   The Go language") || gotReq.URL.Query().Get("q") != "generics site:go.dev" {
		t.Errorf("tool output:\n%s", out.Content)
	}
}

func TestSearcherSelection(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("SEARXNG_URL", "")
	// The keyless backend is not auto-selected: it answers too few real
	// queries to offer web_search on its own.
	if s, err := NewSearcher(SearchConfig{}); s != nil || err != nil {
		t.Error("no backend configured: no search tool")
	}
	t.Setenv("TAVILY_API_KEY", "x")
	if s, _ := NewSearcher(SearchConfig{}); s == nil || s.Name() != "tavily" {
		t.Error("TAVILY_API_KEY should select tavily")
	}
	if s, _ := NewSearcher(SearchConfig{Disabled: true}); s != nil {
		t.Error("disabled")
	}
	if _, err := NewSearcher(SearchConfig{Provider: "brave"}); err == nil {
		t.Error("brave without key should error")
	}
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("BRAVE_API_KEY", "k")
	if s, _ := NewSearcher(SearchConfig{}); s == nil || s.Name() != "brave" {
		t.Error("BRAVE_API_KEY should select brave")
	}
	// ddg is reachable only by asking for it.
	if s, _ := NewSearcher(SearchConfig{Provider: "ddg"}); s == nil || s.Name() != "ddg" {
		t.Error("ddg should be selectable by name")
	}
	if _, err := NewSearcher(SearchConfig{Provider: "nope"}); err == nil {
		t.Error("an unknown provider should error")
	}
}

func TestDDGReadsAbstractResultsAndNestedTopics(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		io.WriteString(w, `{
			"Heading": "Go",
			"AbstractText": "A language.",
			"AbstractURL": "https://duckduckgo.com/Go",
			"Results": [{"FirstURL":"https://go.dev","Text":"Go - the language"}],
			"RelatedTopics": [
				{"Name":"Languages","Topics":[{"FirstURL":"https://go.dev/doc","Text":"Docs — how to"}]},
				{"FirstURL":"https://go.dev","Text":"a duplicate, dropped"}
			]
		}`)
	}))
	defer srv.Close()

	d, err := NewSearcher(SearchConfig{Provider: "ddg", URL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := d.Search(context.Background(), "go language", 10)
	if err != nil {
		t.Fatal(err)
	}
	// Abstract first, then direct results, then nested topics; the repeated
	// URL appears once.
	want := []string{"https://duckduckgo.com/Go", "https://go.dev", "https://go.dev/doc"}
	if len(rs) != len(want) {
		t.Fatalf("got %d results: %+v", len(rs), rs)
	}
	for i, u := range want {
		if rs[i].URL != u {
			t.Errorf("result %d url = %s, want %s", i, rs[i].URL, u)
		}
	}
	if rs[0].Title != "Go" || rs[1].Title != "Go" || rs[2].Title != "Docs" {
		t.Errorf("titles = %q, %q, %q", rs[0].Title, rs[1].Title, rs[2].Title)
	}
	if gotQuery.Get("format") != "json" || gotQuery.Get("q") != "go language" {
		t.Errorf("query = %v", gotQuery)
	}
}

func TestDDGHonorsTheResultLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Results":[
			{"FirstURL":"https://a","Text":"A"},
			{"FirstURL":"https://b","Text":"B"},
			{"FirstURL":"https://c","Text":"C"}]}`)
	}))
	defer srv.Close()
	d, _ := NewSearcher(SearchConfig{Provider: "ddg", URL: srv.URL + "/"})
	rs, err := d.Search(context.Background(), "q", 2)
	if err != nil || len(rs) != 2 {
		t.Errorf("got %d results (%v), want 2", len(rs), err)
	}
}

// This API reports a block or rate limit as an empty 200, which must not
// read to the model as "nothing found".
func TestDDGEmptyAnswerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"RelatedTopics":[]}`)
	}))
	defer srv.Close()
	d, _ := NewSearcher(SearchConfig{Provider: "ddg", URL: srv.URL + "/"})
	if _, err := d.Search(context.Background(), "q", 5); err == nil {
		t.Error("an empty answer should be an error, not zero results")
	}
}

func TestProxiedFetchStillBlocksMetadata(t *testing.T) {
	proxy, _ := url.Parse("http://127.0.0.1:9")
	viaProxy := checkedProxy(func(*http.Request) (*url.URL, error) { return proxy, nil })
	for target, blocked := range map[string]bool{
		"http://169.254.169.254/latest/meta-data/": true,
		"http://[fd00:ec2::254]/":                  true,
		"http://93.184.215.14/":                    false,
	} {
		req, _ := http.NewRequest("GET", target, nil)
		_, err := viaProxy(req)
		if got := errors.Is(err, ErrBlockedAddress); got != blocked {
			t.Errorf("%s through a proxy: blocked=%v (%v), want %v", target, got, err, blocked)
		}
	}
}
