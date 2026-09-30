package providers

import (
	"testing"
	"time"

	"larik/internal/config"
)

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
