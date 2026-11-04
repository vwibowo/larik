package desktop

import "testing"

func TestReopenBrowserAndFilesInFormerPane(t *testing.T) {
	root := t.TempDir()
	a := New(Options{DataDir: t.TempDir(), Cwd: root})
	defer a.Close()
	b := a.startBrowser("http://localhost:3000")
	a.activate(b)
	f := a.startFiles(root)
	a.splitPane(a.paneOf(b), f, true)
	a.closeTab(f)
	if a.split() {
		t.Fatal("closing Files left its pane")
	}
	a.reopenClosed()
	if !a.split() || !a.layout.below || a.focused().files == nil {
		t.Fatal("Files did not reopen in its former pane")
	}
	a.closeTab(b)
	a.reopenClosed()
	if len(a.tabs) != 2 || a.focused() == nil || !a.focused().browser {
		t.Fatalf("browser did not reopen: %d tabs", len(a.tabs))
	}
}
