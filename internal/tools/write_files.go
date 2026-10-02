package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"larik/internal/llm"
	"larik/internal/pathpolicy"
)

// WriteFilesToolName creates several files without a model round trip between them.
const WriteFilesToolName = "write_files"

// WriteFiles delegates each file to write, preserving per-file authorization,
// hooks, checkpoints, and diagnostics.
type WriteFiles struct{}

func (WriteFiles) ReadOnly() bool { return false }

func (WriteFiles) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        WriteFilesToolName,
		Description: "Create or overwrite several independent files in one call. Use this instead of separate write calls when creating two or more files. Each file is checked and written separately; existing files must be read first.",
		Schema: schema(`{"type":"object","properties":{
			"files":{"type":"array","minItems":2,"maxItems":16,"items":{"type":"object","properties":{
				"path":{"type":"string"},"content":{"type":"string","description":"Full file content"}},
				"required":["path","content"]}}},"required":["files"]}`),
	}
}

func (WriteFiles) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	if len(in.Files) < 2 || len(in.Files) > 16 {
		return errorf("write_files needs 2 to 16 files")
	}
	seen := map[string]bool{}
	for _, file := range in.Files {
		path := env.Abs(file.Path)
		if _, err := pathpolicy.WritePath(env.Cwd, path); err != nil {
			return errorf("%s: %v", file.Path, err)
		}
		if seen[path] {
			return errorf("duplicate write path %q", file.Path)
		}
		seen[path] = true
		if err := env.checkFresh(path); err != nil {
			return errorf("%s: %v", file.Path, err)
		}
	}
	caller, ok := CallerFrom(ctx)
	if !ok {
		return errorf("write_files requires an agent tool caller")
	}
	var output strings.Builder
	for i, file := range in.Files {
		args, _ := json.Marshal(file)
		result := caller.CallTool(ctx, "write", args)
		if result.IsError {
			fmt.Fprintf(&output, "%s: %s\n", file.Path, result.Content)
			return Result{Content: fmt.Sprintf("Stopped at file %d of %d; earlier files may already have been written.\n%s", i+1, len(in.Files), strings.TrimSpace(output.String())), IsError: true}
		}
		output.WriteString(result.Content + "\n") // names the file already
	}
	return Result{Content: strings.TrimSpace(output.String())}
}
