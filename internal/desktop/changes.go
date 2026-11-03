package desktop

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// Limits on what the changes pane shows, so a large checkout or a
// generated file can't stall it.
const (
	maxChangedFiles = 60
	maxDiffBytes    = 512 << 10
	filesOpened     = 5 // the first few files start open
)

// changes is the changes pane: the working tree of the main tab's
// repository against HEAD, file by file, kept current while it is open.
type changes struct {
	dir    string // the directory it was opened for
	root   string // its repository's top level
	files  []*fileChange
	more   int // changed files past maxChangedFiles
	err    string
	loaded bool
	stop   chan struct{}
}

// fileChange is one changed file, with its diff viewer.
type fileChange struct {
	path       string
	code       string // git's two status letters, as "M " or "??"
	adds, dels int
	note       string // why there's no diff: binary, too large
	open       bool
	diff       *editor.DiffState
	old, new   string
}

// fileRead is a file's change as read from disk, off the main thread.
type fileRead struct {
	path, code string
	adds, dels int
	note       string
	old, new   string
}

// toggleChanges opens the changes pane beside the main one, or closes it.
func (a *App) toggleChanges() {
	if a.changes != nil {
		a.closeChanges()
		return
	}
	dir := a.currentDir()
	if dir == "" {
		return
	}
	a.right, a.rightFocused = nil, false
	a.changes = &changes{dir: dir, stop: make(chan struct{})}
	go a.watchChanges(a.changes)
}

func (a *App) closeChanges() {
	if a.changes != nil {
		close(a.changes.stop)
		a.changes = nil
	}
}

// watchChanges reads the repository every two seconds until the pane
// closes, which is often enough to follow an agent's edits.
func (a *App) watchChanges(ch *changes) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	heads := headCache{}
	for {
		root, snaps, more, err := readChanges(ch.dir, heads)
		a.update(func() { ch.apply(root, snaps, more, err) })
		select {
		case <-ch.stop:
			return
		case <-a.done:
			return
		case <-tick.C:
		}
	}
}

// apply takes a fresh read, keeping each file's viewer, and whether it is
// open, across reads.
func (ch *changes) apply(root string, snaps []fileRead, more int, err error) {
	ch.loaded, ch.root, ch.more, ch.err = true, root, more, ""
	if err != nil {
		ch.err = err.Error()
		ch.files = nil
		return
	}
	byPath := map[string]*fileChange{}
	for _, f := range ch.files {
		byPath[f.path] = f
	}
	files := make([]*fileChange, 0, len(snaps))
	for i, s := range snaps {
		f := byPath[s.path]
		if f == nil {
			f = &fileChange{path: s.path, open: i < filesOpened}
		}
		f.code, f.adds, f.dels, f.note = s.code, s.adds, s.dels, s.note
		if s.note == "" {
			if f.diff == nil {
				lang := editor.LanguageFor(s.path)
				old, nw := editor.NewBuffer(s.old), editor.NewBuffer(s.new)
				old.SetLanguage(lang)
				nw.SetLanguage(lang)
				f.diff = &editor.DiffState{Old: old, New: nw, Layout: editor.DiffUnified, FitContent: true, Wrap: true}
			} else {
				if s.old != f.old {
					f.diff.Old.Set(s.old)
				}
				if s.new != f.new {
					f.diff.New.Set(s.new)
				}
			}
			f.old, f.new = s.old, s.new
		}
		files = append(files, f)
	}
	ch.files = files
}

// headCache keeps the text of files at HEAD between reads, by commit and
// path, as it changes only when HEAD moves: without it every read would
// start a git process per changed file.
type headCache map[string]string

// readChanges lists what changed in dir's repository against HEAD, with
// each file's text before and after, taking HEAD's text from heads, and
// keeping there what it reads, and no more.
func readChanges(dir string, heads headCache) (root string, snaps []fileRead, more int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, 0, fmt.Errorf("%s is not in a git repository", dir)
	}
	root = strings.TrimSpace(string(out))
	status, err := git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return root, nil, 0, err
	}
	counts := numstat(ctx, root)
	entries := parseStatus(status)
	head, _ := git(ctx, root, "rev-parse", "HEAD") // none before the first commit
	used := map[string]bool{}
	if len(entries) > maxChangedFiles {
		more = len(entries) - maxChangedFiles
		entries = entries[:maxChangedFiles]
	}
	for _, e := range entries {
		s := fileRead{path: e.path, code: e.code}
		if c, ok := counts[e.path]; ok {
			s.adds, s.dels = c[0], c[1]
		}
		if e.code != "??" && e.code[0] != 'A' {
			key := strings.TrimSpace(string(head)) + ":" + e.from
			old, ok := heads[key]
			if !ok {
				out, _ := git(ctx, root, "show", "HEAD:"+e.from)
				old = string(out)
				heads[key] = old
			}
			used[key] = true
			s.old = old
		}
		if data, err := os.ReadFile(filepath.Join(root, e.path)); err == nil {
			s.new = string(data)
			if e.code == "??" {
				s.adds = strings.Count(s.new, "\n")
			}
		}
		switch {
		case len(s.old) > maxDiffBytes || len(s.new) > maxDiffBytes:
			s.note, s.old, s.new = "too large to show", "", ""
		case isBinary(s.old) || isBinary(s.new):
			s.note, s.old, s.new = "binary", "", ""
		}
		snaps = append(snaps, s)
	}
	for key := range heads {
		if !used[key] {
			delete(heads, key)
		}
	}
	return root, snaps, more, nil
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-optional-locks"}, args...)...)
	return cmd.Output()
}

