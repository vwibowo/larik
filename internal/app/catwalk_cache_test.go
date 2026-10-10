package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/llm"
	"larik/internal/providers"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRefreshCatwalkCacheLoadsOnNextStartup(t *testing.T) {
	const modelID = "cache-test-model-20261010"
	t.Cleanup(func() {
		llm.RestoreCatalogEntry(modelID)
		providers.SetCatwalkProviders(nil)
	})

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`[{"id":"cache-test-provider","name":"Cache Test","type":"openai-compatible","api_endpoint":"https://example.test/v1","models":[{"id":"` + modelID + `","context_window":64000,"default_max_tokens":8000,"cost_per_1m_in":1,"cost_per_1m_out":2}]}]`)),
			Request:    req,
		}, nil
	})}

	cachePath := filepath.Join(t.TempDir(), catwalkCacheName)
	if err := refreshCatwalkCache(context.Background(), cachePath, "https://catwalk.test/providers", client); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %o, want 600", info.Mode().Perm())
	}

	entries, err := readCatwalkCache(cachePath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read cache: entries=%d err=%v", len(entries), err)
	}
	LoadCatwalkCache(filepath.Dir(cachePath))
	got, ok := llm.Catalog[modelID]
	if !ok || got.ContextWindow != 64_000 || got.Provider != "cache-test-provider" {
		t.Fatalf("cached catalog entry = %+v, exists=%v", got, ok)
	}
}

func TestReadCatwalkCacheRejectsInvalidData(t *testing.T) {
	path := filepath.Join(t.TempDir(), catwalkCacheName)
	if err := os.WriteFile(path, []byte(`{"not":"an array"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCatwalkCache(path); err == nil {
		t.Fatal("invalid cache unexpectedly accepted")
	}
}

func TestReadCatwalkCacheRejectsStaleData(t *testing.T) {
	path := filepath.Join(t.TempDir(), catwalkCacheName)
	if err := os.WriteFile(path, []byte(`[{"id":"provider","models":[{"id":"model"}]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-catwalkCacheTTL - time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := readCatwalkCache(path); err == nil {
		t.Fatal("stale cache unexpectedly accepted")
	}
}
