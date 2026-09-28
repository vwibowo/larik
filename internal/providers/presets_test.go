package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
)

func TestNewPresetEndpointsAndKeys(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	for _, tc := range []struct{ name, url, env string }{
		{OpenCodeFree, "https://opencode.ai/zen/v1", "OPENCODE_API_KEY"},
		{"nvidia-nim", "https://integrate.api.nvidia.com/v1", "NVIDIA_API_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, "environment-key")
			if c, ok := ChoiceFor(tc.name); !ok || c.BaseURL != tc.url || c.KeyEnv != tc.env {
				t.Fatalf("choice: %+v, %v", c, ok)
			}
			if e := EndpointFor(cfg, tc.name); e.BaseURL != tc.url || e.Key != "environment-key" {
				t.Fatalf("environment endpoint: %+v", e)
			}
			if !slices.Contains(Usable(cfg), tc.name) {
				t.Fatal("provider with an environment key should be usable")
			}
			cfg.Providers[tc.name] = config.ProviderConfig{APIKey: "saved-key"}
			if e := EndpointFor(cfg, tc.name); e.Key != "saved-key" {
				t.Fatalf("saved key did not take precedence: %+v", e)
			}
			delete(cfg.Providers, tc.name)
		})
	}
}

func TestOpenCodeFreeRejectsPaidModelsAndFallbacks(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "key")
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	for _, id := range []string{"big-pickle", "space-bunny-free", "longcat-2.5-preview-free", "mimo-v2.6-flash-free", "mimo-v2.5-free", "ling-3.0-flash-fin-free", "nemotron-3-ultra-free", "nemotron-3.5-lightning-free"} {
		if _, err := Resolve(cfg, OpenCodeFree+"/"+id); err != nil {
			t.Errorf("free model %s: %v", id, err)
		}
	}
	for _, id := range []string{"gpt-5.5", "claude-sonnet-5", "jev-1.13-free", "unknown-free"} {
		if _, err := Resolve(cfg, OpenCodeFree+"/"+id); err == nil || !strings.Contains(err.Error(), "not a documented free") {
			t.Errorf("paid/unsupported model %s: %v", id, err)
		}
	}
	cfg.Providers["alias"] = config.ProviderConfig{Type: OpenCodeFree}
	if _, err := Resolve(cfg, "alias/gpt-5.5"); err == nil {
		t.Fatal("alias must retain the free-model restriction")
	}
	cfg.Providers[OpenCodeFree] = config.ProviderConfig{Type: "openai-compatible", BaseURL: "http://localhost:1/v1"}
	if _, err := Resolve(cfg, OpenCodeFree+"/gpt-5.5"); err == nil {
		t.Fatal("the built-in name must retain the free-model restriction")
	}
	delete(cfg.Providers, OpenCodeFree)
	cfg.Model = "ollama/qwen3:4b"
	cfg.Fallbacks = map[string][]string{cfg.Model: {OpenCodeFree + "/gpt-5.5"}}
	r, err := Resolve(cfg, cfg.Model)
	if err != nil || fmt.Sprintf("%T", r.Provider) == "*llm.fallback" {
		t.Fatalf("invalid paid fallback should be skipped: %v, %T", err, r.Provider)
	}
}

func TestOpenCodeFreeFiltersModelListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-5.5"},{"id":"jev-1.13-free"},{"id":"mimo-v2.6-flash-free"},{"id":"big-pickle"}]}`)
	}))
	defer srv.Close()
	e := EndpointOf(OpenCodeFree, config.ProviderConfig{BaseURL: srv.URL + "/v1", APIKey: "key"})
	models, err := e.ListModels(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "big-pickle" || models[1].ID != "mimo-v2.6-flash-free" {
		t.Fatalf("filtered models: %v %+v", err, models)
	}
}

func TestNewPresetsStreamTextAndToolCalls(t *testing.T) {
	for _, tc := range []struct{ name, model string }{
		{OpenCodeFree, "big-pickle"},
		{"nvidia-nim", "meta/llama-test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer saved-key" {
					t.Errorf("request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}]}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()
			cfg := &config.Config{Providers: map[string]config.ProviderConfig{tc.name: {BaseURL: srv.URL + "/v1", APIKey: "saved-key"}}}
			r, err := Resolve(cfg, tc.name+"/"+tc.model)
			if err != nil {
				t.Fatal(err)
			}
			var done llm.StreamEvent
			for ev, err := range r.Provider.Stream(context.Background(), llm.Request{Model: r.Model, Messages: []llm.Message{llm.UserText("hello")}, Tools: []llm.ToolSpec{{Name: "bash", Schema: []byte(`{"type":"object"}`)}}}) {
				if err != nil {
					t.Fatal(err)
				}
				if ev.Type == llm.EventDone {
					done = ev
				}
			}
			if requests != 1 || done.Message.Text() != "hello" || len(done.Message.ToolUses()) != 1 || done.StopReason != llm.StopToolUse {
				t.Fatalf("requests=%d done=%+v", requests, done)
			}
		})
	}
}
