package llm

import "testing"

func TestKnownSeparatesCatalogFactsFromDefaults(t *testing.T) {
	Catalog["test-model-9"] = ModelInfo{ID: "test-model-9", ContextWindow: 400_000, MaxOutput: 32_000, InputPrice: 1}
	t.Cleanup(func() { delete(Catalog, "test-model-9") })

	for _, spec := range []string{
		"test-model-9",            // the entry itself
		"openrouter/test-model-9", // a vendor-prefixed id
		"test-model-9-20260514",   // a dated release of the same model
	} {
		info, ok := Known(spec)
		if !ok || info.ContextWindow != 400_000 || info.ID != spec {
			t.Errorf("Known(%q) = %+v, %v", spec, info, ok)
		}
	}

	// A guess is never passed off as a catalog fact.
	for _, spec := range []string{"sonnet", "test-model-9-2026051", "test-model-9-2026x514", "", "-20260514"} {
		if info, ok := Known(spec); ok {
			t.Errorf("Known(%q) claimed to know %+v", spec, info)
		}
	}

	// Lookup keeps answering for unknown models, with conservative defaults.
	if info := Lookup("sonnet"); info.ContextWindow != DefaultContextWindow || info.InputPrice != 0 {
		t.Errorf("Lookup of an unknown model = %+v", info)
	}
}
