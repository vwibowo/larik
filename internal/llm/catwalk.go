package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const CatwalkURL = "https://catwalk.charm.land/v2/providers"

// CatwalkProvider is the public provider metadata returned by Catwalk.
type CatwalkProvider struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	APIKey      string         `json:"api_key"`
	APIEndpoint string         `json:"api_endpoint"`
	Type        string         `json:"type"`
	Models      []CatwalkModel `json:"models"`
}

// CatwalkModel describes public model capabilities and list pricing.
type CatwalkModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow int      `json:"context_window"`
	MaxOutput     int      `json:"default_max_tokens"`
	InputPrice    float64  `json:"cost_per_1m_in"`
	OutputPrice   float64  `json:"cost_per_1m_out"`
	CacheWrite    float64  `json:"cost_per_1m_in_cached"`
	CacheRead     float64  `json:"cost_per_1m_out_cached"`
	CanReason     bool     `json:"can_reason"`
	Reasoning     []string `json:"reasoning_levels"`
}

// FetchCatwalk retrieves Catwalk's provider/model metadata. It does not
// return account-specific quota or remaining usage. The response is bounded
// because the public catalog is large.
func FetchCatwalk(ctx context.Context, client *http.Client, url string) ([]CatwalkProvider, error) {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	if url == "" {
		url = CatwalkURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("Catwalk returned " + resp.Status)
	}
	const maxResponse = 16 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponse {
		return nil, errors.New("Catwalk response exceeds size limit")
	}
	var providers []CatwalkProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, err
	}
	for i := range providers {
		for j := range providers[i].Models {
			providers[i].Models[j].ID = strings.TrimSpace(providers[i].Models[j].ID)
		}
	}
	return providers, nil
}

// CatalogEntries converts Catwalk providers into model catalog entries.
func CatalogEntries(providers []CatwalkProvider) map[string]ModelInfo {
	entries := make(map[string]ModelInfo)
	for _, p := range providers {
		for _, m := range p.Models {
			if m.ID == "" {
				continue
			}
			entries[m.ID] = ModelInfo{
				ID: m.ID, Provider: p.ID, ContextWindow: m.ContextWindow,
				MaxOutput: m.MaxOutput, InputPrice: m.InputPrice,
				OutputPrice: m.OutputPrice, CacheWrite: m.CacheWrite, CacheRead: m.CacheRead,
			}
		}
	}
	return entries
}
