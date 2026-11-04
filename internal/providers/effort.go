package providers

import (
	"slices"
	"strings"

	"larik/internal/config"
	"larik/internal/llm"
)

// AvailableEfforts lists the reasoning levels a model's adapter can send.
// Unknown capabilities stay on the provider default rather than guessing.
func AvailableEfforts(cfg *config.Config, provider, model string, listed []Model) []llm.Effort {
	defaultOnly := []llm.Effort{llm.EffortDefault}
	basic := []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	kind := EndpointFor(cfg, provider)
	for _, candidate := range listed {
		if candidate.ID == model && len(candidate.Efforts) > 0 {
			levels := append([]llm.Effort(nil), defaultOnly...)
			for _, e := range []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax} {
				if slices.Contains(candidate.Efforts, e) {
					levels = append(levels, e)
				}
			}
			return levels
		}
	}
	switch {
	case kind.Kind == ClaudeCLI:
		for _, candidate := range listed {
			if candidate.ID == model && candidate.CapsKnown {
				return defaultOnly
			}
		}
		return []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortMax}
	case kind.IsOllama():
		for _, candidate := range listed {
			if candidate.ID == model && candidate.CapsKnown && candidate.Thinking {
				return basic
			}
		}
	case kind.Kind == "gemini":
		if strings.HasPrefix(model, "gemini-3") {
			return basic
		}
	case kind.Kind == "anthropic":
		if strings.HasPrefix(model, "claude-") {
			if strings.Contains(model, "claude-3") || strings.Contains(model, "-4-0") ||
				strings.Contains(model, "-4-1") || strings.Contains(model, "-4-5") ||
				strings.Contains(model, "sonnet-4-2") || strings.Contains(model, "opus-4-2") {
				return basic
			}
			if strings.Contains(model, "-5") || strings.Contains(model, "-4-") {
				return []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortMax}
			}
		}
	case kind.Kind == "openai", kind.Kind == Codex:
		if strings.Contains(model, "chat-latest") {
			break
		}
		if strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6") || strings.Contains(model, "codex") {
			return []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh}
		}
		if strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") {
			return basic
		}
	}
	return defaultOnly
}
