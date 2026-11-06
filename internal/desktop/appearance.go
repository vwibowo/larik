package desktop

import (
	"github.com/egoist/mygo/ui"
)

// Appearance: the window follows the system's light or dark appearance
// and accent colour, or the ones Settings chooses, as macOS's General
// settings offer them.

// appearances are the choices of Settings › Appearance, as saved: the
// system's, light or dark.
var appearances = []string{"", "light", "dark"}

// accent is one of the accent colours Settings offers, macOS's own.
type accent struct {
	name, hex string
}

// accents are the accent colours Settings offers after the system's.
var accents = []accent{
	{"Blue", "#007aff"}, {"Purple", "#a550a7"}, {"Pink", "#f74f9e"}, {"Red", "#ff5257"},
	{"Orange", "#f7821b"}, {"Yellow", "#ffc600"}, {"Green", "#62ba46"}, {"Graphite", "#8c8c8c"},
}

// setAppearance makes the window light, dark or the system's, saving
// the choice.
func (a *App) setAppearance(v string) {
	a.appearance = v
	if a.appear != nil {
		a.appear(v)
	}
	a.settingsChanged()
}

// accented gives t the accent colour hex, with the shades its buttons,
// selection and focus ring take.
func accented(t *ui.Theme, hex string) {
	c := ui.Hex(hex)
	shade := ui.Hex("#000000")
	if t.Dark {
		shade = ui.Hex("#ffffff")
	}
	t.Accent, t.AccentHover, t.AccentPressed = c, c.Mix(shade, 0.12), c.Mix(shade, 0.24)
	t.AccentText = ui.Hex("#ffffff")
	if hex == "#ffc600" { // yellow: white text on it is hard to read
		t.AccentText = ui.Hex("#1d1d1f")
	}
	t.Selection, t.Focus = c.Alpha(0.3), c.Alpha(0.55)
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}
