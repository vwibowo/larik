package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"larik/internal/llm"
	"larik/internal/pathpolicy"
)

const (
	defaultReadLines = 2000
	maxLineLen       = 2000
)

// Read returns a file with line numbers.
type Read struct{}

func (Read) ReadOnly() bool { return true }
func (Read) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read",
		Description: "Read a text file. Returns lines prefixed with line numbers. Use offset/limit for large files. You must read a file before editing or overwriting it.",
		Schema: schema(`{"type":"object","properties":{
			"path":{"type":"string","description":"File path, absolute or relative to the working directory"},
			"offset":{"type":"integer","description":"1-based line to start from"},
			"limit":{"type":"integer","description":"Maximum lines to return (default 2000)"}},
			"required":["path"]}`),
	}
}

func (Read) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	path := env.Abs(in.Path)
	f, err := os.Open(path)
	if err != nil {
		return errorf("%v", err)
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi != nil && fi.IsDir() {
		return errorf("%s is a directory; use glob or bash ls", path)
	}
	if in.Offset < 1 {
		in.Offset = 1
	}
	if in.Limit <= 0 {
		in.Limit = defaultReadLines
	}

	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line, shown := 0, 0
	for sc.Scan() {
		line++
		if line < in.Offset {
			continue
		}
		if shown >= in.Limit {
			fmt.Fprintf(&b, "... (more lines; continue with offset=%d)\n", line)
			break
		}
		text := sc.Text()
		if strings.ContainsRune(text, 0) {
			return errorf("%s looks like a binary file", path)
		}
		if len(text) > maxLineLen {
			text = safeCut(text, maxLineLen) + " ... [line truncated]"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", line, text)
		shown++
	}
	if err := sc.Err(); err != nil {
		return errorf("%v", err)
	}
	env.markRead(path)
	if env.Touch != nil {
		env.Touch(path)
	}
	if line == 0 {
		return Result{Content: "(empty file)"}
	}
	if shown == 0 {
		return errorf("offset %d is past the end of the file (%d lines)", in.Offset, line)
	}
	return Result{Content: Truncate(b.String(), MaxOutputBytes*2)}
}

// Write creates or overwrites a file.
type Write struct{}

func (Write) ReadOnly() bool { return false }
func (Write) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "write",
		Description: "Create a file or overwrite it entirely. Prefer edit for changes to existing files. Existing files must be read first.",
		Schema: schema(`{"type":"object","properties":{
			"path":{"type":"string"},
			"content":{"type":"string","description":"Full file content"}},
			"required":["path","content"]}`),
	}
}

func (Write) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	path := env.Abs(in.Path)
	root, rel, err := env.writeRoot(path)
	if err != nil {
		return errorf("%v", err)
	}
	defer root.Close()
	if err := env.checkFresh(path); err != nil {
		return errorf("%v", err)
	}
	_, statErr := root.Stat(rel)
	existed := statErr == nil
	if err := env.beforeWrite(path); err != nil {
		return errorf("cannot checkpoint %s: %v", path, err)
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return errorf("%v", err)
	}
	if _, err := pathpolicy.WritePath(env.Cwd, path); err != nil {
		return errorf("%v", err)
	}
	if err := replaceFile(root, rel, []byte(in.Content)); err != nil {
		return errorf("%v", err)
	}
	env.markRead(path)
	verb := "Created"
	if existed {
		verb = "Overwrote"
	}
	lines, noun := strings.Count(strings.TrimSuffix(in.Content, "\n"), "\n")+1, "lines"
	if lines == 1 {
		noun = "line"
	}
	return env.afterWrite(ctx, path, Result{Content: fmt.Sprintf("%s %s (%d %s)", verb, path, lines, noun)})
}

// Edit replaces an exact string in a file.
type Edit struct{}

func (Edit) ReadOnly() bool { return false }
func (Edit) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "edit",
		Description: "Replace an exact string in a file. old_string must match the file exactly (including whitespace) and be unique unless replace_all is true. Read the file first.",
		Schema: schema(`{"type":"object","properties":{
			"path":{"type":"string"},
			"old_string":{"type":"string","description":"Exact text to replace"},
			"new_string":{"type":"string","description":"Replacement text"},
			"replace_all":{"type":"boolean","description":"Replace every occurrence"}},
			"required":["path","old_string","new_string"]}`),
	}
}

