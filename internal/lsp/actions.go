package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Code actions: a language server's fixes and refactorings for a range
// ("Add missing import", "Organize imports"). Listing is read-only; the
// apply_code_action tool applies one, through the same write checks as
// edit.

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit is a set of edits, keyed by document either way the
// protocol allows.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []json.RawMessage     `json:"documentChanges,omitempty"`
}

// codeAction is a CodeAction, or a bare Command (Command set, no Edit).
type codeAction struct {
	Title       string                   `json:"title"`
	Kind        string                   `json:"kind,omitempty"`
	IsPreferred bool                     `json:"isPreferred,omitempty"`
	Disabled    *struct{ Reason string } `json:"disabled,omitempty"`
	Edit        *WorkspaceEdit           `json:"edit,omitempty"`
	Command     *command                 `json:"-"`
	Data        json.RawMessage          `json:"data,omitempty"`
	raw         json.RawMessage          // as the server sent it, for codeAction/resolve
}

type command struct {
	Title     string            `json:"title"`
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments,omitempty"`
}

// parseActions decodes (Command | CodeAction)[].
func parseActions(raw json.RawMessage) []codeAction {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []codeAction
	for _, it := range items {
		var shape struct {
			Command json.RawMessage `json:"command"`
		}
		_ = json.Unmarshal(it, &shape)
		var a codeAction
		if len(shape.Command) > 0 && shape.Command[0] == '"' { // a bare Command
			var cmd command
			if json.Unmarshal(it, &cmd) != nil {
				continue
			}
			a = codeAction{Title: cmd.Title, Command: &cmd}
		} else {
			if json.Unmarshal(it, &a) != nil {
				continue
			}
			if len(shape.Command) > 0 && string(shape.Command) != "null" {
				var cmd command
				if json.Unmarshal(shape.Command, &cmd) == nil {
					a.Command = &cmd
				}
			}
		}
		a.raw = it
		if strings.TrimSpace(a.Title) != "" {
			out = append(out, a)
		}
	}
	return out
}

// requestActions asks for the code actions on a line (or at one column),
// passing the diagnostics that overlap it so quick fixes come back.
func (c *client) requestActions(ctx context.Context, path string, line, col int) ([]codeAction, error) {
	c.mu.Lock()
	supported := c.actions
	diags := c.diags[pathToURI(path)]
	c.mu.Unlock()
	if !supported {
		return nil, errors.New("this language server doesn't offer code actions")
	}
	text := lineText(path, line)
	start, end := Position{Line: line - 1}, Position{Line: line - 1, Character: utf16Col(text, utf8.RuneCountInString(text))}
	if col > 0 {
		start.Character = utf16Col(text, col-1)
		end = start
	}
	var overlapping []Diagnostic
	for _, d := range diags {
		if d.Range.Start.Line <= line-1 && d.Range.End.Line >= line-1 {
			overlapping = append(overlapping, d)
		}
	}
	if overlapping == nil {
		overlapping = []Diagnostic{}
	}
	var raw json.RawMessage
	err := c.conn.call(ctx, "textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
		"range":        Range{Start: start, End: end},
		"context":      map[string]any{"diagnostics": overlapping, "triggerKind": 1},
	}, &raw)
	if err != nil {
		return nil, err
	}
	return parseActions(raw), nil
}

// resolve fills in an action's edit when the server defers it.
func (c *client) resolve(ctx context.Context, a codeAction) (codeAction, error) {
	if a.Edit != nil || a.Command != nil && len(a.Data) == 0 || len(a.raw) == 0 {
		return a, nil
	}
	var raw json.RawMessage
	if err := c.conn.call(ctx, "codeAction/resolve", a.raw, &raw); err != nil {
		return a, fmt.Errorf("resolve: %w", err)
	}
	var r codeAction
	if json.Unmarshal(raw, &r) != nil {
		return a, errors.New("resolve: unreadable answer")
	}
	a.Edit = r.Edit
	var shape struct {
		Command *command `json:"command"`
	}
	if json.Unmarshal(raw, &shape) == nil && shape.Command != nil {
		a.Command = shape.Command
	}
	return a, nil
}

