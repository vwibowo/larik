package termbrowser

import (
	"image"

	"github.com/cespare/xxhash/v2"

	"larik/internal/termgfx"
)

// Most screencast frames differ from the last one in a small area: a
// caret blinking, a hovered button, text being typed. The viewer sends
// those areas as small patch images over the last full frame instead of
// the whole page again, which is what keeps it responsive over a tty.

// tileSize is the side of the squares frames are compared in.
const tileSize = 64

// fullFrameShare is the share of changed tiles past which a patch would
// not save enough to be worth it, so the whole frame is sent.
const fullFrameShare = 0.4

// frameDiff remembers the tiles of the last frame.
type frameDiff struct {
	w, h  int
	tiles []uint64 // hash per tile, row by row
}

// update compares f with the previous frame and returns the regions to
// redraw, or full when the whole frame should be sent (the first frame, a
// new size, or most of it changed). An unchanged frame returns neither.
func (d *frameDiff) update(f termgfx.Frame) (rects []image.Rectangle, full bool) {
	b := image.Rect(0, 0, f.W, f.H)
	cols, rows := (f.W+tileSize-1)/tileSize, (f.H+tileSize-1)/tileSize
	tiles := make([]uint64, cols*rows)
	for ty := 0; ty < rows; ty++ {
		for tx := 0; tx < cols; tx++ {
			tiles[ty*cols+tx] = tileHash(f, tileRect(b, tx, ty))
		}
	}
	if d.w != f.W || d.h != f.H || len(d.tiles) != len(tiles) {
		d.w, d.h, d.tiles = f.W, f.H, tiles
		return nil, true
	}
	changed := 0
	for ty := 0; ty < rows; ty++ {
		// Changed tiles in one row merge into one rectangle per run.
		run := -1
		for tx := 0; tx <= cols; tx++ {
			dirty := tx < cols && tiles[ty*cols+tx] != d.tiles[ty*cols+tx]
			if dirty {
				changed++
				if run < 0 {
					run = tx
				}
				continue
			}
			if run >= 0 {
				rects = append(rects, tileRect(b, run, ty).Union(tileRect(b, tx-1, ty)))
				run = -1
			}
		}
	}
	d.tiles = tiles
	if float64(changed) > fullFrameShare*float64(len(tiles)) {
		return nil, true
	}
	return mergeRows(rects), false
}

// mergeRows joins rectangles stacked directly on top of each other with
// the same horizontal extent, which a region spanning several tile rows
// produces.
func mergeRows(rects []image.Rectangle) []image.Rectangle {
	var out []image.Rectangle
	for _, r := range rects {
		merged := false
		for i := range out {
			if out[i].Min.X == r.Min.X && out[i].Max.X == r.Max.X && out[i].Max.Y == r.Min.Y {
				out[i].Max.Y = r.Max.Y
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, r)
		}
	}
	return out
}

func tileRect(b image.Rectangle, tx, ty int) image.Rectangle {
	r := image.Rect(tx*tileSize, ty*tileSize, (tx+1)*tileSize, (ty+1)*tileSize).Add(b.Min)
	return r.Intersect(b)
}

func tileHash(f termgfx.Frame, r image.Rectangle) uint64 {
	h := xxhash.New()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		h.Write(f.Row(y, r.Min.X, r.Max.X))
	}
	return h.Sum64()
}
