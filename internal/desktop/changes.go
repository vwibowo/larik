package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	changespane "larik/internal/desktop/panes/changes"
)

// Limits on what the changes pane shows, so a large checkout or a
// generated file can't stall it.
const (
	maxChangedFiles = changespane.MaxChangedFiles
	maxDiffBytes    = changespane.MaxDiffBytes
	filesOpened     = 5 // the first few files start open
)

// changes is the changes pane: what changed in the main tab's
// repository, file by file, kept current while it is open, against the
// last commit, or against where the branch left its base; and what to do
// with it: stage, discard, commit, open a pull request.
type changes struct{ changespane.State[*fileChange] }

func newChanges(dir string) *changes {
	return &changes{State: changespane.NewState[*fileChange](dir)}
}

// What the changes pane compares with.
const (
	compareCommit = changespane.CompareCommit // the last commit: what isn't committed
	compareBase   = changespane.CompareBase   // where the branch left its base
)

// fileChange is one changed file, with its diffs: what isn't staged and
// what is, for a file whose changes can be taken a hunk at a time (see
// hunks.go), or all of its change.
type fileChange struct {
	path       string
	from       string // its name at the last commit, when it was renamed
	code       string // git's two status letters, as "M " or "??"
	adds, dels int
	note       string // why there's no diff: binary, too large
	open       bool
	parts      []*diffPart
}

// staged reports whether some of the file's change is staged, and all.
func (f *fileChange) staged() (some, all bool) {
	x, y := f.code[0], f.code[1]
	some = x != ' ' && x != '?'
	return some, some && y == ' '
}

// fileRead is a file's change as read from disk, off the main thread:
// its text at the commit compared with, in the working tree, and, when
// hunks says its changes can be taken a hunk at a time, in the index.
type fileRead = changespane.FileRead

// changesRead is a read of the repository, off the main thread.
type changesRead = changespane.Read

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
	a.changes = newChanges(dir)
	go a.watchChanges(a.changes)
}

func (a *App) closeChanges() {
	if a.changes != nil {
		a.changes.Close()
		a.changes = nil
	}
}

// watchChanges checks the repository every two seconds until the pane
// closes. Local changes get a full read when the fingerprint changes,
// after an action, and every 30 seconds; remote changes still get a full
// read on each tick.
func (a *App) watchChanges(ch *changes) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	heads := headCache{}
	var root, indexPath, fingerprint string
	var lastRead time.Time
	lastMode := -1
	force := true
	for {
		mode := int(ch.Mode.Load())
		read := true
		if !isRemote(ch.Dir) {
			if root == "" {
				root, indexPath = changesRepo(ch.Dir)
			}
			if root != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				next, err := changesFingerprint(ctx, root, indexPath)
				cancel()
				if err == nil && !force && mode == lastMode && next == fingerprint && time.Since(lastRead) < 30*time.Second {
					read = false
				}
				fingerprint = next
			}
		}
		if read {
			var r changesRead
			if isRemote(ch.Dir) {
				r = a.readRemoteChanges(ch.Dir, mode)
			} else {
				r = readChanges(ch.Dir, heads, mode)
				if r.Err != nil {
					fingerprint = "" // retry a failed read at the next tick
				}
			}
			lastRead, lastMode = time.Now(), mode
			a.update(func() { ch.apply(r) })
		}
		select {
		case <-ch.Stop:
			return
		case <-a.done:
			return
		case <-tick.C:
			force = false
		case <-ch.Kick:
			force = true
		}
	}
}

// changesRepo resolves paths once for the local Changes pane's cheap
// refresh check. A failed resolution leaves full reads in charge.
func changesRepo(dir string) (root, indexPath string) { return changespane.Repo(dir) }

// changesFingerprint checks the current Git status and the contents of
// changed paths without rebuilding their diffs. A full read still runs
// every 30 seconds to catch preserved file timestamps and config edits.
func changesFingerprint(ctx context.Context, root, indexPath string) (string, error) {
	return changespane.Fingerprint(ctx, root, indexPath)
}

// reread asks for a read now.
func (ch *changes) reread() {
	select {
	case ch.Kick <- struct{}{}:
	default:
	}
}

