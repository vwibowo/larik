package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"sort"
)

// MCPServer configures one MCP server. The JSON shape matches the common
// .mcp.json format: stdio servers set command/args/env, remote servers set
// type ("http" or "sse"), url and headers. Strings may reference
// environment variables as ${VAR} or ${VAR:-default}.
type MCPServer struct {
	Type     string            `json:"type,omitempty"` // stdio (default), http, sse
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`

	Name    string `json:"-"`
	Source  string `json:"-"` // file that defined it
	Trusted bool   `json:"-"` // defined in a personal (non-shared) file
}

// Transport returns the normalized transport type.
func (s MCPServer) Transport() string {
	switch s.Type {
	case "http", "streamable-http", "streamableHttp":
		return "http"
	case "sse":
		return "sse"
	case "", "stdio":
		if s.Command == "" && s.URL != "" {
			return "http"
		}
		return "stdio"
	}
	return s.Type
}

// Hash identifies the server's configuration (before env expansion), so an
// approval stops applying if the command, URL or arguments change.
func (s MCPServer) Hash() string {
	b, _ := json.Marshal(struct {
		T string
		C string
		A []string
		E [][2]string
		U string
		H [][2]string
	}{s.Transport(), s.Command, s.Args, sortedPairs(s.Env), s.URL, sortedPairs(s.Headers)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func sortedPairs(m map[string]string) [][2]string {
	out := make([][2]string, 0, len(m))
	for k, v := range m {
		out = append(out, [2]string{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// Approved reports whether a server may start.
func (c *Config) Approved(s MCPServer) bool {
	return s.Trusted || c.ApprovedMCP[s.Name] == s.Hash()
}

// ApproveMCP records approval of a project-scoped server's current config.
func ApproveMCP(cwd string, s MCPServer) error {
	return updateLocal(cwd, func(raw map[string]any) {
		approved, _ := raw["approved_mcp_servers"].(map[string]any)
		if approved == nil {
			approved = map[string]any{}
		}
		approved[s.Name] = s.Hash()
		raw["approved_mcp_servers"] = approved
	})
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// ExpandEnv replaces ${VAR} and ${VAR:-default}.
func ExpandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		g := envRef.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(g[1]); ok && v != "" {
			return v
		}
		return g[2]
	})
}
