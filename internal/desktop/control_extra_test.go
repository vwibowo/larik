package desktop

import "testing"

func TestPaneAndNotificationControl(t *testing.T) {
	a := New(Options{DataDir: t.TempDir(), Cwd: t.TempDir()})
	defer a.Close()
	first := a.startBrowser("http://localhost:3000")
	a.activate(first)
	second := a.startBrowser("http://localhost:3001")
	a.splitPane(a.paneOf(first), second, false)
	listed := a.command(Request{Cmd: "pane", Op: "list"})
	if len(listed.Panes) != 2 || listed.Panes[0].ID == "" || listed.Panes[0].ID == listed.Panes[1].ID {
		t.Fatalf("pane list: %+v", listed.Panes)
	}
	firstID := listed.Panes[0].ID
	if res := a.command(Request{Cmd: "pane", Op: "layout", State: "rows"}); res.Error != "" {
		t.Fatal(res.Error)
	}
	if a.paneRef(firstID) == nil {
		t.Fatal("pane ID changed when arranging")
	}
	if res := a.command(Request{Cmd: "pane", Op: "focus", Ref: firstID}); res.Error != "" || a.focused() != first {
		t.Fatalf("pane focus: %+v", res)
	}
	if res := a.command(Request{Cmd: "pane", Op: "move", Ref: firstID, Tab: second.id}); res.Error != "" || a.focused() != second {
		t.Fatalf("pane move: %+v", res)
	}
	a.command(Request{Cmd: "notify", Title: "Build", Body: "Done"})
	if got := a.command(Request{Cmd: "notifications", Op: "list"}); len(got.Notifications) != 1 || got.Notifications[0].Body != "Done" {
		t.Fatalf("notification list: %+v", got)
	}
	a.command(Request{Cmd: "notifications", Op: "clear"})
	if got := a.command(Request{Cmd: "notifications", Op: "list"}); len(got.Notifications) != 0 {
		t.Fatalf("notification clear: %+v", got)
	}
}
