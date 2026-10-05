package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"larik/internal/llm"
)

// UserModelsAt reads personal catalog entries only, not merged project settings.
func UserModelsAt(path string) (map[string]llm.ModelInfo, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]llm.ModelInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Models map[string]llm.ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Models == nil {
		file.Models = map[string]llm.ModelInfo{}
	}
	return file.Models, nil
}

// PatchUserModels changes only known catalog fields in personal settings.
// A nil entry removes it. Other settings and unknown future model fields survive.
func (c *Config) PatchUserModels(changes map[string]*llm.ModelInfo) error {
	for name, info := range changes {
		if strings.TrimSpace(name) != name || name == "" || strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("invalid model name %q", name)
		}
		if info == nil {
			continue
		}
		if info.ID != name {
			return fmt.Errorf("model %q: id must match its name", name)
		}
		if info.ContextWindow < 0 || info.MaxOutput < 0 {
			return fmt.Errorf("model %q: token limits must be nonnegative", name)
		}
		for _, price := range []float64{info.InputPrice, info.OutputPrice, info.CacheRead, info.CacheWrite} {
			if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
				return fmt.Errorf("model %q: prices must be finite and nonnegative", name)
			}
		}
	}
	return updateJSON(c.UserConfigPath(), 0o644, func(raw map[string]any) {
		models, _ := raw["models"].(map[string]any)
		if models == nil {
			models = map[string]any{}
		}
		for name, info := range changes {
			if info == nil {
				delete(models, name)
				continue
			}
			entry, _ := models[name].(map[string]any)
			if entry == nil {
				entry = map[string]any{}
			}
			entry["id"] = info.ID
			entry["provider"] = info.Provider
			entry["context_window"] = info.ContextWindow
			entry["max_output"] = info.MaxOutput
			entry["input_price"] = info.InputPrice
			entry["output_price"] = info.OutputPrice
			entry["cache_read_price"] = info.CacheRead
			entry["cache_write_price"] = info.CacheWrite
			models[name] = entry
		}
		if len(models) == 0 {
			delete(raw, "models")
		} else {
			raw["models"] = models
		}
	})
}
