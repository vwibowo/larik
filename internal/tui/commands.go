package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"larik/internal/llm"
	"larik/internal/mcp"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/session"
)

const helpText = `Commands
  /model [provider/model]   show or switch model (e.g. /model openai/gpt-5.5)
  /effort [level]           show or set reasoning effort: low medium high xhigh max default
  /mode [mode]              show or set permission mode: default accept-edits plan yolo
  /undo                     revert file changes from the last turn that made any
  /compact                  summarize the conversation to free context
  /clear                    start a fresh context (history stays in the session file)
  /cost                     token usage and cost for this session
  /sessions                 list sessions for this directory
  /mcp                      MCP servers, status and tools
  /mcp approve <name>       allow a project-defined MCP server to start
  /quit                     exit

Keys
  enter send · shift+enter / alt+enter / ctrl+j newline · esc interrupt
  shift+tab cycle permission mode · ctrl+c clear input / interrupt / quit`

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
		case "/model", "/undo", "/compact", "/clear":
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
			return info("model: " + m.agent.ProviderName() + "/" + m.agent.Model() + "\nproviders: " + strings.Join(providers.Names(), ", "))
		}
		r, err := providers.Resolve(m.opts.Config, arg)
		if err != nil {
			return fail(err.Error())
		}
		m.agent.SetModel(r.Provider, r.Model)
		m.stats = m.agent.Stats()
		return info("switched to " + r.String())

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
			return info("mode: " + string(m.agent.Perms().Mode()))
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

	case "/sessions":
		infos, err := session.List(m.opts.SessionDir)
		if err != nil {
			return fail(err.Error())
		}
		if len(infos) == 0 {
			return info("no sessions yet")
		}
		var b strings.Builder
		for i, in := range infos {
			if i == 15 {
				break
			}
			fmt.Fprintf(&b, "%s  %s  %s\n", in.ID, in.Modified.Format("01-02 15:04"), in.Title)
		}
		b.WriteString("resume with: larik --resume <id>")
		return info(b.String())
	}
	return fail("unknown command " + name + " (try /help)")
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
