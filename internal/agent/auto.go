package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"larik/internal/llm"
	"larik/internal/session"
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
