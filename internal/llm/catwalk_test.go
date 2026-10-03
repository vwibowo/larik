package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCatwalkAndCatalogEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`[{"id":"acme","name":"Acme","type":"openai","models":[{"id":"acme-chat","name":"Chat","context_window":32000,"default_max_tokens":4000,"cost_per_1m_in":1.5,"cost_per_1m_out":3,"can_reason":true}]}]`))
	}))
	defer srv.Close()

	providers, err := FetchCatwalk(context.Background(), srv.Client(), srv.URL+"/v2/providers")
	if err != nil {
		t.Fatal(err)
	}
	entries := CatalogEntries(providers)
	got := entries["acme-chat"]
	if got.Provider != "acme" || got.ContextWindow != 32000 || got.MaxOutput != 4000 || got.InputPrice != 1.5 || got.OutputPrice != 3 {
		t.Fatalf("entry = %+v", got)
	}
}

func TestFetchCatwalkRejectsBadResponses(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"json":   func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("not-json")) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if _, err := FetchCatwalk(context.Background(), srv.Client(), srv.URL); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCatalogEntriesSkipsEmptyModelIDs(t *testing.T) {
	got := CatalogEntries([]CatwalkProvider{{ID: "p", Models: []CatwalkModel{{ID: ""}}}})
	if len(got) != 0 {
		t.Fatalf("entries = %+v", got)
	}
}