func (Edit) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Path       string `json:"path"`
		Old        string `json:"old_string"`
		New        string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	path := env.Abs(in.Path)
	root, rel, err := env.writeRoot(path)
	if err != nil {
		return errorf("%v", err)
	}
	defer root.Close()
	data, err := root.ReadFile(rel)
	if err != nil {
		return errorf("%v", err)
	}
	if err := env.checkFresh(path); err != nil {
		return errorf("%v", err)
	}
	updated, n, err := applyEdit(string(data), in.Old, in.New, in.ReplaceAll)
	if err != nil {
		return errorf("%v in %s", err, path)
	}
	if err := env.beforeWrite(path); err != nil {
		return errorf("cannot checkpoint %s: %v", path, err)
	}
	if _, err := pathpolicy.WritePath(env.Cwd, path); err != nil {
		return errorf("%v", err)
	}
	if err := replaceFile(root, rel, []byte(updated)); err != nil {
		return errorf("%v", err)
	}
	env.markRead(path)
	return env.afterWrite(ctx, path, Result{
		Content: fmt.Sprintf("Edited %s (%d replacement(s))", path, n),
		Display: miniDiff(in.Old, in.New),
	})
}

// applyEdit replaces old with new in content: once, where old must be
// unique, or everywhere with all. It returns the replacements made.
func applyEdit(content, old, new string, all bool) (string, int, error) {
	if old == "" {
		return "", 0, fmt.Errorf("old_string is empty; use write to create files")
	}
	if old == new {
		return "", 0, fmt.Errorf("old_string and new_string are identical")
	}
	n := strings.Count(content, old)
	switch {
	case n == 0:
		return "", 0, fmt.Errorf("old_string not found")
	case n > 1 && !all:
		return "", 0, fmt.Errorf("old_string appears %d times; add surrounding context to make it unique or set replace_all", n)
	case !all:
		n = 1
	}
	return strings.Replace(content, old, new, n), n, nil
}

const maxEdits = 100

// MultiEdit makes several replacements in one file, in order and all or
// nothing, so a change in many places is one call, one undo step and one
// round of diagnostics.
type MultiEdit struct{}

func (MultiEdit) ReadOnly() bool { return false }
func (MultiEdit) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: MultiEditToolName,
		Description: "Make several exact-string replacements in one file in a single call. Edits apply in order, each to the result of " +
			"the ones before, and either all succeed or the file is left unchanged. Each old_string follows the edit tool's rules: an " +
			"exact match (including whitespace), unique unless replace_all is true. Prefer it over repeated edit calls on the same file. Read the file first.",
		Schema: schema(`{"type":"object","properties":{
			"path":{"type":"string"},
			"edits":{"type":"array","minItems":1,"description":"Replacements, applied in order","items":{"type":"object","properties":{
				"old_string":{"type":"string","description":"Exact text to replace"},
				"new_string":{"type":"string","description":"Replacement text"},
				"replace_all":{"type":"boolean","description":"Replace every occurrence"}},
				"required":["old_string","new_string"]}}},
			"required":["path","edits"]}`),
	}
}

// MultiEditToolName is the name of the multi-edit tool.
const MultiEditToolName = "multi_edit"

func (MultiEdit) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Path  string `json:"path"`
		Edits []struct {
			Old        string `json:"old_string"`
			New        string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	switch {
	case len(in.Edits) == 0:
		return errorf("edits is empty")
	case len(in.Edits) > maxEdits:
		return errorf("at most %d edits per call", maxEdits)
	}
	path := env.Abs(in.Path)
	root, rel, err := env.writeRoot(path)
	if err != nil {
		return errorf("%v", err)
	}
	defer root.Close()
	data, err := root.ReadFile(rel)
	if err != nil {
		return errorf("%v", err)
	}
	if err := env.checkFresh(path); err != nil {
		return errorf("%v", err)
	}
	content, total := string(data), 0
	var diffs []string
	for i, e := range in.Edits {
		var n int
		if content, n, err = applyEdit(content, e.Old, e.New, e.ReplaceAll); err != nil {
			return errorf("edit %d of %d: %v in %s (earlier edits count; nothing was changed)", i+1, len(in.Edits), err, path)
		}
		total += n
		diffs = append(diffs, miniDiff(e.Old, e.New))
	}
	if err := env.beforeWrite(path); err != nil {
		return errorf("cannot checkpoint %s: %v", path, err)
	}
	if _, err := pathpolicy.WritePath(env.Cwd, path); err != nil {
		return errorf("%v", err)
	}
	if err := replaceFile(root, rel, []byte(content)); err != nil {
		return errorf("%v", err)
	}
	env.markRead(path)
	return env.afterWrite(ctx, path, Result{
		Content: fmt.Sprintf("Edited %s (%d edits, %d replacement(s))", path, len(in.Edits), total),
		Display: strings.Join(diffs, "\n  ⋯\n"),
	})
}

// miniDiff renders removed/added lines for the UI.
func miniDiff(old, new string) string {
	var b strings.Builder
	for _, l := range strings.Split(old, "\n") {
		b.WriteString("- " + l + "\n")
	}
	for _, l := range strings.Split(new, "\n") {
		b.WriteString("+ " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
