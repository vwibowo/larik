package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"larik/internal/llm"
	"larik/internal/tools"
)

// maxToolName is the tightest limit across providers (^[a-zA-Z0-9_-]{1,64}$).
const maxToolName = 64

// Prefix starts every MCP tool name.
const Prefix = "mcp__"

type tool struct {
	spec     llm.ToolSpec
	remote   string // tool name on the server
	session  *sdk.ClientSession
	readOnly bool
}

func newTool(server string, session *sdk.ClientSession, t *sdk.Tool, used map[string]bool) *tool {
	name := ToolName(server, t.Name)
	for i := 2; used[name]; i++ { // sanitization can collide
		name = shorten(fmt.Sprintf("%s_%d", ToolName(server, t.Name), i))
	}
	used[name] = true

	desc := t.Description
	if desc == "" {
		desc = t.Title
	}
	desc = fmt.Sprintf("[MCP server %q] %s", server, desc)

	// Auto-run only tools that declare themselves read-only AND closed-world;
	// open-world tools (fetchers, search) could leak data via their inputs.
	ro := false
	if a := t.Annotations; a != nil && a.ReadOnlyHint && a.OpenWorldHint != nil && !*a.OpenWorldHint {
		ro = true
	}
	return &tool{
		spec:     llm.ToolSpec{Name: name, Description: desc, Schema: schemaJSON(t.InputSchema)},
		remote:   t.Name,
		session:  session,
		readOnly: ro,
	}
}

func (t *tool) Spec() llm.ToolSpec { return t.spec }
func (t *tool) ReadOnly() bool     { return t.readOnly }

func (t *tool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var args map[string]any
	if len(input) == 0 {
		return tools.Result{Content: "INVALID_JSON: tool input was missing or malformed", IsError: true}
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tools.Result{Content: "INVALID_JSON: " + err.Error(), IsError: true}
	}
	res, err := t.session.CallTool(ctx, &sdk.CallToolParams{Name: t.remote, Arguments: args})
	if err != nil {
		return tools.Result{Content: "MCP call failed: " + err.Error(), IsError: true}
	}
	return tools.Result{Content: tools.Truncate(renderContent(res), tools.MaxOutputBytes), IsError: res.IsError}
}

// renderContent flattens an MCP result into text for the model.
func renderContent(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			parts = append(parts, c.Text)
		case *sdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes, not shown]", c.MIMEType, len(c.Data)))
		case *sdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio %s, not shown]", c.MIMEType))
		case *sdk.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource %s: %s]", c.Name, c.URI))
		case *sdk.EmbeddedResource:
			if r := c.Resource; r != nil {
				if r.Text != "" {
					parts = append(parts, fmt.Sprintf("[resource %s]\n%s", r.URI, r.Text))
				} else {
					parts = append(parts, fmt.Sprintf("[resource %s, %d bytes binary]", r.URI, len(r.Blob)))
				}
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	if len(parts) == 0 {
		return "(no output)"
	}
	return strings.Join(parts, "\n")
}

func schemaJSON(s any) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		if _, ok := m["type"]; !ok {
			m["type"] = "object"
			b, _ = json.Marshal(m)
		}
	}
	return b
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitize(s string) string { return unsafeChars.ReplaceAllString(s, "_") }

// ToolName builds the model-facing name for a server tool.
func ToolName(server, name string) string {
	return shorten(Prefix + sanitize(server) + "__" + sanitize(name))
}

// shorten keeps names within provider limits, adding a hash to stay unique.
func shorten(s string) string {
	if len(s) <= maxToolName {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return s[:maxToolName-9] + "_" + hex.EncodeToString(sum[:4])
}

// ServerOf returns the server part of an MCP tool name, or "".
func ServerOf(toolName string) string {
	if !strings.HasPrefix(toolName, Prefix) {
		return ""
	}
	rest := toolName[len(Prefix):]
	if i := strings.Index(rest, "__"); i >= 0 {
		return rest[:i]
	}
	return ""
}
