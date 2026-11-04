package desktop

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func filesFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"src", "assets", "generated"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		"src/main.go":       "package main\n\nfunc main() {}\n",
		"src/helper.go":     "package main\n",
		"README.md":         "# Project\n",
		"go.sum":            "github.com/example/dependency v1.2.3 h1:" + strings.Repeat("QK9", 55) + "\n",
		".env":              "SECRET=example\n",
		".gitignore":        "generated/\n",
		"generated/out.txt": "generated\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	f, err := os.Create(filepath.Join(root, "assets", "logo.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	return root
}

func TestFilesContextMenuActions(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	path := filepath.Join(root, "README.md")
	opened, revealed, copied := "", "", ""
	a.openPath = func(s string) { opened = s }
	a.showFile = func(s string) { revealed = s }
	a.copyText = func(s string) { copied = s }
	choose := func(label, item string) {
		t.Helper()
		if err := tt.RightClick(label); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(item); err != nil {
			t.Fatal(err, tt.Menu())
		}
		tt.Frame()
	}
	choose("README.md", "Open externally")
	choose("README.md", filesRevealLabel())
	choose("README.md", "Copy Path")
	if opened != path || revealed != path || copied != path {
		t.Fatalf("file actions: open %q, reveal %q, copy %q", opened, revealed, copied)
	}
	choose("README.md", "Copy Relative Path")
	if copied != "README.md" {
		t.Fatalf("relative path: %q", copied)
	}
	if err := tt.RightClick("src"); err != nil {
		t.Fatal(err)
	}
	menu := tt.Menu()
	for _, item := range []string{"New File…", "New Folder…", "Open externally", "Copy Path", "Rename…", "Duplicate", "Move to Trash…"} {
		if !slices.Contains(menu, item) {
			t.Fatalf("folder menu lacks %q: %v", item, menu)
		}
	}
	if err := tt.RightClick("Explorer folder " + filepath.Base(root)); err != nil {
		t.Fatal(err)
	}
	rootMenu := tt.Menu()
	if !slices.Contains(rootMenu, "New File…") || slices.Contains(rootMenu, "Move to Trash…") {
		t.Fatalf("explorer root menu: %v", rootMenu)
	}
}

