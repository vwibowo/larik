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
	if err := env.checkFresh(path); err != nil {
		return errorf("%v", err)
	}
	_, statErr := os.Stat(path)
	existed := statErr == nil
	env.beforeWrite(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errorf("%v", err)
	}
	if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
		return errorf("%v", err)
	}
	env.markRead(path)
	verb := "Created"
	if existed {
		verb = "Overwrote"
	}
	lines := strings.Count(strings.TrimSuffix(in.Content, "\n"), "\n") + 1
	return Result{Content: fmt.Sprintf("%s %s (%d lines)", verb, path, lines)}
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
	if in.Old == in.New {
		return errorf("old_string and new_string are identical")
	}
	path := env.Abs(in.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return errorf("%v", err)
	}
	if err := env.checkFresh(path); err != nil {
		return errorf("%v", err)
	}
	content := string(data)
	if in.Old == "" {
		return errorf("old_string is empty; use write to create files")
	}
	n := strings.Count(content, in.Old)
	switch {
	case n == 0:
		return errorf("old_string not found in %s", path)
	case n > 1 && !in.ReplaceAll:
		return errorf("old_string appears %d times in %s; add surrounding context to make it unique or set replace_all", n, path)
	}
	updated := strings.Replace(content, in.Old, in.New, map[bool]int{true: -1, false: 1}[in.ReplaceAll])
	env.beforeWrite(path)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return errorf("%v", err)
	}
	env.markRead(path)
	return Result{
		Content: fmt.Sprintf("Edited %s (%d replacement(s))", path, map[bool]int{true: n, false: 1}[in.ReplaceAll]),
		Display: miniDiff(in.Old, in.New),
	}
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
