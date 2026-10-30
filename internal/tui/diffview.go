package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
	"larik/internal/checkpoint"
	"larik/internal/textutil"
	"larik/internal/tools"
)

// maxFileDiffs is how many of the session's edits the diff view keeps.
const maxFileDiffs = 100

// maxComparedFile is the largest existing file a write's approval compares
// with the new content.
const maxComparedFile = 2 << 20

// fileDiff is one change to a file, in the "+ " / "- " form of textutil.Diff.
type fileDiff struct{ path, diff, agent string }

// diffView is the full-screen view of one change: an edit the model is asking
// to make (perm is set), one from this session's list (at is its index), or
// a file from the /diff panel, shown hunk by hunk (change is set).
type diffView struct {
	fileDiff
	perm    *agent.Event
	at      int
	top     int
	visible int
	width   int // the width rows was wrapped to
	rows    []string

	change  *checkpoint.Change
	group   int
	hunks   []textutil.Hunk
	hunkRow []int // where each hunk starts in rows
	sel     int   // the selected hunk
	armed   bool  // r was pressed once
}

// recordDiff keeps a finished edit for the diff view.
func (m *model) recordDiff(e agent.Event) {
	if e.IsError {
		return
	}
	var in struct{ Path, Content string }
	_ = json.Unmarshal(e.Input, &in)
	diff := e.Display
	switch e.ToolName {
	case "write":
		if diff == "" {
			diff = prefixLines(strings.TrimSuffix(in.Content, "\n"), "+ ")
		}
	case "edit", tools.MultiEditToolName:
	default:
		return
	}
	if diff == "" {
		return
	}
	m.diffs = append(m.diffs, fileDiff{path: in.Path, diff: diff, agent: e.Agent})
	if len(m.diffs) > maxFileDiffs {
		m.diffs = m.diffs[1:]
		if m.diffView != nil && m.diffView.perm == nil && m.diffView.at > 0 {
			m.diffView.at--
		}
	}
}

// diffShown is the open diff view, unless an approval has taken the screen.
// A view opened from an approval closes with it.
func (m *model) diffShown() *diffView {
	d := m.diffView
	switch {
	case d == nil:
		return nil
	case d.perm != nil && d.perm != m.perm:
		m.diffView = nil
		return nil
	case d.perm == nil && m.perm != nil:
		return nil
	}
	return d
}

// openLastDiff shows the session's latest edit.
func (m *model) openLastDiff() tea.Cmd {
	if len(m.diffs) == 0 {
		return m.showToast("No edits to show yet")
	}
	m.showDiffAt(len(m.diffs) - 1)
	return nil
}

func (m *model) showDiffAt(i int) {
	m.diffView = &diffView{fileDiff: m.diffs[i], at: i}
}

// openPermDiff shows the whole change the open approval asks about.
func (m *model) openPermDiff() {
	if path, diff, _, ok := m.permDiff(m.perm); ok {
		m.diffView = &diffView{fileDiff: fileDiff{path: path, diff: diff, agent: m.perm.Agent}, perm: m.perm}
	}
}

// permDiff is the change an approval asks about and how many of its lines the
// prompt shows. ok is false for tools that don't change a file's text.
func (m *model) permDiff(e *agent.Event) (path, diff string, limit int, ok bool) {
	var in map[string]any
	_ = json.Unmarshal(e.Input, &in)
	path = str(in["path"])
	switch e.ToolName {
	case "write":
		content := str(in["content"])
		if fi, err := os.Stat(m.permPath(e, path)); err == nil && fi.Size() <= maxComparedFile {
			if old, err := os.ReadFile(m.permPath(e, path)); err == nil && !bytes.Contains(old, []byte{0}) {
				return path, textutil.Diff(string(old), content), 12, true
			}
		}
		return path, prefixLines(strings.TrimSuffix(content, "\n"), "+ "), 12, true
	case "edit":
		return path, textutil.Diff(str(in["old_string"]), str(in["new_string"])), 16, true
	case tools.MultiEditToolName:
		edits, _ := in["edits"].([]any)
		var parts []string
		for _, e := range edits {
			ed, _ := e.(map[string]any)
			parts = append(parts, textutil.Diff(str(ed["old_string"]), str(ed["new_string"])))
		}
		return path, strings.Join(parts, "\n  ⋯\n"), 24, true
	}
	return "", "", 0, false
}

