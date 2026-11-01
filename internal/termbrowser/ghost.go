package termbrowser

import (
	"bytes"
	"context"
	"image"
	"math"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/termgfx"
)

// An agent's click shows in the viewer before it lands: a marker on a layer
// above the page, for long enough to see it and stop the turn. Without a
// viewer nobody is watching, so clicks don't wait.

const (
	// ghostDelay is how long a click waits with its marker showing.
	ghostDelay = 400 * time.Millisecond
	// ghostFirstID is the first of the marker's images, after the outline's.
	ghostFirstID = pickFirstID + 4
	// ghostBars is the marker's images: a box and the dot in it.
	ghostBars = 5
	// ghostRadius is half the box, in cells' widths, so it stays the same
	// size on screen at any zoom.
	ghostRadius = 2
	// ghostDot is half the dot's side in device pixels.
	ghostDot = 2
)

// Cursor is where an agent is about to click: a tab and CSS pixels.
type Cursor struct {
	Tab string  `json:"tab"`
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
}

// showClick announces a click at x, y and waits for the viewer to show it.
// The caller runs the returned function once the click is done. An error
// means the context ended first: the click must not happen.
func (s *Session) showClick(ctx context.Context, x, y float64) (func(), error) {
	if s.viewers() == 0 {
		return func() {}, nil
	}
	t, err := s.Tab(ctx)
	if err != nil {
		return nil, err
	}
	v := &s.view
	v.mu.Lock()
	v.cursor = &Cursor{Tab: string(t.id), X: x, Y: y}
	v.mu.Unlock()
	s.changed()
	done := func() {
		v.mu.Lock()
		v.cursor = nil
		v.mu.Unlock()
		s.changed()
	}
	select {
	case <-time.After(ghostDelay):
		return done, nil
	case <-ctx.Done():
		done()
		return nil, ctx.Err()
	}
}

// reaim finds the element with ref again after showClick waited, moving the
// marker with it. With no viewer there was no wait, so x, y still hold.
func (s *Session) reaim(ctx context.Context, ref string, x, y float64) (float64, float64, error) {
	if s.viewers() == 0 {
		return x, y, nil
	}
	nx, ny, err := s.point(ctx, ref)
	if err != nil {
		return 0, 0, err
	}
	if nx != x || ny != y {
		v := &s.view
		v.mu.Lock()
		if v.cursor != nil {
			c := *v.cursor
			c.X, c.Y = nx, ny
			v.cursor = &c
		}
		v.mu.Unlock()
		s.changed()
	}
	return nx, ny, nil
}

// syncGhost shows or removes the marker to match the session's state.
func (v *viewer) syncGhost() tea.Cmd {
	c := v.state.Cursor
	if c == nil || c.Tab != string(v.tab) {
		if !v.ghost {
			return nil
		}
		var out bytes.Buffer
		k := v.kitty
		k.W = &out
		v.deleteGhost(k)
		return tea.Raw(out.String())
	}
	return v.drawGhost(*c)
}

// ghostBox is the marker's box around c, in the page's device pixels, and
// the page's bounds.
func (v *viewer) ghostBox(c Cursor) (center image.Point, page image.Rectangle) {
	r := v.ratio()
	center = image.Pt(int(math.Round(c.X*r)), int(math.Round(c.Y*r)))
	page = image.Rect(0, 0, v.cols*v.cellW, max(v.rows-headerRows, 0)*v.cellH)
	return center, page
}

func (v *viewer) drawGhost(c Cursor) tea.Cmd {
	if v.cellW == 0 || v.cellH == 0 {
		return nil
	}
	center, page := v.ghostBox(c)
	if !center.In(page) {
		return nil // the viewer's view of the page is elsewhere
	}
	var out bytes.Buffer
	k := v.kitty
	k.W = &out
	v.deleteGhost(k) // it may have moved
	radius := ghostRadius * v.cellW
	box := image.Rect(center.X-radius, center.Y-radius, center.X+radius, center.Y+radius).Intersect(page)
	bars := outlineBars(box)
	dot := image.Rect(center.X-ghostDot, center.Y-ghostDot, center.X+ghostDot, center.Y+ghostDot).Intersect(page)
	for i, bar := range append(bars[:], dot) {
		if !bar.Empty() {
			v.place(k, bar, ghostFirstID+i)
		}
	}
	v.ghost = true
	return tea.Raw(out.String())
}

func (v *viewer) deleteGhost(k termgfx.Kitty) {
	if v.ghost {
		for i := range ghostBars {
			k.Delete(ghostFirstID + i)
		}
		v.ghost = false
	}
}
