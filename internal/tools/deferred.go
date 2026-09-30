package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"larik/internal/llm"
)

// Deferred tools keep a large tool set (many MCP servers) out of every
// request. Their definitions aren't sent to the model; it finds one with
// tool_search, which returns the definition as text, and runs it with
// call_tool, naming the tool and passing its arguments.
//
// Both are ordinary tools with fixed definitions, so loading a tool never
// changes the request's tool list, which is part of the cached prompt
// prefix, and it works the same with every provider.

const (
	// ToolSearchName finds deferred tools and returns their definitions.
	ToolSearchName = "tool_search"
	// CallToolName runs a deferred tool by name. The agent unwraps it
	// before checking permission, so rules, hooks and the UI see the tool
	// that is called, not this wrapper.
	CallToolName = "call_tool"
)

const (
	searchDefault = 5
	searchMax     = 20
	// indexBudget bounds the list of deferred tools in tool_search's
	// description; past it the list drops descriptions, then names.
	indexBudget = 8000
)

// Defer returns a registry with ts added as deferred tools: they can be
// looked up with Get and run through call_tool, but aren't in Specs. It
// adds tool_search and call_tool. With nothing to defer it returns r.
func (r *Registry) Defer(ts ...Tool) *Registry {
	if len(ts) == 0 {
		return r
	}
	var specs []llm.ToolSpec
	for _, t := range ts {
		specs = append(specs, t.Spec())
	}
	nr := NewRegistry(append(append([]Tool(nil), r.list...), searchTool{specs: specs}, callTool{})...)
	nr.deferred = ts
	for i, t := range ts {
		nr.byName[specs[i].Name] = t
	}
	return nr
}

// Deferred returns the tools that are reached through call_tool.
func (r *Registry) Deferred() []Tool { return r.deferred }

// IsDeferred reports whether name is a deferred tool.
func (r *Registry) IsDeferred(name string) bool {
	for _, t := range r.deferred {
		if t.Spec().Name == name {
			return true
		}
	}
	return false
}

// ResolveCall unwraps a call_tool input: the deferred tool it names and
// the arguments for it.
func (r *Registry) ResolveCall(input json.RawMessage) (Tool, json.RawMessage, error) {
	var in struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.Tool) == "" {
		return nil, nil, errors.New(`INVALID_JSON: expected {"tool": "<name>", "arguments": {...}}`)
	}
	name := strings.TrimSpace(in.Tool)
	t, ok := r.byName[name]
	if !ok || !r.IsDeferred(name) {
		if ok {
			return nil, nil, fmt.Errorf("%s is a regular tool: call it directly, not through %s", name, CallToolName)
		}
		return nil, nil, fmt.Errorf("no tool named %q. Find the exact name with %s", name, ToolSearchName)
	}
	args := in.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	// Some models send the arguments as a JSON string.
	var asString string
	if json.Unmarshal(args, &asString) == nil && json.Valid([]byte(asString)) {
		args = json.RawMessage(asString)
	}
	return t, args, nil
}

// searchTool is tool_search over a fixed set of deferred tools.
type searchTool struct{ specs []llm.ToolSpec }

func (searchTool) ReadOnly() bool { return true }

func (s searchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: ToolSearchName,
		Description: "Look up tools that aren't loaded by default and get their full definitions (description and input schema). " +
			"Search with keywords (\"create issue\", \"slack send\"), or fetch exact tools with \"select:name1,name2\". " +
			"Then run one with " + CallToolName + ", passing its name and arguments that fit its schema. The tools available this way:\n" + s.index(),
		Schema: schema(`{"type":"object","properties":{
			"query":{"type":"string","description":"Keywords, or select:<name>[,<name>...] for exact tools"},
			"max_results":{"type":"integer","description":"How many tools to return (default 5, max 20)"}},
			"required":["query"]}`),
	}
}

