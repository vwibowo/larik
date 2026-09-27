package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"larik/internal/config"
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
	if _, ok := r.Provider.(*ollama.Provider); !ok || r.Model != "qwen3:4b" {
		t.Fatalf("ollama should use the native adapter, got %T %q", r.Provider, r.Model)
	}
}
