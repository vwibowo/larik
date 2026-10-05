// Package headless runs a single prompt without a TUI, for scripts and CI.
package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"larik/internal/agent"
	"larik/internal/permission"
	"larik/internal/tools"
)

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json" // one event per line
)

const denyReason = "running non-interactively; permission prompts are auto-denied"

// Run executes prompt and writes results. Permission prompts cannot be
// answered interactively, so anything that would ask is denied; use
// --mode accept-edits/yolo or allow rules to grant access up front.
//
// If the model starts background tasks, Run keeps going until they have
// all finished and their results have been delivered to the model.
// It returns a non-nil error when the run ended in an error.
func Run(ctx context.Context, a *agent.Agent, prompt string, format Format, stdout, stderr io.Writer) error {
	p := &printer{format: format, stdout: stdout, stderr: stderr, enc: json.NewEncoder(stdout), atLineStart: true}

	// Background-task events arrive on their own channel for the agent's
	// whole lifetime; drain it concurrently.
	taskDone := make(chan struct{}, agent.MaxBackground*4)
	bgCtx, stopBg := context.WithCancel(ctx)
	defer stopBg()
	go func() {
		for {
			select {
			case e := <-a.Background():
				if e.Kind == agent.EvPermission {
					e.Reply <- deny(e)
				}
				p.handle(e)
				if e.Kind == agent.EvTaskDone {
					select {
					case taskDone <- struct{}{}:
					default:
					}
				}
			case <-bgCtx.Done():
				return
			}
		}
	}()

	failure := p.consume(a.Run(ctx, prompt))
	for ctx.Err() == nil {
		if ch, ok := a.RunNotifications(ctx); ok {
			if f := p.consume(ch); f != "" {
				failure = f
			}
			continue
		}
		// A task that finished just after RunNotifications looked has a
		// result waiting; go round again to deliver it.
		if a.RunningBackground() == 0 && a.PendingNotifications() == 0 {
			break
		}
		select {
		case <-taskDone:
		case <-ctx.Done():
		}
	}
	if failure != "" {
		return ErrReported{failure}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// ErrReported is returned when a run failed and the error was already
// printed, so callers can exit non-zero without printing it again.
type ErrReported struct{ Msg string }

func (e ErrReported) Error() string { return e.Msg }

type printer struct {
	format         Format
	stdout, stderr io.Writer
	enc            *json.Encoder

	mu          sync.Mutex // foreground and background events interleave
	atLineStart bool
}

// consume handles a turn's events and returns the last error text.
func (p *printer) consume(ch <-chan agent.Event) string {
	var failure string
	for e := range ch {
		if e.Kind == agent.EvPermission {
			e.Reply <- deny(e)
		}
		if e.Kind == agent.EvError && e.Agent == "" {
			failure = e.Text
		}
		p.handle(e)
	}
	return failure
}

func (p *printer) handle(e agent.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.format == FormatJSON {
		if e.Kind == agent.EvTextDelta || e.Kind == agent.EvThinkingDelta {
			return // the assistant_message event carries the full text
		}
		_ = p.enc.Encode(e)
		return
	}
	// Keep stderr lines from gluing onto a partial stdout line.
	if e.Kind != agent.EvTextDelta && e.Kind != agent.EvAssistant && !p.atLineStart {
		switch e.Kind {
		case agent.EvToolStart, agent.EvToolEnd, agent.EvPermission, agent.EvTaskDone, agent.EvCompacted, agent.EvNotice, agent.EvError:
			fmt.Fprintln(p.stdout)
			p.atLineStart = true
		}
	}
	switch e.Kind {
	case agent.EvTextDelta:
		fmt.Fprint(p.stdout, e.Text)
		p.atLineStart = strings.HasSuffix(e.Text, "\n")
	case agent.EvAssistant:
		if !p.atLineStart {
			fmt.Fprintln(p.stdout)
			p.atLineStart = true
		}
	case agent.EvToolStart:
		if todos, err := tools.ParseTodos(e.Input); e.ToolName == tools.TodoToolName && err == nil {
			// The task list reads better as a checklist than as JSON.
			fmt.Fprintf(p.stderr, "%s→ tasks\n%s\n", nest(e), indent(tools.FormatTodos(todos), nest(e)+"  "))
			break
		}
		fmt.Fprintf(p.stderr, "%s→ %s %s\n", nest(e), e.ToolName, compact(e.Input))
	case agent.EvToolEnd:
		if e.IsError {
			fmt.Fprintf(p.stderr, "%s✗ %s: %s\n", nest(e), e.ToolName, firstLine(e.Output))
		}
	case agent.EvPermission:
		fmt.Fprintf(p.stderr, "%s✗ %s needs approval (denied in non-interactive mode; add an allow rule or use --mode)\n", nest(e), e.ToolName)
	case agent.EvTaskDone:
		fmt.Fprintf(p.stderr, "◆ background %s (%s) %s\n", e.ToolID, e.Agent, e.StopReason)
	case agent.EvCompacted:
		if e.Compaction == nil || !e.Compaction.Available {
			fmt.Fprintf(p.stderr, "%s✓ context compacted\n", nest(e))
			break
		}
		c := e.Compaction
		fmt.Fprintf(p.stderr, "%s✓ context compacted: %s prompt → ~%s summary", nest(e), tokenCount(c.BeforeTokens), tokenCount(c.AfterTokens))
		switch {
		case c.SavedTokens > 0:
			fmt.Fprintf(p.stderr, " · ~%s saved", tokenCount(c.SavedTokens))
		case c.SavedTokens < 0:
			fmt.Fprintf(p.stderr, " · ~%s larger", tokenCount(-c.SavedTokens))
		}
		fmt.Fprintln(p.stderr)
	case agent.EvNotice:
		fmt.Fprintf(p.stderr, "%s! %s\n", nest(e), e.Text)
	case agent.EvError:
		fmt.Fprintf(p.stderr, "%serror: %s\n", nest(e), e.Text)
	}
}

func tokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%gM", float64(n/100_000)/10)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func compact(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// nest prefixes subagent activity so it reads as part of its task.
func nest(e agent.Event) string {
	if e.Agent == "" {
		return ""
	}
	return "  ↳ [" + e.Agent + "] "
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// deny answers a permission request, which a non-interactive run can't
// put to anyone.
func deny(e agent.Event) agent.PermissionReply {
	if e.ToolName == permission.ExitPlanTool {
		return agent.PermissionReply{Reason: "this is a non-interactive run, so plan mode can't end here; give the plan as your final answer"}
	}
	return agent.PermissionReply{Reason: denyReason}
}
