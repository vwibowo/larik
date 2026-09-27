package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// pickItem is one row of a picker.
type pickItem struct {
	section  string // header shown above the first row of each section
	label    string
	detail   string // dim text after the label
	note     string // right-hand text, e.g. "✓ current"
	noteOK   bool   // render note in the ok color
	warn     bool   // render the label in the warning color
	disabled bool   // shown but can't be chosen
	keep     bool   // stays visible whatever the filter
	value    any
}

// picker is a filterable, scrolling list driven by the keyboard. Typing
// filters; ↑/↓ move over enabled rows; enter chooses.
type picker struct {
	items      []pickItem
	filterable bool
	filter     string
	cursor     int // index into visible()
	offset     int // first rendered line, for scrolling
	height     int // max rows rendered; 0 means all
	// extra returns rows added for the current filter, such as
	// "use this id" when nothing matches exactly.
	extra func(filter string) []pickItem
}

func (p *picker) visible() []pickItem {
	f := strings.ToLower(strings.TrimSpace(p.filter))
	var out []pickItem
	for _, it := range p.items {
		if f == "" || it.keep || strings.Contains(strings.ToLower(it.label+" "+it.section), f) {
			out = append(out, it)
		}
	}
	if p.extra != nil {
		out = append(out, p.extra(p.filter)...)
	}
	return out
}

func (p *picker) selected() (pickItem, bool) {
	vis := p.visible()
	if p.cursor < 0 || p.cursor >= len(vis) || vis[p.cursor].disabled {
		return pickItem{}, false
	}
	return vis[p.cursor], true
}

// selectWhere moves the cursor to the first enabled row matching fn.
func (p *picker) selectWhere(fn func(pickItem) bool) {
	for i, it := range p.visible() {
		if !it.disabled && fn(it) {
			p.cursor = i
			return
		}
	}
	p.home()
}

// home moves to the first enabled row.
func (p *picker) home() {
	p.cursor = -1
	p.move(1)
}

// move steps the cursor by dir, skipping disabled rows; it stays put at
// the ends.
func (p *picker) move(dir int) {
	vis := p.visible()
	for i := p.cursor + dir; i >= 0 && i < len(vis); i += dir {
		if !vis[i].disabled {
			p.cursor = i
			return
		}
	}
	if p.cursor < 0 || p.cursor >= len(vis) {
		p.cursor = 0
	}
}

// handleKey updates the picker. chosen is true when enter picked a row.
func (p *picker) handleKey(msg tea.KeyPressMsg) (chosen bool) {
	switch msg.String() {
	case "up", "ctrl+p":
		p.move(-1)
	case "down", "ctrl+n", "tab":
		p.move(1)
	case "pgup":
		for range max(p.height-1, 1) {
			p.move(-1)
		}
	case "pgdown":
		for range max(p.height-1, 1) {
			p.move(1)
		}
	case "enter":
		_, ok := p.selected()
		return ok
	case "backspace":
		if p.filterable && p.filter != "" {
			r := []rune(p.filter)
			p.filter = string(r[:len(r)-1])
			p.home()
		}
	case "ctrl+u":
		if p.filterable {
			p.filter = ""
			p.home()
		}
	default:
		if p.filterable && msg.Text != "" && msg.Mod&^tea.ModShift == 0 {
			p.filter += msg.Text
			p.home()
		}
	}
	return false
}

// view renders the rows to fit width.
func (p *picker) view(st styles, width int) string {
	vis := p.visible()
	if len(vis) == 0 {
		return st.dim.Render("  no matches")
	}
	labelW, detailW := 0, 0
	for _, it := range vis {
		labelW = max(labelW, lipgloss.Width(it.label))
		detailW = max(detailW, lipgloss.Width(it.detail))
	}
	labelW = min(labelW, max(width/2-4, 12))
	detailW = min(detailW, max(width-labelW-16, 0))

	var lines []string
	cursorLine, section := 0, ""
	for i, it := range vis {
		if it.section != section {
			section = it.section
			if section != "" {
				lines = append(lines, st.dim.Render(strings.ToUpper(section)))
			}
		}
		label := pad(ansi.Truncate(it.label, labelW, "…"), labelW)
		detail := pad(ansi.Truncate(it.detail, detailW, "…"), detailW)
		note := it.note
		switch {
		case it.disabled:
			label, detail = st.dim.Render(label), st.dim.Render(detail)
			note = st.dim.Render(note)
		case it.warn:
			label, detail = st.warn.Render(label), st.warn.Render(detail)
		case i == p.cursor:
			label = st.accent.Render(label)
			detail = st.dim.Render(detail)
		default:
			detail = st.dim.Render(detail)
		}
		if !it.disabled {
			if it.noteOK {
				note = st.ok.Render(note)
			} else {
				note = st.dim.Render(note)
			}
		}
		cursor := "  "
		if i == p.cursor {
			cursor = st.accent.Render("› ")
			cursorLine = len(lines)
		}
		lines = append(lines, ansi.Truncate(cursor+label+"  "+detail+"  "+note, width, "…"))
	}

	if p.height <= 0 || len(lines) <= p.height {
		p.offset = 0
		return strings.Join(lines, "\n")
	}
	h := p.height
	if cursorLine < p.offset+1 { // keep a line of context, often the section header
		p.offset = max(cursorLine-1, 0)
	}
	if cursorLine > p.offset+h-2 { // the last line may become "↓ more"
		p.offset = cursorLine - h + 2
	}
	p.offset = min(p.offset, len(lines)-h)
	window := append([]string(nil), lines[p.offset:p.offset+h]...)
	if p.offset > 0 {
		window[0] = st.dim.Render("  ↑ more")
	}
	if p.offset+h < len(lines) {
		window[h-1] = st.dim.Render("  ↓ more")
	}
	return strings.Join(window, "\n")
}

// filterLine shows what has been typed into a filterable picker.
func (p *picker) filterLine(st styles, placeholder string) string {
	if p.filter == "" {
		return "› " + st.dim.Render(placeholder)
	}
	return "› " + p.filter + st.accent.Render("▏")
}

func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}