// compare switches what the files are compared with.
func (ch *changes) compare(against int) {
	ch.Against = against
	ch.Mode.Store(int32(against))
	ch.reread()
}

// apply takes a fresh read, keeping each file's viewer, and whether it is
// open, across reads. One read while the comparison changed is dropped.
func (ch *changes) apply(r changesRead) {
	if r.Against != ch.Against {
		return // the next read compares with what's chosen now
	}
	if r.Err == nil && r.Base == "" && ch.Against == compareBase {
		ch.compare(compareCommit) // no base any more: back to the commit
	}
	ch.Loaded, ch.Root, ch.More, ch.Err = true, r.Root, r.More, ""
	ch.Branch, ch.Base, ch.Remote, ch.GH = r.Branch, r.Base, r.Remote, r.GH
	if r.Err != nil {
		ch.Err = r.Err.Error()
		ch.Files = nil
		return
	}
	snaps := r.Snaps
	byPath := map[string]*fileChange{}
	for _, f := range ch.Files {
		byPath[f.path] = f
	}
	files := make([]*fileChange, 0, len(snaps))
	for i, s := range snaps {
		f := byPath[s.Path]
		if f == nil {
			f = &fileChange{path: s.Path, open: i < filesOpened}
		}
		f.from, f.code, f.adds, f.dels, f.note = s.From, s.Code, s.Adds, s.Dels, s.Note
		f.parts = ch.parts(f, s)
		files = append(files, f)
	}
	ch.Files = files
}

// parts are the diffs a file shows for its read: what isn't staged and
// what is, when its changes can be taken a hunk at a time, or else all
// of its change; each keeps its viewer from the last read.
func (ch *changes) parts(f *fileChange, s fileRead) []*diffPart {
	if s.Note != "" {
		return nil
	}
	type text struct {
		staged   bool
		old, new string
	}
	var want []text
	switch {
	case !s.Hunks:
		want = []text{{false, s.Old, s.New}}
	default:
		if s.Index != s.New {
			want = append(want, text{false, s.Index, s.New})
		}
		if s.Old != s.Index {
			want = append(want, text{true, s.Old, s.Index})
		}
		if len(want) == 0 {
			want = []text{{false, s.Old, s.New}} // a change of mode alone
		}
	}
	var out []*diffPart
	for _, w := range want {
		var p *diffPart
		for _, q := range f.parts {
			if q.staged == w.staged {
				p = q
			}
		}
		if p == nil {
			lang := editor.LanguageFor(s.Path)
			old, nw := editor.NewBuffer(w.old), editor.NewBuffer(w.new)
			old.SetLanguage(lang)
			nw.SetLanguage(lang)
			p = &diffPart{staged: w.staged, old: w.old, new: w.new,
				diff: &editor.DiffState{Old: old, New: nw, Layout: editor.DiffUnified, FitContent: true, Wrap: true}}
		} else {
			if w.old != p.old {
				p.diff.Old.Set(w.old)
			}
			if w.new != p.new {
				p.diff.New.Set(w.new)
			}
			p.old, p.new = w.old, w.new
		}
		// Hunks can be taken while the index shows.
		p.diff.HunkActions = s.Hunks
		p.diff.LineSelection = s.Hunks
		if !s.Hunks {
			p.diff.SetSelectedLines(editor.DiffLineRange{}, false)
		}
		out = append(out, p)
	}
	return out
}

// headCache keeps the text of files at HEAD between reads, by commit and
// path, as it changes only when HEAD moves: without it every read would
// start a git process per changed file.
type headCache = changespane.HeadCache

// readChanges lists what changed in dir's repository against HEAD, or
// for compareBase, against where the branch left its base, with each
// file's text before and after, taking the text before from heads, and
// keeping there what it reads, and no more.
func readChanges(dir string, heads headCache, against int) changesRead {
	return changespane.ReadChanges(dir, heads, against)
}

// hunkable reports whether a file's change can be taken a hunk at a
// time: a text file in git, changed rather than deleted, renamed or in a
// conflict.
func hunkable(s fileRead) bool { return changespane.Hunkable(s) }

