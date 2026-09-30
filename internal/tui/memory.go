package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"larik/internal/memory"
)

// memoryCommand is /memory: list the notes kept across sessions, or show,
// add or delete one.
func (m *model) memoryCommand(args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	store := m.opts.Memory
	if store == nil {
		return info(`memory is off (remove "memory": {"enabled": false} from your config to turn it on)`)
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "":
		return info(m.memoryList(store))

	case "show", "read":
		if len(args) < 2 {
			return fail("usage: /memory show <name>")
		}
		n, ok := store.Get(args[1], "")
		if !ok {
			return fail("no note named " + args[1] + " (see /memory)")
		}
		return info(fmt.Sprintf("%s (%s, %s, saved %s)\n%s\n\n%s\n\n%s", n.Name, n.Type, n.Scope, n.Modified.Format("2006-01-02"), n.Description, n.Body, m.shortPaths(n.Path)))

	case "delete", "rm", "forget":
		if len(args) < 2 {
			return fail("usage: /memory delete <name>")
		}
		scope, err := store.Delete(args[1], "")
		if err != nil {
			return fail(err.Error() + " (see /memory)")
		}
		m.agent.AddNote(fmt.Sprintf("The user deleted the memory note %q; don't rely on it.", args[1]))
		return info(fmt.Sprintf("deleted the %s note %s", scope, args[1]))

	case "add":
		rest := args[1:]
		scope := memory.ScopeProject
		if len(rest) > 0 && rest[0] == "--user" {
			scope, rest = memory.ScopeUser, rest[1:]
		}
		text := strings.TrimSpace(strings.Join(rest, " "))
		if text == "" {
			return fail("usage: /memory add [--user] <what to remember>")
		}
		name := memory.Slug(text)
		if name == "" {
			return fail("couldn't make a name from that text; use some letters or digits")
		}
		// Don't replace a different note that happens to start the same.
		base := name
		for i := 2; ; i++ {
			if _, taken := store.Get(name, scope); !taken {
				break
			}
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := store.Save(memory.Note{Name: name, Description: text, Type: "project", Body: text, Scope: scope}); err != nil {
			return fail(err.Error())
		}
		// The index in the system prompt is rebuilt at the next fresh
		// context; tell the model now as well.
		m.agent.AddNote(fmt.Sprintf("The user saved a memory note %q (%s scope): %s", name, scope, text))
		return info(fmt.Sprintf("remembered as %s (%s scope)", name, scope))
	}
	return fail("usage: /memory [add [--user] <text> | show <name> | delete <name>]")
}

func (m *model) memoryList(store *memory.Store) string {
	project, user := store.Dirs()
	notes := store.List()
	var b strings.Builder
	for _, group := range []struct{ scope, title, dir string }{
		{memory.ScopeProject, "this project", project},
		{memory.ScopeUser, "every project", user},
	} {
		fmt.Fprintf(&b, "notes for %s  (%s)\n", group.title, m.shortPaths(group.dir))
		n := 0
		for _, note := range notes {
			if note.Scope != group.scope {
				continue
			}
			n++
			fmt.Fprintf(&b, "  %-28s %-9s %s\n", note.Name, note.Type, oneLine(note.Description, 90))
		}
		if n == 0 {
			b.WriteString("  (none)\n")
		}
	}
	b.WriteString("/memory show <name> · /memory add [--user] <text> · /memory delete <name>\nThe notes are Markdown files you can edit. The model sees the list at the start of each context (a new session or /clear).")
	return b.String()
}
