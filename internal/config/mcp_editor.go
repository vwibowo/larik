package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// MCPAt reads only named personal servers, never merged project definitions.
func MCPAt(path string) (map[string]MCPServer, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]MCPServer{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Servers map[string]MCPServer `json:"mcp_servers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Servers == nil {
		file.Servers = map[string]MCPServer{}
	}
	return file.Servers, nil
}

// PatchUserMCP modifies only explicitly edited fields of named personal servers.
// Nil patches delete a personal server. Unknown fields and approvals are preserved.
func (c *Config) PatchUserMCP(changes map[string]map[string]any) error {
	for name, patch := range changes {
		if name == "" || strings.ContainsAny(name, ". \t\n") {
			return fmt.Errorf("invalid MCP server name %q", name)
		}
		for field := range patch {
			switch field {
			case "type", "command", "args", "url", "disabled", "env", "headers", "oauth", "oauth:client_id", "oauth:client_secret", "oauth:scopes", "oauth:callback_port":
			default:
				if !(strings.HasPrefix(field, "env:") || strings.HasPrefix(field, "headers:")) || strings.TrimSpace(strings.SplitN(field, ":", 2)[1]) == "" {
					return fmt.Errorf("invalid MCP field %q", field)
				}
			}
		}
	}
	return updateJSON(c.UserConfigPath(), 0o600, func(raw map[string]any) {
		servers, _ := raw["mcp_servers"].(map[string]any)
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
			// Base fields first: switching transports may clear env/headers/oauth,
			// then nested edits add keys for the new transport.
			for field, value := range patch {
				if !strings.Contains(field, ":") {
					setPath(server, []string{field}, value)
				}
			}
			for field, value := range patch {
				parts := strings.SplitN(field, ":", 2)
				if len(parts) == 1 {
					continue
				}
				nested, _ := server[parts[0]].(map[string]any)
				if nested == nil {
					nested = map[string]any{}
				}
				if value == nil {
					delete(nested, parts[1])
				} else {
					nested[parts[1]] = value
				}
				if len(nested) == 0 {
					delete(server, parts[0])
				} else {
					server[parts[0]] = nested
				}
			}
			servers[name] = server
		}
		if len(servers) == 0 {
			delete(raw, "mcp_servers")
		} else {
			raw["mcp_servers"] = servers
		}
	})
}
