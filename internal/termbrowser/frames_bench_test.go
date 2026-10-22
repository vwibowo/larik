package termbrowser

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// benchJPEG is a Retina-sized page frame, as Chrome sends it.
func benchJPEG(b *testing.B) []byte {
	b.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2800, 1800))
	for y := 0; y < 1800; y++ {
		for x := 0; x < 2800; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: frameQuality}); err != nil {
		b.Fatal(err)
	}
	return buf.Bytes()
}

func BenchmarkJPEGDecode(b *testing.B) {
	data := benchJPEG(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkToRGB(b *testing.B) {
	img, _ := jpeg.Decode(bytes.NewReader(benchJPEG(b)))
	b.ResetTimer()
	for b.Loop() {
		releaseFrame(toRGB(img))
	}
}

func BenchmarkFrameDiff(b *testing.B) {
	img, _ := jpeg.Decode(bytes.NewReader(benchJPEG(b)))
	f := toRGB(img)
	var d frameDiff
	d.update(f)
	b.ResetTimer()
	for b.Loop() {
		d.update(f)
	}
}
