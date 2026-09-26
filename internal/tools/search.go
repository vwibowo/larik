package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"larik/internal/llm"
)

const maxSearchResults = 500

// skipDirs are never descended into by the pure-Go walkers.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".venv": true, "__pycache__": true, "dist": true, "build": true, "target": true}

// Grep searches file contents by regex, preferring ripgrep when installed.
type Grep struct{}

func (Grep) ReadOnly() bool { return true }
func (Grep) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "grep",
		Description: "Search file contents with a regular expression. Returns path:line:text matches. Respects .gitignore when ripgrep is installed.",
		Schema: schema(`{"type":"object","properties":{
			"pattern":{"type":"string","description":"Regular expression"},
			"path":{"type":"string","description":"Directory or file to search (default: working directory)"},
			"glob":{"type":"string","description":"Only search files matching this glob, e.g. *.go"},
			"ignore_case":{"type":"boolean"}},
			"required":["pattern"]}`),
	}
}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
}

func (Grep) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[grepInput](input)
	if err != nil {
		return errorf("%v", err)
	}
	root := env.Abs(in.Path)
	var out string
	if rg, err := exec.LookPath("rg"); err == nil {
		out, err = ripgrep(ctx, rg, root, in)
		if err != nil {
			return errorf("%v", err)
		}
	} else {
		out, err = goGrep(ctx, root, in)
		if err != nil {
			return errorf("%v", err)
		}
	}
	if out == "" {
		return Result{Content: "No matches found."}
	}
	return Result{Content: Truncate(out, MaxOutputBytes)}
}

func ripgrep(ctx context.Context, rg, root string, in grepInput) (string, error) {
	args := []string{"--line-number", "--no-heading", "--color=never", "--max-columns=300", "--max-count=50"}
	if in.IgnoreCase {
		args = append(args, "-i")
	}
	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}
	args = append(args, "-e", in.Pattern, root)
	cmd := exec.CommandContext(ctx, rg, args...)
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil // no matches
	}
	if err != nil {
		if exitErr != nil {
			return "", fmt.Errorf("rg: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return capLines(string(out), maxSearchResults), nil
}

func goGrep(ctx context.Context, root string, in grepInput) (string, error) {
	pat := in.Pattern
	if in.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	count := 0
	err = walkFiles(ctx, root, func(path string) error {
		if in.Glob != "" {
			if ok, _ := doublestar.Match(in.Glob, filepath.Base(path)); !ok {
				if ok, _ := doublestar.Match(in.Glob, path); !ok {
					return nil
				}
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if strings.ContainsRune(line, 0) {
				return nil // binary
			}
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d:%s\n", path, n, safeCut(line, 300))
				if count++; count >= maxSearchResults {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	return b.String(), err
}

// Glob finds files by pattern.
type Glob struct{}

func (Glob) ReadOnly() bool { return true }
func (Glob) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "glob",
		Description: "Find files by glob pattern (supports **), e.g. **/*.go. Returns paths sorted by modification time, newest first.",
		Schema: schema(`{"type":"object","properties":{
			"pattern":{"type":"string"},
			"path":{"type":"string","description":"Directory to search (default: working directory)"}},
			"required":["pattern"]}`),
	}
}

func (Glob) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	if !doublestar.ValidatePattern(in.Pattern) {
		return errorf("invalid glob pattern %q", in.Pattern)
	}
	root := env.Abs(in.Path)
	type hit struct {
		path string
		mod  int64
	}
	var hits []hit
	err = walkFiles(ctx, root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		if ok, _ := doublestar.Match(in.Pattern, filepath.ToSlash(rel)); ok {
			var mod int64
			if fi, err := os.Stat(path); err == nil {
				mod = fi.ModTime().UnixNano()
			}
			hits = append(hits, hit{path, mod})
		}
		return nil
	})
	if err != nil {
		return errorf("%v", err)
	}
	if len(hits) == 0 {
		return Result{Content: "No files found."}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].mod > hits[j].mod })
	var b strings.Builder
	for i, h := range hits {
		if i == maxSearchResults {
			fmt.Fprintf(&b, "... (%d more)\n", len(hits)-i)
			break
		}
		b.WriteString(h.path + "\n")
	}
	return Result{Content: b.String()}
}

func walkFiles(ctx context.Context, root string, fn func(path string) error) error {
	fi, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fn(root)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return fn(path)
	})
	if errors.Is(err, fs.SkipAll) {
		return nil
	}
	return err
}

func capLines(s string, n int) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "") + fmt.Sprintf("... (%d more matches)\n", len(lines)-n)
}
