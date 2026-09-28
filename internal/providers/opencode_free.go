package providers

import "fmt"

// OpenCodeFree is the Zen preset restricted to models currently documented as
// free and served by the Chat Completions endpoint. Keep this list aligned with
// https://opencode.ai/docs/en/zen/; free availability can change.
const OpenCodeFree = "opencode-free"

var openCodeFreeModels = map[string]bool{
	"big-pickle":                  true,
	"space-bunny-free":            true,
	"longcat-2.5-preview-free":    true,
	"mimo-v2.6-flash-free":        true,
	"mimo-v2.5-free":              true,
	"ling-3.0-flash-fin-free":     true,
	"nemotron-3-ultra-free":       true,
	"nemotron-3.5-lightning-free": true,
}

// ValidateModel prevents direct specs and fallback chains from selecting a
// paid or unsupported Zen model through the free preset.
func ValidateModel(provider, model string) error {
	if provider == OpenCodeFree && !openCodeFreeModels[model] {
		return fmt.Errorf("%s: model %q is not a documented free Chat Completions model", OpenCodeFree, model)
	}
	return nil
}
