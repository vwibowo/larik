package desktop

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"larik/internal/worktree"
)

// Limits on what the changes pane shows, so a large checkout or a
// generated file can't stall it.
const (
	maxChangedFiles = 60
	maxDiffBytes    = 512 << 10
	filesOpened     = 5 // the first few files start open
)

// changes is the changes pane: what changed in the main tab's
// repository, file by file, kept current while it is open, against the
// last commit, or against where the branch left its base; and what to do
// with it: stage, discard, commit, open a pull request.
type changes struct {
	dir    string // the directory it was opened for
	root   string // its repository's top level
	files  []*fileChange
	more   int // changed files past maxChangedFiles
	err    string
	loaded bool
	stop   chan struct{}
	kick   chan struct{} // reads again now, after an action

	// against is what the files are compared with: compareCommit, what
	// isn't committed, or compareBase, all the branch has done since it
	// left its base. mode is the same, for the reads off the main thread.
	against int
	mode    atomic.Int32
	branch  string // checked out, or "" when detached
	base    string // what the branch is compared with, as main or origin/main; "" for nothing to
	remote  bool   // there's a remote to push to
	gh      bool   // gh is installed, to open pull requests with
	busy    string // what's under way, as "Committing…"
	message string // the commit message being written
}

// What the changes pane compares with.
const (
	compareCommit = iota // the last commit: what isn't committed
	compareBase          // where the branch left its base
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
type fileRead struct {
	path, from, code string
	adds, dels       int
	note             string
	old, new         string
	index            string
	hunks            bool
}

// changesRead is a read of the repository, off the main thread.
type changesRead struct {
	against            int // what it compared with
	root, branch, base string
	remote, gh         bool
	snaps              []fileRead
	more               int
	err                error
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
	a.changes = &changes{dir: dir, stop: make(chan struct{}), kick: make(chan struct{}, 1)}
	go a.watchChanges(a.changes)
}

func (a *App) closeChanges() {
	if a.changes != nil {
		close(a.changes.stop)
		a.changes = nil
	}
}

// watchChanges reads the repository every two seconds until the pane
// closes, which is often enough to follow an agent's edits, and at once
// after an action.
func (a *App) watchChanges(ch *changes) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	heads := headCache{}
	for {
		var r changesRead
		if isRemote(ch.dir) {
			r = a.readRemoteChanges(ch.dir, int(ch.mode.Load()))
		} else {
			r = readChanges(ch.dir, heads, int(ch.mode.Load()))
		}
		a.update(func() { ch.apply(r) })
		select {
		case <-ch.stop:
			return
		case <-a.done:
			return
		case <-tick.C:
		case <-ch.kick:
		}
	}
}

// reread asks for a read now.
func (ch *changes) reread() {
	select {
	case ch.kick <- struct{}{}:
	default:
	}
}

// compare switches what the files are compared with.
func (ch *changes) compare(against int) {
	ch.against = against
	ch.mode.Store(int32(against))
	ch.reread()
}

// apply takes a fresh read, keeping each file's viewer, and whether it is
// open, across reads. One read while the comparison changed is dropped.
func (ch *changes) apply(r changesRead) {
	if r.against != ch.against {
		return // the next read compares with what's chosen now
	}
	if r.err == nil && r.base == "" && ch.against == compareBase {
		ch.compare(compareCommit) // no base any more: back to the commit
	}
	ch.loaded, ch.root, ch.more, ch.err = true, r.root, r.more, ""
	ch.branch, ch.base, ch.remote, ch.gh = r.branch, r.base, r.remote, r.gh
	if r.err != nil {
		ch.err = r.err.Error()
		ch.files = nil
		return
	}
	snaps := r.snaps
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
		f.from, f.code, f.adds, f.dels, f.note = s.from, s.code, s.adds, s.dels, s.note
		f.parts = ch.parts(f, s)
		files = append(files, f)
	}
	ch.files = files
}

