package tui

import (
	"os"

	"larik/internal/osc"
	"larik/internal/status"

	tea "charm.land/bubbletea/v2"
)

// programStatus is what the session is doing, in OSC 7501's terms.
func (m *model) programStatus() status.Report {
	if m.perm != nil {
		kind, msg := status.Waiting(m.perm.ToolName)
		return status.Report{State: status.Blocked, Kind: kind, Msg: msg}
	}
	if !m.idle() || m.agent.RunningBackground() > 0 {
		return status.Report{State: status.Working}
	}
	switch {
	case !m.turnEnded || m.lastStop == "interrupted":
		return status.Report{State: status.Idle}
	case m.turnFailed:
		return status.Report{State: status.Error}
	}
	return status.Report{State: status.Done}
}

// reportStatus keeps the session's status file current and tells the
// terminal when the program status changes, for terminals that show it in
// tabs and session pickers.
func (m *model) reportStatus() tea.Cmd {
	r := m.programStatus()
	m.writeStatusFile(r)
	if c := m.opts.Config; c == nil || !c.ProgramStatusOn() {
		return nil
	}
	if r == m.reported || (m.reported.State == "" && r.State == status.Idle) {
		return nil // a terminal shows nothing for a program that hasn't reported
	}
	m.reported = r
	return tea.Raw(osc.Status(r))
}

// writeStatusFile records r for other processes, with what the session
// runs on and has used, following the TUI to whichever session it has
// open.
func (m *model) writeStatusFile(r status.Report) {
	if path := m.agent.SessionPath(); path != m.statusPath {
		m.statusFile.Release()
		m.statusFile, m.statusPath = nil, path
		if path != "" {
			m.statusFile = status.NewWriter(path, "tui")
		}
	}
	m.statusFile.Set(r)
	m.statusFile.SetUsage(m.programUsage())
}

// programUsage is what the session runs on and has used, as the footer
// and /cost show it.
func (m *model) programUsage() status.Usage {
	return status.Usage{
		Model:    m.agent.Model(),
		Provider: m.agent.ProviderName(),
		Mode:     string(m.agent.Perms().Mode()),
		CostUSD:  m.stats.CostUSD,
		Tokens:   m.stats.ContextTokens,
		Window:   m.stats.ContextWindow,
	}
}

// clearStatus removes Larik's record from the terminal on the way out, so
// a tab doesn't go on showing a session that has closed, and lets go of
// the session's status file.
func clearStatus(m *model) {
	m.statusFile.Release()
	if m.reported.State != "" {
		os.Stdout.WriteString(osc.ClearStatus())
	}
}
