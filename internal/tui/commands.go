package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/mcp"
	"larik/internal/permission"
	"larik/internal/providers"
)

var modeCycle = []permission.Mode{permission.ModeDefault, permission.ModeAcceptEdits, permission.ModePlan}

func (m *model) cycleMode() tea.Cmd {
	perms := m.agent.Perms()
	cur := perms.Mode()
	next := modeCycle[0]
	for i, md := range modeCycle {
		if md == cur {
			next = modeCycle[(i+1)%len(modeCycle)]
		}
	}
	perms.SetMode(next)
	return nil
}

func (m *model) command(line string) tea.Cmd {
	fields := strings.Fields(line)
	name, args := fields[0], fields[1:]
	arg := strings.Join(args, " ")
	info := func(s string) tea.Cmd { return m.println(m.st.dim.Render(s)) }
	fail := func(s string) tea.Cmd { return m.println(m.st.err.Render(s)) }

	// Commands that would race with a running turn.
	if m.running {
		switch name {
		case "/model", "/connect", "/undo", "/compact", "/clear", "/resume", "/new", "/fork", "/rewind":
			return fail(name + " is unavailable while a turn is running (esc to interrupt)")
		}
	}

	switch name {
	case "/help", "/?":
		return info(helpText)

	case "/quit", "/exit", "/q":
		return tea.Quit

	case "/model":
		if arg == "" {
			return m.openModelPicker()
		}
		return m.switchModel(arg, m.agent.Effort())

	case "/connect":
		if arg != "" {
			if _, ok := providers.ChoiceFor(arg); !ok {
				return fail("unknown provider " + arg + " (known: " + strings.Join(providers.Names(), ", ") + ")")
			}
		}
		return m.openWizard(arg)

	case "/effort":
		if arg == "" {
			e := string(m.agent.Effort())
			if e == "" {
				e = "default"
			}
			return info("effort: " + e)
		}
		switch arg {
		case "default":
			m.agent.SetEffort(llm.EffortDefault)
		case "low", "medium", "high", "xhigh", "max":
			m.agent.SetEffort(llm.Effort(arg))
		default:
			return fail("unknown effort " + arg)
		}
		return info("effort set to " + arg)

	case "/mode":
		if arg == "" {
			return m.openModePicker()
		}
		md, err := permission.ParseMode(arg)
		if err != nil {
			return fail(err.Error())
		}
		m.agent.Perms().SetMode(md)
		return info("mode set to " + string(md))

	case "/undo":
		paths, err := m.agent.Undo()
		if err != nil {
			return fail(err.Error())
		}
		return info("↶ restored " + strings.Join(paths, ", "))

	case "/compact":
		m.busyLabel = "compacting conversation…"
		a := m.agent
		return tea.Batch(m.spin.Tick, func() tea.Msg {
			summary, err := a.Compact(context.Background())
			return compactedMsg{summary: summary, err: err}
		})

	case "/clear":
		m.agent.Clear()
		m.stats = m.agent.Stats()
		return info("context cleared")

	case "/cost":
		s := m.agent.Stats()
		cost := "unknown (model not in price catalog)"
		if s.CostUSD > 0 {
			cost = fmt.Sprintf("$%.4f", s.CostUSD)
		}
		return info(fmt.Sprintf("input %d · output %d · cache read %d · cache write %d · cost %s",
			s.Total.Input, s.Total.Output, s.Total.CacheRead, s.Total.CacheWrite, cost))

	case "/mcp":
		return m.mcpCommand(args, info, fail)

	case "/hooks":
		return m.hooksCommand(args, info, fail)

	case "/skills":
		return m.skillsCommand(info)

	case "/agents":
		return m.agentsCommand(info)

	case "/lsp":
		return m.lspCommand(info)

	case "/sandbox":
		if m.opts.Sandbox == nil {
			note := m.opts.SandboxNote
			if note == "" {
				note = "disabled in settings (\"sandbox\": {\"enabled\": false})"
			}
			return info("no sandbox: " + note)
		}
		return info(m.opts.Sandbox.Summary() + "\nwritable: " + strings.Join(m.opts.Sandbox.Writable(), ", ") +
			"\nsandboxed commands run without asking; commands the model runs with sandbox:false ask first")

	case "/tasks":
		return m.tasksCommand(args, info, fail)

	case "/worktrees":
		return m.worktreesCommand(args, info, fail)

	case "/sessions", "/resume", "/new", "/fork", "/rewind":
		return m.sessionCommand(name, args, info, fail)
	}
	if sk, ok := m.opts.Skills.Get(strings.TrimPrefix(name, "/")); ok && sk.UserInvocable {
		if m.running {
			m.queue = append(m.queue, line)
			return nil
		}
		return m.submit(line) // the agent expands the skill
	}
	return fail("unknown command " + name + " (try /help or /skills)")
}