// parts are the diffs a file shows for its read: what isn't staged and
// what is, when its changes can be taken a hunk at a time, or else all
// of its change; each keeps its viewer from the last read.
func (ch *changes) parts(f *fileChange, s fileRead) []*diffPart {
	if s.note != "" {
		return nil
	}
	type text struct {
		staged   bool
		old, new string
	}
	var want []text
	switch {
	case !s.hunks:
		want = []text{{false, s.old, s.new}}
	default:
		if s.index != s.new {
			want = append(want, text{false, s.index, s.new})
		}
		if s.old != s.index {
			want = append(want, text{true, s.old, s.index})
		}
		if len(want) == 0 {
			want = []text{{false, s.old, s.new}} // a change of mode alone
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
			lang := editor.LanguageFor(s.path)
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
		p.diff.HunkActions = s.hunks
		p.diff.LineSelection = s.hunks
		if !s.hunks {
			p.diff.SetSelectedLines(editor.DiffLineRange{}, false)
		}
		out = append(out, p)
	}
	return out
}

// headCache keeps the text of files at HEAD between reads, by commit and
// path, as it changes only when HEAD moves: without it every read would
// start a git process per changed file.
type headCache map[string]string

// readChanges lists what changed in dir's repository against HEAD, or
// for compareBase, against where the branch left its base, with each
// file's text before and after, taking the text before from heads, and
// keeping there what it reads, and no more.
func readChanges(dir string, heads headCache, against int) (r changesRead) {
	r.against = against
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		r.err = fmt.Errorf("%s is not in a git repository", dir)
		return r
	}
	r.root = strings.TrimSpace(string(out))
	root := r.root
	branch, _ := git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	r.branch = strings.TrimSpace(string(branch))
	r.base = findBase(ctx, root, r.branch)
	remotes, _ := git(ctx, root, "remote")
	r.remote = strings.TrimSpace(string(remotes)) != ""
	_, err = exec.LookPath("gh")
	r.gh = err == nil

	status, err := git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		r.err = err
		return r
	}
	entries := parseStatus(status)
	head, _ := git(ctx, root, "rev-parse", "HEAD") // none before the first commit
	since := strings.TrimSpace(string(head))
	if against == compareBase && r.base != "" {
		mb, err := git(ctx, root, "merge-base", "HEAD", r.base)
		if err == nil {
			since = strings.TrimSpace(string(mb))
			entries = sinceEntries(ctx, root, since, entries)
		}
	}
	counts := numstat(ctx, root, cmp.Or(since, "HEAD"))
	var index map[string]string // path → its blob in the index, for files with staged changes
	used := map[string]bool{}
	if len(entries) > maxChangedFiles {
		r.more = len(entries) - maxChangedFiles
		entries = entries[:maxChangedFiles]
	}
	for _, e := range entries {
		s := fileRead{path: e.path, code: e.code}
		if e.from != e.path {
			s.from = e.from
		}
		if c, ok := counts[e.path]; ok {
			s.adds, s.dels = c[0], c[1]
		}
		if e.code != "??" && e.code[0] != 'A' && since != "" {
			key := since + ":" + e.from
			old, ok := heads[key]
			if !ok {
				out, _ := git(ctx, root, "show", since+":"+e.from)
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
		case against == compareCommit && hunkable(s):
			// What's in the index: HEAD's text, with nothing staged.
			s.hunks, s.index = true, s.old
			if s.code[0] != ' ' {
				if index == nil {
					index = indexBlobs(ctx, root)
				}
				blob, ok := index[s.path]
				if !ok {
					s.hunks = false
					break
				}
				key := "blob:" + blob
				text, ok := heads[key]
				if !ok {
					out, _ := git(ctx, root, "cat-file", "blob", blob)
					text = string(out)
					heads[key] = text
				}
				used[key] = true
				s.index = text
			}
		}
		r.snaps = append(r.snaps, s)
	}
	for key := range heads {
		if !used[key] {
			delete(heads, key)
		}
	}
	return r
}

// hunkable reports whether a file's change can be taken a hunk at a
// time: a text file in git, changed rather than deleted, renamed or in a
// conflict.
func hunkable(s fileRead) bool {
	x, y := s.code[0], s.code[1]
	return s.from == "" && strings.IndexByte(" MA", x) >= 0 && strings.IndexByte(" M", y) >= 0
}

// indexBlobs lists the blob of each file in the index, by path.
func indexBlobs(ctx context.Context, root string) map[string]string {
	out, err := git(ctx, root, "ls-files", "-s", "-z")
	if err != nil {
		return nil
	}
	blobs := map[string]string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		// "<mode> <blob> <stage>\t<path>"
		meta, path, ok := strings.Cut(entry, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 && f[2] == "0" {
			blobs[path] = f[1]
		}
	}
	return blobs
}

// findBase is what the branch is compared with: for one of larik's
// worktrees, the branch its repository's checkout is on, which it was
// made from; otherwise the remote's default branch, or main or master.
// It's "" on the base itself, or with none.
func findBase(ctx context.Context, root, branch string) string {
	base := ""
	if strings.HasPrefix(branch, worktree.BranchPrefix) {
		if common, err := git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			out, _ := git(ctx, filepath.Dir(strings.TrimSpace(string(common))), "symbolic-ref", "--quiet", "--short", "HEAD")
			base = strings.TrimSpace(string(out))
		}
	}
	if base == "" {
		if out, err := git(ctx, root, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
			base = strings.TrimSpace(string(out))
		}
	}
	for _, b := range []string{"main", "master"} {
		if base != "" {
			break
		}
		if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			base = b
		}
	}
	if base == branch || strings.TrimPrefix(base, "origin/") == branch {
		return ""
	}
	return base
}

