package termgfx

import (
	"image"
	"runtime"
	"sync"
)

// Frame is a picture as packed 8-bit RGB rows: what the kitty protocol
// takes (f=24), so nothing is converted twice.
type Frame struct {
	W, H int
	Pix  []byte // len W*H*3
}

func (f Frame) Row(y, x0, x1 int) []byte { return f.Pix[(y*f.W+x0)*3 : (y*f.W+x1)*3] }

// Sub copies the rectangle r out of f.
func (f Frame) Sub(r image.Rectangle) Frame {
	out := Frame{W: r.Dx(), H: r.Dy(), Pix: make([]byte, 0, r.Dx()*r.Dy()*3)}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		out.Pix = append(out.Pix, f.Row(y, r.Min.X, r.Max.X)...)
	}
	return out
}

// pixBuffers recycles frames' pixels: a Retina frame is 15 MB, and a new
// one each frame at 30 frames a second keeps the garbage collector busy.
var pixBuffers sync.Pool

// Release gives f's pixels back for another frame; f must not be used
// after.
func Release(f Frame) {
	if f.Pix != nil {
		pix := f.Pix[:0]
		pixBuffers.Put(&pix)
	}
}

// pixels returns a buffer of n bytes, recycled when one is big enough.
// Every byte is overwritten by the caller.
func pixels(n int) []byte {
	if p, ok := pixBuffers.Get().(*[]byte); ok && cap(*p) >= n {
		return (*p)[:n]
	}
	return make([]byte, n)
}

// ToRGB converts a decoded frame. JPEG frames are YCbCr, converted in
// bands across the CPUs: at Retina sizes that is millions of pixels a
// frame, the largest cost after decoding. The frame's pixels may be
// handed back with Release.
func ToRGB(img image.Image) Frame {
	b := img.Bounds()
	f := Frame{W: b.Dx(), H: b.Dy(), Pix: pixels(b.Dx() * b.Dy() * 3)}
	bands := min(runtime.GOMAXPROCS(0), max(f.H/64, 1))
	var wg sync.WaitGroup
	for i := 0; i < bands; i++ {
		y0, y1 := f.H*i/bands, f.H*(i+1)/bands
		wg.Add(1)
		go func() {
			defer wg.Done()
			convertRows(img, f, y0, y1)
		}()
	}
	wg.Wait()
	return f
}

func convertRows(img image.Image, f Frame, y0, y1 int) {
	b := img.Bounds()
	switch src := img.(type) {
	case *image.YCbCr:
		for y := y0; y < y1; y++ {
			out := f.Pix[y*f.W*3:]
			for x := 0; x < f.W; x++ {
				yi := src.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := src.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := ycbcr(src.Y[yi], src.Cb[ci], src.Cr[ci])
				out[x*3], out[x*3+1], out[x*3+2] = r, g, bl
			}
		}
	case *image.RGBA:
		for y := y0; y < y1; y++ {
			in := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			out := f.Pix[y*f.W*3:]
			for x := 0; x < f.W; x++ {
				out[x*3], out[x*3+1], out[x*3+2] = in[x*4], in[x*4+1], in[x*4+2]
			}
		}
	default:
		for y := y0; y < y1; y++ {
			for x := 0; x < f.W; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				o := (y*f.W + x) * 3
				f.Pix[o], f.Pix[o+1], f.Pix[o+2] = byte(r>>8), byte(g>>8), byte(bl>>8)
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
