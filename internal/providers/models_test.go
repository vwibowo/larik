package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"larik/internal/chatgpt"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/llm/ollama"
)

func TestOllamaModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[
			{"name":"qwen3:4b","size":2497293931,"details":{"parameter_size":"4.0B","context_length":262144},"capabilities":["completion","tools","thinking"]},
			{"name":"embeddinggemma:latest","size":621875917,"details":{},"capabilities":["embedding"]},
			{"name":"old-model","size":1}]}`))
	}))
	defer srv.Close()

	ms, err := EndpointOf("ollama", config.ProviderConfig{BaseURL: srv.URL + "/v1"}).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d models", len(ms))
	}
	q, e, old := ms[0], ms[1], ms[2]
	if !q.Chat || !q.Tools || !q.Thinking || !q.CapsKnown || q.Params != "4.0B" || q.Context != 262144 {
		t.Errorf("qwen3: %+v", q)
	}
	if e.Chat || !e.CapsKnown {
		t.Errorf("embedding model must be known not to chat: %+v", e)
	}
	if !old.Chat || old.CapsKnown {
		t.Errorf("model without capabilities should be a chat guess: %+v", old)
	}
}

func TestOpenAICompatibleModelsSendKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[{"id":"b-model"},{"id":"text-embedding-3-small"},{"id":"a-model"}]}`))
	}))
	defer srv.Close()

	ms, err := EndpointOf("mine", config.ProviderConfig{Type: "openai-compatible", BaseURL: srv.URL + "/v1", APIKey: "k1"}).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].ID != "a-model" || ms[1].ID != "b-model" || ms[2].Chat {
		t.Fatalf("want sorted ids with the embedding model marked: %+v", ms)
	}

	_, err = EndpointOf("mine", config.ProviderConfig{Type: "openai-compatible", BaseURL: srv.URL + "/v1", APIKey: "wrong"}).ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("want a rejected-key error, got %v", err)
	}
}

func TestListModelsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := EndpointOf("lmstudio", config.ProviderConfig{BaseURL: url + "/v1"}).ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "can't reach") {
		t.Fatalf("want a can't-reach error, got %v", err)
	}
}

func TestEndpointDefaults(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "gk")
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	e := EndpointFor(cfg, "groq")
	if e.BaseURL != "https://api.groq.com/openai/v1" || e.Key != "gk" {
		t.Fatalf("preset endpoint: %+v", e)
	}
	t.Setenv("MY_KEY", "mk")
	cfg.Providers["groq"] = config.ProviderConfig{APIKeyEnv: "MY_KEY", BaseURL: "http://proxy/v1"}
	if e := EndpointFor(cfg, "groq"); e.Key != "mk" || e.BaseURL != "http://proxy/v1" {
		t.Fatalf("configured endpoint: %+v", e)
	}
}

func TestOllamaUsesTheNativeAdapter(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	r, err := Resolve(cfg, "ollama/qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	p := r.Provider
	for { // the stall timeout and retries wrap it
		w, ok := p.(interface{ Unwrap() llm.Provider })
		if !ok {
			break
		}
		p = w.Unwrap()
	}
	if _, ok := p.(*ollama.Provider); !ok || r.Model != "qwen3:4b" {
		t.Fatalf("ollama should use the native adapter, got %T %q", p, r.Model)
	}
	if _, ok := r.Provider.(llm.ModelProber); !ok {
		t.Fatal("the wrappers must keep ollama's context-window and tool probes")
	}
}

func pullServer(t *testing.T, lines ...string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			http.NotFound(w, r)
			return
		}
		for _, l := range lines {
			w.Write([]byte(l + "\n"))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPull(t *testing.T) {
	srv := pullServer(t,
		`{"status":"pulling manifest"}`,
		`{"status":"pulling 3e4c","total":100,"completed":40}`,
		`{"status":"pulling 3e4c","total":100,"completed":100}`,
		`{"status":"success"}`)
	var got []PullProgress
	err := EndpointOf("ollama", config.ProviderConfig{BaseURL: srv.URL + "/v1"}).Pull(context.Background(), "qwen3:4b", func(p PullProgress) { got = append(got, p) })
	if err != nil || len(got) != 3 || got[1].Completed != 40 || got[1].Total != 100 {
		t.Fatalf("err %v progress %+v", err, got)
	}

	srv = pullServer(t, `{"status":"pulling manifest"}`, `{"error":"pull model manifest: file does not exist"}`)
	err = EndpointOf("ollama", config.ProviderConfig{BaseURL: srv.URL}).Pull(context.Background(), "nope:1b", func(PullProgress) {})
	if err == nil || err.Error() != "ollama.com has no model named nope:1b" {
		t.Fatalf("unknown model: %v", err)
	}

	srv = pullServer(t, `{"status":"pulling manifest"}`)
	if err := EndpointOf("ollama", config.ProviderConfig{BaseURL: srv.URL}).Pull(context.Background(), "m", func(PullProgress) {}); err == nil || !strings.Contains(err.Error(), "ended early") {
		t.Fatalf("a stream without success should fail: %v", err)
	}

	if err := EndpointOf("groq", config.ProviderConfig{}).Pull(context.Background(), "m", func(PullProgress) {}); err == nil {
		t.Fatal("only Ollama can download models")
	}
}

func TestCodexAdvertisesReasoningLevels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"slug":"gpt-test","visibility":"list","context_window":1000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`))
	}))
	defer srv.Close()
	e := Endpoint{Kind: Codex, BaseURL: srv.URL, Token: func(context.Context) (string, string, error) { return "t", "a", nil }}
	ms, err := e.ListModels(context.Background())
	if err != nil || len(ms) != 1 || !slices.Equal(ms[0].Efforts, []llm.Effort{llm.EffortLow, llm.EffortHigh}) {
		t.Fatalf("reasoning levels: %+v, %v", ms, err)
	}
}

func TestCodexNeedsSignIn(t *testing.T) {
	cfg := &config.Config{ConfigDir: t.TempDir(), Providers: map[string]config.ProviderConfig{}}
	if _, err := Resolve(cfg, "codex/gpt-6-luna"); err == nil || !strings.Contains(err.Error(), "/connect codex") {
		t.Fatalf("codex without a sign-in: %v", err)
	}
	if EndpointFor(cfg, Codex).Token != nil || slices.Contains(Usable(cfg), Codex) {
		t.Fatal("codex is only usable once signed in")
	}

	chatgpt.Save(chatgpt.Path(cfg.ConfigDir), chatgpt.Tokens{AccessToken: "t", AccountID: "a", Expires: time.Now().Add(time.Hour)})
	r, err := Resolve(cfg, "codex/gpt-6-luna")
	if err != nil || r.Provider.Name() != "codex" || r.Model != "gpt-6-luna" {
		t.Fatalf("signed in: %v %+v", err, r)
	}
	if !slices.Contains(Usable(cfg), Codex) {
		t.Fatal("codex should be usable once signed in")
	}

	// With no model list from the server, the known models are offered.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	cfg.Providers[Codex] = config.ProviderConfig{BaseURL: srv.URL}
	ms, err := EndpointFor(cfg, Codex).ListModels(context.Background())
	if err != nil || len(ms) != len(CodexModels) || !slices.ContainsFunc(ms, func(m Model) bool { return m.ID == "gpt-6-luna" && m.Tools }) {
		t.Fatalf("fallback models: %v %+v", err, ms)
	}
}
