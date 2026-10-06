package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/app"
	"larik/internal/session"
	"larik/internal/tools"
	"larik/internal/worktree"
)

// sessionCommand handles /sessions, /resume, /new, /fork and /rewind.
func (m *model) sessionCommand(name string, args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	if name == "/sessions" || (name == "/resume" && len(args) == 0) {
		return m.listSessions(info, fail)
	}
	if m.opts.App == nil || m.sess == nil {
		return fail(name + " is not available here")
	}
	o, current := m.sessionOptions()

	switch name {
	case "/resume":
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
				fmt.Fprintf(&b, "%3d  %s\n", i+1, oneLine(session.PromptText(st.All[prompts[i]]), 90))
			}
			b.WriteString("/rewind <n> branches off just before prompt n and puts it back in the input to edit.\n" +
				"The current session stays as it is. If files changed since that prompt you are asked whether to restore them too;\n" +
				"/rewind <n> files restores without asking, /rewind <n> keep leaves the files alone.")
			return info(b.String())
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > len(prompts) {
			return fail(fmt.Sprintf("pick a prompt number from 1 to %d (see /rewind)", len(prompts)))
		}
		files := ""
		if len(args) > 1 {
			if files = args[1]; files != "files" && files != "keep" {
				return fail("usage: /rewind <n> [files | keep]")
			}
		}
		return m.rewindTo(st, prompts, n, files, info, fail)
	}
	return nil
}

// sessionOptions carries the current effort and mode over to a session
// being opened; current is the model, which a resumed session replaces
// with its own.
func (m *model) sessionOptions() (o app.Options, current string) {
	eff := string(m.agent.Effort())
	if eff == "" {
		eff = "default"
	}
	return app.Options{Effort: eff, Mode: string(m.agent.Perms().Mode())}, m.agent.ProviderName() + "/" + m.agent.Model()
}

// rewindTo branches off just before prompt n (1-based, of prompts in st)
// and puts it back in the input. files is "files" to restore changed files
// without asking, "keep" to leave them, or "" to ask when any changed.
func (m *model) rewindTo(st *session.State, prompts []int, n int, files string, info, fail func(string) tea.Cmd) tea.Cmd {
	o, current := m.sessionOptions()
	at := prompts[n-1]
	o.Model, o.ResumeID, o.Fork, o.ForkAt = current, m.sess.ID, true, &at
	label := fmt.Sprintf("rewound to before prompt %d of %s", n, m.sess.ID)
	prefill := session.PromptText(st.All[at])

	var changed []string
	since := time.Time{}
	if at < len(st.AllTimes) {
		since = st.AllTimes[at]
		changed = m.agent.FilesSince(since)
	}
	branch := func(restore bool) tea.Cmd {
		if !restore {
			return m.open(o, label, prefill)
		}
		// Files first: a branch whose files still hold later changes
		// would disagree with the conversation it continues.
		paths, err := m.agent.UndoSince(since)
		if err != nil {
			msg := "restoring files: " + err.Error()
			if len(paths) > 0 {
				msg += " (restored " + m.shortPaths(strings.Join(paths, ", ")) + " before stopping)"
			}
			return fail(msg + "; nothing was branched")
		}
		return tea.Sequence(m.open(o, label+" · files restored", prefill), info("↶ restored "+m.shortPaths(strings.Join(paths, ", "))))
	}
	switch {
	case len(changed) == 0 || files == "keep":
		return branch(false)
	case files == "files":
		return branch(true)
	}
	m.rewindOffer = &rewindOffer{files: changed, branch: branch, picker: rewindChoices()}
	return nil
}

// rewindOffer is the question /rewind asks when files changed after the
// prompt it is rewinding to.
type rewindOffer struct {
	files  []string
	branch func(restore bool) tea.Cmd
	picker *picker
}

func rewindChoices() *picker {
	p := &picker{items: []pickItem{
		{label: "1. Branch and restore files", detail: "put the files back as they were before this prompt", value: true},
		{label: "2. Branch only", detail: "leave the files as they are now", value: false},
	}}
	p.home()
	return p
}

