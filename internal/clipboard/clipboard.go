// Package clipboard reads an image from the system clipboard, which a
// terminal can't paste: bracketed paste carries only text.
package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // decode clipboard PNGs
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNoImage means the clipboard holds no image (or nothing at all).
var ErrNoImage = errors.New("no image on the clipboard")

// ErrUnsupported means there is no way to read the clipboard here.
var ErrUnsupported = errors.New("reading images from the clipboard isn't supported here")

// ReadImage returns the clipboard's image as PNG.
func ReadImage(ctx context.Context) ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return readMac(ctx)
	case "linux":
		return readLinux(ctx)
	}
	return nil, ErrUnsupported
}

// readMac asks AppleScript for the clipboard as PNG; screenshots and
// images copied from apps are available in that form.
func readMac(ctx context.Context) ([]byte, error) {
	f, err := os.CreateTemp("", "larik-clip-*.png")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
try
	set eof f to 0
	write (the clipboard as «class PNGf») to f
	close access f
on error e
	close access f
	error e
end try`, name)
	if err := exec.CommandContext(ctx, "osascript", "-e", script).Run(); err != nil {
		return nil, ErrNoImage // the clipboard has no PNG form
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, ErrNoImage
	}
	return data, nil
}

func readLinux(ctx context.Context) ([]byte, error) {
	var cmd *exec.Cmd
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" && has("wl-paste"):
		cmd = exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", "image/png")
	case has("xclip"):
		cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-target", "image/png", "-out")
	default:
		return nil, fmt.Errorf("%w: install wl-clipboard (Wayland) or xclip (X11)", ErrUnsupported)
	}
	data, err := cmd.Output()
	if err != nil || len(data) == 0 || !bytes.HasPrefix(data, []byte("\x89PNG")) {
		return nil, ErrNoImage
	}
	return data, nil
}

func has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// maxSide is the longest side Fit scales an image down to: larger ones
// cost more tokens without helping the model read them.
const maxSide = 2000

// Fit returns the image in a form under maxBytes: as is when it already
// fits and isn't oversized, else scaled down and re-encoded as JPEG. ext
// is the file extension for the result.
func Fit(data []byte, maxBytes int) (out []byte, ext string, err error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("the clipboard image couldn't be read: %w", err)
	}
	b := img.Bounds()
	if len(data) <= maxBytes && max(b.Dx(), b.Dy()) <= maxSide {
		return data, ".png", nil
	}
	side := min(max(b.Dx(), b.Dy()), maxSide)
	for side >= 64 {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, scale(img, side), &jpeg.Options{Quality: 85}); err != nil {
			return nil, "", err
		}
		if buf.Len() <= maxBytes {
			return buf.Bytes(), ".jpg", nil
		}
		side = side * 3 / 4
	}
	return nil, "", errors.New("the clipboard image is too large to attach")
}

// scale shrinks img so its longest side is side, averaging the source
// pixels behind each output pixel (a box filter), onto white, since JPEG
// has no transparency.
func scale(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if long := max(w, h); long > side {
		w, h = max(w*side/long, 1), max(h*side/long, 1)
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
		for x := range w {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
			var r, g, bl, n uint64
			for sy := y0; sy < max(y1, y0+1); sy++ {
				for sx := x0; sx < max(x1, x0+1); sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					// Composite onto white.
					r += uint64(cr + (0xffff - ca))
					g += uint64(cg + (0xffff - ca))
					bl += uint64(cb + (0xffff - ca))
					n++
				}
			}
			out.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), 0xffff})
		}
	}
	return out
}

// Save writes an image into dir with a fresh name, readable only by the
// user, and removes pasted images older than keep from earlier sessions.
func Save(dir string, data []byte, ext string, keep func(os.FileInfo) bool) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && !e.IsDir() && !keep(fi) {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	f, err := os.CreateTemp(dir, "paste-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// WriteText puts text on the system clipboard with the platform's tool.
func WriteText(ctx context.Context, text string) error {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		cmd = exec.CommandContext(ctx, "pbcopy")
	case runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" && has("wl-copy"):
		cmd = exec.CommandContext(ctx, "wl-copy")
	case runtime.GOOS == "linux" && has("xclip"):
		cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-in")
	default:
		return ErrUnsupported
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
