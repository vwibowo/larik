// Package tools implements the built-in tools the agent can call.
package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"larik/internal/llm"
	"larik/internal/pathpolicy"
)

// Result is what a tool returns to the model. Display is an optional
// richer rendering for the UI (e.g. a diff) that the model never sees.
type Result struct {
	Content string
	IsError bool
	Display string
}

type Tool interface {
	Spec() llm.ToolSpec
	// ReadOnly tools never modify state; they may run in parallel and are
	// allowed in plan mode.
	ReadOnly() bool
	Run(ctx context.Context, env *Env, input json.RawMessage) Result
}

// Env is the shared state tools operate in.
type Env struct {
	Cwd string

	// BeforeWrite is called with the absolute path before a file is modified,
	// so checkpoints can snapshot it.
	BeforeWrite func(path string) error

	// Diagnostics, if set, returns compiler/linter feedback for a file just
	// written (from language servers); it's appended to edit/write results.
	Diagnostics func(ctx context.Context, path string) string
	// Touch, if set, is told about files the agent reads (to warm up LSP).
	Touch func(path string)

	// Sandbox, if set, confines bash commands (unless a call opts out).
	Sandbox Sandbox
	// RawOutputDir is a private directory beside the session transcript.
	RawOutputDir string
	TokenSaver   *atomic.Bool

	mu    sync.Mutex
	reads map[string]time.Time // path -> mtime when last read
}

func NewEnv(cwd string) *Env { return &Env{Cwd: cwd, reads: map[string]time.Time{}} }

// Abs resolves p against the working directory.
func (e *Env) Abs(p string) string {
	if p == "" {
		return e.Cwd
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(e.Cwd, p)
}

func (e *Env) markRead(path string) {
	if fi, err := os.Stat(path); err == nil {
		e.mu.Lock()
		e.reads[path] = fi.ModTime()
		e.mu.Unlock()
	}
}

// checkFresh ensures an existing file was read and hasn't changed since, so
// the model never overwrites content it hasn't seen.
func (e *Env) checkFresh(path string) error {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	e.mu.Lock()
	seen, ok := e.reads[path]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s exists but has not been read yet; read it first", path)
	}
	if !fi.ModTime().Equal(seen) {
		return fmt.Errorf("%s was modified since it was last read; read it again", path)
	}
	return nil
}

func (e *Env) beforeWrite(path string) error {
	if e.BeforeWrite != nil {
		return e.BeforeWrite(path)
	}
	return nil
}

func (e *Env) writeRoot(path string) (*os.Root, string, error) {
	rel, err := pathpolicy.WritePath(e.Cwd, path)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(e.Cwd)
	return root, rel, err
}

// replaceFile writes a sibling temporary file and renames it over the target.
// Replacing the directory entry avoids modifying a hard-linked file outside
// the project and avoids following a target symlink installed during the write.
func replaceFile(root *os.Root, rel string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := root.Stat(rel); err == nil {
		mode = fi.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(rel), ".larik-write-"+hex.EncodeToString(nonce[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	var n int
	if n, err = f.Write(data); err != nil || n != len(data) {
		f.Close()
		if err == nil {
			return io.ErrShortWrite
		}
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = root.Chmod(tmp, mode); err != nil {
		return err
	}
	return root.Rename(tmp, rel)
}

func errorf(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

func decode[T any](input json.RawMessage) (T, error) {
	var v T
	if len(input) == 0 {
		return v, fmt.Errorf("INVALID_JSON: tool input was missing or malformed")
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return v, fmt.Errorf("INVALID_JSON: %v", err)
	}
	return v, nil
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// Registry is an ordered tool set.
type Registry struct {
	list   []Tool
	byName map[string]Tool
	// specs are taken once, when the registry is built for a fresh
	// context: tool descriptions are part of the cached prompt prefix, so
	// a tool whose description depends on config (the task tool's model
	// roles) must not change it mid-context.
	specs []llm.ToolSpec
}

func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{byName: map[string]Tool{}}
	for _, t := range ts {
		spec := t.Spec()
		r.list = append(r.list, t)
		r.specs = append(r.specs, spec)
		r.byName[spec.Name] = t
	}
	return r
}

// With returns a new registry with ts appended.
func (r *Registry) With(ts ...Tool) *Registry {
	return NewRegistry(append(append([]Tool(nil), r.list...), ts...)...)
}

// Builtin returns the built-in tools.
func Builtin() []Tool {
	return []Tool{Read{}, Write{}, Edit{}, Bash{}, RawOutput{}, Grep{}, Glob{}, TodoWrite{}, MultiEdit{}}
}

// Default returns a registry of the built-in tools.
func Default() *Registry { return NewRegistry(Builtin()...) }

func (r *Registry) Get(name string) (Tool, bool) { t, ok := r.byName[name]; return t, ok }

// Specs returns the tool specs as they were when the registry was built.
func (r *Registry) Specs() []llm.ToolSpec {
	return append([]llm.ToolSpec(nil), r.specs...)
}

// afterWrite appends diagnostics for a modified file to a tool result.
func (e *Env) afterWrite(ctx context.Context, path string, res Result) Result {
	if e.Diagnostics == nil || res.IsError {
		return res
	}
	if d := e.Diagnostics(ctx, path); d != "" {
		res.Content += "\n\n" + d
	}
	return res
}

// Sandbox runs a shell script confined by the OS sandbox.
type Sandbox interface {
	Command(script, dir string) *exec.Cmd
}

// ConcurrencySafe marks tools that aren't read-only (they still need
// permission) but can safely run in parallel with other such calls.
type ConcurrencySafe interface {
	ConcurrencySafe() bool
}

// Parallel reports whether a tool may run alongside other parallel calls.
func Parallel(t Tool) bool {
	if t.ReadOnly() {
		return true
	}
	c, ok := t.(ConcurrencySafe)
	return ok && c.ConcurrencySafe()
}
