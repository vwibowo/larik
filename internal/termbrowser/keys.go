package termbrowser

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// keyEvent is one Input.dispatchKeyEvent for Chrome.
type keyEvent struct {
	Type                  string `json:"type"` // keyDown, keyUp, char
	Key                   string `json:"key,omitempty"`
	Code                  string `json:"code,omitempty"`
	Text                  string `json:"text,omitempty"`
	WindowsVirtualKeyCode int    `json:"windowsVirtualKeyCode,omitempty"`
	Modifiers             int    `json:"modifiers,omitempty"`
}

// CDP modifier bits.
const (
	cdpAlt   = 1
	cdpCtrl  = 2
	cdpMeta  = 4
	cdpShift = 8
)

// namedKeys are the non-printing keys a page cares about, with their DOM
// key, code and Windows virtual key code.
var namedKeys = map[rune]struct {
	key, code string
	vk        int
}{
	tea.KeyEnter:     {"Enter", "Enter", 13},
	tea.KeyTab:       {"Tab", "Tab", 9},
	tea.KeyBackspace: {"Backspace", "Backspace", 8},
	tea.KeyEscape:    {"Escape", "Escape", 27},
	tea.KeyDelete:    {"Delete", "Delete", 46},
	tea.KeyUp:        {"ArrowUp", "ArrowUp", 38},
	tea.KeyDown:      {"ArrowDown", "ArrowDown", 40},
	tea.KeyLeft:      {"ArrowLeft", "ArrowLeft", 37},
	tea.KeyRight:     {"ArrowRight", "ArrowRight", 39},
	tea.KeyHome:      {"Home", "Home", 36},
	tea.KeyEnd:       {"End", "End", 35},
	tea.KeyPgUp:      {"PageUp", "PageUp", 33},
	tea.KeyPgDown:    {"PageDown", "PageDown", 34},
	tea.KeyInsert:    {"Insert", "Insert", 45},
	tea.KeyF1:        {"F1", "F1", 112},
	tea.KeyF2:        {"F2", "F2", 113},
	tea.KeyF3:        {"F3", "F3", 114},
	tea.KeyF4:        {"F4", "F4", 115},
	tea.KeyF5:        {"F5", "F5", 116},
	tea.KeyF6:        {"F6", "F6", 117},
	tea.KeyF7:        {"F7", "F7", 118},
	tea.KeyF8:        {"F8", "F8", 119},
	tea.KeyF9:        {"F9", "F9", 120},
	tea.KeyF10:       {"F10", "F10", 121},
	tea.KeyF11:       {"F11", "F11", 122},
	tea.KeyF12:       {"F12", "F12", 123},
}

func modifiers(m tea.KeyMod) int {
	out := 0
	if m.Contains(tea.ModAlt) {
		out |= cdpAlt
	}
	if m.Contains(tea.ModCtrl) {
		out |= cdpCtrl
	}
	if m.Contains(tea.ModMeta) || m.Contains(tea.ModSuper) {
		out |= cdpMeta
	}
	if m.Contains(tea.ModShift) {
		out |= cdpShift
	}
	return out
}

// keyEvents turns a terminal key press into the events Chrome needs to
// see it typed: keyDown (carrying text for printable keys, so the page
// gets its input event) and keyUp. It returns nil for keys it can't map.
func keyEvents(k tea.Key) []keyEvent {
	mods := modifiers(k.Mod)
	if n, ok := namedKeys[k.Code]; ok {
		down := keyEvent{Type: "rawKeyDown", Key: n.key, Code: n.code, WindowsVirtualKeyCode: n.vk, Modifiers: mods}
		if k.Code == tea.KeyEnter && mods == 0 {
			down.Type, down.Text = "keyDown", "\r"
		}
		up := down
		up.Type, up.Text = "keyUp", ""
		return []keyEvent{down, up}
	}
	text := k.Text
	if text == "" && k.Code == tea.KeySpace {
		text = " "
	}
	r := k.Code
	if text == "" && (mods&(cdpCtrl|cdpMeta|cdpAlt)) != 0 && unicode.IsPrint(r) {
		// ctrl+a and the like: no text, but a key the page can handle.
		up := unicode.ToUpper(r)
		code := ""
		if up >= 'A' && up <= 'Z' {
			code = "Key" + string(up)
		} else if up >= '0' && up <= '9' {
			code = "Digit" + string(up)
		}
		down := keyEvent{Type: "rawKeyDown", Key: string(r), Code: code, WindowsVirtualKeyCode: int(up), Modifiers: mods}
		upEv := down
		upEv.Type = "keyUp"
		return []keyEvent{down, upEv}
	}
	if text == "" {
		return nil
	}
	down := keyEvent{Type: "keyDown", Key: text, Text: text, Modifiers: mods &^ cdpShift}
	if rs := []rune(text); len(rs) == 1 {
		up := unicode.ToUpper(rs[0])
		if up >= 'A' && up <= 'Z' {
			down.Code, down.WindowsVirtualKeyCode = "Key"+string(up), int(up)
		} else if up >= '0' && up <= '9' {
			down.Code, down.WindowsVirtualKeyCode = "Digit"+string(up), int(up)
		} else if up == ' ' {
			down.Code, down.WindowsVirtualKeyCode = "Space", 32
		}
	}
	upEv := down
	upEv.Type, upEv.Text = "keyUp", ""
	return []keyEvent{down, upEv}
}
