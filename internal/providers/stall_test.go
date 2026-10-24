package providers

import (
	"testing"
	"time"

	"larik/internal/claudeagent"
	"larik/internal/config"
)

func TestClaudeCLIStallTimeout(t *testing.T) {
	for _, tc := range []struct {
		name             string
		global, provider int
		want             time.Duration
	}{
		{"default", 0, 0, 5 * time.Minute},
		{"global", 90, 0, 90 * time.Second},
		{"provider", 90, 20, 20 * time.Second},
		{"disabled global", -1, 0, 0},
		{"disabled provider", 90, -1, 0},
		{"override disabled global", -1, 20, 20 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{StallTimeout: tc.global, Providers: map[string]config.ProviderConfig{ClaudeCLI: {StallTimeout: tc.provider}}}
			resolved, err := resolveSpec(cfg, ClaudeCLI+"/sonnet")
			if err != nil {
				t.Fatal(err)
			}
			runtime, ok := resolved.Runtime.(*claudeagent.Runtime)
			if !ok || runtime.StallTimeout != tc.want {
				t.Fatalf("runtime=%+v, want timeout %s", resolved.Runtime, tc.want)
			}
		})
	}
}

func TestStallTimeout(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"homelab": {Type: "openai-compatible", BaseURL: "http://llama.home.lab/v1"},
		"slow":    {Type: "openai-compatible", BaseURL: "http://slow.lan/v1", StallTimeout: 1800},
		"patient": {Type: "openai-compatible", BaseURL: "http://patient.lan/v1", StallTimeout: -1},
	}}
	for name, want := range map[string]time.Duration{
		"anthropic":  stallTimeoutHosted,
		"nvidia-nim": stallTimeoutHosted,
		"ollama":     stallTimeoutLocal,
		"homelab":    stallTimeoutLocal,
		"slow":       30 * time.Minute,
		"patient":    0,
	} {
		if got := stallTimeout(cfg, name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	// The top-level setting replaces the defaults, not a provider's own.
	cfg.StallTimeout = 60
	for name, want := range map[string]time.Duration{"anthropic": time.Minute, "ollama": time.Minute, "slow": 30 * time.Minute, "patient": 0} {
		if got := stallTimeout(cfg, name); got != want {
			t.Errorf("with stall_timeout 60, %s: %s, want %s", name, got, want)
		}
	}
}