func (m *model) mcpCommand(args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	mgr := m.opts.MCP
	if mgr == nil {
		return fail("MCP is not available")
	}
	if len(args) >= 2 && args[0] == "approve" {
		name := args[1]
		cfg, ok := mgr.Config(name)
		if !ok {
			return fail("no MCP server named " + name)
		}
		if err := mgr.Approve(name); err != nil {
			return fail(err.Error())
		}
		what := cfg.URL
		if cfg.Transport() == "stdio" {
			what = strings.TrimSpace(cfg.Command + " " + strings.Join(cfg.Args, " "))
		}
		return info(fmt.Sprintf("approved %s (%s); starting it now. Its tools join the next fresh context (/clear or a new session).", name, what))
	}
	statuses := mgr.Statuses()
	if len(statuses) == 0 {
		return info("no MCP servers configured (add mcp_servers to .larik/settings.json or ~/.config/larik/config.json, or a .mcp.json)")
	}
	var b strings.Builder
	for _, st := range statuses {
		mark := map[mcp.State]string{mcp.StateConnected: "●", mcp.StateFailed: "✗", mcp.StateConnecting: "…", mcp.StateNeedsApproval: "?", mcp.StateDisabled: "○"}[st.State]
		fmt.Fprintf(&b, "%s %s  [%s] %s", mark, st.Name, st.Transport, st.State)
		if st.State == mcp.StateConnected {
			fmt.Fprintf(&b, " · %d tools", len(st.Tools))
		}
		b.WriteString("\n")
		if st.Err != "" {
			fmt.Fprintf(&b, "    %s\n", st.Err)
		}
		if st.State == mcp.StateNeedsApproval {
			fmt.Fprintf(&b, "    defined in %s · /mcp approve %s\n", st.Source, st.Name)
		}
		for _, t := range st.Tools {
			fmt.Fprintf(&b, "    %s\n", t)
		}
	}
	return info(strings.TrimRight(b.String(), "\n"))
}

func (m *model) hooksCommand(args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	cfg := m.opts.Config
	if len(args) >= 1 && args[0] == "approve" {
		if cfg.ProjectHooks.Empty() {
			return info("no project hooks to approve")
		}
		if err := cfg.ApproveProjectHooks(); err != nil {
			return fail(err.Error())
		}
		m.opts.Hooks.SetConfig(cfg.ActiveHooks())
		return info("project hooks approved (pinned to their current content) and active now")
	}
	var b strings.Builder
	list := func(title string, c hooks.Config) {
		if c.Empty() {
			return
		}
		b.WriteString(title + "\n")
		for _, ev := range hooks.Events {
			for _, mt := range c[ev] {
				matcher := mt.Matcher
				if matcher == "" {
					matcher = "*"
				}
				for _, h := range mt.Hooks {
					fmt.Fprintf(&b, "  %-16s %-12s %s\n", ev, matcher, h.Command)
				}
			}
		}
	}
	list("personal (~/.config/larik/config.json, .larik/settings.local.json):", cfg.TrustedHooks)
	if !cfg.ProjectHooks.Empty() {
		state := "approved"
		if !cfg.ProjectHooksApproved() {
			state = "NOT approved; they will not run until you run /hooks approve"
		}
		list("project (.larik/settings.json), "+state+":", cfg.ProjectHooks)
	}
	if b.Len() == 0 {
		return info("no hooks configured (add \"hooks\" to ~/.config/larik/config.json or .larik/settings.json)")
	}
	return info(strings.TrimRight(b.String(), "\n"))
}

