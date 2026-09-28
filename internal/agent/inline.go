package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"larik/internal/permission"
	"larik/internal/tools"
)

// inlineCmd is Claude Code's !`command` syntax in skills and commands.
var inlineCmd = regexp.MustCompile("!`([^`\n]+)`")

// runInline replaces each !`command` in a skill or command the user ran
// with that command's output, so it arrives as context. Each runs like the
// bash tool, in the sandbox when there is one, but only when the
// permission rules allow it without asking: the text may come from a
// repository, and there is nobody to ask before the prompt is sent.
func (a *Agent) runInline(ctx context.Context, name, text string, emit func(Event)) string {
	var ran, skipped []string
	out := inlineCmd.ReplaceAllStringFunc(text, func(m string) string {
		command := strings.TrimSpace(inlineCmd.FindStringSubmatch(m)[1])
		raw, _ := json.Marshal(map[string]string{"command": command})
		if a.opts.Perms != nil {
			if d, reason := a.opts.Perms.Decide(permission.Call{Tool: "bash", Input: raw}); d != permission.Allow {
				if reason == "" {
					reason = "it would need your approval"
				}
				skipped = append(skipped, command)
				return fmt.Sprintf("[%s was not run: %s]", command, reason)
			}
		}
		res := tools.Bash{}.Run(ctx, a.env, raw)
		ran = append(ran, command)
		return strings.TrimRight(res.Content, "\n")
	})
	if len(ran) > 0 {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("/%s ran %s for context", name, strings.Join(quoteAll(ran), ", "))})
	}
	if len(skipped) > 0 {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("/%s: didn't run %s (not allowed without asking; add an allow rule to run it)", name, strings.Join(quoteAll(skipped), ", "))})
	}
	return out
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "`" + s + "`"
	}
	return out
}
