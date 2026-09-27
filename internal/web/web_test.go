package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
}
