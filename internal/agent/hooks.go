package agent

import (
	"context"
	"strings"
	"time"

	"larik/internal/hooks"
)

// maxStopContinuations bounds how often Stop hooks may force another turn.
const maxStopContinuations = 5

// runHook runs hooks for an event, surfacing their user-facing messages.
func (a *Agent) runHook(ctx context.Context, emit func(Event), in hooks.Input, target string) hooks.Result {
	if !a.opts.Hooks.Has(in.HookEventName) {
		return hooks.Result{}
	}
	if a.opts.Perms != nil {
		in.PermissionMode = string(a.opts.Perms.Mode())
	}
	res := a.opts.Hooks.Run(ctx, in, target)
	for _, m := range res.Messages {
		emit(Event{Kind: EvNotice, Text: "hook: " + m})
	}
	return res
}

func hookContext(source string, parts []string) string {
	return "<hook-context source=\"" + source + "\">\n" + strings.Join(parts, "\n") + "\n</hook-context>"
}

func hookFeedback(source, text string) string {
	return "<hook-feedback source=\"" + source + "\">\n" + text + "\n</hook-feedback>"
}

// sessionStart fires SessionStart once per fresh context; its context is
// delivered with the next user message.
func (a *Agent) sessionStart(ctx context.Context, emit func(Event)) {
	a.mu.Lock()
	if a.sessionStarted {
		a.mu.Unlock()
		return
	}
	a.sessionStarted = true
	source := a.startSource
	a.mu.Unlock()

	res := a.runHook(ctx, emit, hooks.Input{HookEventName: hooks.SessionStart, Source: source}, source)
	if len(res.Context) > 0 {
		a.mu.Lock()
		a.notes = append(a.notes, hookContext("SessionStart", res.Context))
		a.mu.Unlock()
	}
}

// requestHalt records a hook's continue:false so the loop ends after the
// current tool round.
func (a *Agent) requestHalt(reason string) {
	a.mu.Lock()
	a.halted = true
	if reason != "" {
		a.haltReason = reason
	}
	a.mu.Unlock()
}

func (a *Agent) takeHalt() (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, r := a.halted, a.haltReason
	a.halted, a.haltReason = false, ""
	return h, r
}

// notify fires Notification hooks without waiting; results are ignored
// because the event stream may already be closed when they finish.
func (a *Agent) notify(message string) {
	if !a.opts.Hooks.Has(hooks.Notification) {
		return
	}
	in := hooks.Input{HookEventName: hooks.Notification, Message: message}
	if a.opts.Perms != nil {
		in.PermissionMode = string(a.opts.Perms.Mode())
	}
	go a.opts.Hooks.Run(context.Background(), in, "")
}

// End fires SessionEnd hooks. Call it once when the front end exits.
func (a *Agent) End(reason string) {
	if !a.opts.Hooks.Has(hooks.SessionEnd) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a.opts.Hooks.Run(ctx, hooks.Input{HookEventName: hooks.SessionEnd, Reason: reason}, reason)
}
