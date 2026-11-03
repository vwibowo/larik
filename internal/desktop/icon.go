package desktop

import (
	"bytes"
	"encoding/binary"
	"image/png"

	"github.com/egoist/mygo/ui"
)

// iconMark is Larik's mark, the TUI's three slanted bars.
var iconMark = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor">` +
	`<path d="M2 16 6.5 8h3L5 16zM8.25 16l4.5-8h3l-4.5 8zM14.5 16 19 8h3l-4.5 8z"/></svg>`))

// iconView draws the app icon at size×size pixels, on the grid of macOS
// icons: a rounded square 824/1024 across, centred.
func iconView(size float32) func(c *ui.Context) {
	s := size / 1024
	return func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Column(c).Fill().Center().Children(func() {
			ui.Column(c).Size(824*s, 824*s).Radius(185*s).Background(ui.Hex("#221d1b")).Border(max(1, 6*s), ui.Hex("#3d3532")).Center().
				Shadow(0, 10*s, 24*s, 0, ui.RGBA(0, 0, 0, 0.35)).Children(func() {
				ui.Icon(c, iconMark).FontSize(560 * s).TextColor(ui.Hex("#2dd4bf"))
			})
		})
	}
}

// iconPNG renders the icon as a PNG of size×size pixels.
func iconPNG(size int) ([]byte, error) {
	tt := ui.NewTester(iconView(float32(size)), size, size)
	tt.Frame()
	var buf bytes.Buffer
	err := png.Encode(&buf, tt.Image())
	return buf.Bytes(), err
}

// icns builds a macOS .icns file: a header, then a PNG for each size
// Finder and the Dock ask for.
func icns() ([]byte, error) {
	types := []struct {
		kind string
		size int
	}{
		{"ic11", 32}, {"ic12", 64}, {"ic07", 128}, {"ic13", 256}, {"ic08", 256},
		{"ic14", 512}, {"ic09", 512}, {"ic10", 1024},
	}
	rendered := map[int][]byte{}
	var body bytes.Buffer
	for _, t := range types {
		img, ok := rendered[t.size]
		if !ok {
			var err error
			if img, err = iconPNG(t.size); err != nil {
				return nil, err
			}
			rendered[t.size] = img
		}
		body.WriteString(t.kind)
		binary.Write(&body, binary.BigEndian, uint32(8+len(img)))
		body.Write(img)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes(), nil
}
