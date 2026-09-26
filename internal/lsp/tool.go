package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"larik/internal/llm"
	"larik/internal/tools"
)

const maxResults = 50

// Tool gives the model code navigation through the language servers.
type Tool struct{ M *Manager }

func (Tool) ReadOnly() bool { return true }

func (t Tool) Spec() llm.ToolSpec {
	var langs []string
	for _, st := range t.M.Statuses() {
		langs = append(langs, st.Languages)
	}
	return llm.ToolSpec{
		Name: "lsp",
		Description: "Semantic code navigation via language servers (" + strings.Join(langs, " ") + "). " +
			"Operations: definition, references, hover (type/docs), symbols (outline of a file), workspace_symbols (search by name), diagnostics (errors in a file). " +
			"Positions are 1-based line and column, as shown by the read tool. Prefer this over grep for finding where a symbol is defined or used.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"operation":{"type":"string","enum":["definition","references","hover","symbols","workspace_symbols","diagnostics"]},
			"path":{"type":"string","description":"File path (required except for workspace_symbols, where it selects the language)"},
			"line":{"type":"integer","description":"1-based line (definition, references, hover)"},
			"column":{"type":"integer","description":"1-based column of the symbol (definition, references, hover)"},
			"query":{"type":"string","description":"Symbol name to search for (workspace_symbols)"}},
			"required":["operation"]}`),
	}
}

type toolInput struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Query     string `json:"query"`
}

func (t Tool) Run(ctx context.Context, env *tools.Env, input json.RawMessage) tools.Result {
	var in toolInput
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return errorf("INVALID_JSON: expected {\"operation\": ...}")
	}
	path := ""
	if in.Path != "" {
		path = env.Abs(in.Path)
	}
	if path == "" {
		if in.Operation != "workspace_symbols" {
			return errorf("path is required for %s", in.Operation)
		}
		path = t.anyOpenPath()
		if path == "" {
			return errorf("workspace_symbols needs a path to pick the language server (any file of that language)")
		}
	}
	c, lang, _, err := t.M.clientFor(ctx, path)
	if err != nil {
		return errorf("language server failed to start: %v", err)
	}
	if c == nil {
		return errorf("no language server configured for %s", path)
	}
	if _, _, err := c.sync(path, lang); err != nil && in.Operation != "workspace_symbols" {
		return errorf("%v", err)
	}
	uri := pathToURI(path)

	pos := func() (map[string]any, error) {
		if in.Line < 1 || in.Column < 1 {
			return nil, fmt.Errorf("%s needs 1-based line and column", in.Operation)
		}
		line := lineText(path, in.Line)
		return map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     Position{Line: in.Line - 1, Character: utf16Col(line, in.Column-1)},
		}, nil
	}

	switch in.Operation {
	case "definition", "references":
		params, err := pos()
		if err != nil {
			return errorf("%v", err)
		}
		method := "textDocument/definition"
		if in.Operation == "references" {
			method = "textDocument/references"
			params["context"] = map[string]any{"includeDeclaration": true}
		}
		var raw json.RawMessage
		if err := c.conn.call(ctx, method, params, &raw); err != nil {
			return errorf("%s: %v", in.Operation, err)
		}
		return tools.Result{Content: t.formatLocations(parseLocations(raw))}

	case "hover":
		params, err := pos()
		if err != nil {
			return errorf("%v", err)
		}
		var raw json.RawMessage
		if err := c.conn.call(ctx, "textDocument/hover", params, &raw); err != nil {
			return errorf("hover: %v", err)
		}
		if text := strings.TrimSpace(hoverText(raw)); text != "" {
			return tools.Result{Content: tools.Truncate(text, tools.MaxOutputBytes)}
		}
		return tools.Result{Content: "No hover information at that position."}

	case "symbols":
		var syms []documentSymbol
		if err := c.conn.call(ctx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": uri}}, &syms); err != nil {
			return errorf("symbols: %v", err)
		}
		var b strings.Builder
		writeSymbols(&b, syms, 0)
		if b.Len() == 0 {
			return tools.Result{Content: "No symbols found."}
		}
		return tools.Result{Content: tools.Truncate(b.String(), tools.MaxOutputBytes)}

	case "workspace_symbols":
		if in.Query == "" {
			return errorf("workspace_symbols needs a query")
		}
		var syms []documentSymbol
		if err := c.conn.call(ctx, "workspace/symbol", map[string]any{"query": in.Query}, &syms); err != nil {
			return errorf("workspace_symbols: %v", err)
		}
		var b strings.Builder
		for i, s := range syms {
			if i == maxResults {
				fmt.Fprintf(&b, "... and %d more\n", len(syms)-maxResults)
				break
			}
			if s.Location == nil {
				continue
			}
			fmt.Fprintf(&b, "%s %s  %s:%d", kindName(s.Kind), s.Name, t.M.rel(uriToPath(s.Location.URI)), s.Location.Range.Start.Line+1)
			if s.ContainerName != "" {
				fmt.Fprintf(&b, "  (in %s)", s.ContainerName)
			}
			b.WriteString("\n")
		}
		if b.Len() == 0 {
			return tools.Result{Content: "No symbols match " + in.Query + "."}
		}
		return tools.Result{Content: b.String()}

	case "diagnostics":
		if report := t.M.Diagnostics(ctx, path); report != "" {
			return tools.Result{Content: report}
		}
		return tools.Result{Content: "No errors or warnings reported for " + t.M.rel(path) + "."}
	}
	return errorf("unknown operation %q", in.Operation)
}

func (t Tool) formatLocations(locs []Location) string {
	if len(locs) == 0 {
		return "No results."
	}
	sort.SliceStable(locs, func(i, j int) bool {
		if locs[i].URI != locs[j].URI {
			return locs[i].URI < locs[j].URI
		}
		return locs[i].Range.Start.Line < locs[j].Range.Start.Line
	})
	var b strings.Builder
	for i, l := range locs {
		if i == maxResults {
			fmt.Fprintf(&b, "... and %d more\n", len(locs)-maxResults)
			break
		}
		path := uriToPath(l.URI)
		line := lineText(path, l.Range.Start.Line+1)
		col := runeCol(line, l.Range.Start.Character) + 1
		fmt.Fprintf(&b, "%s:%d:%d  %s\n", t.M.rel(path), l.Range.Start.Line+1, col, strings.TrimSpace(line))
	}
	return b.String()
}

func writeSymbols(b *strings.Builder, syms []documentSymbol, depth int) {
	for _, s := range syms {
		line := s.SelectionRange.Start.Line + 1
		if s.Location != nil { // SymbolInformation form
			line = s.Location.Range.Start.Line + 1
		}
		fmt.Fprintf(b, "%s%s %s  :%d\n", strings.Repeat("  ", depth), kindName(s.Kind), s.Name, line)
		writeSymbols(b, s.Children, depth+1)
	}
}

// lineText returns a 1-based line of a file, or "".
func lineText(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for i := 1; sc.Scan(); i++ {
		if i == n {
			return sc.Text()
		}
	}
	return ""
}

func (t Tool) anyOpenPath() string {
	t.M.mu.Lock()
	defer t.M.mu.Unlock()
	for _, c := range t.M.clients {
		c.mu.Lock()
		for uri := range c.docs {
			c.mu.Unlock()
			return uriToPath(uri)
		}
		c.mu.Unlock()
	}
	return ""
}

func errorf(format string, args ...any) tools.Result {
	return tools.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
