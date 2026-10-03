package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
)

func TestCatwalkSupportedProviderIsRunnable(t *testing.T) {
	t.Setenv("CATWALK_TEST_KEY", "key-from-env")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key-from-env" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"catwalk-chat"}]}`))
	}))
	defer srv.Close()
	defer SetCatwalkProviders(nil)

	SetCatwalkProviders([]llm.CatwalkProvider{{
		ID: "example-cloud", Name: "Example Cloud", Type: "openai-compatible",
		APIKey: "$CATWALK_TEST_KEY", APIEndpoint: srv.URL + "/v1",
	}})
	choice, ok := ChoiceFor("example-cloud")
	if !ok || !choice.Catwalk || choice.Type != "openai-compatible" || choice.KeyEnv != "CATWALK_TEST_KEY" {
		t.Fatalf("choice = %+v, found %v", choice, ok)
	}
	if !slices.Contains(Names(), "example-cloud") {
		t.Fatal("Catwalk provider is not included in provider names")
	}

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"example-cloud": {Type: choice.Type, BaseURL: choice.BaseURL, APIKeyEnv: choice.KeyEnv},
		},
	}
	ep := EndpointFor(cfg, "example-cloud")
	if ep.Key != "key-from-env" || ep.BaseURL != srv.URL+"/v1" {
		t.Fatalf("endpoint = %+v", ep)
	}
	models, err := ep.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "catwalk-chat" {
		t.Fatalf("models = %+v, error = %v", models, err)
	}
}

func TestCatwalkChoicesOnlyExposeSupportedSafeEntries(t *testing.T) {
	defer SetCatwalkProviders(nil)
	SetCatwalkProviders([]llm.CatwalkProvider{
		{ID: "works", Name: "Works", Type: "anthropic", APIEndpoint: "$MISSING_ENDPOINT"},
		{ID: "unknown-protocol", Name: "Unknown", Type: "custom"},
		{ID: "bad/name", Name: "Bad", Type: "openai"},
		{ID: "openai", Name: "Spoofed built-in", Type: "openai"},
	})
	if c, ok := ChoiceFor("works"); !ok || c.Type != "anthropic" || c.BaseURL != "" {
		t.Fatalf("supported choice = %+v, found %v", c, ok)
	}
	if ep := EndpointOf("works", config.ProviderConfig{}); ep.BaseURL != "" {
		t.Fatalf("an unresolved Catwalk endpoint must not fall through to another provider: %+v", ep)
	}
	for _, name := range []string{"unknown-protocol", "bad/name"} {
		if _, ok := ChoiceFor(name); ok {
			t.Errorf("unsupported/invalid provider %q was exposed", name)
		}
	}
	if got, _ := ChoiceFor("openai"); got.Title != "OpenAI" {
		t.Fatalf("Catwalk should not replace built-in providers: %+v", got)
	}
}
