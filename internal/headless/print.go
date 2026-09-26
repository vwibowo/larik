// Package headless runs a single prompt without a TUI, for scripts and CI.
package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"larik/internal/agent"
)

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json" // one event per line
)

// Run executes prompt and writes results. Permission prompts cannot be
// answered interactively, so anything that would ask is denied; use
// --mode accept-edits/yolo or allow rules to grant access up front.
// It returns a non-nil error when the run ended in an error.
func Run(ctx context.Context, a *agent.Agent, prompt string, format Format, stdout, stderr io.Writer) error {
	enc := json.NewEncoder(stdout)
	var failure string
	endsWithNewline := true
	for e := range a.Run(ctx, prompt) {
		if e.Kind == agent.EvPermission {
			e.Reply <- agent.PermissionReply{Allow: false, Reason: "running non-interactively; permission prompts are auto-denied"}
		}
		if e.Kind == agent.EvError {
			failure = e.Text
		}
		if format == FormatJSON {
			if e.Kind == agent.EvTextDelta || e.Kind == agent.EvThinkingDelta {
				continue // the assistant_message event carries the full text
			}
			if err := enc.Encode(e); err != nil {
				return err
			}
			continue
		}
		switch e.Kind {
		case agent.EvTextDelta:
			fmt.Fprint(stdout, e.Text)
			endsWithNewline = strings.HasSuffix(e.Text, "\n")
		case agent.EvAssistant:
			if !endsWithNewline {
				fmt.Fprintln(stdout)
				endsWithNewline = true
			}
		case agent.EvToolStart:
			fmt.Fprintf(stderr, "→ %s %s\n", e.ToolName, compact(e.Input))
		case agent.EvToolEnd:
			if e.IsError {
				fmt.Fprintf(stderr, "✗ %s: %s\n", e.ToolName, firstLine(e.Output))
			}
		case agent.EvPermission:
			fmt.Fprintf(stderr, "✗ %s needs approval (denied in non-interactive mode; add an allow rule or use --mode)\n", e.ToolName)
		case agent.EvNotice:
			fmt.Fprintf(stderr, "! %s\n", e.Text)
		case agent.EvError:
			fmt.Fprintf(stderr, "error: %s\n", e.Text)
		}
	}
	if failure != "" {
		return fmt.Errorf("%s", failure)
	}
	return nil
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