// indexBlobs lists the blob of each file in the index, by path.
func indexBlobs(ctx context.Context, root string) map[string]string {
	return changespane.IndexBlobs(ctx, root)
}

// findBase is what the branch is compared with: for one of larik's
// worktrees, the branch its repository's checkout is on, which it was
// made from; otherwise the remote's default branch, or main or master.
// It's "" on the base itself, or with none.
func findBase(ctx context.Context, root, branch string) string {
	return changespane.FindBase(ctx, root, branch)
}

// sinceEntries lists what changed since the commit, as git status lists
// what changed since HEAD: what the branch committed and what it hasn't,
// with what status says isn't tracked.
func sinceEntries(ctx context.Context, root, since string, status []statusEntry) []statusEntry {
	return changespane.SinceEntries(ctx, root, since, status)
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return changespane.Git(ctx, dir, args...)
}

// numstat counts lines added and deleted per tracked file since the
// commit.
func numstat(ctx context.Context, root, since string) map[string][2]int {
	return changespane.Numstat(ctx, root, since)
}

type statusEntry = changespane.StatusEntry

// parseStatus reads `git status --porcelain=v1 -z`.
func parseStatus(out []byte) []statusEntry { return changespane.ParseStatus(out) }

func isBinary(s string) bool { return changespane.IsBinary(s) }

// changesPane shows the changes card.
func (a *App) changesPane(c *ui.Context, t *ui.Theme, ch *changes) {
	where := ch.Root
	if where == "" {
		where = ch.Dir
	}
	changespane.View(c, t, changespane.ViewOptions{
		Icon: iconDiff, Root: ch.Root, Dir: ch.Dir, Branch: ch.Branch, Base: ch.Base,
		Busy: ch.Busy, Error: ch.Err, LocationTooltip: tildePath(where),
		CountLabel: fileCount(len(ch.Files) + ch.More), Loaded: ch.Loaded,
		CanPullRequest: ch.canPullRequest(), Files: len(ch.Files), More: ch.More,
		Against:        func() int { return ch.Against },
		Card:           func(fn func()) { a.opacityCard(c, t, fn) },
		Close:          func() { a.closeButton(c, t, "Close changes", a.closeChanges) },
		AskPullRequest: func() { a.askPullRequest(ch) },
		CompareBar:     func() { a.compareBar(c, t, ch) },
		CommitBox:      func() { a.commitBox(c, t, ch) },
		DrawFiles: func() {
			for _, f := range ch.Files {
				a.fileDiff(c, t, ch, f)
			}
		},
	})
}

// compareBar chooses what the files are compared with: the last commit,
// or where the branch left its base.
func (a *App) compareBar(c *ui.Context, t *ui.Theme, ch *changes) {
	ui.Row(c.Key("compare")).Gap(8).Padding(6, 12, 6, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		seg := ui.Segmented(c.Key("against"), &ch.Against, "Uncommitted", "Since "+ch.Base).Label("Compare with").FontSize(12)
		if seg.Changed() {
			ch.compare(ch.Against)
		}
		if ch.Against == compareBase {
			ui.Text(c, "all "+ch.Branch+" has done since it left "+ch.Base).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
	})
}

// commitBox is where a commit is written: its message, what it commits,
// and staging all or none.
func (a *App) commitBox(c *ui.Context, t *ui.Theme, ch *changes) {
	mod, keys := ui.Ctrl, "Ctrl+Enter"
	if runtime.GOOS == "darwin" {
		mod, keys = ui.Super, "⌘↩"
	}
	ui.Column(c.Key("commit")).Gap(6).Padding(8, 12, 8, 16).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.TextArea(c.Key("message"), &ch.Message).Lines(1, 6).Placeholder("Commit message").Label("Commit message").FontSize(12).
			OnShortcut(mod, ui.KeyEnter, func() { a.commit(ch) })
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			n := ch.stagedCount()
			link := func(key, text string) bool {
				b := ui.ButtonBase(c.Key(key)).Cursor(ui.CursorPointer).Label(text).Disabled(ch.Busy != "").Children(func() {
					ui.Text(c, text).FontSize(12).TextColor(t.Accent)
				})
				return b.Clicked()
			}
			if n < len(ch.Files) && link("stage-all", "Stage All") {
				a.stageAll(ch, true)
			}
			if n > 0 && link("unstage-all", "Unstage All") {
				a.stageAll(ch, false)
			}
			if ch.Busy != "" {
				ui.Text(c, ch.Busy).FontSize(12).TextColor(t.TextMuted).SingleLine()
			}
			ui.Spacer(c)
			label, tip := "Commit", fmt.Sprintf("Commit the %s staged (%s)", fileCount(n), keys)
			if n == 0 {
				label, tip = "Commit All", "Stage every change and commit it ("+keys+")"
			}
			b := ui.Button(c.Key("commit"), label).Tooltip(tip).FontSize(12).Padding(3, 12)
			ready := strings.TrimSpace(ch.Message) != "" && ch.Busy == ""
			if !ready {
				b.Disabled(true)
			}
			if b.Clicked() && ready {
				a.commit(ch)
			}
		})
	})
}

