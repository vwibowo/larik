package tui

import (
	tea "charm.land/bubbletea/v2"
	"larik/internal/desktop"
)

func (m *model) openDatabase() tea.Cmd {
	if m.opts.App == nil || !m.opts.App.DesktopTab {
		return m.println(m.st.err.Render("Database Manager requires Larik Desktop"))
	}
	dataDir, cwd := m.opts.App.Cfg.DataDir, m.opts.App.Cwd
	return func() tea.Msg {
		_, err := desktop.Call(dataDir, desktop.Request{Cmd: "open", Cwd: cwd, Database: true, Split: true})
		if err != nil {
			return outputMsg("database pane: " + err.Error())
		}
		return outputMsg("Database pane opened")
	}
}
