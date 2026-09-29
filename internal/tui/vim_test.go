package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// vimRun types keys in normal mode on text, where | marks the cursor,
// and returns the result marked the same way. "<esc>" leaves insert mode
// and other text typed in insert mode is inserted.
func vimRun(v *vimEditor, text, keys string) string {
	c := strings.Index(text, "|")
	b := vimBuf{r: []rune(strings.Replace(text, "|", "", 1))}
	b.c = len([]rune(text[:c]))
	for keys != "" {
		if rest, ok := strings.CutPrefix(keys, "<esc>"); ok {
			if v.insert {
				v.escape(&b)
			} else {
				v.pending = nil
			}
			keys = rest
			continue
		}
		r := []rune(keys)[0]
		keys = keys[len(string(r)):]
		if v.insert {
			b.insert(b.c, []rune{r})
			b.c++
			v.record("", string(r), false)
			continue
		}
		v.key(&b, r)
	}
	return string(b.r[:b.c]) + "|" + string(b.r[b.c:])
}

func TestVimCommands(t *testing.T) {
	for _, tc := range []struct{ text, keys, want string }{
		// motions
		{"|hello world", "w", "hello |world"},
		{"|hello world", "e", "hell|o world"},
		{"hello |world", "b", "|hello world"},
		{"|foo.bar baz", "w", "foo|.bar baz"},
		{"|foo.bar baz", "W", "foo.bar |baz"},
		{"|abc", "$", "ab|c"},
		{"  a|bc", "0", "|  abc"},
		{"  a|bc", "^", "  |abc"},
		{"|a b c d", "3w", "a b c |d"},
		{"|one\ntwo", "j", "one\n|two"},
		{"one\ntw|o", "k", "on|e\ntwo"},
		{"long line\n|x", "k", "|long line\nx"},
		{"a\nb\n|c", "gg", "|a\nb\nc"},
		{"|a\nb\nc", "G", "a\nb\n|c"},
		{"|a\nb\nc", "2G", "a\n|b\nc"},
		{"|a,b,c", "f,", "a|,b,c"},
		{"|a,b,c", "2f,", "a,b|,c"},
		{"|a,b,c", "t,", "|a,b,c"},
		{"|a,b,c", "f,;", "a,b|,c"},
		{"a,b,|c", "F,", "a,b|,c"},
		{"|ab", "l", "a|b"},
		{"a|b", "l", "a|b"}, // stays on the last character
		{"|word\n\nnext", "w", "word\n|\nnext"},
		// operators
		{"|hello world", "dw", "|world"},
		{"hello |world", "dw", "hello| "},
		{"|hello world", "de", "| world"},
		{"hello |world", "db", "|world"},
		{"|a b c d", "d2w", "|c d"},
		{"|a b c d", "2dw", "|c d"},
		{"hel|lo", "d$", "he|l"},
		{"hel|lo", "D", "he|l"},
		{"hel|lo", "d0", "|lo"},
		{"|one\ntwo\nthree", "dd", "|two\nthree"},
		{"one\n|two\nthree", "2dd", "|one"},
		{"one\n|two", "dd", "|one"},
		{"|one\ntwo", "dj", "|"},
		{"a\n|b\nc", "dk", "|c"},
		{"|ab,cd", "dt,", "|,cd"},
		{"|ab,cd", "df,", "|cd"},
		{"|hello world", "cwhi<esc>", "h|i world"},
		{"hell|o world", "cwX<esc>", "hell|X world"},
		{"|hello world", "ccnew<esc>", "ne|w"},
		{"he|llo", "Cy<esc>", "he|y"},
		{"|abc", "x", "|bc"},
		{"|abc", "2x", "|c"},
		{"ab|c", "x", "a|b"},
		{"ab|c", "X", "a|c"},
		{"|abc", "sz<esc>", "|zbc"},
		{"|ab", "~~", "A|B"},
		{"|abc", "rz", "|zbc"},
		{"|one\n  two", "J", "one| two"},
		{"|a\nb\nc", "3J", "a b| c"},
		// text objects
		{"say he|llo there", "diw", "say | there"},
		{"say he|llo there", "daw", "say |there"},
		{"say he|llo", "daw", "sa|y"},
		{`x = "a b|c" + 1`, `ci"z<esc>`, `x = "|z" + 1`},
		{`x = "a b|c" + 1`, `da"`, `x = | + 1`},
		{"f(a, |b)", "ci(x<esc>", "f(|x)"},
		{"f(a, (|b))", "di(", "f(a, (|))"},
		{"f(a, (|b))", "da(", "f(a, |)"},
		{"f(|a, (b))", "di)", "f(|)"},
		{"{\n  |a\n}", "diB", "{|}"},
		// yank and put
		{"|ab cd", "ywP", "ab| ab cd"},
		{"|a,b,c,d", "t,;", "a,|b,c,d"},
		{"|ab cd", "yw$p", "ab cdab| "},
		{"|one\ntwo", "yyjp", "one\ntwo\n|one"},
		{"one\n|two", "yykP", "|two\none\ntwo"},
		{"|one\ntwo", "ddp", "two\n|one"},
		{"|ab", "xp", "b|a"},
		// insert commands
		{"a|b", "iX<esc>", "a|Xb"},
		{"a|b", "aX<esc>", "ab|X"},
		{"  a|b", "IX<esc>", "  |Xab"},
		{"a|b", "AX<esc>", "ab|X"},
		{"|a\nb", "oX<esc>", "a\n|X\nb"},
		{"a\n|b", "OX<esc>", "a\n|X\nb"},
		// undo and repeat
		{"|a b c", "dwu", "|a b c"},
		{"|a b c", "dwdwuu", "|a b c"},
		{"|a b c", "dwdwu", "|b c"},
		{"|a b c", "cwX<esc>u", "|a b c"},
		{"|a b c", "dw.", "|c"},
		{"|a b c", "cwX<esc>w.", "X |X c"},
		{"|abcdef", "x3.", "|ef"},
		{"|a\nb", "AX<esc>j.", "aX\nb|X"},
		// counts and bad input
		{"|abc", "zl", "a|bc"},
		{"|abc", "d<esc>l", "a|bc"},
		{"|a b", "dz", "|a b"},
	} {
		if got := vimRun(newVimNormal(), tc.text, tc.keys); got != tc.want {
			t.Errorf("%q + %s = %q, want %q", tc.text, tc.keys, got, tc.want)
		}
	}
}

