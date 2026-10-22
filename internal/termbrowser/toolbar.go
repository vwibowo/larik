package termbrowser

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/chromedp/cdproto/target"
)

// The pane's top row works like a browser's toolbar: back, forward, reload
// (stop while loading) and an address field, all clickable. The second row
// shows the page's title and the shortcuts.
//
//	 ←  →  ↻  │ https://example.com/                     ● explore is driving ♪
//	 Example Domain                       alt+←/→ back/forward · ctrl+l address

// Toolbar cells, 0-based columns of the top row.
const (
	backStart, backEnd       = 0, 3 // " ← "
	forwardStart, forwardEnd = 3, 6 // " → "
	reloadStart, reloadEnd   = 6, 9 // " ↻ "
	addressStart             = 11   // after "│ "
)

// toolbarHit is what a click on the top row lands on.
type toolbarHit int

const (
	hitNone toolbarHit = iota
	hitBack
	hitForward
	hitReload
	hitAddress
)

func (v *viewer) hitToolbar(x int) toolbarHit {
	switch {
	case x >= backStart && x < backEnd:
		return hitBack
	case x >= forwardStart && x < forwardEnd:
		return hitForward
	case x >= reloadStart && x < reloadEnd:
		return hitReload
	case x >= addressStart-1 && x < v.addressEnd():
		return hitAddress
	}
	return hitNone
}

// badges are the notes at the right end of the toolbar.
func (v *viewer) badges() string {
	b := ""
	if v.state.Driving != "" {
		b += " ● " + v.state.Driving + " is driving"
	}
	if v.state.Sound {
		b += " ♪"
	}
	if v.zoom != 0 && v.zoom != 100 {
		b += fmt.Sprintf(" %d%%", v.zoom)
	}
	if b != "" {
		b += " "
	}
	return b
}

// addressEnd is the column just past the address field.
func (v *viewer) addressEnd() int {
	return max(v.cols-ansi.StringWidth(v.badges())-1, addressStart+8)
}

// SGR fragments for the toolbar.
const (
	sgrBar      = "\x1b[0;7m"   // the bar: reversed
	sgrDisabled = "\x1b[0;7;2m" // a button with nowhere to go
	sgrField    = "\x1b[0m"     // the address field: like an input box
	sgrCaret    = "\x1b[0;7m"
	sgrReset    = "\x1b[0m"
)

// toolbarLine renders the top row.
func (v *viewer) toolbarLine() string {
	var b strings.Builder
	button := func(label string, enabled bool) {
		if enabled {
			b.WriteString(sgrBar + label)
		} else {
			b.WriteString(sgrDisabled + label)
		}
	}
	button(" ← ", v.tab != "" && v.hist.index > 0)
	button(" → ", v.tab != "" && v.hist.index < len(v.hist.ids)-1)
	if v.loading {
		button(" ✕ ", true)
	} else {
		button(" ↻ ", v.tab != "")
	}
	b.WriteString(sgrBar + " │")
	fieldW := v.addressEnd() - addressStart
	b.WriteString(sgrField + " ")
	b.WriteString(v.addressField(fieldW - 1))
	b.WriteString(sgrBar + v.badges())
	if pad := v.cols - addressStart - fieldW - ansi.StringWidth(v.badges()); pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString(sgrReset)
	return b.String()
}

// addressField renders the address, w cells wide: the page's URL, or the
// text being edited with its caret (all of it highlighted while selected).
func (v *viewer) addressField(w int) string {
	if w <= 0 {
		return ""
	}
	if !v.editing {
		url := v.targets[v.tab].URL
		return padRight(ansi.Truncate(url, w, "…"), w)
	}
	text, cursor := v.address.text, v.address.cursor
	start := v.address.scroll(w)
	var b strings.Builder
	n := 0 // cells written
	for i := start; i < len(text); i++ {
		rw := runeWidth(text[i])
		if n+rw > w {
			break
		}
		switch {
		case v.address.selected:
			b.WriteString(sgrCaret + string(text[i]) + sgrField)
		case i == cursor:
			b.WriteString(sgrCaret + string(text[i]) + sgrField)
		default:
			b.WriteRune(text[i])
		}
		n += rw
	}
	if cursor == len(text) && !v.address.selected && n < w {
		b.WriteString(sgrCaret + " " + sgrField)
		n++
	}
	b.WriteString(strings.Repeat(" ", max(w-n, 0)))
	return b.String()
}

// runeWidth is how many cells r takes: two for wide characters.
func runeWidth(r rune) int { return max(ansi.StringWidth(string(r)), 1) }

// scroll is the index of the first rune shown in a field w cells wide: as
// much as fits before the caret, with the caret's own cell.
func (a *addressBar) scroll(w int) int {
	start, used := a.cursor, 1
	for start > 0 && used+runeWidth(a.text[start-1]) <= w {
		start--
		used += runeWidth(a.text[start])
	}
	return start
}