func (m *model) skillsCommand(info func(string) tea.Cmd) tea.Cmd {
	set := m.opts.Skills
	if set == nil || len(set.List()) == 0 {
		return info("no skills found (add <name>/SKILL.md under .larik/skills, ~/.config/larik/skills, or .claude/skills)")
	}
	var b strings.Builder
	for _, sk := range set.List() {
		flags := []string{sk.Scope}
		if !sk.ModelInvocable {
			flags = append(flags, "manual only")
		}
		if !sk.UserInvocable {
			flags = append(flags, "model only")
		}
		desc := sk.Description
		if len(desc) > 90 {
			desc = desc[:87] + "..."
		}
		fmt.Fprintf(&b, "/%-24s %s  [%s]\n", sk.Name, desc, strings.Join(flags, ", "))
	}
	for _, sk := range set.Shadowed {
		fmt.Fprintf(&b, "  shadowed: %s (%s)\n", sk.Name, sk.Path)
	}
	for _, w := range set.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", w)
	}
	return info(strings.TrimRight(b.String(), "\n"))
}

func (m *model) agentsCommand(info func(string) tea.Cmd) tea.Cmd {
	set := m.opts.Agents
	if set == nil {
		return info("subagents are disabled")
	}
	var b strings.Builder
	for _, d := range set.List() {
		tools, model := "all tools", "inherits model"
		if d.Tools != nil {
			tools = strings.Join(d.Tools, ", ")
		}
		if d.Model != "" && d.Model != "inherit" {
			model = d.Model
		}
		desc := d.Description
		if len(desc) > 90 {
			desc = desc[:87] + "..."
		}
		fmt.Fprintf(&b, "%-18s %s\n  %s · %s · %s\n", d.Name, desc, tools, model, d.Source)
	}
	for _, w := range set.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", w)
	}
	b.WriteString("define more in .larik/agents/<name>.md or .claude/agents/<name>.md")
	return info(b.String())
}

func (m *model) lspCommand(info func(string) tea.Cmd) tea.Cmd {
	statuses := m.opts.LSP.Statuses()
	if len(statuses) == 0 {
		return info("no language servers found on PATH (gopls, typescript-language-server, pyright-langserver, rust-analyzer, clangd), and none configured under \"lsp\"")
	}
	var b strings.Builder
	for _, st := range statuses {
		state := "idle (starts on first matching file)"
		if len(st.Running) > 0 {
			state = "running in " + strings.Join(st.Running, ", ")
		}
		if len(st.Failed) > 0 {
			state = "failed: " + strings.Join(st.Failed, "; ")
		}
		fmt.Fprintf(&b, "%-11s %s\n  %s · %s\n", st.Name, st.Command, st.Languages, state)
	}
	b.WriteString("server logs: ~/.local/share/larik/logs/lsp-<name>.log")
	return info(b.String())
}

func (m *model) tasksCommand(args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	if len(args) >= 2 && args[0] == "stop" {
		if err := m.agent.StopBackground(args[1]); err != nil {
			return fail(err.Error())
		}
		return info("stopped " + args[1])
	}
	tasks := m.agent.BackgroundTasks()
	if len(tasks) == 0 {
		return info("no background tasks (the model starts them with task(run_in_background: true))")
	}
	var b strings.Builder
	for _, t := range tasks {
		elapsed := time.Since(t.Started)
		if !t.Finished.IsZero() {
			elapsed = t.Finished.Sub(t.Started)
		}
		fmt.Fprintf(&b, "%-6s %-10s %6s  %s\n", t.ID, t.Status, elapsed.Round(time.Second), t.Label)
	}
	return info(strings.TrimRight(b.String(), "\n"))
}
