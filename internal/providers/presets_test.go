package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
)

func TestNewPresetEndpointsAndKeys(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	for _, tc := range []struct{ name, url, env string }{
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

func TestOpenCodeFreeIsNotBuiltin(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "legacy-key")
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	if _, ok := ChoiceFor("opencode-free"); ok || slices.Contains(Usable(cfg), "opencode-free") {
		t.Fatal("removed provider is still offered")
	}
	if _, err := Resolve(cfg, "opencode-free/big-pickle"); err == nil {
		t.Fatal("removed provider still resolves without custom configuration")
	}
}

func TestClaudeCodeCLIChoiceAndResolution(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	choice, ok := ChoiceFor(ClaudeCLI)
	if !ok || choice.Title != "Claude Code CLI" || !choice.CLI {
		t.Fatalf("CLI choice = %+v, %v", choice, ok)
	}
	if !slices.Contains(Usable(cfg), ClaudeCLI) {
		t.Fatal("Claude Code CLI should be available to connect without a saved key")
	}
	r, err := Resolve(cfg, ClaudeCLI+"/sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if r.String() != ClaudeCLI+"/sonnet" || r.Runtime == nil {
		t.Fatalf("resolved runtime = %+v", r)
	}
}

func TestNVIDIAPresetStreamsTextAndToolCalls(t *testing.T) {
	for _, tc := range []struct{ name, model string }{
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

// TestCustomProviderKeepsItsName checks that a custom provider built on
// the OpenAI or Anthropic adapter reports its own name, so reasoning from
// one organization isn't replayed to another and fallback chains keep
// both providers.
func TestCustomProviderKeepsItsName(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"work":      {Type: "openai", APIKey: "k"},
		"work-anth": {Type: "anthropic", APIKey: "k"},
	}}
	for _, name := range []string{"work", "work-anth"} {
		r, err := Resolve(cfg, name+"/m")
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Provider.Name(); got != name {
			t.Errorf("%s: provider name %q", name, got)
		}
	}
}