// previewCut reports whether highlightDiff would hide part of diff, by lines
// past limit or by clipping a long line.
func (m *model) previewCut(diff string, limit int) bool {
	lines := strings.Split(diff, "\n")
	if len(lines) > limit {
		return true
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > m.diffWidth() {
			return true
		}
	}
	return false
}

func (m *model) handleDiffKey(d *diffView, msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.keys.is(k, actViewDiff) {
		k = "esc"
	}
	if d.change != nil {
		armed := d.armed
		d.armed = false
		switch k {
		case "n", "tab":
			d.selectHunk(d.sel + 1)
			return nil
		case "p", "shift+tab":
			d.selectHunk(d.sel - 1)
			return nil
		case "r":
			switch {
			case len(d.hunks) == 0:
			case !d.change.Latest:
				return m.showToast("A later turn changed this file; reject that change first")
			case armed:
				return m.rejectHunk(d)
			default:
				d.armed = true
			}
			return nil
		}
	}
	switch k {
	case "esc", "q", "d":
		m.diffView = nil
	case "ctrl+c":
		// Still the way out of everything underneath.
		m.diffView = nil
		if d.perm != nil {
			return m.handlePermissionKey(msg)
		}
		if m.running {
			m.interrupt()
		}
	case "up", "k":
		d.scroll(-1)
	case "down", "j":
		d.scroll(1)
	case "pgup", "b":
		d.scroll(-max(d.visible-1, 1))
	case "pgdown", "space", " ":
		d.scroll(max(d.visible-1, 1))
	case "home", "g":
		d.top = 0
	case "end", "G":
		d.scroll(len(d.rows))
	case "left", "h":
		if d.perm == nil && d.change == nil && d.at > 0 {
			m.showDiffAt(d.at - 1)
		}
	case "right", "l":
		if d.perm == nil && d.change == nil && d.at < len(m.diffs)-1 {
			m.showDiffAt(d.at + 1)
		}
	}
	return nil
}

func (d *diffView) scroll(by int) {
	d.top = min(max(d.top+by, 0), max(len(d.rows)-d.visible, 0))
}

// diffViewPanel draws the view to fit the panel: the title and keys stay put
// and the diff scrolls between them. Lines wrap, so nothing is hidden.
func (m *model) diffViewPanel(d *diffView) string {
	w := max(m.width-6, 20)
	if d.width != w {
		d.width = w
		if d.change != nil {
			d.rows = m.changeRowsText(d, w)
		} else {
			rendered := m.highlightLines(d.path, strings.Split(d.diff, "\n"))
			d.rows = strings.Split(lipgloss.Wrap(rendered, w, ""), "\n")
		}
	}
	d.visible = max(m.availablePanelRows()-4, 1) // the border and two fixed rows
	d.scroll(0)
	end := min(d.top+d.visible, len(d.rows))

	where := fmt.Sprintf("%d-%d of %s", d.top+1, end, textutil.Plural(len(d.rows), "row"))
	hint := "↑/↓ scroll · pgup/pgdn page · esc back"
	switch {
	case d.change != nil:
		hint = "↑/↓ scroll · esc back"
		if len(d.hunks) > 0 {
			hint = "↑/↓ scroll · n/p hunks · r reject hunk · esc back"
		}
		if d.armed {
			hint = m.st.warn.Render("press r again to put this hunk back as it was · any other key cancels")
		}
	case d.perm == nil:
		where = fmt.Sprintf("edit %d of %d · %s", d.at+1, len(m.diffs), where)
		hint = "↑/↓ scroll · ←/→ other edits · esc close"
	}
	if d.agent != "" {
		where = "subagent " + d.agent + " · " + where
	}
	lines := []string{spread(m.st.accent.Render(m.shortPaths(d.path)), m.st.dim.Render(where), w)}
	lines = append(lines, d.rows[d.top:end]...)
	lines = append(lines, m.st.dim.Render(hint))
	return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
}
