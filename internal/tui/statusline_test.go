package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
