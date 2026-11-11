package desktop

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTerminalTabs(t *testing.T) {
	needTerminal(t)
	t.Setenv("SHELL", "/bin/sh")
	data, dir := t.TempDir(), t.TempDir()
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: dir})
	defer a.Close()
	mu.Lock()
	a.newTab(dir, "", "")
	session := shownAt(a, 0)
	run := `printf '\033]777;notify;Tests;all green\007'; sleep 30`
	sh := a.startShell(dir, run)
	if sh == nil || !sh.shell() || a.tabTitle(sh) != run || a.focused() != session {
		mu.Unlock()
		t.Fatalf("the terminal tab: %+v", sh)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	mu.Unlock()
	frameUntil(t, mu, tt, "the program's notification", func() bool { return len(a.events) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if e := a.events[0]; e.tab != sh.id || e.title != "Tests" || e.body != "all green" || e.read {
		t.Fatalf("the notification: %+v", e)
	}
	if !a.needsLook(sh) || a.tabInfo(sh).Kind != "terminal" || a.tabInfo(sh).Run != run {
		t.Fatalf("the terminal tab rings: %v %+v", a.needsLook(sh), a.tabInfo(sh))
	}
	tt.Frame()
	snapshot(t, tt, "terminal-tab-dark.png")
	if !tt.HasText("Pane: New session") || !tt.HasText("Terminal") {
		t.Fatalf("the terminal has a pane: %q", tt.Texts())
	}
	// Saved as a terminal in its folder, beside larik's tabs.
	st := a.state()
	if len(st.Tabs) != 1 || st.Tabs[0] != "" || st.Terminals[0] != (savedTerminal{Dir: dir, Run: run}) {
		t.Fatalf("saved: %+v", st)
	}
	// Choosing it in the sidebar shows it, ends its ring and reads it.
	a.selectKey(tabKey(sh))
	if a.focused() != sh || a.needsLook(sh) || a.unreadEvents() != 0 {
		t.Fatalf("chosen: focused %s, unread %d", a.focused().id, a.unreadEvents())
	}
	tt.Frame()
	opaque := tt.Image().RGBAAt(900, 700)
	toolbar := tt.Image().RGBAAt(900, 20)
	a.paneOpacity = 0.5
	tt.Frame()
	if translucent := tt.Image().RGBAAt(900, 700); translucent == opaque {
		t.Fatalf("terminal background stayed opaque: %v", translucent)
	}
	if got := tt.Image().RGBAAt(900, 20); got != toolbar {
		t.Errorf("toolbar faded with the terminal: %v, was %v", got, toolbar)
	}
	// It comes back as a shell there, not running the program again.
	a.restored = true
	a.saveState()
	a.writing.Wait()
	next, mu2 := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer next.Close()
	mu2.Lock()
	defer mu2.Unlock()
	if !next.restore() || len(next.tabs) != 1 || !next.tabs[0].shell() || next.tabs[0].run != "" || next.tabs[0].dir != dir || shownAt(next, 0) != next.tabs[0] {
		t.Fatalf("restored: %+v", next.tabs)
	}
}

func TestEmptyStatePaneOpacity(t *testing.T) {
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: t.TempDir(), Cwd: t.TempDir()})
	defer a.Close()
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Lock()
	tt.Frame()
	opaque := tt.Image().RGBAAt(900, 700)
	a.paneOpacity = 0.5
	tt.Frame()
	mu.Unlock()
	if got := tt.Image().RGBAAt(900, 700); got == opaque {
		t.Fatalf("empty-state pane background stayed opaque: %v", got)
	}
}

func TestShellCommand(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := shellCommand(""); len(got) != 2 || got[0] != "/bin/zsh" || got[1] != "-l" {
		t.Fatalf("the shell: %q", got)
	}
	if got := shellCommand("lazygit"); len(got) != 3 || got[1] != "-ilc" || got[2] != "lazygit" {
		t.Fatalf("a program: %q", got)
	}
}
