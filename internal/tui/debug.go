package tui

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"larik/internal/session"
	"larik/internal/trace"
	"larik/internal/trace/viewer"
)

// debugCommand is /debug [on|off]: debug-mode recording for this session
// and those opened after it in this window.
func (m *model) debugCommand(arg string) tea.Cmd {
	if m.sess == nil {
		return m.println(m.st.err.Render("/debug needs a session Larik manages; start larik with --debug instead"))
	}
	switch arg {
	case "":
		if rec := m.sess.Trace(); rec != nil {
			return m.println(m.st.dim.Render(fmt.Sprintf("recording to %s (%s) · /trace to review · /debug off to stop", shortPath(rec.Dir()), sizeOf(rec.Size()))))
		}
		return m.println(m.st.dim.Render("not recording · /debug on to start"))
	case "on":
		rec, err := m.setRecording(true)
		if err != nil {
			return m.println(m.st.err.Render("debug: " + err.Error()))
		}
		return m.println(m.st.dim.Render("recording requests, responses and tool calls to " + shortPath(rec.Dir()) + " · /trace to review"))
	case "off":
		_, _ = m.setRecording(false)
		return m.println(m.st.dim.Render("stopped recording; the trace is kept · /trace to review"))
	}
	return m.println(m.st.err.Render("usage: /debug [on|off]"))
}

// setRecording starts or stops tracing this session, and the sessions opened
// after it in this window. A session Larik does not manage cannot be traced,
// which is not an error: the setting still saves, for the next start.
func (m *model) setRecording(on bool) (*trace.Recorder, error) {
	if m.opts.App != nil {
		m.opts.App.Debug = on
	}
	if m.sess == nil {
		return nil, nil
	}
	if !on {
		m.sess.StopTrace()
		return nil, nil
	}
	return m.sess.StartTrace()
}

// openTrace is /trace: serve this session's trace and open it in the
// browser.
func (m *model) openTrace() tea.Cmd {
	path := m.agent.SessionPath()
	if path == "" {
		return m.println(m.st.err.Render("this session has no transcript, so no trace"))
	}
	dir := session.TraceDir(path)
	if _, err := os.Stat(filepath.Join(dir, trace.EventsFile)); err != nil {
		return m.println(m.st.err.Render("nothing recorded for this session yet · /debug on, then run a prompt"))
	}
	if m.traceView == nil || m.traceDir != dir {
		if m.traceView != nil {
			m.traceView.Close()
		}
		srv, err := viewer.Start(viewer.Options{Dir: dir, Title: m.agent.SessionID()}, 0)
		if err != nil {
			return m.println(m.st.err.Render("trace viewer: " + err.Error()))
		}
		m.traceView, m.traceDir = srv, dir
	}
	openBrowser(m.traceView.URL())
	return m.println(m.st.dim.Render("trace viewer: ") + m.traceView.URL())
}

func sizeOf(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
