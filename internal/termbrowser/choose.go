package termbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/chromedp/cdproto/target"
)

// Headless Chrome draws no popup for a <select>: clicking one shows
// nothing. So a script in the tab stops the native popup and asks the
// viewer, which offers the options on its second row, one at a time:
//
//	▾ ‹ Indonesia › 102/250   ↑/↓ choose · type to find · enter set · esc cancel
//
// and sets the choice in the page, with the input and change events a
// user's choice fires.
//
// The script can't tell the user's click from an agent's (both are CDP
// input), so the viewer does: it offers the options only right after the
// user pressed on the page. An agent's click on a <select> is ignored
// here; its native popup would not have shown anyway, and agents pick
// options with browser_select.

// userClickWindow is how soon after the user's press a <select> asking
// counts as theirs.
const userClickWindow = time.Second

// selectHook catches a press on a single-choice <select> in any frame.
const selectHook = `(() => {
  if (window.__larikSelectHooked || typeof window.` + askBinding + ` !== 'function') return;
  window.__larikSelectHooked = true;
  const ask = window.` + askBinding + `;
  document.addEventListener('mousedown', (ev) => {
    const t = ev.composedPath ? ev.composedPath()[0] : ev.target;
    const el = t && t.closest ? t.closest('select') : null;
    if (!el || ev.button !== 0 || el.multiple || el.size > 1 || el.disabled) return;
    ev.preventDefault();
    el.focus();
    window.__larikSelect = el;
    ask(JSON.stringify({type: 'select', index: el.selectedIndex,
      options: Array.from(el.options).map((o) => ({label: o.label || o.text, disabled: o.disabled}))}));
  }, true);
})()`

type selectOption struct {
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

// choice is a <select> waiting for the user to pick an option.
type choice struct {
	session target.SessionID
	context int // the frame's execution context, where the answer goes
	options []selectOption
	index   int
	// typed is what the user typed to find an option, and when they last
	// typed: a pause starts a new search.
	typed   string
	typedAt time.Time
}

// openChoice starts choosing from the options a page's script sent.
func (v *viewer) openChoice(msg bindingMsg) {
	if time.Since(v.pressedAt) > userClickWindow {
		return // an agent's click, not the user's
	}
	var p struct {
		Index   int            `json:"index"`
		Options []selectOption `json:"options"`
	}
	if json.Unmarshal([]byte(msg.payload), &p) != nil || len(p.Options) == 0 {
		return
	}
	v.choosing = &choice{session: msg.session, context: msg.context, options: p.Options, index: min(max(p.Index, 0), len(p.Options)-1)}
	v.editing = false
}

// move steps to the next enabled option by step, staying put at the ends.
func (c *choice) move(step int) {
	for i := c.index + step; i >= 0 && i < len(c.options); i += step {
		if !c.options[i].Disabled {
			c.index = i
			return
		}
	}
}

// find types s and moves to the first option, from the current one, whose
// label starts with all that was typed.
func (c *choice) find(s string, now time.Time) {
	if now.Sub(c.typedAt) > time.Second {
		c.typed = ""
	}
	c.typed += strings.ToLower(s)
	c.typedAt = now
	start := c.index
	if len([]rune(c.typed)) == 1 {
		start++ // a single letter steps to the next match
	}
	for k := range len(c.options) {
		i := (start + k) % len(c.options)
		o := c.options[i]
		if !o.Disabled && strings.HasPrefix(strings.ToLower(strings.TrimSpace(o.Label)), c.typed) {
			c.index = i
			return
		}
	}
}

// chooseKey handles a key while an option is being chosen.
func (v *viewer) chooseKey(msg tea.KeyPressMsg) tea.Cmd {
	c, k := v.choosing, msg.Key()
	switch k.Code {
	case tea.KeyEscape:
		v.choosing = nil
	case tea.KeyEnter, tea.KeySpace:
		return v.applyChoice()
	case tea.KeyUp, tea.KeyLeft:
		c.move(-1)
	case tea.KeyDown, tea.KeyRight, tea.KeyTab:
		c.move(+1)
	case tea.KeyPgUp:
		c.move(-min(10, c.index))
	case tea.KeyPgDown:
		c.move(min(10, len(c.options)-1-c.index))
	case tea.KeyHome:
		c.index = -1
		c.move(+1)
	case tea.KeyEnd:
		c.index = len(c.options)
		c.move(-1)
	default:
		if k.Text != "" {
			c.find(k.Text, time.Now())
		}
	}
	return nil
}

// applyChoice sets the chosen option in the page.
func (v *viewer) applyChoice() tea.Cmd {
	c, conn := v.choosing, v.conn
	v.choosing = nil
	if c == nil || conn == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.call(ctx, c.session, "Runtime.evaluate", map[string]any{
			"expression": fmt.Sprintf(`(() => { const s = window.__larikSelect; if (!s) return;
				s.selectedIndex = %d;
				s.dispatchEvent(new Event('input', {bubbles: true}));
				s.dispatchEvent(new Event('change', {bubbles: true})); })()`, c.index),
			"contextId": c.context,
		}, nil)
		return nil
	}
}

// choiceLine is the second row while choosing, and the columns of its
// ‹ and › buttons.
func (v *viewer) choiceLine() (line string, prev, next int) {
	c := v.choosing
	label := strings.TrimSpace(c.options[c.index].Label)
	if label == "" {
		label = "(empty)"
	}
	label = ansi.Truncate(label, max(v.cols-24, 8), "…")
	prev = 3 // " ▾ ‹"
	next = prev + 2 + ansi.StringWidth(label) + 1
	line = " ▾ ‹ \x1b[0;7m " + label + " \x1b[0m ›" +
		fmt.Sprintf("\x1b[2m %d/%d  ↑/↓ choose · type to find · enter set · esc cancel\x1b[0m", c.index+1, len(c.options))
	return line, prev, next + 2 // the label's padding
}

// clickChoice steps with ‹ and ›, and sets the option when its label is
// clicked.
func (v *viewer) clickChoice(x int) tea.Cmd {
	_, prev, next := v.choiceLine()
	switch {
	case x == prev:
		v.choosing.move(-1)
	case x == next:
		v.choosing.move(+1)
	case x > prev && x < next:
		return v.applyChoice()
	}
	return nil
}
