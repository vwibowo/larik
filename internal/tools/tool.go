// Package tools implements the built-in tools the agent can call.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"larik/internal/llm"
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
	BeforeWrite func(path string)

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

func (e *Env) beforeWrite(path string) {
	if e.BeforeWrite != nil {
		e.BeforeWrite(path)
	}
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
}

func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{byName: map[string]Tool{}}
	for _, t := range ts {
		r.list = append(r.list, t)
		r.byName[t.Spec().Name] = t
	}
	return r
}

// Default returns the built-in tool set.
func Default() *Registry {
	return NewRegistry(Read{}, Write{}, Edit{}, Bash{}, Grep{}, Glob{})
}

func (r *Registry) Get(name string) (Tool, bool) { t, ok := r.byName[name]; return t, ok }

func (r *Registry) Specs() []llm.ToolSpec {
	out := make([]llm.ToolSpec, len(r.list))
	for i, t := range r.list {
		out[i] = t.Spec()
	}
	return out
}
