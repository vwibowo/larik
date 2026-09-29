package clipboard

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pngOf(t *testing.T, w, h int, noisy bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.NRGBA{uint8(x), uint8(y), 128, 255}
			if noisy {
				c = color.NRGBA{uint8(rand.IntN(256)), uint8(rand.IntN(256)), uint8(rand.IntN(256)), 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFit(t *testing.T) {
	small := pngOf(t, 40, 30, false)
	if out, ext, err := Fit(small, 1<<20); err != nil || ext != ".png" || !bytes.Equal(out, small) {
		t.Fatalf("a small image should pass through: %s %v", ext, err)
	}

	wide := pngOf(t, 3000, 20, false)
	out, ext, err := Fit(wide, 5<<20)
	if err != nil || ext != ".jpg" {
		t.Fatalf("an oversized image should be scaled: %s %v", ext, err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != maxSide || cfg.Height != 13 {
		t.Fatalf("scaled to %dx%d (%v), want %dx13", cfg.Width, cfg.Height, err, maxSide)
	}

	noisy := pngOf(t, 600, 600, true) // noise compresses badly
	if out, _, err := Fit(noisy, 40<<10); err != nil || len(out) > 40<<10 {
		t.Fatalf("an image over the byte limit should shrink until it fits: %d bytes, %v", len(out), err)
	}
	if _, _, err := Fit([]byte("not an image"), 1<<20); err == nil {
		t.Fatal("garbage should be an error")
	}
}

func TestScaleOntoWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4)) // fully transparent
	r, g, b, _ := scale(img, 2).At(0, 0).RGBA()
	if r != 0xffff || g != 0xffff || b != 0xffff {
		t.Fatalf("transparent should become white, got %x %x %x", r, g, b)
	}
}

func TestSavePrunesOldPastes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pastes")
	os.MkdirAll(dir, 0o700)
	old := filepath.Join(dir, "paste-old.png")
	os.WriteFile(old, []byte("x"), 0o600)
	past := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(old, past, past)

	path, err := Save(dir, []byte("img"), ".png", func(fi os.FileInfo) bool { return time.Since(fi.ModTime()) < 7*24*time.Hour })
	if err != nil || !strings.HasSuffix(path, ".png") {
		t.Fatal(path, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("a pasted image must be private, got %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an old paste should be removed")
	}
}
