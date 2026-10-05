package llm

import "strings"

// ModelInfo describes a model's limits and list price (USD per million tokens).
// Zero prices mean "unknown"; cost is then not reported.
type ModelInfo struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	ContextWindow int     `json:"context_window"`
	MaxOutput     int     `json:"max_output"`
	InputPrice    float64 `json:"input_price,omitempty"`
	OutputPrice   float64 `json:"output_price,omitempty"`
	CacheRead     float64 `json:"cache_read_price,omitempty"`
	CacheWrite    float64 `json:"cache_write_price,omitempty"`
}

// Catalog holds known models. Users can add or override entries in config.
var Catalog = map[string]ModelInfo{}

var builtInCatalog = map[string]ModelInfo{}

// RestoreCatalogEntry drops a runtime override, restoring built-in metadata if present.
// The next setup may layer trusted config and Catwalk metadata over it again.
func RestoreCatalogEntry(id string) {
	if info, ok := builtInCatalog[id]; ok {
		Catalog[id] = info
	} else {
		delete(Catalog, id)
	}
}

func init() {
	for _, m := range []ModelInfo{
		// Anthropic (first-party list prices).
		{ID: "claude-opus-5", Provider: "anthropic", ContextWindow: 1_000_000, MaxOutput: 128_000, InputPrice: 5, OutputPrice: 25, CacheRead: 0.5, CacheWrite: 6.25},
		{ID: "claude-opus-5-5", Provider: "anthropic", ContextWindow: 1_000_000, MaxOutput: 128_000, InputPrice: 4, OutputPrice: 20, CacheRead: 0.2, CacheWrite: 5},
		{ID: "claude-fable-5-1", Provider: "anthropic", ContextWindow: 1_000_000, MaxOutput: 128_000, InputPrice: 10, OutputPrice: 50, CacheRead: 0.25, CacheWrite: 12.5},
		{ID: "claude-sonnet-5", Provider: "anthropic", ContextWindow: 1_000_000, MaxOutput: 128_000, InputPrice: 2, OutputPrice: 10, CacheRead: 0.2, CacheWrite: 2.5},
		{ID: "claude-opus-4-8", Provider: "anthropic", ContextWindow: 1_000_000, MaxOutput: 128_000, InputPrice: 5, OutputPrice: 25, CacheRead: 0.5, CacheWrite: 6.25},
		// Google (prices omitted: introductory pricing changes 2027-01-01).
		{ID: "gemini-3.8-flash", Provider: "gemini", ContextWindow: 1_048_576, MaxOutput: 65_536},
		{ID: "gemini-3.1-pro-preview", Provider: "gemini", ContextWindow: 1_048_576, MaxOutput: 65_536},
		{ID: "claude-haiku-4-5", Provider: "anthropic", ContextWindow: 200_000, MaxOutput: 64_000, InputPrice: 1, OutputPrice: 5, CacheRead: 0.1, CacheWrite: 1.25},
		// Codex models on a ChatGPT plan (no per-token price). No provider,
		// so bare ids keep routing to openai by name.
		{ID: "gpt-6-astra", ContextWindow: 272_000, MaxOutput: 128_000},
		{ID: "gpt-6-sol", ContextWindow: 272_000, MaxOutput: 128_000},
		{ID: "gpt-6-luna", ContextWindow: 272_000, MaxOutput: 128_000},
		{ID: "gpt-5.6-sol", ContextWindow: 272_000, MaxOutput: 128_000},
		{ID: "gpt-5.6-terra", ContextWindow: 272_000, MaxOutput: 128_000},
		{ID: "gpt-5.6-luna", ContextWindow: 272_000, MaxOutput: 128_000},
	} {
		Catalog[m.ID] = m
		builtInCatalog[m.ID] = m
	}
}

// DefaultContextWindow is assumed for models missing from the catalog.
const DefaultContextWindow = 128_000

// Lookup returns catalog info for a model, falling back to conservative defaults.
func Lookup(model string) ModelInfo {
	if m, ok := Known(model); ok {
		return m
	}
	return ModelInfo{ID: model, ContextWindow: DefaultContextWindow, MaxOutput: 16_000}
}

// Known reports what the catalog says about a model, and whether it says
// anything at all. Callers that must not guess (reporting a context window,
// say) use this instead of Lookup's defaults.
func Known(model string) (ModelInfo, bool) {
	if m, ok := Catalog[model]; ok {
		return m, true
	}
	// Tolerate provider-prefixed ids such as "anthropic/claude-sonnet-5" via OpenRouter.
	if i := strings.LastIndex(model, "/"); i >= 0 {
		if m, ok := Catalog[model[i+1:]]; ok {
			m.ID = model
			return m, true
		}
	}
	// Tolerate a dated release of a catalogued model, e.g.
	// "claude-sonnet-5-20260514": the limits and prices are the model's.
	if base, ok := trimReleaseDate(model); ok {
		if m, ok := Catalog[base]; ok {
			m.ID = model
			return m, true
		}
	}
	return ModelInfo{}, false
}

// trimReleaseDate strips a trailing "-YYYYMMDD" version suffix.
func trimReleaseDate(model string) (string, bool) {
	const dateLen = len("-20060102")
	if len(model) <= dateLen || model[len(model)-dateLen] != '-' {
		return "", false
	}
	for _, r := range model[len(model)-dateLen+1:] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return model[:len(model)-dateLen], true
}

// Cost returns the USD cost of a usage record, or 0 if the price is unknown.
func (m ModelInfo) Cost(u Usage) float64 {
	return (float64(u.Input)*m.InputPrice +
		float64(u.Output)*m.OutputPrice +
		float64(u.CacheRead)*m.CacheRead +
		float64(u.CacheWrite)*m.CacheWrite) / 1e6
}
