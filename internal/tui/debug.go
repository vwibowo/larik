package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/session"
	"larik/internal/termbrowser"
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

// traceOwner names the browser tab /trace uses, apart from the agents' tabs.
const traceOwner = "trace"

// Trace navigation completes back on the UI thread, where stale results can
// be ignored and failed panels cleaned up before considering a fallback.
type traceOpenedMsg struct {
	id        uint64
	browser   *termbrowser.Session
	panel     *termbrowser.Panel
	created   bool
	url, mode string
	err       error
}

// tracePlacement decides where /trace opens: usePanel for Larik's browser
// panel, else the system browser. A forced "browser" that can't be honored
// returns why in problem and opens nothing.
func tracePlacement(mode string, panelOK bool) (usePanel bool, problem string) {
	switch mode {
	case "external":
		return false, ""
	case "browser":
		if !panelOK {
			return false, "the browser panel can't be shown here: browser tools must be on (/browser on) and the window wide enough"
		}
		return true, ""
	}
	return panelOK, ""
}

// openTrace is /trace [browser|external]: serve this session's trace and
// open it in Larik's browser panel, or the system browser.
func (m *model) openTrace(arg string) tea.Cmd {
	mode := "auto"
	if m.opts.Config != nil {
		mode = m.opts.Config.TraceOpenIn()
	}
	switch arg {
	case "":
	case "browser", "external":
		mode = arg
	default:
		return m.println(m.st.err.Render("usage: /trace [browser|external]"))
	}
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
	m.traceOpenID++
	id := m.traceOpenID
	url := m.traceView.URL()
	line := m.println(m.st.dim.Render("trace viewer: ") + url)
	panelOK := m.opts.App != nil && m.opts.App.Browser != nil && m.panelAllowed()
	usePanel, problem := tracePlacement(mode, panelOK)
	if problem != "" {
		return tea.Batch(line, m.println(m.st.err.Render("trace: "+problem)))
	}
	if !usePanel {
		openBrowser(url)
		return line
	}
	b := m.opts.App.Browser
	created := m.browser == nil
	init, err := m.prepareBrowserPanel()
	if err != nil {
		return tea.Batch(line, m.traceOpenFailed(mode, url, err))
	}
	panel := m.browser
	navigate := m.traceNavigate
	if navigate == nil {
		navigate = b.OpenURL
	}
	nav := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return traceOpenedMsg{id: id, browser: b, panel: panel, created: created, url: url, mode: mode, err: navigate(ctx, traceOwner, url)}
	}
	// OpenURL starts Chrome in the trace owner's tab. Do not concurrently
	// run the main-tab startup path from openBrowser.
	return tea.Batch(line, init, nav)
}

func (m *model) traceOpenFailed(mode, url string, err error) tea.Cmd {
	if mode == "browser" {
		return m.println(m.st.err.Render("trace: couldn't show the browser panel: " + err.Error() + "; use /trace external to open the system browser"))
	}
	openBrowser(url)
	return m.println(m.st.warn.Render("trace: couldn't show the browser panel (" + err.Error() + "); opened the system browser"))
}

func (m *model) traceOpened(msg traceOpenedMsg) tea.Cmd {
	if msg.id != m.traceOpenID || m.opts.App == nil || m.opts.App.Browser != msg.browser || m.traceView == nil || m.traceView.URL() != msg.url {
		return nil // superseded invocation or session/app we switched away from
	}
	if msg.err == nil {
		return nil
	}
	var clear tea.Cmd
	if msg.created && m.browser == msg.panel {
		clear = m.closeBrowser()
	}
	return tea.Batch(clear, m.traceOpenFailed(msg.mode, msg.url, msg.err))
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