func TestFilesContextMenuCreateRenameDuplicateTrash(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	choose := func(label, item string) {
		t.Helper()
		if err := tt.RightClick(label); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(item); err != nil {
			t.Fatal(err, tt.Menu())
		}
		tt.Frame()
	}
	choose("src", "New File…")
	tt.Frame()
	snapshot(t, tt, "files-new-file-dialog.png")
	if !tt.Focused("Name") {
		t.Fatal("new file name did not get keyboard focus")
	}
	tt.Type("new.go")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if _, err := os.Stat(filepath.Join(root, "src", "new.go")); err != nil {
		t.Fatal(err)
	}
	choose("src", "New Folder…")
	tt.Frame()
	tt.Type("nested")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !isDirectory(filepath.Join(root, "src", "nested")) {
		t.Fatal("folder was not created")
	}
	choose("new.go", "Rename…")
	tt.Frame()
	tt.Type("renamed.go")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if _, err := os.Stat(filepath.Join(root, "src", "renamed.go")); err != nil {
		t.Fatal(err)
	}
	choose("renamed.go", "Rename…")
	tt.Frame()
	tt.Type("main.go")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.filesEdit == nil || a.filesEdit.err == "" {
		t.Fatal("rename collision should stay in the dialog with an error")
	}
	if _, err := os.Stat(filepath.Join(root, "src", "renamed.go")); err != nil {
		t.Fatal("rename collision changed the original", err)
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	choose("renamed.go", "Duplicate")
	if _, err := os.Stat(filepath.Join(root, "src", "renamed copy.go")); err != nil {
		t.Fatal(err)
	}
	trashed := ""
	a.trashItem = func(path string) error {
		trashed = path
		return os.Remove(path)
	}
	choose("renamed copy.go", "Move to Trash…")
	tt.Frame()
	if err := tt.Click("Move to Trash"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if trashed != filepath.Join(root, "src", "renamed copy.go") {
		t.Fatalf("trashed %q", trashed)
	}
}

func TestDuplicateFilesFolderKeepsContents(t *testing.T) {
	root := filesFixture(t)
	src := filepath.Join(root, "src")
	first, err := duplicateFilesPath(src)
	if err != nil {
		t.Fatal(err)
	}
	second, err := duplicateFilesPath(src)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "src copy" || filepath.Base(second) != "src copy 2" {
		t.Fatalf("duplicate names: %q, %q", first, second)
	}
	data, err := os.ReadFile(filepath.Join(second, "main.go"))
	if err != nil || string(data) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("duplicated contents: %q, %v", data, err)
	}
}

func TestFilesTabBrowsingAndPreview(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tb := a.focused()
	if tb == nil || tb.files == nil || tb.dir != root {
		t.Fatalf("Files tab: %+v", tb)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	snapshot(t, tt, "files-light.png")
	for _, label := range []string{"Files", "src", "assets", "README.md", "Select a file to preview"} {
		if !tt.HasText(label) {
			t.Fatalf("missing %q in %q", label, tt.Texts())
		}
	}
	if tt.HasText("generated") || tt.HasText(".env") {
		t.Fatal("hidden and ignored entries should start hidden")
	}
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("main.go") {
		t.Fatalf("folder did not expand: %q", tt.Texts())
	}
	if err := tt.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.files.selected != filepath.Join(root, "src", "main.go") || tb.files.preview.code == nil || tb.files.preview.code.Buffer.String() != "package main\n\nfunc main() {}\n" || !tb.files.preview.code.ReadOnly || tb.files.preview.code.SoftWrap {
		t.Fatalf("file preview: selected %q, texts %q", tb.files.selected, tt.Texts())
	}
	if tb.files.preview.code.Buffer.Language() == nil {
		t.Fatal("Go preview has no syntax highlighting")
	}
	if err := tt.Click("Preview main.go"); err != nil {
		t.Fatal(err)
	}
	tt.Type("not an edit")
	if tb.files.preview.code.Buffer.String() != "package main\n\nfunc main() {}\n" {
		t.Fatal("typing changed a read-only preview")
	}
	opened, revealed := "", ""
	a.openPath = func(p string) { opened = p }
	a.showFile = func(p string) { revealed = p }
	if err := tt.Click("Open externally"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(filesRevealLabel()); err != nil {
		t.Fatal(err)
	}
	if opened != tb.files.selected || revealed != tb.files.selected {
		t.Fatalf("actions: opened %q, revealed %q", opened, revealed)
	}
	if err := tt.Click("Wrap lines"); err != nil {
		t.Fatal(err)
	}
	if !tb.files.wrap || !tb.files.preview.code.SoftWrap {
		t.Fatal("wrap toggle did not update the code viewer")
	}
	tt.SetDark(true)
	tt.Frame()
	snapshot(t, tt, "files-dark.png")
	if err := tt.Click("go.sum"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Wrap lines"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.files.wrap {
		t.Fatal("long lines should remain intact by default")
	}
	snapshot(t, tt, "files-go-sum-dark.png")
	compact := ui.NewTester(a.view, 760, 500)
	compact.Frame()
	compact.Frame()
	if !compact.HasText("main.go") {
		t.Fatal("the compact Files view lacks its tree")
	}
	if err := compact.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	compact.Frame()
	snapshot(t, compact, "files-minimum.png")
	if !compact.HasText("Open externally") || !compact.HasText("Back to Files") {
		t.Fatal("preview actions are missing at the compact size")
	}
	compact.SetDark(true)
	compact.Frame()
	snapshot(t, compact, "files-minimum-dark.png")
	if err := compact.Click("Back to Files"); err != nil {
		t.Fatal(err)
	}
	compact.Frame()
	if tb.files.previewOnly || !compact.HasText("src") {
		t.Fatal("Back to Files did not restore the explorer")
	}
}

func TestFilesSearchVisibilityAndFailures(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tb := a.focused()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click("File visibility"); err != nil {
		t.Fatal(err)
	}
	if menu := tt.Menu(); len(menu) != 2 {
		t.Fatalf("visibility menu: %q", menu)
	}
	tt.ChooseMenuItem("Show hidden files")
	tt.Frame()
	if !tt.HasText(".env") || tt.HasText("generated") || slices.Contains(tt.Texts(), ".git") {
		t.Fatalf("hidden files should exclude ignored folders: %q", tt.Texts())
	}
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("File visibility"); err != nil {
		t.Fatal(err)
	}
	tt.ChooseMenuItem("Show ignored files")
	tt.Frame()
	if !tt.HasText("generated") || !slices.Contains(tt.Texts(), ".git") {
		t.Fatalf("ignored folders are missing: %q", tt.Texts())
	}
	if tb.files.selected != filepath.Join(root, "src", "main.go") || tb.files.open[filepath.Join(root, "src")] == nil || !*tb.files.open[filepath.Join(root, "src")] {
		t.Fatal("changing visibility lost the selected file or expanded folder")
	}
	if err := tt.Click("File visibility"); err != nil {
		t.Fatal(err)
	}
	tt.ChooseMenuItem("Show hidden files")
	tt.Frame()
	if tt.HasText(".env") || !slices.Contains(tt.Texts(), ".git") {
		t.Fatalf("the visibility controls are not independent: %q", tt.Texts())
	}
	if err := tt.Click("Search files and folders"); err != nil {
		t.Fatal(err)
	}
	tt.Type("helper")
	tt.Frame()
	if !tt.HasText("src") || !tt.HasText("helper.go") || tt.HasText("README.md") {
		t.Fatalf("search tree: %q", tt.Texts())
	}
	tt.Type("no-such-file")
	tt.Frame()
	if !tt.HasText("No matching files or folders") {
		t.Fatalf("empty search: %q", tt.Texts())
	}
	tb.files.query = ""
	a.searchFiles(tb)
	tt.Frame()
	tb.files.query = "   "
	a.searchFiles(tb)
	tt.Frame()
	if !tt.HasText("README.md") {
		t.Fatal("whitespace-only search hid the folder tree")
	}
	tb.files.query = ""
	a.searchFiles(tb)
	a.selectFile(tb, filepath.Join(root, "missing.txt"), false)
	tt.Frame()
	if !strings.Contains(tb.files.preview.note, "Can't read this file") {
		t.Fatalf("missing preview: %+v", tb.files.preview)
	}
	a.selectFile(tb, filepath.Join(root, "assets", "logo.png"), false)
	if tb.files.preview.image == nil {
		t.Fatalf("image preview: %+v", tb.files.preview)
	}
	binary := filepath.Join(root, "binary.dat")
	if err := os.WriteFile(binary, []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFilePreview(binary).note, "binary") {
		t.Fatal("binary fallback missing")
	}
	large := filepath.Join(root, "large.txt")
	if err := os.WriteFile(large, make([]byte, maxFilePreview+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFilePreview(large).note, "too large") {
		t.Fatal("large-file fallback missing")
	}
	if _, err := readFilesDir(context.Background(), root, filepath.Join(root, "gone"), false, false); !os.IsNotExist(err) {
		t.Fatalf("missing folder: %v", err)
	}
}

func TestFilesChooserAndTreeKeyboard(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openNew()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	tt.Key(0, ui.KeyF)
	tt.Frame()
	if tb := a.focused(); tb == nil || tb.files == nil {
		t.Fatal("F did not choose Files")
	}
	if info := a.tabInfo(a.focused()); info.Kind != "files" {
		t.Fatalf("control kind: %q", info.Kind)
	}
	if res := a.command(Request{Cmd: "send", Tab: a.focused().id, Text: "hello"}); res.Error != "a Files tab has no terminal" {
		t.Fatalf("send to Files: %q", res.Error)
	}
	a.openFind()
	tt.Frame()
	if !tt.Focused("Search files and folders") {
		t.Fatal("Find did not focus the Files search field")
	}
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.focused().files.selected != filepath.Join(root, "src", "helper.go") {
		t.Fatalf("tree keyboard selected %q", a.focused().files.selected)
	}
}

func TestFilesFollowFocusSplitAndRestore(t *testing.T) {
	root := filesFixture(t)
	other := t.TempDir()
	data := t.TempDir()
	a := New(Options{Cwd: root, DataDir: data})
	defer a.Close()
	a.openFiles(root)
	files := a.focused()
	b := a.startBrowser("")
	b.dir = other
	a.showInSplit(b, false)
	if !a.split() || files.dir != other || files.files.root != other {
		t.Fatalf("Files did not follow focused split: dir %q", files.dir)
	}
	a.activate(files)
	if files.dir != other {
		t.Fatal("focusing Files changed its root")
	}
	ctx := files.files.ctx
	a.focusPane(a.paneOf(b))
	b.dir = root
	a.focusPane(a.paneOf(b))
	if ctx.Err() == nil || files.dir != root {
		t.Fatal("stale folder load was not cancelled on root change")
	}
	a.activate(files)
	st := a.state()
	if len(st.Files) != 1 || st.Files[0] != root && st.Files[1] != root {
		t.Fatalf("saved Files tab: %+v", st.Files)
	}
	state, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.statePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.statePath(), state, 0o600); err != nil {
		t.Fatal(err)
	}
	next := New(Options{Cwd: root, DataDir: data})
	defer next.Close()
	if !next.restore() || len(next.tabs) != 2 || next.focused().files == nil || next.focused().dir != root {
		t.Fatalf("Files restore: tabs %d, focused %+v", len(next.tabs), next.focused())
	}
	remote := remotePath("server", "/work")
	next.followFiles(remote)
	if next.focused().dir != remote {
		t.Fatal("Files did not follow remote working folder")
	}
	tt := ui.NewTester(next.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("Can't open folder: not connected to server") {
		t.Fatalf("remote state missing: %q", tt.Texts())
	}
}

func TestFilesNarrowSplit(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	files := a.focused()
	a.openBrowser("example.com")
	a.showInSplit(files, false)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	tt.Frame()
	if !files.files.narrow || !tt.HasText("Search files and folders") || tt.HasText("Select a file to preview") {
		t.Fatalf("split Files should show its explorer: %q", tt.Texts())
	}
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !files.files.previewOnly || !tt.HasText("Back to Files") || files.files.preview.code == nil {
		t.Fatalf("split preview: %q", tt.Texts())
	}
}
