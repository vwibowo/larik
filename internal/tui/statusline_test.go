package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// runStatusNow runs the status command the model asks for, if any, and
// hands the result back.
func runStatusNow(t *testing.T, m *model) bool {
	t.Helper()
	cmd := m.refreshStatus()
	if cmd == nil {
		return false
	}
	m.Update(cmd().(statusDoneMsg))
	return true
}

func TestStatusLineCommand(t *testing.T) {
	m := testModel(t)
	got := filepath.Join(t.TempDir(), "in.json")
	m.status = &statusCmd{command: `cat > "` + got + `"; printf '\033[1mmain\033[0m · ok\nsecond line\n\n'`}
	m.opts.Version = "9.9"
	m.width = 60

	if !runStatusNow(t, m) {
		t.Fatal("the first refresh should run the command")
	}
	var in statusInput
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	if in.Model.ID != "m" || in.SessionID == "" || in.Cwd != m.opts.Config.Cwd || in.Version != "9.9" || in.Larik.Mode != "default" {
		t.Errorf("input = %s", data)
	}

	lines := strings.Split(plain(m.statusLine()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "main · ok") || lines[1] != "second line" {
		t.Fatalf("footer = %q", lines)
	}
	for _, want := range []string{"◆ m", "ollama"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("custom footer should keep built-in right-side information %q: %q", want, lines[0])
		}
	}
	if !strings.Contains(lines[0], modeLabels[m.agent.Perms().Mode()]) {
		t.Errorf("the mode chip must stay: %q", lines[0])
	}
	if runStatusNow(t, m) {
		t.Error("nothing changed, so the command shouldn't run again")
	}
	m.stats.CostUSD = 1.5
	if !runStatusNow(t, m) {
		t.Error("a new cost should run the command again")
	}

	// ctrl+c's warning needs the default footer.
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !strings.Contains(plain(m.statusLine()), "ctrl+c again to quit") {
		t.Error("the quit warning should show over the custom status line")
	}
}

func TestCustomSidebarCommandRendersInSidebar(t *testing.T) {
	m := testModel(t)
	m.sidebarStatus = newStatusCmd("printf 'git: 2 changed\\nactivity: responding\\n'", 300)
	lines, err := runStatusLimit(m.sidebarStatus.command, m.opts.Config.Cwd, m.statusPayload(), sidebarMaxLines)
	if err != nil {
		t.Fatal(err)
	}
	m.statusDone(statusDoneMsg{lines: lines, sidebar: true})
	m.setWidth(120)
	got := plain(m.sessionSidebar(44, 30))
	for _, want := range []string{"Custom", "git: 2 changed", "activity: responding"} {
		if !strings.Contains(got, want) {
			t.Errorf("custom sidebar missing %q: %s", want, got)
		}
	}
}

func TestStatusLinesHaveOutputLimit(t *testing.T) {
	if got := statusLinesLimit("a\nb\nc\nd", 2); len(got) != 2 || got[1] != "b" {
		t.Fatalf("limited lines = %q", got)
	}
}

func TestCustomSidebarPollsForExternalChanges(t *testing.T) {
	m := testModel(t)
	file := filepath.Join(t.TempDir(), "state.txt")
	if err := os.WriteFile(file, []byte("idle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.showInfo = true
	m.sidebarStatus = newStatusCmd("cat "+file, 300)
	first := m.refreshStatus()().(statusDoneMsg)
	m.statusDone(first)
	if got := strings.Join(m.sidebarStatus.lines, "\n"); got != "idle" {
		t.Fatalf("initial sidebar output = %q", got)
	}
	if err := os.WriteFile(file, []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if cmd := m.refreshStatus(); cmd != nil {
		t.Fatal("sidebar should respect its poll interval")
	}
	m.sidebarStatus.ended = time.Now().Add(-m.sidebarStatus.interval)
	second := m.refreshStatus()().(statusDoneMsg)
	m.statusDone(second)
	if got := strings.Join(m.sidebarStatus.lines, "\n"); got != "changed" {
		t.Fatalf("refreshed sidebar output = %q", got)
	}
}

func TestStatusLineFailureKeepsDefaultFooter(t *testing.T) {
	m := testModel(t)
	m.status = &statusCmd{command: "echo broken >&2; exit 3"}
	report := m.statusDone(m.refreshStatus()().(statusDoneMsg))
	if report == nil || !strings.Contains(string(report().(outputMsg)), "broken") {
		t.Error("the failure should be reported")
	}
	if !strings.Contains(plain(m.statusLine()), "◆ m") {
		t.Errorf("a failing command should leave the default footer: %q", plain(m.statusLine()))
	}
	m.stats.CostUSD = 2
	if report := m.statusDone(m.refreshStatus()().(statusDoneMsg)); report != nil {
		t.Error("the failure should be reported only once")
	}
}