// index lists the deferred tools compactly, by server for MCP tools.
func (s searchTool) index() string {
	var lines []string
	for _, sp := range s.specs {
		lines = append(lines, "- "+sp.Name+": "+clipLine(sp.Description, 90))
	}
	if out := strings.Join(lines, "\n"); len(out) <= indexBudget {
		return out
	}
	// Too many for descriptions: names only, grouped by their prefix.
	groups := map[string][]string{}
	var order []string
	for _, sp := range s.specs {
		group, short := "tools", sp.Name
		if rest, ok := strings.CutPrefix(sp.Name, "mcp__"); ok {
			if server, tool, ok := strings.Cut(rest, "__"); ok {
				group, short = "mcp__"+server+"__", tool
			}
		}
		if _, seen := groups[group]; !seen {
			order = append(order, group)
		}
		groups[group] = append(groups[group], short)
	}
	lines = lines[:0]
	for _, g := range order {
		lines = append(lines, fmt.Sprintf("- %s (%d tools): %s", g, len(groups[g]), strings.Join(groups[g], ", ")))
	}
	if out := strings.Join(lines, "\n"); len(out) <= indexBudget {
		return out + "\n(Names are shortened: prefix each with its group, e.g. mcp__server__tool.)"
	}
	lines = lines[:0]
	for _, g := range order {
		lines = append(lines, fmt.Sprintf("- %s: %d tools", g, len(groups[g])))
	}
	return strings.Join(lines, "\n") + "\n(Too many to list: search by keyword.)"
}

func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (s searchTool) Run(_ context.Context, _ *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return errorf("query is empty: give keywords, or select:<name>")
	}
	limit := in.MaxResults
	if limit <= 0 {
		limit = searchDefault
	}
	limit = min(limit, searchMax)

	var found []llm.ToolSpec
	var missing []string
	if names, ok := strings.CutPrefix(query, "select:"); ok {
		for _, name := range strings.Split(names, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if i := s.find(name); i >= 0 {
				found = append(found, s.specs[i])
			} else {
				missing = append(missing, name)
			}
		}
	} else {
		found = s.search(query, limit)
	}
	if len(found) == 0 {
		msg := fmt.Sprintf("No tools match %q.", query)
		if len(missing) > 0 {
			msg = "No tools named " + strings.Join(missing, ", ") + "."
		}
		return Result{Content: msg + " Try other keywords; the available tools are listed in this tool's description."}
	}
	var b strings.Builder
	for _, sp := range found {
		fmt.Fprintf(&b, "<tool name=%q>\n%s\nInput schema: %s\n</tool>\n", sp.Name, strings.TrimSpace(sp.Description), compactJSON(sp.Schema))
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "Not found: %s.\n", strings.Join(missing, ", "))
	}
	fmt.Fprintf(&b, "Run one with %s: {\"tool\": \"<name>\", \"arguments\": {...}}.", CallToolName)
	names := make([]string, len(found))
	for i, sp := range found {
		names[i] = sp.Name
	}
	return Result{Content: Truncate(b.String(), MaxOutputBytes), Display: strings.Join(names, ", ")}
}

func (s searchTool) find(name string) int {
	for i, sp := range s.specs {
		if sp.Name == name {
			return i
		}
	}
	return -1
}

// search ranks the tools by how many query words their name and
// description contain; a word in the name counts for more.
func (s searchTool) search(query string, limit int) []llm.ToolSpec {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	type hit struct {
		i, score int
	}
	var hits []hit
	for i, sp := range s.specs {
		name, desc := strings.ToLower(sp.Name), strings.ToLower(sp.Description)
		score := 0
		for _, w := range words {
			switch {
			case strings.Contains(name, w):
				score += 3
			case strings.Contains(desc, w):
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{i, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]llm.ToolSpec, len(hits))
	for j, h := range hits {
		out[j] = s.specs[h.i]
	}
	return out
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// callTool is call_tool. The agent resolves it to the named tool before it
// runs (Registry.ResolveCall), so Run is reached only if that was skipped.
type callTool struct{}

func (callTool) ReadOnly() bool { return false }

func (callTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: CallToolName,
		Description: "Run a tool found with " + ToolSearchName + ": give its exact name and the arguments its input schema asks for. " +
			"Permission rules and prompts apply to the tool that is run, as if you had called it directly.",
		Schema: schema(`{"type":"object","properties":{
			"tool":{"type":"string","description":"The tool's exact name, as ` + ToolSearchName + ` returned it"},
			"arguments":{"type":"object","description":"The arguments for that tool"}},
			"required":["tool","arguments"]}`),
	}
}

func (callTool) Run(context.Context, *Env, json.RawMessage) Result {
	return errorf("%s wasn't resolved to a tool; find one with %s first", CallToolName, ToolSearchName)
}
