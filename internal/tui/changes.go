package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"larik/internal/checkpoint"
	"larik/internal/textutil"
)

// changesPicker is the /diff panel: the files changed, turn by turn. Rows
// with a header are titles; the rest are files.
type changesPicker struct {
	rows    []changeRow
	sel     int
	top     int
	visible int
}

type changeRow struct {
	header string
	group  int
	change checkpoint.Change
	hunks  []textutil.Hunk
}

// changeRows flattens the sets for the panel.
func (m *model) changeRows() []changeRow {
	var rows []changeRow
	for _, set := range m.agent.Changes() {
		title := "turn"
		if set.Owner != "" {
			title = set.Owner
		}
		rows = append(rows, changeRow{header: fmt.Sprintf("%s at %s · %s", title, set.Time.Format("15:04"), textutil.Plural(len(set.Changes), "file"))})
		for _, c := range set.Changes {
			r := changeRow{group: set.Group, change: c}
			if !c.Large && !c.Binary {
				r.hunks = textutil.Hunks(string(c.Before), string(c.After))
			}
			rows = append(rows, r)
		}
	}
	return rows
}

func (m *model) openChanges() tea.Cmd {
	rows := m.changeRows()
	if len(rows) == 0 {
		return m.showToast("No file changes in this session")
	}
	m.changesPick = &changesPicker{rows: rows, sel: 1}
	return nil
}

func (p *changesPicker) move(by int) {
	for i := p.sel + by; i >= 0 && i < len(p.rows); i += by {
		if p.rows[i].header == "" {
			p.sel = i
			return
		}
	}
}

func (m *model) handleChangesKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.changesPick
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.changesPick = nil
	case "up", "k":
		p.move(-1)
	case "down", "j":
		p.move(1)
	case "enter", "space", " ":
		m.openChange(p.rows[p.sel])
	}
	return nil
}

func (m *model) openChange(r changeRow) {
	c := r.change
	m.diffView = &diffView{fileDiff: fileDiff{path: c.Path}, change: &c, group: r.group, hunks: r.hunks}
}

// changesView draws the panel to fit, keeping the selection in view.
func (m *model) changesView() string {
	p := m.changesPick
	w := max(m.width-6, 20)
	p.visible = max(m.availablePanelRows()-4, 1)
	if p.sel < p.top+1 { // +1 so a turn's title comes into view with its first file
		p.top = max(p.sel-1, 0)
	} else if p.sel >= p.top+p.visible {
		p.top = p.sel - p.visible + 1
	}
	lines := []string{m.st.accent.Render("Changes this session")}
	for i := p.top; i < min(p.top+p.visible, len(p.rows)); i++ {
		r := p.rows[i]
		if r.header != "" {
			lines = append(lines, m.st.dim.Render(r.header))
			continue
		}
		cursor, name := "  ", m.shortPaths(r.change.Path)
		if i == p.sel {
			cursor, name = m.st.accent.Render("› "), m.st.accent.Render(name)
		}
		lines = append(lines, spread(cursor+name, m.changeStat(r), w))
	}
	lines = append(lines, m.st.dim.Render("↑/↓ select · enter open · esc close"))
	return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
}

// changeStat sums up a file's change for its row.
func (m *model) changeStat(r changeRow) string {
	c := r.change
	note := ""
	switch {
	case !c.Existed:
		note = "new · "
	case !c.Exists:
		note = "deleted · "
	}
	switch {
	case c.Large:
		return m.st.dim.Render(note + "too large to compare")
	case c.Binary:
		return m.st.dim.Render(note + "binary")
	}
	added, removed := 0, 0
	for _, h := range r.hunks {
		added, removed = added+h.Added, removed+h.Removed
	}
	return m.st.dim.Render(note) + m.st.diffAdd.Render(fmt.Sprintf("+%d", added)) + " " + m.st.diffDel.Render(fmt.Sprintf("-%d", removed))
}

// changeRowsText lays out a file's hunks for the diff view and notes where
// each starts.
func (m *model) changeRowsText(d *diffView, w int) []string {
	c := d.change
	switch {
	case c.Large:
		return []string{m.st.dim.Render("Too large to compare; /undo can still restore it.")}
	case c.Binary:
		return []string{m.st.dim.Render("Binary file; /undo can still restore it.")}
	}
	var rows []string
	d.hunkRow = d.hunkRow[:0]
	for i, h := range d.hunks {
		d.hunkRow = append(d.hunkRow, len(rows))
		title := fmt.Sprintf("  hunk %d of %d · line %d · +%d -%d", i+1, len(d.hunks), h.NewStart+1, h.Added, h.Removed)
		style := m.st.dim
		if i == d.sel {
			title = "› " + title[2:]
			style = m.st.accent
		}
		rows = append(rows, style.Render(title))
		body := m.highlightLines(c.Path, strings.Split(h.Text, "\n"))
		rows = append(rows, strings.Split(lipgloss.Wrap(body, w, ""), "\n")...)
		if i < len(d.hunks)-1 {
			rows = append(rows, "")
		}
	}
	return rows
}

// selectHunk moves the selection and scrolls to it.
func (d *diffView) selectHunk(i int) {
	if i < 0 || i >= len(d.hunks) {
		return
	}
	d.sel, d.width = i, 0 // the marker moves, so draw again
	d.top = d.hunkRow[i]
}

// rejectHunk puts the selected hunk back as it was before the turn.
func (m *model) rejectHunk(d *diffView) tea.Cmd {
	if !m.idle() {
		return m.showToast("Wait for the current turn to finish")
	}
	c, h := d.change, d.hunks[d.sel]
	content := textutil.RevertHunk(string(c.Before), string(c.After), h)
	if err := m.agent.RejectChange(*c, []byte(content), !c.Existed && content == ""); err != nil {
		return m.showToast(err.Error())
	}
	sel := d.sel
	rows := m.changeRows()
	if m.changesPick != nil {
		m.changesPick.rows = rows
		m.changesPick.sel = min(m.changesPick.sel, len(rows)-1)
		if len(rows) == 0 {
			m.changesPick = nil
		} else if rows[m.changesPick.sel].header != "" {
			m.changesPick.sel = 0
			m.changesPick.move(1)
		}
	}
	m.diffView = nil
	for _, r := range rows {
		if r.header == "" && r.group == d.group && r.change.Path == c.Path {
			m.openChange(r)
			m.diffView.sel = min(sel, max(len(r.hunks)-1, 0))
			break
		}
	}
	return m.showToast("Hunk rejected")
}