// at is the index of the rune at cell col of a field w cells wide.
func (a *addressBar) at(col, w int) int {
	i, n := a.scroll(w), 0
	for i < len(a.text) && n+runeWidth(a.text[i]) <= col {
		n += runeWidth(a.text[i])
		i++
	}
	return i
}

// addressBar is the text being typed into the address field.
type addressBar struct {
	text     []rune
	cursor   int
	selected bool // all of it, as after clicking in: typing replaces it
}

func (a *addressBar) begin(url string) {
	a.text, a.cursor, a.selected = []rune(url), len([]rune(url)), true
}

// insert types s at the caret, replacing a selection.
func (a *addressBar) insert(s string) {
	s = strings.NewReplacer("\n", "", "\r", "").Replace(s)
	if a.selected {
		a.text, a.cursor, a.selected = nil, 0, false
	}
	rs := []rune(s)
	a.text = append(a.text[:a.cursor], append(rs, a.text[a.cursor:]...)...)
	a.cursor += len(rs)
}

// key edits the text; it reports whether the key was an editing key.
func (a *addressBar) key(k tea.Key) bool {
	sel := a.selected
	a.selected = false
	switch k.Code {
	case tea.KeyLeft:
		if sel {
			a.cursor = 0
		} else if a.cursor > 0 {
			a.cursor--
		}
	case tea.KeyRight:
		if !sel && a.cursor < len(a.text) {
			a.cursor++
		}
	case tea.KeyHome:
		a.cursor = 0
	case tea.KeyEnd:
		a.cursor = len(a.text)
	case tea.KeyBackspace:
		switch {
		case sel:
			a.text, a.cursor = nil, 0
		case a.cursor > 0:
			a.text = append(a.text[:a.cursor-1], a.text[a.cursor:]...)
			a.cursor--
		}
	case tea.KeyDelete:
		switch {
		case sel:
			a.text, a.cursor = nil, 0
		case a.cursor < len(a.text):
			a.text = append(a.text[:a.cursor], a.text[a.cursor+1:]...)
		}
	default:
		if k.Mod.Contains(tea.ModCtrl) && (k.Code == 'a' || k.Code == 'e') {
			a.cursor = map[rune]int{'a': 0, 'e': len(a.text)}[k.Code]
			return true
		}
		if k.Text == "" {
			a.selected = sel
			return false
		}
		a.selected = sel
		a.insert(k.Text)
	}
	return true
}

// history is the shown tab's back/forward list.
type history struct {
	index int   // the current entry
	ids   []int // entry ids, oldest first
}

type historyMsg struct {
	session target.SessionID
	hist    history
}

// fetchHistory reads the tab's navigation history for the buttons.
func (v *viewer) fetchHistory() tea.Cmd {
	conn, session := v.conn, v.session
	if conn == nil || session == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID int `json:"id"`
			} `json:"entries"`
		}
		if conn.call(ctx, session, "Page.getNavigationHistory", nil, &res) != nil {
			return nil
		}
		h := history{index: res.CurrentIndex}
		for _, e := range res.Entries {
			h.ids = append(h.ids, e.ID)
		}
		return historyMsg{session, h}
	}
}

// goHistory moves back (-1) or forward (+1), if there is somewhere to go.
// It goes by entry id rather than with a script's history.back, which a
// page can intercept.
func (v *viewer) goHistory(step int) tea.Cmd {
	i := v.hist.index + step
	if v.tab == "" || i < 0 || i >= len(v.hist.ids) {
		return nil
	}
	v.hist.index = i // the buttons answer at once; the page catches up
	return v.input("Page.navigateToHistoryEntry", map[string]any{"entryId": v.hist.ids[i]})
}

// reloadOrStop is the third button: stop while a page loads, else reload.
func (v *viewer) reloadOrStop() tea.Cmd {
	if v.loading {
		return v.input("Page.stopLoading", nil)
	}
	return v.input("Page.reload", nil)
}

// clickToolbar acts on a click in the top row.
func (v *viewer) clickToolbar(x int) tea.Cmd {
	switch v.hitToolbar(x) {
	case hitBack:
		v.editing = false
		return v.goHistory(-1)
	case hitForward:
		v.editing = false
		return v.goHistory(+1)
	case hitReload:
		v.editing = false
		return v.reloadOrStop()
	case hitAddress:
		if v.editing {
			// A second click places the caret where it lands.
			fieldW := v.addressEnd() - addressStart - 1
			v.address.selected = false
			v.address.cursor = v.address.at(max(x-addressStart, 0), fieldW)
			return nil
		}
		v.editing = true
		v.address.begin(v.targets[v.tab].URL)
	}
	return nil
}
