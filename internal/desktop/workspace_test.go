package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWorkspacesSwitchAndMoveWithoutClosingPanes(t *testing.T) {
	a := New(Options{Cwd: t.TempDir()})
	one := a.startBrowser("https://example.com")
	a.activate(one)
	w1 := a.activeWorkspace
	w2 := a.newWorkspace("")
	two := a.startBrowser("https://example.org")
	a.activate(two)
	if len(a.panes()) != 1 || a.workspaceOf(one) != w1 || a.workspaceOf(two) != w2 {
		t.Fatal("each workspace should have its own pane tree")
	}
	a.activate(one)
	if a.activeWorkspace != w1 || a.focused() != one {
		t.Fatal("activating an existing pane should switch workspaces")
	}
	a.contentSize = func() (int, int) { return 720, 480 }
	a.overflow = func(*tab) bool {
		t.Fatal("an explicit workspace move should stay in the target window")
		return true
	}
	a.remember()
	if err := a.movePaneToWorkspace(one, w2); err != nil {
		t.Fatal(err)
	}
	if a.activeWorkspace != w2 || a.workspaceOf(one) != w2 || len(a.panes()) != 2 || len(a.tabs) != 2 {
		t.Fatal("moving a pane should preserve its tab and process")
	}
	a.switchWorkspace(w1)
	if a.undoLayout() || a.workspaceOf(one) != w2 {
		t.Fatal("undo must not put a moved pane back in its former workspace")
	}
	a.switchWorkspace(w2)
	if err := a.reorderWorkspace(w2, 1); err != nil || a.workspaces[0] != w2 {
		t.Fatal("workspace order should change")
	}
}

func TestWorkspaceSwitchPreservesLayoutFocusZoomAndUndo(t *testing.T) {
	a := New(Options{})
	one := a.startBrowser("https://example.com")
	a.activate(one)
	two := a.startBrowser("https://example.org")
	a.activate(two)
	a.layout.percent = 35
	a.remember()
	a.toggleZoom()
	w1 := a.activeWorkspace
	w2 := a.newWorkspace("")
	a.switchWorkspace(w1)
	if a.layout.percent != 35 || a.focused() != two || !a.zoomed || len(a.undo) != 1 {
		t.Fatal("workspace layout, focus, zoom, or undo history changed on switch")
	}
	a.switchWorkspace(w2)
	if len(a.panes()) != 0 || a.zoomed {
		t.Fatal("new workspace inherited another workspace's layout")
	}
}

func TestWorkspaceOpenAndCloseOthersStayInTheirWorkspace(t *testing.T) {
	a := New(Options{Cwd: t.TempDir()})
	one := a.startBrowser("https://example.com")
	a.activate(one)
	w1 := a.activeWorkspace
	w2 := a.newWorkspace("")
	two := a.startBrowser("https://example.org")
	a.activate(two)
	a.switchWorkspace(w1)
	res := a.command(Request{Cmd: "open", URL: "https://example.net", Workspace: w2.id, Background: true})
	if res.Error != "" || res.Tab == nil || a.activeWorkspace != w1 || a.focused() != one {
		t.Fatalf("background open changed workspace focus: %+v", res)
	}
	if got := a.workspaceOf(a.tabByID(res.Tab.ID)); got != w2 {
		t.Fatal("new pane did not join the requested workspace")
	}
	a.switchWorkspace(w2)
	a.closeOthers(two)
	if len(a.tabs) != 2 || a.workspaceOf(one) != w1 || a.workspaceOf(two) != w2 {
		t.Fatal("Close Other Panes affected a different workspace")
	}
}

func TestDroppingAnInactivePaneTransfersOwnership(t *testing.T) {
	a := New(Options{})
	one := a.startBrowser("https://example.com")
	a.activate(one)
	w1 := a.activeWorkspace
	w2 := a.newWorkspace("")
	two := a.startBrowser("https://example.org")
	a.activate(two)
	if !a.dropTab(one, a.paneOf(two), zoneRight) || a.workspaceOf(one) != w2 || len(w1.layout.leaves()) != 0 || len(a.panes()) != 2 {
		t.Fatal("dropping an inactive pane should move it into this workspace")
	}
}

func TestWorkspaceTabsAppearInTitlebarAndSwitch(t *testing.T) {
	a := New(Options{Cwd: t.TempDir()})
	one := a.startBrowser("https://example.com")
	a.activate(one)
	w1 := a.activeWorkspace
	a.newWorkspace("Review")
	a.activate(a.startBrowser("https://example.org"))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	snapshot(t, tt, "workspace-tabs-light.png")
	tt.SetDark(true)
	tt.Frame()
	snapshot(t, tt, "workspace-tabs-dark.png")
	if !tt.HasText("Workspace 1") || !tt.HasText("Review") {
		t.Fatal("workspace tabs are missing from the titlebar")
	}
	if err := tt.Click("Workspace 1"); err != nil {
		t.Fatal(err)
	}
	if a.activeWorkspace != w1 || a.focused() != one {
		t.Fatal("clicking a workspace tab did not restore its pane")
	}
	a.contentSize = func() (int, int) { return 720, 480 }
	narrow := ui.NewTester(a.view, 720, 480)
	narrow.SetDark(true)
	narrow.Frame()
	snapshot(t, narrow, "workspace-tabs-narrow.png")
	if !narrow.HasText("Workspace 1") {
		t.Fatal("active workspace disappeared at the minimum window width")
	}
}

