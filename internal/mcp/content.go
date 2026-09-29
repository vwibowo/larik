package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"larik/internal/llm"
	"larik/internal/tools"
)

// ErrUnknownServer is returned for a server name that isn't connected.
var ErrUnknownServer = errors.New("no connected MCP server by that name")

// Resource is one resource a server offers.
type Resource struct {
	Server, URI, Name, Description, MIMEType string
}

// Ref is how a prompt mentions the resource: server:uri.
func (r Resource) Ref() string { return r.Server + ":" + r.URI }

// connected returns the connected server named name.
func (m *Manager) connected(name string) (*server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[name]
	if !ok || s.state != StateConnected || s.session == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServer, name)
	}
	return s, nil
}

// Resources lists the resources of every connected server that has them
// (of one server, when only is set). A server that fails to list is
// skipped; the first such error is returned with the rest.
func (m *Manager) Resources(ctx context.Context, only string) ([]Resource, error) {
	m.mu.Lock()
	var names []string
	for name, s := range m.servers {
		if s.state == StateConnected && s.resources && (only == "" || name == only) {
			names = append(names, name)
		}
	}
	m.mu.Unlock()
	if only != "" && len(names) == 0 {
		if _, err := m.connected(only); err != nil {
			return nil, err
		}
		return nil, nil // connected, but offers no resources
	}
	sort.Strings(names)
	var out []Resource
	var firstErr error
	for _, name := range names {
		s, err := m.connected(name)
		if err != nil {
			continue
		}
		for r, err := range s.session.Resources(ctx, nil) {
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", name, err)
				}
				break
			}
			out = append(out, Resource{Server: name, URI: r.URI, Name: r.Name, Description: r.Description, MIMEType: r.MIMEType})
		}
	}
	return out, firstErr
}

// maxResourceBytes bounds what one resource read may add to a prompt.
const maxResourceBytes = 256 << 10

// ReadResource reads a resource as blocks for a prompt: text as text,
// images as images, anything else described.
func (m *Manager) ReadResource(ctx context.Context, server, uri string) ([]llm.Block, error) {
	s, err := m.connected(server)
	if err != nil {
		return nil, err
	}
	res, err := s.session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, err
	}
	ref := server + ":" + uri
	var blocks []llm.Block
	for _, c := range res.Contents {
		switch {
		case c.Text != "" || c.Blob == nil:
			text := tools.Truncate(c.Text, maxResourceBytes)
			blocks = append(blocks, llm.Block{Type: llm.BlockText, Attachment: ref,
				Text: fmt.Sprintf("<mcp-resource server=%q uri=%q>\n%s\n</mcp-resource>", server, c.URI, text)})
		case imageMIME[c.MIMEType] && len(c.Blob) <= maxResourceImage:
			blocks = append(blocks,
				llm.Block{Type: llm.BlockText, Attachment: ref, Text: fmt.Sprintf("<mcp-resource server=%q uri=%q type=%q/>", server, c.URI, c.MIMEType)},
				llm.Block{Type: llm.BlockImage, Attachment: ref, MediaType: c.MIMEType, Data: base64.StdEncoding.EncodeToString(c.Blob)})
		default:
			blocks = append(blocks, llm.Block{Type: llm.BlockText, Attachment: ref,
				Text: fmt.Sprintf("<mcp-resource server=%q uri=%q type=%q>(binary, %d bytes, not shown)</mcp-resource>", server, c.URI, c.MIMEType, len(c.Blob))})
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("%s is empty", ref)
	}
	return blocks, nil
}

var imageMIME = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

const maxResourceImage = 5 << 20

// Prompts.

// Prompt is a server prompt, run as a slash command.
type Prompt struct {
	Command, Server, Name, Description string
	Args                               []*sdk.PromptArgument
}

// PromptCommand is the slash command (without the /) for a server prompt,
// in Claude Code's form: mcp__<server>__<prompt>.
func PromptCommand(server, prompt string) string {
	return Prefix + sanitize(server) + "__" + sanitize(prompt)
}

