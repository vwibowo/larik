package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/app"
	"larik/internal/session"
	"larik/internal/termbrowser"
	"larik/internal/trace"
)

func traceCommandModel(t *testing.T, browser bool, closed bool) (*model, *[]string) {
	t.Helper()
	t.Setenv("LARIK_FORCE_KITTY", "1")
	m := testModel(t)
	m.width, m.height = 120, 40
	m.send = func(tea.Msg) {} // no live terminal or renderer
	dir := session.TraceDir(m.agent.SessionPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, trace.EventsFile), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if browser {
		sock, err := os.MkdirTemp("", "lk")
		if err != nil {
			t.Fatal(err)
		}
		temp := sock
		t.Cleanup(func() { os.RemoveAll(temp) })
		b := termbrowser.New(termbrowser.Options{SocketDir: sock})
		if closed {
			b.Close() // ServeDefault fails deterministically, regardless of socket path length.
		}
		t.Cleanup(b.Close)
		m.opts.App = &app.App{Browser: b}
		m.opts.Config.Browser.Enabled = true
	}
	var external []string
	old := openBrowser
	openBrowser = func(url string) { external = append(external, url) }
	t.Cleanup(func() {
		openBrowser = old
		if m.browser != nil {
			m.closeBrowser()
		}
		if m.traceView != nil {
			m.traceView.Close()
		}
	})
	return m, &external
}

// Non-panel command paths contain only printable output commands. Never run a
// panel's Init command here: it is a graphics viewer, not a fake terminal.
func traceOutput(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		return ""
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out string
		for _, child := range msg {
			out += traceOutput(t, child)
		}
		return out
	case outputMsg:
		return string(msg) + "\n"
	case tea.RawMsg:
		return "" // discard graphics cleanup; never write to a real terminal
	case nil:
		return ""
	default:
		t.Fatalf("unexpected trace command message %T", msg)
		return ""
	}
}

func TestTraceCommandPlacementAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, arg, setting string
		browser            bool
		width              int
		paneSize           int
		pane               bool
		unsupported        bool
		wantExternal       bool
		wantProblem        bool
	}{
		{name: "browser tools off", wantExternal: true},
		{name: "narrow", browser: true, width: 60, wantExternal: true},
		{name: "pane preference", browser: true, pane: true, wantExternal: true},
		{name: "percentage leaves no conversation", browser: true, width: 100, paneSize: 80, wantExternal: true},
		{name: "unsupported terminal", browser: true, unsupported: true, wantExternal: true},
		{name: "forced external", arg: "external", browser: true, wantExternal: true},
		{name: "configured external", setting: "external", browser: true, wantExternal: true},
		{name: "forced unavailable", arg: "browser", wantProblem: true},
		{name: "configured unavailable", setting: "browser", wantProblem: true},
		{name: "argument overrides setting", arg: "external", setting: "browser", wantExternal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, external := traceCommandModel(t, tc.browser, false)
			m.opts.Config.Trace.OpenIn = tc.setting
			if tc.width > 0 {
				m.width = tc.width
			}
			m.opts.Config.Browser.PaneSize = tc.paneSize
			if tc.pane {
				m.opts.Config.Browser.Show = "pane"
			}
			if tc.unsupported {
				for _, name := range []string{"LARIK_FORCE_KITTY", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "WEZTERM_PANE", "CMUX_SOCKET_PATH", "KONSOLE_VERSION", "TERM_PROGRAM"} {
					t.Setenv(name, "")
				}
				t.Setenv("TERM", "dumb")
			}
			m.traceNavigate = func(context.Context, string, string) error { t.Fatal("unexpected browser navigation"); return nil }
			out := traceOutput(t, m.command(strings.TrimSpace("/trace "+tc.arg)))
			if m.traceView == nil || !strings.Contains(out, m.traceView.URL()) {
				t.Fatalf("trace URL not printed: %s", out)
			}
			if (len(*external) > 0) != tc.wantExternal {
				t.Fatalf("external opens=%v", *external)
			}
			if tc.wantProblem && !strings.Contains(out, "can't be shown") {
				t.Fatalf("missing failure reason: %s", out)
			}
			if m.browser != nil {
				t.Fatal("unusable panel was opened")
			}
		})
	}
}

