package termgfx

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// Unicode placeholders put an image in the text: a virtual placement says
// how many cells it fills, and special characters, colored with its id,
// stand for those cells. The terminal draws the image wherever the text
// goes, so it scrolls and redraws with the text around it, and a program
// that only knows about text can move it. Only the first cell of each row
// is marked; the rest of the row follows from it.

// placeholder is the character that stands for an image cell.
const placeholder = '\U0010EEEE'

// MaxRows is how many rows of cells Placeholders can number.
const MaxRows = len(rowColumn)

// InlineTerminal reports whether the environment names a terminal shown
// to draw placeholders correctly. It is narrower than Terminal on purpose:
// the rest speak the protocol, but a wrong guess here leaves boxes in the
// text, so each joins once it has been tried. cmux comes first because it
// looks like Ghostty from inside.
func InlineTerminal(env func(string) string) bool {
	if env("TMUX") != "" || env("CMUX_SOCKET_PATH") != "" {
		return false
	}
	term, program := env("TERM"), strings.ToLower(env("TERM_PROGRAM"))
	return env("KITTY_WINDOW_ID") != "" || strings.Contains(term, "kitty") ||
		env("GHOSTTY_RESOURCES_DIR") != "" || program == "ghostty" || strings.Contains(term, "ghostty")
}

// ShowVirtual transmits f under id, to fill cols by rows cells wherever
// Placeholders for id are printed. Nothing shows until they are.
func (k Kitty) ShowVirtual(f Frame, id, cols, rows int) error {
	return k.transmit(f, fmt.Sprintf("a=T,U=1,q=2,f=24,s=%d,v=%d,i=%d,c=%d,r=%d", f.W, f.H, id, cols, rows))
}

// ShowVirtualPNG is ShowVirtual for a PNG file's bytes, which the terminal
// decodes itself, so its transparency survives: a Frame has none.
func (k Kitty) ShowVirtualPNG(data []byte, id, cols, rows int) error {
	return k.transmitBytes(data, fmt.Sprintf("a=T,U=1,q=2,f=100,i=%d,c=%d,r=%d", id, cols, rows))
}

// IsPNG reports whether data starts like a PNG file.
func IsPNG(data []byte) bool { return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) }

// Placeholders is the text that shows image id, one line per row. Ids
// carry 24 bits, in the foreground color. rows must be at most MaxRows.
func Placeholders(id, cols, rows int) []string {
	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&255, id>>8&255, id&255)
	rest := strings.Repeat(string(placeholder), cols-1)
	lines := make([]string, rows)
	for r := range lines {
		lines[r] = color + string(placeholder) + string(rowColumn[r]) + string(rowColumn[0]) + rest + "\x1b[39m"
	}
	return lines
}

// Decode reads a PNG or JPEG into a frame.
func Decode(data []byte) (Frame, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Frame{}, err
	}
	return ToRGB(img), nil
}

// rowColumn holds the diacritics that number rows and columns, from the
// protocol's rowcolumn-diacritics.txt.
var rowColumn = [...]rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611,
	0x0612, 0x0613, 0x0614, 0x0615, 0x0616, 0x0617, 0x0657, 0x0658,
}
