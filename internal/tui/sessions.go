package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/app"
	"larik/internal/session"
	"larik/internal/worktree"
)

// sessionCommand handles /sessions, /resume, /new, /fork and /rewind.
func (m *model) sessionCommand(name string, args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	if name == "/sessions" {
		return m.listSessions(info, fail)
	}
	if m.opts.App == nil || m.sess == nil {
		return fail(name + " is not available here")
	}
	// Carry the current effort and mode over; the model too, except when
	// resuming a session, which picks up its own.
	eff := string(m.agent.Effort())
	if eff == "" {
		eff = "default"
	}
	o := app.Options{Effort: eff, Mode: string(m.agent.Perms().Mode())}
	current := m.agent.ProviderName() + "/" + m.agent.Model()

	switch name {
	case "/resume":
		if len(args) == 0 {
			return m.listSessions(info, fail)
		}
		path, err := session.Find(m.opts.App.SessionDir, args[0])
		if err != nil {
			return fail(err.Error())
		}
		if path == m.sess.Path {
			return info("already in session " + m.sess.ID)
		}
		o.ResumeID = args[0]
		return m.open(o, "resumed", "")

	case "/new":
		o.Model = current
		return m.open(o, "new session", "")

	case "/fork":
		if st, err := session.Load(m.sess.Path); err != nil || len(st.All) == 0 {
			return info("nothing to branch yet")
		}
		o.Model, o.ResumeID, o.Fork = current, m.sess.ID, true
		return m.open(o, "branched from "+m.sess.ID, "")

	case "/rewind":
		st, err := session.Load(m.sess.Path)
		if err != nil {
			return fail(err.Error())
		}
		prompts := session.Prompts(st.All)
		if len(prompts) == 0 {
			return info("nothing to rewind yet")
		}
		if len(args) == 0 {
			var b strings.Builder
			first := max(0, len(prompts)-20)
			for i := first; i < len(prompts); i++ {
				fmt.Fprintf(&b, "%3d  %s\n", i+1, oneLine(st.All[prompts[i]].Text(), 90))
			}
			b.WriteString("/rewind <n> branches off just before prompt n and puts it back in the input to edit.\n" +
				"The current session stays as it is. Files are not changed; use /undo for that.")
			return info(b.String())
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > len(prompts) {
			return fail(fmt.Sprintf("pick a prompt number from 1 to %d (see /rewind)", len(prompts)))
		}
		keep := prompts[n-1]
		o.Model, o.ResumeID, o.Fork, o.ForkAt = current, m.sess.ID, true, &keep
		return m.open(o, fmt.Sprintf("rewound to before prompt %d of %s", n, m.sess.ID), st.All[keep].Text())
	}
	return nil
}

// open starts or loads a session and switches the TUI to it. The old
// session is closed only once the new one is ready.
func (m *model) open(o app.Options, label, prefill string) tea.Cmd {
	s, err := m.opts.App.Open(o)
	if err != nil {
		return m.println(m.st.err.Render(err.Error()))
	}
	var notes []string
	if n := m.agent.RunningBackground(); n > 0 {
		notes = append(notes, fmt.Sprintf("stopped %d background task(s) of the previous session", n))
	}
	close(m.bgStop)
	m.bgStop = make(chan struct{})
	m.sess.Close("other")

	m.sess, m.agent = s, s.Agent
	m.opts.Agent, m.opts.Hooks, m.opts.History = s.Agent, s.Hooks, s.History
	m.perm, m.permQueue, m.bgReplies, m.queue = nil, nil, nil, nil
	m.stats = s.Agent.Stats()
	if prefill != "" {
		m.input.SetValue(prefill)
		m.input.MoveToEnd()
	}

	head := fmt.Sprintf("session %s · %s/%s", s.ID, s.Agent.ProviderName(), s.Agent.Model())
	if s.ForkOf != "" {
		head += " · branch of " + s.ForkOf
	}
	cmds := []tea.Cmd{m.println("\n" + m.st.accent.Render("⇄ ") + m.st.dim.Render(head))}
	for _, n := range notes {
		cmds = append(cmds, m.println(m.st.warn.Render("! "+n)))
	}
	if len(s.History) > 0 {
		cmds = append(cmds, m.printHistory(label))
	} else {
		cmds = append(cmds, m.println(m.st.dim.Render("── "+label+" ──")))
	}
	return tea.Batch(m.waitBackground(), tea.Sequence(cmds...))
}

func (m *model) listSessions(info, fail func(string) tea.Cmd) tea.Cmd {
	dir := m.opts.SessionDir
	if m.opts.App != nil {
		dir = m.opts.App.SessionDir
	}
	infos, err := session.List(dir)
	if err != nil {
		return fail(err.Error())
	}
	if len(infos) == 0 {
		return info("no sessions yet")
	}
	var b strings.Builder
	for i, in := range infos {
		if i == 15 {
			fmt.Fprintf(&b, "… %d more\n", len(infos)-i)
			break
		}
		mark := " "
		if in.ID == m.agent.SessionID() {
			mark = "*"
		}
		title := in.Title
		if title == "" {
			title = "(empty)"
		}
		if in.ForkOf != "" {
			title = "⑂ " + title + "  (branch of " + in.ForkOf + ")"
		}
		fmt.Fprintf(&b, "%s %s  %s  %s\n", mark, in.ID, in.Modified.Format("01-02 15:04"), title)
	}
	if m.opts.App != nil {
		b.WriteString("switch with /resume <id> (a unique prefix is enough)")
	} else {
		b.WriteString("resume with: larik --resume <id>")
	}
	return info(b.String())
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		s = string([]rune(s)[:n-1]) + "…"
	}
	return s
}

// worktreesCommand lists or removes worktrees kept by isolated subagents.
func (m *model) worktreesCommand(args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	repo := agent.GitRoot(m.agent.Cwd())
	if repo == "" {
		return info("not in a git repository")
	}
	ctx := context.Background()
	if len(args) >= 2 && args[0] == "remove" {
		var targets []string
		if args[1] == "all" {
			list, err := worktree.List(ctx, repo)
			if err != nil {
				return fail(err.Error())
			}
			for _, in := range list {
				targets = append(targets, in.Branch)
			}
		} else {
			targets = args[1:]
		}
		var done []string
		for _, name := range targets {
			w, err := worktree.Open(ctx, repo, name)
			if err == nil {
				err = w.Remove(ctx)
			}
			if err != nil {
				return fail(name + ": " + err.Error())
			}
			done = append(done, w.Branch)
		}
		if len(done) == 0 {
			return info("no worktrees to remove")
		}
		return info("removed " + strings.Join(done, ", ") + " (worktree and branch)")
	}
	list, err := worktree.List(ctx, repo)
	if err != nil {
		return fail(err.Error())
	}
	if len(list) == 0 {
		return info("no worktrees from isolated subagents")
	}
	var b strings.Builder
	for _, in := range list {
		state := fmt.Sprintf("%d commit(s) not in HEAD", in.Commits)
		if in.Commits == 0 {
			state = "merged or empty"
		}
		if in.Dirty {
			state += ", uncommitted changes"
		}
		fmt.Fprintf(&b, "%s  %s\n    %s\n", in.Branch, m.st.dim.Render(state), in.Path)
	}
	b.WriteString("merge with: git merge <branch> · remove with: /worktrees remove <branch|all>")
	return info(b.String())
}