// numstat counts lines added and deleted per tracked file.
func numstat(ctx context.Context, root string) map[string][2]int {
	out, err := git(ctx, root, "diff", "HEAD", "--numstat", "--no-renames")
	if err != nil {
		return nil
	}
	counts := map[string][2]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		adds, _ := strconv.Atoi(f[0])
		dels, _ := strconv.Atoi(f[1])
		counts[f[2]] = [2]int{adds, dels}
	}
	return counts
}

type statusEntry struct {
	code, path string
	from       string // the path in HEAD: the old name of a rename
}

// parseStatus reads `git status --porcelain=v1 -z`.
func parseStatus(out []byte) []statusEntry {
	var entries []statusEntry
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		f := string(fields[i])
		if len(f) < 4 {
			continue
		}
		e := statusEntry{code: f[:2], path: f[3:]}
		e.from = e.path
		if e.code[0] == 'R' || e.code[0] == 'C' {
			if i+1 < len(fields) {
				i++
				e.from = string(fields[i])
			}
		}
		entries = append(entries, e)
	}
	return entries
}

func isBinary(s string) bool {
	return strings.IndexByte(s[:min(len(s), 8000)], 0) >= 0
}

// changesPane shows the changes card.
func (a *App) changesPane(c *ui.Context, t *ui.Theme, ch *changes) {
	a.card(c, t, func() {
		ui.Row(c).Gap(8).Padding(9, 12, 9, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, iconDiff).FontSize(15).TextColor(t.Accent)
			ui.Text(c, "Changes").Bold()
			where := ch.root
			if where == "" {
				where = ch.dir
			}
			ui.Text(c, filepath.Base(where)).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(tildePath(where))
			ui.Spacer(c)
			if ch.loaded && ch.err == "" {
				ui.Text(c, fileCount(len(ch.files)+ch.more)).FontSize(12).TextColor(t.TextMuted).SingleLine()
			}
			a.closeButton(c, t, "Close changes", a.closeChanges)
		})
		scroll := ui.Scroll(c).Grow(1).Padding(8, 12).Gap(6)
		scroll.Children(func() {
			switch {
			case !ch.loaded:
				ui.Spinner(c).Margin(ui.Auto)
			case ch.err != "":
				ui.Text(c, ch.err).TextColor(t.TextMuted).Margin(ui.Auto)
			case len(ch.files) == 0:
				ui.Text(c, "No changes against HEAD.").TextColor(t.TextMuted).Margin(ui.Auto)
			}
			for _, f := range ch.files {
				a.fileDiff(c, t, ch.root, f)
			}
			if ch.more > 0 {
				ui.Text(c, fmt.Sprintf("and %d more", ch.more)).FontSize(12).TextColor(t.TextMuted).Padding(4)
			}
		})
	})
}

func (a *App) fileDiff(c *ui.Context, t *ui.Theme, root string, f *fileChange) {
	ui.Column(c.Key(f.path)).Radius(8).Border(1, t.Border).Clip().Children(func() {
		head := ui.Row(c).Gap(8).Padding(6, 10).AlignItems(ui.Center).Background(t.Surface.Alpha(0.6)).Cursor(ui.CursorPointer)
		head.Children(func() {
			ui.Text(c, strings.TrimSpace(f.code)).Font(mono).FontSize(12).TextColor(codeColor(t, f.code)).Width(18)
			ui.Text(c, f.path).Font(mono).FontSize(12.5).SingleLine().Shrink(1).Grow(1).Tooltip(f.path)
			if f.adds > 0 {
				ui.Text(c, fmt.Sprintf("+%d", f.adds)).Font(mono).FontSize(12).TextColor(t.Success)
			}
			if f.dels > 0 {
				ui.Text(c, fmt.Sprintf("−%d", f.dels)).Font(mono).FontSize(12).TextColor(t.Danger)
			}
		})
		if head.Clicked() {
			f.open = !f.open
		}
		head.ContextMenu(func(m *ui.Menu) { a.fileMenu(m, filepath.Join(root, f.path), f) })
		if !f.open {
			return
		}
		if f.note != "" || f.diff == nil {
			ui.Text(c, "Not shown: "+f.note).FontSize(12).TextColor(t.TextMuted).Padding(8, 12)
			return
		}
		f.diff.Font = ui.Font{Family: mono, Size: 12}
		editor.DiffView(c, f.diff)
	})
}

// fileMenu is what a changed file's context menu offers. A deleted file
// has nothing on disk to open or show.
func (a *App) fileMenu(m *ui.Menu, path string, f *fileChange) {
	gone := strings.Contains(f.code, "D")
	if !gone && m.Item("Open").Chosen() && a.openPath != nil {
		a.openPath(path)
	}
	show := "Show in Folder"
	if runtime.GOOS == "darwin" {
		show = "Show in Finder"
	}
	if !gone && m.Item(show).Chosen() && a.showFile != nil {
		a.showFile(path)
	}
	if m.Item("Copy Path").Chosen() && a.copyText != nil {
		a.copyText(path)
	}
}

// codeColor colours git's status letters: added green, deleted red, the
// rest amber.
func codeColor(t *ui.Theme, code string) ui.Color {
	switch {
	case code == "??" || strings.Contains(code, "A"):
		return t.Success
	case strings.Contains(code, "D"):
		return t.Danger
	}
	return t.Warning
}

func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// tildePath shortens a path under the home directory to ~/….
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(filepath.Separator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}
