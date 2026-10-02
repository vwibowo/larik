package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

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
	lines := newSearchLines(env.Cwd)
	if rg := ripgrepPath(); rg != "" {
		err = ripgrep(ctx, rg, root, in, lines)
	} else {
		err = goGrep(ctx, root, in, lines)
	}
	if err != nil {
		return errorf("%v", err)
	}
	if lines.n == 0 && !lines.full {
		return Result{Content: "No matches found."}
	}
	return Result{Content: lines.String("more matches not shown; narrow the pattern, path or glob")}
}

var ripgrepPath = sync.OnceValue(func() string {
	rg, _ := exec.LookPath("rg")
	return rg
})

// ripgrep streams rg's matches into out and stops rg once out is full, so
// a broad pattern over a large tree costs no more than the lines shown.
func ripgrep(ctx context.Context, rg, root string, in grepInput, out *searchLines) error {
	args := []string{"--line-number", "--no-heading", "--color=never", "--max-columns=300", "--max-count=50"}
	if in.IgnoreCase {
		args = append(args, "-i")
	}
	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}
	args = append(args, "-e", in.Pattern, root)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, rg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	stopped := false
	for sc.Scan() {
		if !out.add(sc.Text()) {
			stopped = true
			break
		}
	}
	if stopped || sc.Err() != nil {
		cancel() // rg may still be writing; what's left isn't shown
	}
	err = cmd.Wait()
	if stopped {
		return nil
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		return nil // no matches
	case err != nil && out.n == 0:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("rg: %s", msg)
		}
		return err
	}
	// Exit 2 with matches: some files couldn't be read; show what was found.
	return nil
}

// goGrep is grep without ripgrep: files are searched a batch at a time in
// parallel, and their matches added in walk order, so results stay the
// same from run to run and the search stops once out is full.
func goGrep(ctx context.Context, root string, in grepInput, out *searchLines) error {
	pat := in.Pattern
	if in.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return err
	}
	var files []string
	err = WalkFiles(ctx, root, func(path string) error {
		if in.Glob != "" {
			if ok, _ := doublestar.Match(in.Glob, filepath.Base(path)); !ok {
				if ok, _ := doublestar.Match(in.Glob, path); !ok {
					return nil
				}
			}
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return err
	}
	workers := runtime.GOMAXPROCS(0)
	for start := 0; start < len(files); start += 4 * workers {
		batch := files[start:min(start+4*workers, len(files))]
		found := make([][]string, len(batch))
		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		for i, path := range batch {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				found[i] = grepFile(path, re)
			}()
		}
		wg.Wait()
		for _, lines := range found {
			for _, line := range lines {
				if !out.add(line) {
					return nil
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// grepFile returns path's matching lines as path:line:text, at most 50 as
// rg --max-count gives. A file with a NUL in its first 8 KB is binary.
func grepFile(path string, re *regexp.Regexp) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	if head, _ := r.Peek(8192); bytes.IndexByte(head, 0) >= 0 {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan() && len(out) < 50; n++ {
		line := sc.Text()
		if strings.ContainsRune(line, 0) {
			return nil // binary after all
		}
		if re.MatchString(line) {
			out = append(out, fmt.Sprintf("%s:%d:%s", path, n, safeCut(line, 300)))
		}
	}
	return out
}

// searchLines collects result lines for the model: paths relative to the
// working directory, at most maxSearchResults lines and MaxOutputBytes
// bytes, cut on line boundaries so no result is half shown.
type searchLines struct {
	prefix string
	b      strings.Builder
	n      int
	full   bool
}

func newSearchLines(cwd string) *searchLines {
	return &searchLines{prefix: strings.TrimSuffix(cwd, string(filepath.Separator)) + string(filepath.Separator)}
}

// add records a line and reports whether there is room for more.
func (s *searchLines) add(line string) bool {
	if s.full {
		return false
	}
	line = strings.TrimPrefix(line, s.prefix)
	if s.n >= maxSearchResults || s.b.Len()+len(line)+1 > MaxOutputBytes {
		s.full = true
		return false
	}
	s.b.WriteString(line)
	s.b.WriteByte('\n')
	s.n++
	return true
}

// String is the collected lines, ending with "... (more)" when some were
// left out.
func (s *searchLines) String(more string) string {
	if !s.full {
		return s.b.String()
	}
	return s.b.String() + "... (" + more + ")\n"
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
	// Start at the pattern's literal directory, so a pattern that names a
	// directory the walk would skip (.github/workflows/*.yml, dist/**)
	// still searches it.
	if base, rest := doublestar.SplitPattern(filepath.ToSlash(in.Pattern)); base != "." && !filepath.IsAbs(base) {
		root, in.Pattern = filepath.Join(root, filepath.FromSlash(base)), rest
	}
	type hit struct {
		path string
		mod  int64
	}
	var hits []hit
	match := func(path string) error {
		rel, _ := filepath.Rel(root, path)
		if ok, _ := doublestar.Match(in.Pattern, filepath.ToSlash(rel)); ok {
			var mod int64
			if fi, err := os.Stat(path); err == nil {
				mod = fi.ModTime().UnixNano()
			}
			hits = append(hits, hit{path, mod})
		}
		return nil
	}
	// ripgrep lists files as grep searches them: .gitignore'd build output
	// and caches left out, in parallel. When that finds nothing (a pattern
	// aimed at ignored or hidden files), the plain walk still looks.
	if rg := ripgrepPath(); rg != "" {
		err = rgFiles(ctx, rg, root, func(path string) error {
			if rel, _ := filepath.Rel(root, path); !inSkippedDir(rel) {
				return match(path)
			}
			return nil
		})
	}
	if len(hits) == 0 {
		err = WalkFiles(ctx, root, match)
	}
	if err != nil {
		return errorf("%v", err)
	}
	if len(hits) == 0 {
		return Result{Content: "No files found."}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].mod > hits[j].mod })
	lines := newSearchLines(env.Cwd)
	for _, h := range hits {
		if !lines.add(h.path) {
			break
		}
	}
	return Result{Content: lines.String(fmt.Sprintf("%d more", len(hits)-lines.n))}
}

// rgFiles calls fn for each file rg lists under root.
func rgFiles(ctx context.Context, rg, root string, fn func(path string) error) error {
	cmd := exec.CommandContext(ctx, rg, "--files", "--no-messages", "--color=never", root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if err := fn(sc.Text()); err != nil {
			break
		}
	}
	io.Copy(io.Discard, stdout)
	cmd.Wait() // exit 1 is no files, 2 some unreadable: what was listed stands
	return ctx.Err()
}

// inSkippedDir reports whether rel lies in a directory WalkFiles skips.
func inSkippedDir(rel string) bool {
	dirs := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	for _, d := range dirs {
		if skipDirs[d] {
			return true
		}
	}
	return false
}

// WalkFiles calls fn for each regular file under root, skipping hidden
// directories and common dependency and build output directories.
func WalkFiles(ctx context.Context, root string, fn func(path string) error) error {
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