// sinceEntries lists what changed since the commit, as git status lists
// what changed since HEAD: what the branch committed and what it hasn't,
// with what status says isn't tracked.
func sinceEntries(ctx context.Context, root, since string, status []statusEntry) []statusEntry {
	out, err := git(ctx, root, "diff", "--name-status", "-z", "--no-renames", since)
	if err != nil {
		return status
	}
	var entries []statusEntry
	fields := bytes.Split(out, []byte{0})
	for i := 0; i+1 < len(fields); i += 2 {
		if code := string(fields[i]); code != "" {
			path := string(fields[i+1])
			entries = append(entries, statusEntry{code: code[:1] + " ", path: path, from: path})
		}
	}
	for _, e := range status {
		if e.code == "??" {
			entries = append(entries, e)
		}
	}
	return entries
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-optional-locks"}, args...)...)
	return cmd.Output()
}

// numstat counts lines added and deleted per tracked file since the
// commit.
func numstat(ctx context.Context, root, since string) map[string][2]int {
	out, err := git(ctx, root, "diff", since, "--numstat", "--no-renames")
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
			if ch.loaded && ch.canPullRequest() {
				pr := ui.Button(c.Key("pr"), "Pull Request").FontSize(12).Padding(2, 8).Tooltip("Open " + ch.branch + "'s pull request into " + strings.TrimPrefix(ch.base, "origin/") + ", or push it and start one, with gh")
				if ch.busy != "" {
					pr.Disabled(true)
				}
				if pr.Clicked() && ch.busy == "" {
					a.askPullRequest(ch)
				}
			}
			a.closeButton(c, t, "Close changes", a.closeChanges)
		})
		if ch.base != "" {
			a.compareBar(c, t, ch)
		}
		if ch.against == compareCommit && len(ch.files) > 0 {
			a.commitBox(c, t, ch)
		}
		scroll := ui.Scroll(c).Grow(1)
		scroll.Children(func() {
			switch {
			case !ch.loaded:
				ui.Spinner(c).Margin(ui.Auto)
			case ch.err != "":
				ui.Text(c, ch.err).TextColor(t.TextMuted).Margin(ui.Auto)
			case len(ch.files) == 0 && ch.against == compareBase:
				ui.Text(c, "Nothing since "+ch.branch+" left "+ch.base+".").TextColor(t.TextMuted).Margin(ui.Auto)
			case len(ch.files) == 0:
				ui.Text(c, "No changes against HEAD.").TextColor(t.TextMuted).Margin(ui.Auto)
			}
			for _, f := range ch.files {
				a.fileDiff(c, t, ch, f)
			}
			if ch.more > 0 {
				ui.Text(c, fmt.Sprintf("and %d more", ch.more)).FontSize(12).TextColor(t.TextMuted).Padding(4)
			}
		})
	})
}

// compareBar chooses what the files are compared with: the last commit,
// or where the branch left its base.
func (a *App) compareBar(c *ui.Context, t *ui.Theme, ch *changes) {
	ui.Row(c.Key("compare")).Gap(8).Padding(6, 12, 6, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		seg := ui.Segmented(c.Key("against"), &ch.against, "Uncommitted", "Since "+ch.base).Label("Compare with").FontSize(12)
		if seg.Changed() {
			ch.compare(ch.against)
		}
		if ch.against == compareBase {
			ui.Text(c, "all "+ch.branch+" has done since it left "+ch.base).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
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
		ui.TextArea(c.Key("message"), &ch.message).Lines(1, 6).Placeholder("Commit message").Label("Commit message").FontSize(12).
			OnShortcut(mod, ui.KeyEnter, func() { a.commit(ch) })
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			n := ch.stagedCount()
			link := func(key, text string) bool {
				return ui.Text(c.Key(key), text).FontSize(12).TextColor(t.Accent).Cursor(ui.CursorPointer).Clicked() && ch.busy == ""
			}
			if n < len(ch.files) && link("stage-all", "Stage All") {
				a.stageAll(ch, true)
			}
			if n > 0 && link("unstage-all", "Unstage All") {
				a.stageAll(ch, false)
			}
			if ch.busy != "" {
				ui.Text(c, ch.busy).FontSize(12).TextColor(t.TextMuted).SingleLine()
			}
			ui.Spacer(c)
			label, tip := "Commit", fmt.Sprintf("Commit the %s staged (%s)", fileCount(n), keys)
			if n == 0 {
				label, tip = "Commit All", "Stage every change and commit it ("+keys+")"
			}
			b := ui.Button(c.Key("commit"), label).Tooltip(tip).FontSize(12).Padding(3, 12)
			ready := strings.TrimSpace(ch.message) != "" && ch.busy == ""
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
			if ch.against == compareCommit {
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
				if ch.busy != "" {
					box.Disabled(true)
				}
				if box.Changed() && ch.busy == "" {
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
	path := filepath.Join(ch.root, f.path)
	if ch.against == compareCommit && ch.busy == "" {
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
