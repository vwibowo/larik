package desktop

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	changespane "larik/internal/desktop/panes/changes"
)

// Hunks: what changed in a file can be staged, unstaged or discarded a
// change at a time, or a few lines of one, as GitHub Desktop and Xcode
// do, to keep some of what an agent did and not the rest. A file with
// changes both staged and not shows two diffs, as git keeps them: what
// isn't staged, the index to the working tree, and what is, HEAD to the
// index. The change under the pointer has buttons, Stage and Discard or
// Unstage, and with lines chosen by their numbers they take only those.
//
// They don't go through git apply and a patch: the pane has the texts on
// both sides of the change, so it makes the text the index, or the file,
// is to have, and puts it there (hash-object and update-index, or a
// write), having checked that what's there is still what the change was
// read from. That leaves no context lines to match, or ends of files
// without newlines to mark.

// diffPart is one diff of a file: what isn't staged, or what is, or, for
// a file whose changes can't be taken a hunk at a time, all of it.
type diffPart struct {
	staged   bool // HEAD to the index; else the index to the working tree
	old, new string
	diff     *editor.DiffState
}

// hunkAction is what a hunk's button does.
type hunkAction int

const (
	stageHunk hunkAction = iota
	unstageHunk
	discardHunk
)

func (act hunkAction) label(lines bool) string {
	what := [...]string{"Stage", "Unstage", "Discard"}[act]
	if lines {
		what += " Lines"
	}
	return what
}

// applyHunk is the text with hunk h of the change from old to new made,
// or the lines of it pick picks (every line when nil): forward, on old,
// as staging makes it in the index; or backward, on new, as unstaging
// and discarding take it out of the index or the file. Lines are split
// as the diff viewer splits them, so h's lines are theirs: on newlines,
// a newline at the end making an empty last line, and an empty text
// none. CRLF line endings stay.
func applyHunk(old, new string, h editor.DiffHunk, pick func(editor.DiffSide, int) bool, forward bool) (string, error) {
	return changespane.ApplyHunk(old, new, h, pick, forward)
}

// linesMatch reports whether the viewer's buffer has text's lines, as
// diffLines splits them: a line ending with a lone CR, say, it doesn't.
func linesMatch(b *editor.Buffer, text string) bool { return changespane.LinesMatch(b, text) }

// diffLines splits text as the diff viewer does.
func diffLines(text string) []string { return changespane.DiffLines(text) }

// hunkPick is what the part's chosen lines pick of hunk h: nil, the
// whole hunk, when none of them is in it.
func hunkPick(d *editor.DiffState, h editor.DiffHunk) func(editor.DiffSide, int) bool {
	return changespane.HunkPick(d, h)
}

// hunkActions draws the buttons on the change under the pointer: Stage
// and Discard on what isn't staged, Unstage on what is, taking the lines
// chosen in it when there are.
func (a *App) hunkActions(ch *changes, f *fileChange, p *diffPart) func(c *ui.Context, bar ui.Element, h *editor.DiffHunkActions) {
	return func(c *ui.Context, bar ui.Element, h *editor.DiffHunkActions) {
		t := c.Theme()
		lines := hunkPick(p.diff, h.Hunk) != nil
		acts := []hunkAction{stageHunk, discardHunk}
		if p.staged {
			acts = []hunkAction{unstageHunk}
		}
		bar.Row().Gap(4)
		bar.Children(func() {
			for _, act := range acts {
				label := act.label(lines)
				btn := ui.ButtonBase(c).Key(label).Padding(0, 8).Height(18).Radius(4).Background(t.Surface).Border(1, t.Border).
					Label(label + " " + f.path)
				btn.Children(func() { ui.Text(c, label).FontSize(11) })
				if ch.Busy != "" {
					btn.Disabled(true)
				}
				if btn.Clicked() && ch.Busy == "" {
					a.hunkDo(ch, f, p, h.Hunk, act)
				}
			}
		})
	}
}

// hunkDo stages, unstages or discards hunk h of the part's change, or
// the lines of it chosen, asking first before discarding.
func (a *App) hunkDo(ch *changes, f *fileChange, p *diffPart, h editor.DiffHunk, act hunkAction) {
	pick := hunkPick(p.diff, h)
	if !linesMatch(p.diff.Old, p.old) || !linesMatch(p.diff.New, p.new) {
		a.notice = "Can't take that change: the diff and the file don't line up. Stage the whole file instead."
		return
	}
	text, err := applyHunk(p.old, p.new, h, pick, act == stageHunk)
	if err != nil {
		a.notice = err.Error()
		return
	}
	// What the index, or the file, has now must be what the change was
	// read from: an agent may have moved on.
	was := p.old
	if act != stageHunk {
		was = p.new
	}
	path, added := f.path, f.code[0] == 'A'
	do := func() {
		p.diff.SetSelectedLines(editor.DiffLineRange{}, false)
		busy := map[hunkAction]string{stageHunk: "Staging…", unstageHunk: "Unstaging…", discardHunk: "Discarding…"}[act]
		a.changesDo(ch, busy, func(ctx context.Context, root string) (string, error) {
			if isRemote(root) {
				op := "set-index"
				if act == discardHunk {
					op = "write-kept"
				}
				if act == unstageHunk && added && text == "" {
					op = "unstage"
				}
				_, err := a.remoteProjectCall(remoteProjectRequest{Op: op, Dir: root, Path: path, Was: was, Text: text})
				return "", err
			}
			if act == discardHunk {
				return "", writeKept(filepath.Join(root, path), was, text)
			}
			if act == unstageHunk && added && text == "" {
				return "", unstage(ctx, root, path) // nothing of the new file left staged
			}
			return "", setIndex(ctx, root, path, was, text)
		})
	}
	if act != discardHunk {
		do()
		return
	}
	what := "this change"
	if pick != nil {
		what = "these lines"
	}
	a.confirmAsk(fmt.Sprintf("Discard %s to “%s”?", what, filepath.Base(f.path)),
		"They go back to how they are staged, or at the last commit. This can't be undone.", "Discard", do)
}

// setIndex puts text in the index as path's, if the index has was for it
// now, keeping its mode.
func setIndex(ctx context.Context, root, path, was, text string) error {
	return changespane.SetIndex(ctx, root, path, was, text)
}

// writeKept writes text to the file at path, keeping its mode, if it has
// was now.
func writeKept(path, was, text string) error { return changespane.WriteKept(path, was, text) }