// Actions lists the code actions for a line of path.
func (m *Manager) Actions(ctx context.Context, path string, line, col int) ([]codeAction, error) {
	c, lang, _, err := m.clientFor(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("language server failed to start: %w", err)
	}
	if c == nil {
		return nil, fmt.Errorf("no language server configured for %s", m.rel(path))
	}
	if _, _, err := c.sync(path, lang); err != nil {
		return nil, err
	}
	// Quick fixes come from diagnostics, so make sure they're current.
	uri := pathToURI(path)
	c.mu.Lock()
	after := c.counter
	c.mu.Unlock()
	if !c.pullDiagnostics(ctx, uri, DiagnosticsTimeout) {
		c.waitFor(ctx, uri, after, 750*time.Millisecond)
	}
	return c.requestActions(ctx, path, line, col)
}

// FormatActions lists actions for the model, numbered, with their kinds.
func FormatActions(rel string, line int, actions []codeAction) string {
	if len(actions) == 0 {
		return fmt.Sprintf("No code actions at %s:%d.", rel, line)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Code actions at %s:%d (apply one with apply_code_action and its exact title):\n", rel, line)
	for i, a := range actions {
		fmt.Fprintf(&b, "%d. %s", i+1, a.Title)
		var notes []string
		if a.Kind != "" {
			notes = append(notes, a.Kind)
		}
		if a.IsPreferred {
			notes = append(notes, "preferred")
		}
		if a.Disabled != nil {
			notes = append(notes, "unavailable: "+a.Disabled.Reason)
		}
		if len(notes) > 0 {
			b.WriteString("  [" + strings.Join(notes, ", ") + "]")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// FileWriter changes a file through the caller's write checks; fn gets
// the current content and returns the new.
type FileWriter func(path string, fn func(old []byte) ([]byte, error)) error

// Applied is what applying a code action changed.
type Applied struct {
	Title   string
	Files   map[string]int // path -> edits applied
	Preview []string       // "- old" / "+ new" lines, for the user
}

// ApplyAction applies the code action titled title at path:line, writing
// edits with write. Its workspace edit is applied first, then its command
// runs; edits the server sends back during that command are applied too,
// and only then.
func (m *Manager) ApplyAction(ctx context.Context, path string, line, col int, title string, write FileWriter) (Applied, error) {
	actions, err := m.Actions(ctx, path, line, col)
	if err != nil {
		return Applied{}, err
	}
	a, err := pickAction(actions, title)
	if err != nil {
		return Applied{}, err
	}
	c, _, _, _ := m.clientFor(ctx, path)
	if a, err = c.resolve(ctx, a); err != nil {
		return Applied{}, err
	}
	applied := Applied{Title: a.Title, Files: map[string]int{}}
	var mu sync.Mutex
	apply := func(e WorkspaceEdit) error {
		byFile, err := editsByFile(e)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		for _, p := range sortedKeys(byFile) {
			edits := byFile[p]
			err := write(p, func(old []byte) ([]byte, error) {
				out, preview, err := applyTextEdits(string(old), edits)
				applied.Preview = append(applied.Preview, preview...)
				return []byte(out), err
			})
			if err != nil {
				return fmt.Errorf("%s: %w", m.rel(p), err)
			}
			applied.Files[p] += len(edits)
		}
		return nil
	}
	if a.Edit != nil {
		if err := apply(*a.Edit); err != nil {
			return applied, err
		}
	}
	if a.Command != nil {
		c.mu.Lock()
		c.applier = apply
		c.mu.Unlock()
		err := c.conn.call(ctx, "workspace/executeCommand", map[string]any{"command": a.Command.Command, "arguments": a.Command.Arguments}, nil)
		c.mu.Lock()
		c.applier = nil
		c.mu.Unlock()
		if err != nil {
			return applied, fmt.Errorf("running %s: %w", a.Command.Command, err)
		}
	}
	if len(applied.Files) == 0 {
		return applied, errors.New("the action made no changes")
	}
	return applied, nil
}

// pickAction finds an action by its exact title, or failing that a single
// case-insensitive match.
func pickAction(actions []codeAction, title string) (codeAction, error) {
	title = strings.TrimSpace(title)
	var loose []codeAction
	for _, a := range actions {
		if a.Title == title {
			return usable(a)
		}
		if strings.EqualFold(a.Title, title) {
			loose = append(loose, a)
		}
	}
	if len(loose) == 1 {
		return usable(loose[0])
	}
	var titles []string
	for _, a := range actions {
		titles = append(titles, fmt.Sprintf("%q", a.Title))
	}
	if len(titles) == 0 {
		return codeAction{}, errors.New("no code actions there any more")
	}
	return codeAction{}, fmt.Errorf("no code action titled %q here; the actions are %s", title, strings.Join(titles, ", "))
}

func usable(a codeAction) (codeAction, error) {
	if a.Disabled != nil {
		return a, fmt.Errorf("%q is unavailable: %s", a.Title, a.Disabled.Reason)
	}
	return a, nil
}

// editsByFile gathers a workspace edit's text edits per file. Creating,
// renaming or deleting files isn't supported.
func editsByFile(e WorkspaceEdit) (map[string][]TextEdit, error) {
	out := map[string][]TextEdit{}
	for uri, edits := range e.Changes {
		out[uriToPath(uri)] = append(out[uriToPath(uri)], edits...)
	}
	for _, raw := range e.DocumentChanges {
		var dc struct {
			Kind         string `json:"kind"`
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []TextEdit `json:"edits"`
		}
		if json.Unmarshal(raw, &dc) != nil {
			return nil, errors.New("unreadable document change")
		}
		if dc.Kind != "" {
			return nil, fmt.Errorf("the action would %s a file, which larik doesn't do through code actions", dc.Kind)
		}
		p := uriToPath(dc.TextDocument.URI)
		out[p] = append(out[p], dc.Edits...)
	}
	return out, nil
}

func sortedKeys(m map[string][]TextEdit) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// applyTextEdits applies LSP text edits (positions in UTF-16 units) to
// content. Edits must not overlap. It also returns a short preview.
func applyTextEdits(content string, edits []TextEdit) (string, []string, error) {
	type span struct {
		start, end int
		text       string
	}
	lineStarts := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	offset := func(p Position) (int, error) {
		if p.Line >= len(lineStarts) {
			if p.Line == len(lineStarts) && p.Character == 0 {
				return len(content), nil // the end of a file without a final newline
			}
			return 0, fmt.Errorf("line %d is past the end of the file", p.Line+1)
		}
		start := lineStarts[p.Line]
		end := len(content)
		if p.Line+1 < len(lineStarts) {
			end = lineStarts[p.Line+1] - 1
		}
		units := 0
		for i, r := range content[start:end] {
			if units >= p.Character {
				return start + i, nil
			}
			units += len(utf16.Encode([]rune{r}))
		}
		return end, nil // past the end of the line clamps to it
	}
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		s, err := offset(e.Range.Start)
		if err != nil {
			return "", nil, err
		}
		t, err := offset(e.Range.End)
		if err != nil {
			return "", nil, err
		}
		if t < s {
			return "", nil, errors.New("an edit ends before it starts")
		}
		spans = append(spans, span{s, t, e.NewText})
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var preview []string
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", nil, errors.New("the action's edits overlap")
		}
	}
	var b strings.Builder
	last := 0
	for _, s := range spans {
		b.WriteString(content[last:s.start])
		b.WriteString(s.text)
		last = s.end
		if old := content[s.start:s.end]; old != "" {
			preview = append(preview, prefixEach(old, "- "))
		}
		if s.text != "" {
			preview = append(preview, prefixEach(s.text, "+ "))
		}
	}
	b.WriteString(content[last:])
	return b.String(), preview, nil
}

func prefixEach(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n"+prefix)
}
