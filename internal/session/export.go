package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"larik/internal/llm"
)

// exportResultLines caps each tool result in an export.
const exportResultLines = 30

// PromptText is a user message's text without the <system-note> block Larik
// puts in front of a prompt (plan mode, changed files and the like): what
// the user actually typed, for showing them their prompt again.
func PromptText(m llm.Message) string {
	text := strings.TrimSpace(m.Text())
	if rest, ok := strings.CutPrefix(text, "<system-note>"); ok {
		if _, after, ok := strings.Cut(rest, "</system-note>"); ok {
			text = strings.TrimSpace(after)
		}
	}
	return text
}

// Markdown renders a session's full history for reading: prompts, replies,
// and each tool call with its result (capped). Thinking, attached file
// contents and images are left out; attachments are named.
func Markdown(st *State, id string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Larik session %s\n\n", id)
	var meta []string
	if st.Meta.Cwd != "" {
		meta = append(meta, "Directory: `"+st.Meta.Cwd+"`")
	}
	if st.Meta.Model != "" {
		meta = append(meta, "Model: "+strings.TrimPrefix(st.Meta.Provider+"/"+st.Meta.Model, "/"))
	}
	if st.Meta.ForkOf != "" {
		meta = append(meta, "Branched from: "+st.Meta.ForkOf)
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, "  \n") + "\n\n")
	}

	// Results come in the message after their calls; pair them by id.
	results := map[string]llm.Block{}
	for _, m := range st.All {
		for _, bl := range m.Blocks {
			if bl.Type == llm.BlockToolResult {
				results[bl.ID] = bl
			}
		}
	}
	for _, m := range st.All {
		switch m.Role {
		case llm.RoleUser:
			text := strings.TrimSpace(m.Text())
			var attached []string
			for _, bl := range m.Blocks {
				if bl.Attachment != "" && bl.Type != llm.BlockImage {
					attached = append(attached, "`"+bl.Attachment+"`")
				}
			}
			if text == "" && len(attached) == 0 {
				continue // only tool results, shown with their calls
			}
			text = PromptText(m)
			heading := "## User"
			if m.Role == llm.RoleUser && !IsPrompt(m) && text != "" {
				heading = "## Larik" // background results, hook feedback
			}
			b.WriteString(heading + "\n\n")
			if text != "" {
				b.WriteString(text + "\n\n")
			}
			if len(attached) > 0 {
				b.WriteString("_Attached: " + strings.Join(attached, ", ") + "_\n\n")
			}
		case llm.RoleAssistant:
			var parts []string
			for _, bl := range m.Blocks {
				switch bl.Type {
				case llm.BlockText:
					if t := strings.TrimSpace(bl.Text); t != "" {
						parts = append(parts, t)
					}
				case llm.BlockToolUse:
					parts = append(parts, toolMarkdown(bl, results[bl.ID]))
				}
			}
			if len(parts) == 0 {
				continue
			}
			b.WriteString("## Assistant")
			if m.Model != "" {
				b.WriteString(" · " + m.Model)
			}
			b.WriteString("\n\n" + strings.Join(parts, "\n\n") + "\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func toolMarkdown(use, res llm.Block) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**", use.Name)
	if len(use.Input) > 0 && string(use.Input) != "{}" {
		var pretty any
		if json.Unmarshal(use.Input, &pretty) == nil {
			if in, err := json.MarshalIndent(pretty, "", "  "); err == nil {
				b.WriteString("\n\n" + fence(string(in), "json"))
			}
		}
	}
	if res.ID == "" {
		return b.String() // no result recorded (an interrupted turn)
	}
	label := "Result"
	if res.IsError {
		label = "Error"
	}
	out := strings.TrimRight(res.Content, "\n")
	if lines := strings.Split(out, "\n"); len(lines) > exportResultLines {
		out = strings.Join(lines[:exportResultLines], "\n") + fmt.Sprintf("\n… (%d more lines)", len(lines)-exportResultLines)
	}
	fmt.Fprintf(&b, "\n\n<details><summary>%s</summary>\n\n%s\n\n</details>", label, fence(out, ""))
	return b.String()
}

// fence wraps s in a code fence longer than any backtick run inside it.
func fence(s, lang string) string {
	ticks := "```"
	for strings.Contains(s, ticks) {
		ticks += "`"
	}
	return ticks + lang + "\n" + s + "\n" + ticks
}
