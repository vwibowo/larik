package tui

import "testing"

func TestTracePlacement(t *testing.T) {
	for _, c := range []struct {
		mode      string
		panelOK   bool
		wantPanel bool
		wantProb  bool
	}{
		{"auto", true, true, false},
		{"auto", false, false, false},
		{"external", true, false, false},
		{"external", false, false, false},
		{"browser", true, true, false},
		{"browser", false, false, true},
	} {
		panel, problem := tracePlacement(c.mode, c.panelOK)
		if panel != c.wantPanel || (problem != "") != c.wantProb {
			t.Errorf("%s panelOK=%v: panel=%v problem=%q", c.mode, c.panelOK, panel, problem)
		}
	}
}

// A bad argument is refused before anything is served or opened.
func TestTraceRejectsUnknownArgument(t *testing.T) {
	m := testModel(t)
	old := openBrowser
	openBrowser = func(string) { t.Error("the system browser must not open") }
	defer func() { openBrowser = old }()
	cmd := m.openTrace("sideways")
	if cmd == nil {
		t.Fatal("expected a usage message")
	}
}