func (a *App) fileDiff(c *ui.Context, t *ui.Theme, ch *changes, f *fileChange) {
	// A row per file, as Xcode lists changes: a check box that stages it,
	// while what isn't committed shows; a disclosure arrow that shows the
	// diff under it, git's status, the path, and the lines added and
	// removed.
	ui.Column(c.Key(f.path)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			if ch.Against == compareCommit {
				some, all := f.staged()
				on := all
				tip := "Stage " + f.path
				switch {
				case all:
					tip = "Staged: unstage " + f.path
				case some:
					tip = "Partly staged: stage the rest of " + f.path
				}
				box := ui.Checkbox(c.Key("stage"), &on, "").Label(tip).Tooltip(tip).Padding(0, 0, 0, 10)
				if ch.Busy != "" {
					box.Disabled(true)
				}
				if box.Changed() && ch.Busy == "" {
					a.stage(ch, f, !all)
				}
			}
			head := ui.Row(c).Grow(1).Shrink(1).MinWidth(0).Gap(6).Padding(5, 12, 5, 8).AlignItems(ui.Center)
			if head.Hovered() {
				head.Background(t.Text.Alpha(0.04))
			}
			head.Children(func() {
				arrow := iconRight
				if f.open {
					arrow = iconDown
				}
				ui.Icon(c, arrow).FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, strings.TrimSpace(f.code)).Font(mono).FontSize(11).TextColor(codeColor(t, f.code)).Width(18)
				ui.Text(c, f.path).FontSize(12).SingleLine().Shrink(1).Grow(1).Tooltip(f.path)
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
			head.ContextMenu(func(m *ui.Menu) { a.fileMenu(m, ch, f) })
		})
		if !f.open {
			return
		}
		if f.note != "" || len(f.parts) == 0 {
			ui.Text(c, "Not shown: "+f.note).FontSize(12).TextColor(t.TextMuted).Padding(8, 12)
			return
		}
		for _, p := range f.parts {
			ui.Column(c.Key(p.staged)).Children(func() {
				if len(f.parts) > 1 {
					// Both, as git keeps them: what a commit takes is the staged.
					label := "Unstaged"
					if p.staged {
						label = "Staged"
					}
					ui.Text(c, label).FontSize(11).Bold().TextColor(t.TextMuted).Padding(6, 12, 4, 12)
				}
				p.diff.Font = ui.Font{Family: mono, Size: 12}
				p.diff.RenderHunkActions = a.hunkActions(ch, f, p)
				editor.DiffView(c, p.diff)
			})
		}
	})
}

// fileMenu is what a changed file's context menu offers: staging it and
// discarding its change, while what isn't committed shows, then opening
// it. A deleted file has nothing on disk to open or show.
func (a *App) fileMenu(m *ui.Menu, ch *changes, f *fileChange) {
	path := filepath.Join(ch.Root, f.path)
	if ch.Against == compareCommit && ch.Busy == "" {
		if _, all := f.staged(); all {
			if m.Item("Unstage").Chosen() {
				a.stage(ch, f, false)
			}
		} else if m.Item("Stage").Chosen() {
			a.stage(ch, f, true)
		}
		if m.Item("Discard Changes…").Chosen() {
			a.askDiscard(ch, f)
		}
		m.Separator()
	}
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
