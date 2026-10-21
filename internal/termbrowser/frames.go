package termbrowser

import (
	"image"
	"runtime"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// Most screencast frames differ from the last one in a small area: a
// caret blinking, a hovered button, text being typed. The viewer sends
// those areas as small patch images over the last full frame instead of
// the whole page again, which is what keeps it responsive over a tty.

// rgbFrame is a picture as packed 8-bit RGB rows: what the kitty protocol
// takes (f=24), so nothing is converted twice.
type rgbFrame struct {
	w, h int
	pix  []byte // len w*h*3
}

func (f rgbFrame) row(y, x0, x1 int) []byte { return f.pix[(y*f.w+x0)*3 : (y*f.w+x1)*3] }

// sub copies the rectangle r out of f.
func (f rgbFrame) sub(r image.Rectangle) rgbFrame {
	out := rgbFrame{w: r.Dx(), h: r.Dy(), pix: make([]byte, 0, r.Dx()*r.Dy()*3)}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		out.pix = append(out.pix, f.row(y, r.Min.X, r.Max.X)...)
	}
	return out
}

// toRGB converts a decoded frame. JPEG frames are YCbCr, converted in
// bands across the CPUs: at Retina sizes that is millions of pixels a
// frame, the largest cost after decoding.
func toRGB(img image.Image) rgbFrame {
	b := img.Bounds()
	f := rgbFrame{w: b.Dx(), h: b.Dy(), pix: make([]byte, b.Dx()*b.Dy()*3)}
	bands := min(runtime.GOMAXPROCS(0), max(f.h/64, 1))
	var wg sync.WaitGroup
	for i := 0; i < bands; i++ {
		y0, y1 := f.h*i/bands, f.h*(i+1)/bands
		wg.Add(1)
		go func() {
			defer wg.Done()
			convertRows(img, f, y0, y1)
		}()
	}
	wg.Wait()
	return f
}

func convertRows(img image.Image, f rgbFrame, y0, y1 int) {
	b := img.Bounds()
	switch src := img.(type) {
	case *image.YCbCr:
		for y := y0; y < y1; y++ {
			out := f.pix[y*f.w*3:]
			for x := 0; x < f.w; x++ {
				yi := src.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := src.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := ycbcr(src.Y[yi], src.Cb[ci], src.Cr[ci])
				out[x*3], out[x*3+1], out[x*3+2] = r, g, bl
			}
		}
	case *image.RGBA:
		for y := y0; y < y1; y++ {
			in := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			out := f.pix[y*f.w*3:]
			for x := 0; x < f.w; x++ {
				out[x*3], out[x*3+1], out[x*3+2] = in[x*4], in[x*4+1], in[x*4+2]
			}
		}
	default:
		for y := y0; y < y1; y++ {
			for x := 0; x < f.w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				o := (y*f.w + x) * 3
				f.pix[o], f.pix[o+1], f.pix[o+2] = byte(r>>8), byte(g>>8), byte(bl>>8)
			}
		}
	}
}

// ycbcr is JFIF's YCbCr to RGB in 16.16 fixed point, as image/color does.
func ycbcr(y, cb, cr uint8) (uint8, uint8, uint8) {
	yy := int32(y) * 0x10101
	cb1, cr1 := int32(cb)-128, int32(cr)-128
	r := yy + 91881*cr1
	g := yy - 22554*cb1 - 46802*cr1
	b := yy + 116130*cb1
	return clamp16(r), clamp16(g), clamp16(b)
}

func clamp16(v int32) uint8 {
	switch {
	case v < 0:
		return 0
	case v > 0xffffff:
		return 0xff
	}
	return uint8(v >> 16)
}

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
func (d *frameDiff) update(f rgbFrame) (rects []image.Rectangle, full bool) {
	b := image.Rect(0, 0, f.w, f.h)
	cols, rows := (f.w+tileSize-1)/tileSize, (f.h+tileSize-1)/tileSize
	tiles := make([]uint64, cols*rows)
	for ty := 0; ty < rows; ty++ {
		for tx := 0; tx < cols; tx++ {
			tiles[ty*cols+tx] = tileHash(f, tileRect(b, tx, ty))
		}
	}
	if d.w != f.w || d.h != f.h || len(d.tiles) != len(tiles) {
		d.w, d.h, d.tiles = f.w, f.h, tiles
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

func tileHash(f rgbFrame, r image.Rectangle) uint64 {
	h := xxhash.New()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		h.Write(f.row(y, r.Min.X, r.Max.X))
	}
	return h.Sum64()
}
