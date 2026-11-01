package termgfx

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestShowVirtual(t *testing.T) {
	var out bytes.Buffer
	if err := (Kitty{W: &out}).ShowVirtual(Frame{W: 1, H: 1, Pix: []byte{1, 2, 3}}, 0x2A1B01, 16, 4); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "a=T,U=1,q=2,f=24,s=1,v=1,i=2759425,c=16,r=4,") {
		t.Fatalf("%q", s)
	}
	if strings.Contains(s, "\x1b[") {
		t.Errorf("a virtual placement moves no cursor: %q", s)
	}
}

func TestPlaceholders(t *testing.T) {
	lines := Placeholders(0x2A1B01, 16, 2)
	if len(lines) != 2 {
		t.Fatalf("%d lines", len(lines))
	}
	if !strings.HasPrefix(lines[0], "\x1b[38;2;42;27;1m\U0010EEEE\u0305\u0305\U0010EEEE") {
		t.Errorf("row 0: %q", lines[0])
	}
	if !strings.Contains(lines[1], "\U0010EEEE\u030D\u0305") {
		t.Errorf("row 1: %q", lines[1])
	}
}

// What the TUI does to its text must leave placeholders whole: a line that
// measures wrong, or loses its marks, misplaces or breaks the image.
func TestPlaceholdersSurviveTextHandling(t *testing.T) {
	const cols, rows = 16, 4
	lines := Placeholders(0x2A1B01, cols, rows)
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != cols {
			t.Errorf("row %d measures %d cells", i, w)
		}
		if w := lipgloss.Width(l); w != cols {
			t.Errorf("row %d: lipgloss measures %d cells", i, w)
		}
	}
	joined := lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(lines, "\n"), " | beside")
	if got := strings.Count(joined, "beside"); got != 1 || !strings.Contains(joined, string(rowColumn[3])) {
		t.Errorf("joined: %q", joined)
	}

	// The renderer stores cells and writes them back out.
	scr := uv.NewScreenBuffer(40, rows)
	uv.NewStyledString(strings.Join(lines, "\n")).Draw(scr, scr.Bounds())
	got := scr.Render()
	if n := strings.Count(got, string(placeholder)); n != cols*rows {
		t.Errorf("%d placeholder cells after rendering, want %d", n, cols*rows)
	}
	for r := range rows {
		if !strings.Contains(got, string(placeholder)+string(rowColumn[r])+string(rowColumn[0])) {
			t.Errorf("row %d lost its marks: %q", r, got)
		}
	}
	if !strings.Contains(got, "38;2;42;27;1m") {
		t.Errorf("the id's color is gone: %q", got)
	}
}

func TestInlineTerminal(t *testing.T) {
	for _, tc := range []struct {
		vars map[string]string
		want bool
	}{
		{map[string]string{"TERM_PROGRAM": "ghostty"}, true},
		{map[string]string{"GHOSTTY_RESOURCES_DIR": "/x"}, true},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, true},
		{map[string]string{"TERM_PROGRAM": "ghostty", "TMUX": "/s"}, false},
		{map[string]string{"GHOSTTY_RESOURCES_DIR": "/x", "CMUX_SOCKET_PATH": "/s"}, false},
		{map[string]string{"WEZTERM_PANE": "1"}, false},
		{map[string]string{"KONSOLE_VERSION": "1"}, false},
		{map[string]string{}, false},
	} {
		if got := InlineTerminal(func(k string) string { return tc.vars[k] }); got != tc.want {
			t.Errorf("%v: got %v", tc.vars, got)
		}
	}
}