// Prompts lists the prompts of connected servers, sorted by command.
func (m *Manager) Prompts() []Prompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Prompt
	for name, s := range m.servers {
		if s.state != StateConnected {
			continue
		}
		for _, p := range s.prompts {
			out = append(out, Prompt{Command: PromptCommand(name, p.Name), Server: name, Name: p.Name, Description: p.Description, Args: p.Arguments})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}

// ArgHint describes a prompt's arguments, e.g. "<repo> [branch]".
func (p Prompt) ArgHint() string {
	var parts []string
	for _, a := range p.Args {
		if a.Required {
			parts = append(parts, "<"+a.Name+">")
		} else {
			parts = append(parts, "["+a.Name+"]")
		}
	}
	return strings.Join(parts, " ")
}

// ExpandPrompt turns "/mcp__server__prompt args" into the prompt's text,
// fetched from the server. ok is false when line isn't a known prompt.
// Arguments are split on spaces and given in the prompt's order; the last
// argument takes the rest of the line, so a single argument can be a
// sentence.
func (m *Manager) ExpandPrompt(ctx context.Context, line string) (text string, ok bool, err error) {
	cmd, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	var p Prompt
	for _, x := range m.Prompts() {
		if x.Command == cmd {
			p, ok = x, true
			break
		}
	}
	if !ok {
		return "", false, nil
	}
	args := map[string]string{}
	words := strings.Fields(rest)
	for i, a := range p.Args {
		switch {
		case i >= len(words):
		case i == len(p.Args)-1:
			args[a.Name] = strings.Join(words[i:], " ")
		default:
			args[a.Name] = words[i]
		}
		if args[a.Name] == "" && a.Required {
			return "", true, fmt.Errorf("/%s needs %s", cmd, p.ArgHint())
		}
	}
	s, err := m.connected(p.Server)
	if err != nil {
		return "", true, err
	}
	res, err := s.session.GetPrompt(ctx, &sdk.GetPromptParams{Name: p.Name, Arguments: args})
	if err != nil {
		return "", true, fmt.Errorf("/%s: %w", cmd, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The user ran the /%s prompt from the %q MCP server.\n\n<mcp-prompt server=%q name=%q>\n", cmd, p.Server, p.Server, p.Name)
	for _, msg := range res.Messages {
		if msg.Role == "assistant" {
			b.WriteString("[assistant] ")
		}
		b.WriteString(contentText(msg.Content) + "\n")
	}
	b.WriteString("</mcp-prompt>")
	return b.String(), true, nil
}

// contentText renders prompt content as text; non-text parts are named.
func contentText(c sdk.Content) string {
	switch c := c.(type) {
	case *sdk.TextContent:
		return c.Text
	case *sdk.EmbeddedResource:
		if c.Resource != nil && c.Resource.Text != "" {
			return fmt.Sprintf("<resource uri=%q>\n%s\n</resource>", c.Resource.URI, c.Resource.Text)
		}
		if c.Resource != nil {
			return fmt.Sprintf("(embedded resource %s)", c.Resource.URI)
		}
	case *sdk.ImageContent:
		return fmt.Sprintf("(image, %s)", c.MIMEType)
	case *sdk.AudioContent:
		return fmt.Sprintf("(audio, %s)", c.MIMEType)
	case *sdk.ResourceLink:
		return fmt.Sprintf("(resource %s)", c.URI)
	}
	return ""
}

// Resource tools, offered when a connected server has resources.

// ListResourcesTool lists MCP resources.
type ListResourcesTool struct{ M *Manager }

func (ListResourcesTool) ReadOnly() bool { return true }
func (ListResourcesTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "list_mcp_resources",
		Description: "List the resources (documents, records, files) that connected MCP servers offer. Read one with read_mcp_resource.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"server":{"type":"string","description":"Only this server's resources"}}}`),
	}
}

func (t ListResourcesTool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		Server string `json:"server"`
	}
	_ = json.Unmarshal(input, &in)
	rs, err := t.M.Resources(ctx, in.Server)
	if len(rs) == 0 {
		if err != nil {
			return tools.Result{Content: err.Error(), IsError: true}
		}
		return tools.Result{Content: "No resources."}
	}
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, "server=%s uri=%s", r.Server, r.URI)
		if r.Name != "" {
			fmt.Fprintf(&b, " name=%q", r.Name)
		}
		if r.MIMEType != "" {
			b.WriteString(" type=" + r.MIMEType)
		}
		if r.Description != "" {
			b.WriteString(" — " + oneLine(r.Description))
		}
		b.WriteString("\n")
	}
	if err != nil {
		fmt.Fprintf(&b, "(some servers couldn't list theirs: %v)\n", err)
	}
	return tools.Result{Content: tools.Truncate(b.String(), tools.MaxOutputBytes)}
}

// ReadResourceTool reads an MCP resource.
type ReadResourceTool struct{ M *Manager }

func (ReadResourceTool) ReadOnly() bool { return true }
func (ReadResourceTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read_mcp_resource",
		Description: "Read a resource from an MCP server, by server name and URI (see list_mcp_resources).",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"server":{"type":"string"},
			"uri":{"type":"string"}},
			"required":["server","uri"]}`),
	}
}

func (t ReadResourceTool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		Server string `json:"server"`
		URI    string `json:"uri"`
	}
	if json.Unmarshal(input, &in) != nil || in.Server == "" || in.URI == "" {
		return tools.Result{Content: "INVALID_JSON: expected server and uri", IsError: true}
	}
	blocks, err := t.M.ReadResource(ctx, in.Server, in.URI)
	if err != nil {
		return tools.Result{Content: err.Error(), IsError: true}
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == llm.BlockImage {
			parts = append(parts, "(an image; the user can attach it with @"+in.Server+":"+in.URI+")")
			continue
		}
		parts = append(parts, b.Text)
	}
	return tools.Result{Content: tools.Truncate(strings.Join(parts, "\n"), tools.MaxOutputBytes)}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// IsServer reports whether name is a connected server.
func (m *Manager) IsServer(name string) bool {
	_, err := m.connected(name)
	return err == nil
}
