package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"larik/internal/lsp"
)

// LSPAt reads only personal definitions, never merged shared project commands.
func LSPAt(path string) (map[string]lsp.ServerConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]lsp.ServerConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		LSP map[string]lsp.ServerConfig `json:"lsp"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.LSP == nil {
		file.LSP = map[string]lsp.ServerConfig{}
	}
	return file.LSP, nil
}

// PatchUserLSP atomically changes only edited server fields. Unknown fields,
// including arbitrary initialization_options objects, remain byte-semantic intact.
// A nil server patch removes a personal definition, not a shared definition.
func (c *Config) PatchUserLSP(changes map[string]map[string]any) error {
	for name, patch := range changes {
		if name == "" || strings.Contains(name, ".") {
			return fmt.Errorf("invalid server name %q", name)
		}
		for field := range patch {
			if field != "disabled" && field != "command" && field != "extensions" && field != "root_markers" && field != "language_id" && !strings.HasPrefix(field, "env:") {
				return fmt.Errorf("invalid LSP field %q", field)
			}
		}
	}
	return updateJSON(c.UserConfigPath(), 0o644, func(raw map[string]any) {
		servers, _ := raw["lsp"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		for name, patch := range changes {
			if patch == nil {
				delete(servers, name)
				continue
			}
			server, _ := servers[name].(map[string]any)
			if server == nil {
				server = map[string]any{}
			}
			for field, value := range patch {
				if strings.HasPrefix(field, "env:") {
					env, _ := server["env"].(map[string]any)
					if env == nil {
						env = map[string]any{}
					}
					key := strings.TrimPrefix(field, "env:")
					if value == nil {
						delete(env, key)
					} else {
						env[key] = value
					}
					if len(env) == 0 {
						delete(server, "env")
					} else {
						server["env"] = env
					}
				} else {
					setPath(server, []string{field}, value)
				}
			}
			if len(server) == 0 {
				delete(servers, name)
			} else {
				servers[name] = server
			}
		}
		if len(servers) == 0 {
			delete(raw, "lsp")
		} else {
			raw["lsp"] = servers
		}
	})
}