func TestTraceCommandPanelSetupFailure(t *testing.T) {
	for _, mode := range []string{"auto", "browser"} {
		t.Run(mode, func(t *testing.T) {
			m, external := traceCommandModel(t, true, true)
			m.opts.Config.Trace.OpenIn = mode
			m.traceNavigate = func(context.Context, string, string) error {
				t.Fatal("navigated after panel setup failure")
				return nil
			}
			out := traceOutput(t, m.command("/trace"))
			if !strings.Contains(out, m.traceView.URL()) || !strings.Contains(out, "couldn't show") {
				t.Fatalf("missing output: %s", out)
			}
			if (len(*external) > 0) != (mode == "auto") {
				t.Fatalf("external opens=%v mode=%s", *external, mode)
			}
			if m.browser != nil {
				t.Fatal("failed panel retained")
			}
		})
	}
}

func TestTraceCommandNavigationCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, mode  string
		fail, stale bool
	}{
		{name: "success", mode: "auto"},
		{name: "startup fails with auto fallback", mode: "auto", fail: true},
		{name: "forced browser reports failure", mode: "browser", fail: true},
		{name: "old session failure ignored", mode: "auto", fail: true, stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, external := traceCommandModel(t, true, false)
			m.opts.Config.Trace.OpenIn = tc.mode
			calls := 0
			m.traceNavigate = func(ctx context.Context, owner, url string) error {
				calls++
				if owner != traceOwner || url != m.traceView.URL() {
					t.Errorf("wrong trace destination: %q %q", owner, url)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("trace navigation has no deadline")
				}
				if tc.fail {
					return errors.New("fake Chrome startup failure")
				}
				return nil
			}
			cmd := m.command("/trace")
			batch, ok := cmd().(tea.BatchMsg)
			if !ok || len(batch) < 2 || m.browser == nil {
				t.Fatalf("trace did not prepare panel: %T; %s", batch, traceOutput(t, cmd))
			}
			out := traceOutput(t, batch[0])
			if !strings.Contains(out, m.traceView.URL()) {
				t.Fatal("trace URL missing")
			}
			// The final command is navigation; deliberately don't run the
			// viewer initialization commands against the developer's terminal.
			msg, ok := batch[len(batch)-1]().(traceOpenedMsg)
			if !ok || calls != 1 {
				t.Fatal("trace navigation did not run exactly once")
			}
			if tc.stale {
				m.opts.App = &app.App{}
			}
			_, next := m.Update(msg)
			if tc.fail && !tc.stale {
				if m.browser != nil {
					t.Fatal("failed new panel was not cleaned up")
				}
				if !strings.Contains(traceOutput(t, next), "fake Chrome startup failure") {
					t.Fatal("startup failure hidden")
				}
			} else if m.browser == nil {
				t.Fatal("completion removed a valid panel")
			}
			wantExternal := tc.fail && !tc.stale && tc.mode == "auto"
			if (len(*external) > 0) != wantExternal {
				t.Fatalf("external opens=%v", *external)
			}
		})
	}
}

func TestTraceIgnoresSupersededNavigationFailure(t *testing.T) {
	m, external := traceCommandModel(t, true, false)
	m.traceNavigate = func(context.Context, string, string) error {
		return errors.New("old navigation failed")
	}
	first := m.command("/trace")().(tea.BatchMsg)
	old := first[len(first)-1]().(traceOpenedMsg)
	panel := m.browser
	m.traceNavigate = func(context.Context, string, string) error { return nil }
	second := m.command("/trace")().(tea.BatchMsg)
	latest := second[len(second)-1]().(traceOpenedMsg)
	m.Update(latest)
	_, cmd := m.Update(old)
	if cmd != nil || m.browser != panel || len(*external) != 0 {
		t.Fatal("superseded failure closed the current panel or triggered fallback")
	}
}

func TestTraceSettingViaConfigCommand(t *testing.T) {
	m, external := traceCommandModel(t, true, false)
	m.configCommand("trace_open_in=external")
	if m.opts.Config.TraceOpenIn() != "external" {
		t.Fatal("config command did not update trace preference")
	}
	out := traceOutput(t, m.command("/trace"))
	if len(*external) != 1 || !strings.Contains(out, m.traceView.URL()) {
		t.Fatalf("saved preference not used: %s %v", out, *external)
	}
}
