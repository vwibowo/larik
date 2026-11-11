package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFilesMultiSelectAndBulkTrash(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tb := a.focused()
	a.trashItem = func(path string) error {
		if filepath.Base(path) == "go.sum" {
			return errors.New("busy")
		}
		return os.RemoveAll(path)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Super, "go.sum"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tb.files.picked, []string{filepath.Join(root, "README.md"), filepath.Join(root, "go.sum")}) {
		t.Fatalf("picked %q", tb.files.picked)
	}
	if err := tt.RightClick("README.md"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), "Move To…") || slices.Contains(tt.Menu(), "Rename…") {
		t.Fatalf("multi menu %v", tt.Menu())
	}
	if err := tt.ChooseMenuItem("Move to Trash…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Move to Trash"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, err := os.Lstat(filepath.Join(root, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("README stayed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "go.sum")); err != nil {
		t.Fatalf("failed item lost: %v", err)
	}
	if !slices.Equal(tb.files.picked, []string{filepath.Join(root, "go.sum")}) {
		t.Fatalf("failed selection %q", tb.files.picked)
	}
	if !strings.Contains(a.notice, "1 failed") {
		t.Fatalf("missing failure summary: %q", a.notice)
	}
}

func TestFilesDragAndMoveTo(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tb := a.focused()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	src, ok := tt.Find("README.md")
	if !ok {
		t.Fatal("missing source")
	}
	dst, ok := tt.Find("assets")
	if !ok {
		t.Fatal("missing destination")
	}
	tt.Press(src.X+8, src.Y+src.H/2)
	tt.Move(src.X+25, src.Y+src.H/2)
	tt.Move(dst.X+dst.W/2, dst.Y+dst.H/2)
	tt.Release(dst.X+dst.W/2, dst.Y+dst.H/2)
	tt.Frame()
	if _, err := os.Stat(filepath.Join(root, "assets", "README.md")); err != nil {
		t.Fatalf("drop did not move: %v", err)
	}
	a.beginMoveFiles(tb, []string{filepath.Join(root, "go.sum")})
	a.filesMove.dest = filepath.Join(root, "src")
	tt.Frame()
	if err := tt.Click("Move"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, err := os.Stat(filepath.Join(root, "src", "go.sum")); err != nil {
		t.Fatalf("Move To did not move: %v", err)
	}
}

func TestFilesDragToOtherPaneTreeAreaUsesRoot(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Files behavior")
	}
	left, right := t.TempDir(), t.TempDir()
	src := filepath.Join(left, "root-drop.txt")
	if err := os.WriteFile(src, []byte("root target"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Cwd: left})
	defer a.Close()
	a.openFiles(left)
	to := a.startFiles(right)
	a.showInSplit(to, false)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	from, ok := tt.Find("root-drop.txt")
	if !ok {
		t.Fatalf("source missing: %q", tt.Texts())
	}
	tree, ok := tt.Find("Files tree area " + filepath.Base(right))
	if !ok {
		t.Fatalf("target tree area missing: %q", tt.Texts())
	}
	dropX, dropY := tree.X+tree.W/2, tree.Y+tree.H-12
	tt.Press(from.X+from.W/2, from.Y+from.H/2)
	tt.Move(from.X+from.W/2+20, from.Y+from.H/2)
	tt.Move(dropX, dropY)
	tt.Release(dropX, dropY)
	tt.Frame()
	if data, err := os.ReadFile(filepath.Join(right, "root-drop.txt")); err != nil || string(data) != "root target" {
		t.Fatalf("drop in target tree area did not reach root: %q, %v; tree=%+v drop=(%v,%v) notice=%q texts=%q", data, err, tree, dropX, dropY, a.notice, tt.Texts())
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("source stayed after root drop: %v", err)
	}
}

func TestFilesMoveConflictsAndValidation(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"left", "right", "left/dir", "right/dir"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string) {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("left/a.txt", "new")
	write("right/a.txt", "old")
	write("left/dir/new.txt", "new")
	write("right/dir/old.txt", "old")
	trash := func(path string) error { return os.RemoveAll(path) }
	if got := normalizeFilesSelection([]string{filepath.Join(root, "left", "dir", "new.txt"), filepath.Join(root, "left", "dir")}); !slices.Equal(got, []string{filepath.Join(root, "left", "dir")}) {
		t.Fatalf("nested selection %q", got)
	}
	if err := validateFilesMove(root, root, filepath.Join(root, "left", "dir"), filepath.Join(root, "left", "dir")); err == nil {
		t.Fatal("accepted self move")
	}
	if err := moveFilesPath(filepath.Join(root, "left", "a.txt"), filepath.Join(root, "right", "a.txt"), false, trash); !os.IsExist(err) {
		t.Fatalf("collision %v", err)
	}
	to, err := availableFilesName(filepath.Join(root, "right", "a.txt"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := moveFilesPath(filepath.Join(root, "left", "a.txt"), to, false, trash); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(to) != "a copy.txt" {
		t.Fatalf("keep both path %q", to)
	}
	if err := moveFilesPath(filepath.Join(root, "left", "dir"), filepath.Join(root, "right", "dir"), true, trash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "right", "dir", "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old folder was merged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "right", "dir", "new.txt")); err != nil {
		t.Fatalf("new folder missing: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := validateFilesMove(root, root, to, filepath.Join(root, "escape")); err == nil {
		t.Fatal("accepted symlink escape")
	}
}

func TestFilesSelectionKeysAndPDFClassification(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Files behavior")
	}
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tb := a.focused()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "go.sum"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tb.files.picked, []string{filepath.Join(root, "go.sum"), filepath.Join(root, "README.md")}) {
		t.Fatalf("range %q", tb.files.picked)
	}
	tt.Key(ui.Super, ui.KeyA)
	tt.Frame()
	if len(tb.files.picked) != len(tb.files.lastRows) {
		t.Fatalf("select all: %d of %d", len(tb.files.picked), len(tb.files.lastRows))
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(tb.files.picked) != 0 || tb.files.selected != "" {
		t.Fatalf("escape left selection: %q %q", tb.files.picked, tb.files.selected)
	}
	pdf := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\nminimal fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := readFilePreview(pdf); !p.pdf || p.note != "" {
		t.Fatalf("PDF classification: %+v", p)
	}
	if err := os.WriteFile(pdf, []byte("not a PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := readFilePreview(pdf); p.pdf || !strings.Contains(p.note, "could not") {
		t.Fatalf("invalid PDF: %+v", p)
	}
}

func TestFilesMoveConflictDialogChoices(t *testing.T) {
	root := filesFixture(t)
	a := New(Options{Cwd: root})
	defer a.Close()
	a.openFiles(root)
	tb := a.focused()
	a.trashItem = func(path string) error { return os.RemoveAll(path) }
	src := filepath.Join(root, "README.md")
	destDir := filepath.Join(root, "assets")
	dest := filepath.Join(destDir, "README.md")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	a.startFilesMove(tb, tb, []string{src}, destDir)
	tt.Frame()
	if err := tt.Click("Keep Both"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, err := os.Stat(filepath.Join(destDir, "README copy.md")); err != nil {
		t.Fatalf("Keep Both: %v", err)
	}
	if err := os.WriteFile(src, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.startFilesMove(tb, tb, []string{src}, destDir)
	tt.Frame()
	if err := tt.Click("Replace"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if data, err := os.ReadFile(dest); err != nil || string(data) != "replacement" {
		t.Fatalf("Replace: %q %v", data, err)
	}
	if err := os.WriteFile(src, []byte("skip"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.startFilesMove(tb, tb, []string{src}, destDir)
	tt.Frame()
	if err := tt.Click("Skip"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if data, err := os.ReadFile(src); err != nil || string(data) != "skip" {
		t.Fatalf("Skip: %q %v", data, err)
	}
}

func TestCopyFilesPathToDirectoriesAndConflicts(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	dest := filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "note.txt"), []byte("source"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := copyFilesPathTo(src, dest, false, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "nested", "note.txt")); err != nil || string(data) != "source" {
		t.Fatalf("copied directory contents: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(src, "nested", "note.txt")); err != nil {
		t.Fatalf("copy removed source: %v", err)
	}
	if err := copyFilesPathTo(src, dest, false, nil); !os.IsExist(err) {
		t.Fatalf("copy conflict: %v", err)
	}
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "old.txt")
	newSource := filepath.Join(root, "new.txt")
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newSource, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	trash := func(path string) error {
		if data, err := os.ReadFile(path); err != nil || string(data) != "old" {
			return errors.New("replacement backup did not contain old data")
		}
		return os.Remove(path)
	}
	if err := copyFilesPathTo(newSource, old, true, trash); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "new" {
		t.Fatalf("replacement copy: %q, %v", data, err)
	}
	if data, err := os.ReadFile(newSource); err != nil || string(data) != "new" {
		t.Fatalf("replacement removed source: %q, %v", data, err)
	}
	if err := os.WriteFile(old, []byte("old again"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFilesPathTo(newSource, old, true, func(string) error { return errors.New("trash failed") }); err == nil {
		t.Fatal("replacement succeeded when Trash failed")
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "old again" {
		t.Fatalf("failed replacement did not restore old destination: %q, %v", data, err)
	}
}

func TestFilesCrossVolumeMoveFallback(t *testing.T) {
	root := t.TempDir()
	src, dest := filepath.Join(root, "source.txt"), filepath.Join(root, "destination.txt")
	if err := os.WriteFile(src, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	rename := func(from, to string) error {
		if from == src {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
		}
		return os.Rename(from, to)
	}
	trashed := ""
	if err := moveFilesAcrossVolumesUsing(src, dest, func(path string) error { trashed = path; return os.Remove(path) }, rename); err != nil {
		t.Fatal(err)
	}
	if trashed != src {
		t.Fatalf("source not trashed: %q", trashed)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "content" {
		t.Fatalf("destination %q %v", data, err)
	}
	if err := os.WriteFile(src, []byte("another"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(root, "second.txt")
	if err := moveFilesAcrossVolumesUsing(src, second, func(string) error { return errors.New("trash refused") }, rename); err == nil {
		t.Fatal("trash failure was ignored")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source lost on trash failure: %v", err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("destination remained after trash failure: %v", err)
	}
}

func TestFilesMoveBetweenLocalTabs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Files behavior")
	}
	left, right := t.TempDir(), t.TempDir()
	src := filepath.Join(left, "note.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Cwd: left})
	defer a.Close()
	from, to := a.startFiles(left), a.startFiles(right)
	a.moveFilesDrop(filesDrag{source: from, paths: []string{src}, copying: true}, to, right)
	if data, err := os.ReadFile(filepath.Join(right, "note.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("cross-tab copy: %q, %v", data, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("copy removed source: %v", err)
	}
	src = filepath.Join(left, "move.txt")
	if err := os.WriteFile(src, []byte("move"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.moveFilesDrop(filesDrag{source: from, paths: []string{src}}, to, right)
	if _, err := os.Stat(filepath.Join(right, "move.txt")); err != nil {
		t.Fatalf("cross-tab move: %v", err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("source stayed after move: %v", err)
	}
}