func (m *model) handleRewindKey(msg tea.KeyPressMsg) tea.Cmd {
	o := m.rewindOffer
	k := msg.String()
	switch {
	case k == "esc" || k == "ctrl+c":
		m.rewindOffer = nil
		return m.println(m.st.dim.Render("rewind cancelled"))
	case k == "1" || k == "2":
		m.rewindOffer = nil
		return o.branch(k == "1")
	}
	if o.picker.handleKey(msg) {
		it, _ := o.picker.selected()
		m.rewindOffer = nil
		return o.branch(it.value.(bool))
	}
	return nil
}

func (m *model) rewindView() string {
	o := m.rewindOffer
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render("Restore files too?"), m.st.dim.Render(fmt.Sprintf("%d changed since that prompt", len(o.files))), w)
	list := o.files
	if len(list) > 8 {
		list = append(list[:8:8], fmt.Sprintf("… and %d more", len(o.files)-8))
	}
	files := m.st.dim.Render(m.shortPaths(strings.Join(list, "\n")))
	o.picker.height = max(m.availablePanelRows()-len(list)-5, 1)
	hint := "1–2 or ↑/↓ + enter choose · esc cancel · changes made outside Larik to these files are overwritten"
	return m.st.modal.Width(max(m.width-2, 10)).Render(head + "\n" + files + "\n" + o.picker.view(m.st, w) + "\n" + m.st.dim.Render(hint))
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
	m.promptMenu, m.promptCount = nil, 0
	m.resetConversation()
	m.opts.Agent, m.opts.Hooks, m.opts.History = s.Agent, s.Hooks, s.History
	m.perm, m.permQueue, m.bgReplies, m.queue = nil, nil, nil, nil
	m.taskCalls = nil
	m.todos, _ = tools.LatestTodos(s.History)
	m.lastReply = lastReply(s.History)
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
	p := &picker{filterable: true, matchDetail: true}
	for _, in := range infos {
		title := in.Title
		if title == "" {
			title = "(empty)"
		}
		if in.ForkOf != "" {
			title = "⑂ " + title + " (branch of " + in.ForkOf + ")"
		}
		item := pickItem{label: in.ID, detail: in.Modified.Format("01-02 15:04") + "  " + oneLine(title, 90), value: in.ID}
		if in.ID == m.agent.SessionID() {
			item.note, item.noteOK = "✓ current", true
		}
		p.items = append(p.items, item)
	}
	p.home()
	m.sessionPick = p
	return nil
}

func (m *model) handleSessionPickerKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" || msg.String() == "ctrl+c" {
		m.sessionPick = nil
		return nil
	}
	p := m.sessionPick
	if !p.handleKey(msg) {
		return nil
	}
	item, ok := p.selected()
	if !ok {
		return nil
	}
	m.sessionPick = nil
	id := item.value.(string)
	if m.opts.App == nil { // the caller owns the session; switch on restart
		return m.println(m.st.dim.Render("resume with: larik --resume " + id))
	}
	return m.command("/resume " + id)
}

func (m *model) sessionPickerView() string {
	p := m.sessionPick
	w := max(m.width-6, 20)
	p.height = max(min(14, m.availablePanelRows()-5), 1)
	header := spread(m.st.accent.Render("Sessions"), m.st.dim.Render("newest first"), w)
	hint := m.st.dim.Render("↑/↓ move · type to filter · enter resume · esc close")
	return m.st.modal.Width(max(m.width-2, 10)).Render(header + "\n" + p.filterLine(m.st, "filter by ID or title…") + "\n" + p.view(m.st, w) + "\n" + hint)
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

// exportSession writes the session's full history as Markdown to path,
// or to larik-<id>.md in the working directory. It won't overwrite a file.
func (m *model) exportSession(path string, info, fail func(string) tea.Cmd) tea.Cmd {
	src := m.agent.SessionPath()
	if src == "" {
		return fail("this session isn't saved, so there is nothing to export")
	}
	st, err := session.Load(src)
	if err != nil {
		return fail(err.Error())
	}
	if len(st.All) == 0 {
		return info("nothing to export yet")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "larik-" + m.agent.SessionID() + ".md"
	}
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(homeDir(), path[2:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.agent.Cwd(), path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return fail(tildePath(path) + " exists; give another file name (/export <file>)")
	}
	if err != nil {
		return fail(err.Error())
	}
	_, err = f.WriteString(session.Markdown(st, m.agent.SessionID()))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return fail(err.Error())
	}
	return info("✓ exported " + plural(len(st.All), "message") + " to " + m.shortPaths(path))
}
