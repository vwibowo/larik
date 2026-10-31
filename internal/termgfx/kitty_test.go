package termgfx

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKittyShowChunksAndPlaces(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	img.Set(1, 0, color.RGBA{4, 5, 6, 255})
	var out bytes.Buffer
	if err := (Kitty{W: &out}).Show(ToRGB(img), Placement{ID: 7, Z: 2, Col: 3, Row: 4, X: 5, Y: 6}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.HasPrefix(s, "\x1b7\x1b[5;4H\x1b_Ga=T,q=2,f=24,s=2,v=1,i=7,p=1,X=5,Y=6,z=2,C=1,o=z,m=0;") || !strings.HasSuffix(s, "\x1b\\\x1b8") {
		t.Fatalf("escape = %q", s)
	}
	apc := s[strings.Index(s, "\x1b_G"):]
	payload := apc[strings.Index(apc, ";")+1 : strings.Index(apc, "\x1b\\")]
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	pix, _ := io.ReadAll(zr)
	if !bytes.Equal(pix, []byte{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("pixels = %v", pix)
	}

	// A large image goes in 4096-byte chunks, the key list only on the first.
	big := image.NewRGBA(image.Rect(0, 0, 300, 300))
	rng := rand.New(rand.NewPCG(1, 2)) // noise, so zlib can't shrink it to one chunk
	for i := range big.Pix {
		big.Pix[i] = byte(rng.Uint32())
	}
	out.Reset()
	(Kitty{W: &out}).Show(ToRGB(big), Placement{ID: 1, Cols: 30, Rows: 10})
	chunks := strings.Split(out.String(), "\x1b_G")[1:]
	if len(chunks) < 2 || !strings.Contains(chunks[0], ",c=30,r=10,o=z,m=1;") || !strings.HasPrefix(chunks[len(chunks)-1], "m=0;") {
		t.Fatalf("%d chunks; first %.80q last %.20q", len(chunks), chunks[0], chunks[len(chunks)-1])
	}
	for _, c := range chunks[1 : len(chunks)-1] {
		if !strings.HasPrefix(c, "m=1;") || len(c[4:strings.Index(c, "\x1b\\")]) != kittyChunk {
			t.Fatalf("middle chunk %.20q", c)
		}
	}
}

func TestKittyDeleteAndTmux(t *testing.T) {
	var out bytes.Buffer
	(Kitty{W: &out}).Delete(0)
	(Kitty{W: &out}).Delete(3)
	if got := out.String(); got != "\x1b_Ga=d,d=A,q=2\x1b\\\x1b_Ga=d,d=I,i=3,q=2\x1b\\" {
		t.Fatalf("delete = %q", got)
	}
	out.Reset()
	(Kitty{W: &out, Tmux: true}).Delete(0)
	if got := out.String(); got != "\x1bPtmux;\x1b\x1b_Ga=d,d=A,q=2\x1b\x1b\\\x1b\\" {
		t.Fatalf("tmux passthrough = %q", got)
	}
}

func TestKittyTerminal(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	for _, tc := range []struct {
		vars     map[string]string
		ok, tmux bool
	}{
		{map[string]string{"TERM": "xterm-kitty"}, true, false},
		{map[string]string{"TERM_PROGRAM": "ghostty"}, true, false},
		{map[string]string{"WEZTERM_PANE": "3", "TMUX": "/tmp/t"}, true, true},
		{map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"}, false, false},
	} {
		ok, tmux := Terminal(env(tc.vars))
		if ok != tc.ok || tmux != tc.tmux {
			t.Errorf("%v: ok=%v tmux=%v", tc.vars, ok, tmux)
		}
	}
}

func TestKittyShowThroughFile(t *testing.T) {
	var out bytes.Buffer
	f := Frame{W: 2, H: 1, Pix: []byte{1, 2, 3, 4, 5, 6}}
	if err := (Kitty{W: &out, Files: true}).Show(f, Placement{ID: 1}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, ",t=t,m=0;") || strings.Contains(s, "o=z") {
		t.Fatalf("escape = %q", s)
	}
	apc := s[strings.Index(s, "\x1b_G"):]
	name, err := base64.StdEncoding.DecodeString(apc[strings.Index(apc, ";")+1 : strings.Index(apc, "\x1b\\")])
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(string(name))
	data, err := os.ReadFile(string(name))
	if err != nil || !bytes.Equal(data, f.Pix) || !strings.Contains(filepath.Base(string(name)), "tty-graphics-protocol") {
		t.Fatalf("frame file %s: %v %v", name, data, err)
	}
}
