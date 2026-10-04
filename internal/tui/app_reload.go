package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"larik/internal/app"
)

// reloadApp rebuilds setup-time services and reconnects the current session.
// Do not carry the previous model context into a newly built prompt/tool list.
func (m *model) reloadApp() tea.Cmd {
	return m.reloadAppModel("")
}

func (m *model) reloadAppModel(chosen string) tea.Cmd {
	if m.opts.App == nil || m.sess == nil {
		return m.println(m.st.err.Render("app reload is unavailable for an externally managed session"))
	}
	if !m.idle() || m.agent.RunningBackground() > 0 || m.perm != nil {
		return m.println(m.st.warn.Render("wait for the turn and background tasks before reloading"))
	}
	old := m.opts.App
	next, err := app.Setup(old.Cwd, old.Version)
	if err != nil {
		return m.println(m.st.err.Render("reload: " + err.Error()))
	}
	next.Debug = old.Debug
	id := m.sess.ID
	eff := string(m.agent.Effort())
	mode := string(m.agent.Perms().Mode())
	previous := m.agent.ProviderName() + "/" + m.agent.Model()
	model := previous
	if chosen != "" {
		model = chosen
	}
	// Opening the same JSONL twice concurrently risks interleaved appends.
	m.sess.Close("other")
	s, err := next.Open(app.Options{ResumeID: id, Model: model, Effort: eff, Mode: mode})
	if err != nil {
		next.Close()
		recovered, recoverErr := old.Open(app.Options{ResumeID: id, Model: previous, Effort: eff, Mode: mode})
		if recoverErr != nil {
			return m.println(m.st.err.Render(fmt.Sprintf("reload failed: %v; reopen the session with larik --resume %s (%v)", err, id, recoverErr)))
		}
		close(m.bgStop)
		m.bgStop = make(chan struct{})
		m.sess, m.agent = recovered, recovered.Agent
		m.opts.Session, m.opts.Agent, m.opts.Hooks, m.opts.History = recovered, recovered.Agent, recovered.Hooks, recovered.History
		return tea.Batch(m.waitBackground(), m.println(m.st.err.Render("reload failed; original session restored: "+err.Error())))
	}
	// The saved transcript remains available for display/fork, but its cached
	// system prompt and tool schema must not be replayed into this new setup.
	s.Agent.Clear()
	close(m.bgStop)
	m.bgStop = make(chan struct{})
	// The replacement is fully open, so the previous app's MCP, LSP, browser,
	// and sandbox services can now stop. App.Close is idempotent because the
	// caller may still have a deferred close for the initial app.
	old.Close()
	m.ownedApps = []*app.App{next}
	m.opts.App, m.opts.Session = next, s
	m.opts.Config, m.opts.MCP, m.opts.Skills = next.Cfg, next.MCP, next.Skills
	m.opts.Memory, m.opts.Agents, m.opts.LSP = next.Memory, next.AgentDefs, next.LSP
	m.opts.Sandbox, m.opts.Audio = next.Sandbox, next.Audio
	m.opts.SandboxNote, m.opts.SearchNote, m.opts.BrowserNote = next.SandboxNote, next.SearchNote, next.BrowserNote
	m.sess, m.agent = s, s.Agent
	m.opts.Agent, m.opts.Hooks, m.opts.History = s.Agent, s.Hooks, s.History
	m.stats = s.Agent.Stats()
	m.todos = nil
	m.lastReply = lastReply(s.History)
	m.resetConversation()
	m.applyUIConfig()
	m.applyTheme(m.wantDark())
	return tea.Batch(m.waitBackground(), tea.Sequence(m.printHistory("app reloaded · fresh model context")))
}
