package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"larik/internal/hooks"
)

// PersonalHooksAt reads only the user's config, not merged or shared hooks.
// Maps retain fields unknown to the native editor when an entry is edited.
func PersonalHooksAt(path string) (map[string][]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := map[string][]any{}
	if len(raw["hooks"]) == 0 || string(raw["hooks"]) == "null" {
		return out, nil
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(raw["hooks"], &events); err != nil {
		return nil, fmt.Errorf("hooks: %w", err)
	}
	for _, ev := range hooks.Events {
		b, ok := events[string(ev)]
		if !ok {
			continue
		}
		var matchers []any
		if err := json.Unmarshal(b, &matchers); err != nil {
			return nil, fmt.Errorf("hooks.%s: %w", ev, err)
		}
		for _, v := range matchers {
			m, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s: invalid matcher", ev)
			}
			if pattern, exists := m["matcher"]; exists {
				if _, ok := pattern.(string); !ok {
					return nil, fmt.Errorf("hooks.%s: invalid matcher pattern", ev)
				}
			}
			if _, ok := m["hooks"].([]any); !ok {
				return nil, fmt.Errorf("hooks.%s: invalid hook list", ev)
			}
			for _, h := range m["hooks"].([]any) {
				hook, ok := h.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("hooks.%s: invalid hook", ev)
				}
				for _, key := range []string{"type", "command", "prompt", "model"} {
					if value, exists := hook[key]; exists {
						if _, ok := value.(string); !ok {
							return nil, fmt.Errorf("hooks.%s: invalid %s field", ev, key)
						}
					}
				}
			}
		}
		out[string(ev)] = matchers
	}
	return out, nil
}

// PatchUserHooks replaces only edited supported events in personal settings.
// Unknown events and other top-level settings (notably approval) are untouched.
func (c *Config) PatchUserHooks(changes map[string][]any) error {
	for name := range changes {
		supported := false
		for _, ev := range hooks.Events {
			if string(ev) == name {
				supported = true
				break
			}
		}
		if !supported {
			return fmt.Errorf("unsupported hook event %q", name)
		}
	}
	return updateJSON(c.UserConfigPath(), 0o644, func(raw map[string]any) {
		events, _ := raw["hooks"].(map[string]any)
		if events == nil {
			events = map[string]any{}
		}
		for name, matchers := range changes {
			if len(matchers) == 0 {
				delete(events, name)
			} else {
				events[name] = matchers
			}
		}
		if len(events) == 0 {
			delete(raw, "hooks")
		} else {
			raw["hooks"] = events
		}
	})
}
