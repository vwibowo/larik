package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// Auto mode (permission.ModeAuto) puts each call that would otherwise ask
// the user to a model first. The model sees the call and what the user
// asked for, and approves it or hands it to the user with a reason.

// AutoCall is what the classifier judges.
type AutoCall struct {
	Tool  string
	Input json.RawMessage
	Cwd   string
	// Prompts are the user's most recent prompts, oldest first: what was
	// actually asked for. Tool results and the model's own text are left
	// out, since instructions injected there must not count as the user's.
	Prompts []string
	// Task, for a subagent's call, is the task the parent agent gave it.
	// A model wrote it, not the user.
	Task string
	// Sandboxed reports whether bash normally runs in the OS sandbox, so a
	// bash call here is one that opted out of it.
	Sandboxed bool
}

// AutoVerdict is the classifier's answer. Reason says why, in a sentence.
type AutoVerdict struct {
	Allow  bool
	Reason string
}

// AutoApprover judges a call in auto mode. An error sends the call to the
// user, as does a verdict that doesn't allow it.
type AutoApprover func(ctx context.Context, call AutoCall) (AutoVerdict, error)

const (
	autoPrompts   = 3    // recent prompts shown to the classifier
	autoPromptLen = 4000 // characters kept of each
	autoCacheSize = 200
)

// SetAutoApprover sets the auto-mode classifier. Subagents spawned
// afterwards use it too.
func (a *Agent) SetAutoApprover(fn AutoApprover) {
	a.mu.Lock()
	a.opts.AutoApprove = fn
	a.mu.Unlock()
}

func (a *Agent) autoApprover() AutoApprover {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.AutoApprove
}

// autoApprove asks the classifier about a call. Calls it approved earlier
// in the session (the same tool with the same input) are approved again
// without asking it.
func (a *Agent) autoApprove(ctx context.Context, tool string, input json.RawMessage) (AutoVerdict, error) {
	approve := a.autoApprover()
	if approve == nil {
		return AutoVerdict{}, errors.New("no model is set up to check calls")
	}
	key := tool + "\x00" + string(input)
	root := a
	for root.parent != nil {
		root = root.parent
	}
	root.mu.Lock()
	cached := root.autoAllowed[key]
	root.mu.Unlock()
	if cached {
		return AutoVerdict{Allow: true, Reason: "approved earlier this session"}, nil
	}

	call := AutoCall{Tool: tool, Input: input, Cwd: a.opts.Cwd, Prompts: root.recentPrompts()}
	if a != root {
		call.Task = a.firstPrompt()
	}
	if a.opts.Perms != nil {
		call.Sandboxed = a.opts.Perms.Sandboxed()
	}
	verdict, err := approve(ctx, call)
	if err == nil && verdict.Allow {
		root.mu.Lock()
		if root.autoAllowed == nil || len(root.autoAllowed) >= autoCacheSize {
			root.autoAllowed = map[string]bool{}
		}
		root.autoAllowed[key] = true
		root.mu.Unlock()
	}
	return verdict, err
}

// autoVerdicts are classifier verdicts started ahead of time for one batch
// of tool calls, by tool name and input.
type autoVerdicts map[string]*autoPending

type autoPending struct {
	done    chan struct{}
	verdict AutoVerdict
	err     error
}

// prefetchAuto starts the classifier, concurrently, on every call in uses
// that the permission rules would otherwise put to the user. Authorizing
// a batch still goes call by call, in order (hooks run and the user is
// asked one at a time), but each call's verdict is then already under
// way instead of costing a model round trip of its own. A call that a
// PreToolUse hook settles or rewrites leaves its verdict unused.
func (a *Agent) prefetchAuto(ctx context.Context, uses []llm.Block, registry *tools.Registry) autoVerdicts {
	perms := a.opts.Perms
	if perms == nil || perms.Mode() != permission.ModeAuto || len(uses) < 2 || a.autoApprover() == nil {
		return nil
	}
	type call struct {
		name  string
		input json.RawMessage
	}
	var calls []call
	for _, use := range uses {
		tool, ok := registry.Get(use.Name)
		if !ok || use.Input == nil {
			continue
		}
		name, input := use.Name, use.Input
		if name == tools.CallToolName {
			inner, args, err := registry.ResolveCall(input)
			if err != nil {
				continue
			}
			tool, name, input = inner, inner.Spec().Name, args
		}
		if name == tools.CodeToolName || name == tools.WriteFilesToolName || name == permission.ExitPlanTool {
			continue // authorized call by call, or always the user's
		}
		if d, _ := perms.Decide(permission.Call{Tool: name, ReadOnly: tool.ReadOnly(), Input: input}); d == permission.Ask {
			calls = append(calls, call{name, input})
		}
	}
	if len(calls) < 2 {
		return nil // nothing to overlap
	}
	out := autoVerdicts{}
	for _, c := range calls {
		key := c.name + "\x00" + string(c.input)
		if out[key] != nil {
			continue
		}
		p := &autoPending{done: make(chan struct{})}
		out[key] = p
		go func() {
			defer close(p.done)
			p.verdict, p.err = a.autoApprove(ctx, c.name, c.input)
		}()
	}
	return out
}

// get returns the verdict on a call: the one started ahead when there is
// one for exactly this call, otherwise a fresh one.
func (v autoVerdicts) get(ctx context.Context, a *Agent, name string, input json.RawMessage) (AutoVerdict, error) {
	p := v[name+"\x00"+string(input)]
	if p == nil {
		return a.autoApprove(ctx, name, input)
	}
	select {
	case <-p.done:
		return p.verdict, p.err
	case <-ctx.Done():
		return AutoVerdict{}, ctx.Err()
	}
}

// recentPrompts returns the last few things the user typed.
func (a *Agent) recentPrompts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for i := len(a.messages) - 1; i >= 0 && len(out) < autoPrompts; i-- {
		if m := a.messages[i]; session.IsPrompt(m) {
			out = append([]string{clip(userText(m), autoPromptLen)}, out...)
		}
	}
	return out
}

// firstPrompt is the message the agent was started with: a subagent's task.
func (a *Agent) firstPrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.messages {
		if m.Role == llm.RoleUser {
			if text := strings.TrimSpace(m.Text()); text != "" {
				return clip(text, autoPromptLen)
			}
		}
	}
	return ""
}

// userText is a prompt without the notes Larik puts in front of it.
func userText(m llm.Message) string {
	text := strings.TrimSpace(m.Text())
	if rest, ok := strings.CutPrefix(text, "<system-note>"); ok {
		if _, after, ok := strings.Cut(rest, "</system-note>"); ok {
			text = strings.TrimSpace(after)
		}
	}
	return text
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "… [cut]"
	}
	return s
}