func TestWorkspaceCapacityCountsOnlyActivePanes(t *testing.T) {
	a := New(Options{})
	a.contentSize = func() (int, int) { return 720, 480 }
	calls := 0
	a.overflow = func(*tab) bool { calls++; return false }
	for i := 0; i < 3; i++ {
		tb := a.startBrowser("https://example.com")
		a.activate(tb)
	}
	if calls == 0 {
		t.Fatal("crowded active workspace should request another window")
	}
	a.newWorkspace("")
	calls = 0
	a.activate(a.startBrowser("https://example.org"))
	if calls != 0 || len(a.panes()) != 1 {
		t.Fatal("hidden workspaces should not count toward pane capacity")
	}
}

func TestWorkspacesRestoreAndMigrate(t *testing.T) {
	data := t.TempDir()
	a := New(Options{DataDir: data, Cwd: data})
	one := a.startBrowser("https://example.com")
	a.activate(one)
	w1 := a.activeWorkspace
	w2 := a.newWorkspace("Review")
	two := a.startBrowser("https://example.org")
	a.activate(two)
	a.switchWorkspace(w1)
	if err := a.reorderWorkspace(w2, 1); err != nil {
		t.Fatal(err)
	}
	st := a.state()
	if st.Version != 5 || len(st.Workspaces) != 2 || st.ActiveWorkspace != w1.id || st.Workspaces[0].ID != w2.id {
		t.Fatalf("saved workspaces: %+v", st.Workspaces)
	}
	path := a.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := New(Options{DataDir: data, Cwd: data})
	defer restored.Close()
	if !restored.restore() || len(restored.workspaces) != 2 || restored.activeWorkspace.name != w1.name || restored.workspaces[0].name != w2.name {
		t.Fatal("workspace names or active workspace did not restore")
	}
	if len(restored.workspaces[0].layout.leaves()) != 1 || len(restored.workspaces[1].layout.leaves()) != 1 {
		t.Fatal("workspace pane trees did not restore")
	}
	old := st
	old.Version, old.Workspaces, old.ActiveWorkspace = 4, nil, ""
	b, _ = json.Marshal(old)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	migrated := New(Options{DataDir: data, Cwd: data})
	defer migrated.Close()
	if !migrated.restore() || len(migrated.workspaces) != 1 || len(migrated.panes()) != 2 {
		t.Fatal("version 4 layout did not migrate to one workspace")
	}
}

func TestWorkspaceCloseConfirmationAndLastWorkspace(t *testing.T) {
	a := New(Options{})
	if err := a.closeWorkspace(a.activeWorkspace); err == nil {
		t.Fatal("last workspace should not close")
	}
	w := a.newWorkspace("")
	term := &tab{id: "running", cmd: []string{"sleep", "10"}}
	a.tabs = append(a.tabs, term)
	a.activate(term)
	if err := a.closeWorkspace(w); err != nil || !a.confirming || a.closingWorkspace != w {
		t.Fatal("running terminal should require close confirmation")
	}
	a.cancelClose()
	if len(a.workspaces) != 2 || a.closingWorkspace != nil {
		t.Fatal("cancelling should leave workspace and terminal intact")
	}
}

func TestClosingInactiveWorkspaceLeavesActivePanesRunning(t *testing.T) {
	a := New(Options{})
	one := a.startBrowser("https://example.com")
	a.activate(one)
	w1 := a.activeWorkspace
	w2 := a.newWorkspace("")
	two := a.startBrowser("https://example.org")
	a.activate(two)
	a.switchWorkspace(w1)
	if err := a.closeWorkspace(w2); err != nil {
		t.Fatal(err)
	}
	if len(a.workspaces) != 1 || len(a.tabs) != 1 || a.focused() != one || a.tabByID(two.id) != nil {
		t.Fatal("closing an inactive workspace affected the active pane")
	}
}

func TestWorkspaceControlRoutesAcrossWindows(t *testing.T) {
	main, child := New(Options{}), New(Options{})
	child.windowID = "child"
	w := child.newWorkspace("Build")
	g := &windowGroup{main: main, active: main, ids: []string{"child"}, children: map[string]*windowEntry{"child": {app: child}}}
	listed := g.control(Request{Cmd: "workspace", Op: "list"})
	if len(listed.Workspaces) != 3 || listed.Workspaces[2].Window != "child" {
		t.Fatalf("workspace list: %+v", listed.Workspaces)
	}
	res := g.control(Request{Cmd: "workspace", Op: "rename", Workspace: w.id, Name: "Tests"})
	if res.Error != "" || child.activeWorkspace.name != "Tests" {
		t.Fatalf("workspace command routed incorrectly: %+v", res)
	}
}