func newVimNormal() *vimEditor {
	v := newVim()
	v.insert = false
	return v
}

func TestVimInComposer(t *testing.T) {
	m := testModel(t)
	m.opts.Config.EditorMode = "vim"
	m = newModel(m.opts)
	press := func(keys ...tea.KeyPressMsg) {
		for _, k := range keys {
			m.Update(k)
		}
	}
	esc := tea.KeyPressMsg{Code: tea.KeyEscape}
	typeText(m, "hello world")
	if !strings.Contains(plain(m.statusLine()), "INSERT") {
		t.Fatalf("footer = %q", plain(m.statusLine()))
	}
	press(esc)
	if !strings.Contains(plain(m.statusLine()), "NORMAL") {
		t.Fatalf("esc should enter normal mode: %q", plain(m.statusLine()))
	}
	typeText(m, "0dw")
	if got := m.input.Value(); got != "world" {
		t.Fatalf("0dw: %q", got)
	}
	typeText(m, "A")
	typeText(m, "!")
	press(esc)
	typeText(m, "u")
	if got := m.input.Value(); got != "world" {
		t.Fatalf("u should undo the insert: %q", got)
	}
	// Multi-line: the cursor round-trips through the textarea.
	m.input.SetValue("one\ntwo\nthree")
	typeText(m, "ggjdd")
	if got := m.input.Value(); got != "one\nthree" {
		t.Fatalf("ggjdd: %q", got)
	}
	if m.input.Line() != 1 {
		t.Errorf("cursor line = %d, want 1", m.input.Line())
	}
	// enter still sends from normal mode, and the next prompt starts in insert.
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.input.Value() != "" || !m.vim.insert {
		t.Errorf("after enter: %q, insert=%v", m.input.Value(), m.vim.insert)
	}
	// k on the first line recalls the previous prompt. (The prompt
	// started a turn, so esc on the empty input would interrupt it.)
	m.vim.insert = false
	typeText(m, "k")
	if got := m.input.Value(); got != "one\nthree" {
		t.Errorf("k should recall history: %q", got)
	}
	// Ctrl shortcuts still work in normal mode.
	press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !m.showThinking {
		t.Error("ctrl+o should still toggle thinking")
	}
}

func TestVimSettingPersists(t *testing.T) {
	m := testModel(t)
	m.command("/vim")()
	if m.vim == nil || m.opts.Config.EditorMode != "vim" {
		t.Fatal("/vim should switch vim mode on")
	}
	data, err := os.ReadFile(filepath.Join(m.opts.Config.ConfigDir, "config.json"))
	if err != nil || !strings.Contains(string(data), `"editor_mode": "vim"`) {
		t.Errorf("config.json = %s (%v)", data, err)
	}
	m.command("/vim")()
	if m.vim != nil {
		t.Fatal("/vim again should switch it off")
	}
}
