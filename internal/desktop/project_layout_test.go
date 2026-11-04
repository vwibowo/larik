package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectLayoutRestoresTabsAndPanes(t *testing.T) {
	data, root := t.TempDir(), t.TempDir()
	first := New(Options{DataDir: data, Cwd: root})
	b := first.startBrowser("http://localhost:3000")
	b.dir = root
	first.activate(b)
	f := first.startFiles(root)
	first.splitPane(first.paneOf(b), f, true)
	if err := first.saveProjectLayout(root, "Review"); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second := New(Options{DataDir: data, Cwd: root})
	defer second.Close()
	if err := second.openProjectLayout(root, "Review"); err != nil {
		t.Fatal(err)
	}
	if len(second.tabs) != 2 || !second.split() || !second.layout.below || second.focused().files == nil {
		t.Fatalf("restored layout: tabs=%d split=%v focus=%v", len(second.tabs), second.split(), second.focused())
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	third := New(Options{DataDir: data, Cwd: filepath.Dir(root)})
	defer third.Close()
	if err := third.openProjectLayout(root, "Review"); err == nil {
		t.Fatal("layout opened missing project folder")
	}
}
