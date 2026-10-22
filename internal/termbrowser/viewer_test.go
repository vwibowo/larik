package termbrowser

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/chromedp/chromedp"
)

func TestKittyShowChunksAndPlaces(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	img.Set(1, 0, color.RGBA{4, 5, 6, 255})
	var out bytes.Buffer
	if err := (kitty{w: &out}).Show(toRGB(img), placement{id: 7, z: 2, col: 3, row: 4, x: 5, y: 6}); err != nil {
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
	(kitty{w: &out}).Show(toRGB(big), placement{id: 1, cols: 30, rows: 10})
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
	(kitty{w: &out}).Delete(0)
	(kitty{w: &out}).Delete(3)
	if got := out.String(); got != "\x1b_Ga=d,d=A,q=2\x1b\\\x1b_Ga=d,d=I,i=3,q=2\x1b\\" {
		t.Fatalf("delete = %q", got)
	}
	out.Reset()
	(kitty{w: &out, tmux: true}).Delete(0)
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
		ok, tmux := kittyTerminal(env(tc.vars))
		if ok != tc.ok || tmux != tc.tmux {
			t.Errorf("%v: ok=%v tmux=%v", tc.vars, ok, tmux)
		}
	}
}

func TestFrameDiff(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 512, 384)) // 8x6 tiles
	var d frameDiff
	if _, full := d.update(toRGB(img)); !full {
		t.Fatal("the first frame is a full one")
	}
	if rects, full := d.update(toRGB(img)); full || len(rects) != 0 {
		t.Fatalf("an unchanged frame needs nothing: %v %v", rects, full)
	}
	// Two neighbouring tiles in row 0 and the same pair in row 1 make one
	// rectangle; one more tile elsewhere is its own.
	for _, p := range []image.Point{{70, 10}, {140, 10}, {70, 70}, {140, 70}, {10, 150}} {
		img.Set(p.X, p.Y, color.RGBA{255, 0, 0, 255})
	}
	rects, full := d.update(toRGB(img))
	want := []image.Rectangle{image.Rect(64, 0, 192, 128), image.Rect(0, 128, 64, 192)}
	if full || !reflect.DeepEqual(rects, want) {
		t.Fatalf("rects = %v (full %v), want %v", rects, full, want)
	}
	// Most of the frame changing sends it whole.
	for i := range img.Pix {
		img.Pix[i] ^= 0xff
	}
	if _, full := d.update(toRGB(img)); !full {
		t.Fatal("a mostly changed frame should be sent whole")
	}
	if _, full := d.update(toRGB(image.NewRGBA(image.Rect(0, 0, 100, 100)))); !full {
		t.Fatal("a new size is a full frame")
	}
}

func TestKeyEvents(t *testing.T) {
	ev := keyEvents(tea.Key{Code: 'a', Text: "a"})
	if len(ev) != 2 || ev[0].Type != "keyDown" || ev[0].Text != "a" || ev[0].Code != "KeyA" || ev[1].Type != "keyUp" {
		t.Fatalf("a: %+v", ev)
	}
	ev = keyEvents(tea.Key{Code: tea.KeyEnter})
	if ev[0].Key != "Enter" || ev[0].Text != "\r" || ev[0].WindowsVirtualKeyCode != 13 {
		t.Fatalf("enter: %+v", ev)
	}
	ev = keyEvents(tea.Key{Code: tea.KeyLeft, Mod: tea.ModShift})
	if ev[0].Type != "rawKeyDown" || ev[0].Key != "ArrowLeft" || ev[0].Modifiers != cdpShift {
		t.Fatalf("shift+left: %+v", ev)
	}
	ev = keyEvents(tea.Key{Code: 'c', Mod: tea.ModCtrl})
	if ev[0].Code != "KeyC" || ev[0].Modifiers != cdpCtrl || ev[0].Text != "" {
		t.Fatalf("ctrl+c: %+v", ev)
	}
	if ev := keyEvents(tea.Key{Code: 'é', Text: "é"}); ev[0].Text != "é" {
		t.Fatalf("é: %+v", ev)
	}
}

func TestOpenPane(t *testing.T) {
	argv := []string{"/opt/larik", "browser", "view", "--socket", "/tmp/a b.sock"}
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	var ran []string
	run := func(_ context.Context, args []string) error { ran = args; return nil }
	for _, tc := range []struct {
		vars map[string]string
		name string
		want []string
	}{
		{map[string]string{"TMUX": "/tmp/tmux", "TMUX_PANE": "%3", "KITTY_WINDOW_ID": "1"}, "tmux",
			[]string{"tmux", "split-window", "-h", "-d", "-l", "40%", "-t", "%3", "/opt/larik browser view --socket '/tmp/a b.sock'"}},
		{map[string]string{"CMUX_SURFACE_ID": "surface:2"}, "cmux",
			[]string{"cmux", "new-split", "right", "--focus", "false", "--command", "/opt/larik browser view --socket '/tmp/a b.sock'", "--surface", "surface:2"}},
		{map[string]string{"KITTY_WINDOW_ID": "5"}, "kitty",
			append([]string{"kitty", "@", "launch", "--location=vsplit", "--keep-focus", "--cwd=current", "--next-to", "id:5", "--"}, argv...)},
		{map[string]string{"WEZTERM_PANE": "9"}, "wezterm",
			append([]string{"wezterm", "cli", "split-pane", "--right", "--percent", "40", "--pane-id", "9", "--"}, argv...)},
	} {
		ran = nil
		name, err := openPane(context.Background(), env(tc.vars), argv, 40, run)
		if err != nil || name != tc.name || !reflect.DeepEqual(ran, tc.want) {
			t.Errorf("%v: %s %v\n got %q\nwant %q", tc.vars, name, err, ran, tc.want)
		}
	}
	if _, err := openPane(context.Background(), env(nil), argv, 40, run); !errors.Is(err, errNoSplit) {
		t.Errorf("a plain terminal: %v", err)
	}
	failing := func(context.Context, []string) error { return errors.New("no server running") }
	if _, err := openPane(context.Background(), env(map[string]string{"TMUX": "x"}), argv, 40, failing); err == nil || !strings.Contains(err.Error(), "tmux could not split") {
		t.Errorf("a failed split: %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"plain/path-1.0": "plain/path-1.0", "": "''", "it's": `'it'\''s'`, "a b": "'a b'", "$HOME": "'$HOME'"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	for in, want := range map[string]string{
		"example.com": "https://example.com", "localhost:3000/x": "http://localhost:3000/x",
		"http://a.b": "http://a.b", "about:blank": "about:blank", "  ": "",
	} {
		if got := normalizeAddress(in); got != want {
			t.Errorf("normalizeAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDevToolsURL(t *testing.T) {
	dir := t.TempDir()
	if devToolsURL(dir) != "" {
		t.Fatal("no file, no endpoint")
	}
	os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte("9333\n/devtools/browser/abc\n"), 0o600)
	if got := devToolsURL(dir); got != "ws://127.0.0.1:9333/devtools/browser/abc" {
		t.Fatalf("got %q", got)
	}
	os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte("x\n/other\n"), 0o600)
	if devToolsURL(dir) != "" {
		t.Fatal("a malformed file is no endpoint")
	}
}

func TestSocketPathFallsBackWhenTooLong(t *testing.T) {
	short, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	if p, err := socketPath(short, "s.sock"); err != nil || p != filepath.Join(short, "s.sock") {
		t.Fatalf("short dir: %q %v", p, err)
	}
	long := filepath.Join(short, strings.Repeat("d", 120))
	p, err := socketPath(long, "s.sock")
	if err != nil || strings.HasPrefix(p, long) || len(p) > 104 {
		t.Fatalf("long dir: %q %v", p, err)
	}
}

// The control socket sends the state on connect and again when the agent
// moves to another tab, and stops when the session closes.
func TestControlSocketStreamsState(t *testing.T) {
	s := New(Options{})
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path, err := s.Serve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Serve(dir); again != path {
		t.Fatal("Serve twice should reuse the socket")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	states := make(chan State, 8)
	done := make(chan error, 1)
	go func() { done <- Subscribe(path, func(st State) { states <- st }) }()
	next := func() State {
		t.Helper()
		select {
		case st := <-states:
			return st
		case <-time.After(5 * time.Second):
			t.Fatal("no state")
			return State{}
		}
	}
	if st := next(); st.WS != "" || st.Focus != "" {
		t.Fatalf("first state: %+v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.viewers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	s.focusOn(&tab{id: "T1", owner: "explore"})
	s.mu.Unlock()
	if st := next(); st.Focus != "T1" || st.Driving != "explore" {
		t.Fatalf("after focus: %+v", st)
	}
	s.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Subscribe should end with the connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the session should end the stream")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the socket should be removed: %v", err)
	}
}

func TestControlSocketRejectsOtherUsers(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if sameUser(a) {
		t.Fatal("a connection whose peer can't be checked must be refused")
	}
}

// The whole viewer path against a real Chrome: the session's socket says
// where Chrome is and which tab the agent uses; a separate connection
// attaches to it and gets frames; letting go leaves the tab open.
func TestViewerAttachesAndStreams(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	if _, err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path, err := s.Serve(dir)
	if err != nil {
		t.Fatal(err)
	}
	states := make(chan State, 8)
	go Subscribe(path, func(st State) { states <- st })
	var st State
	select {
	case st = <-states:
	case <-time.After(10 * time.Second):
		t.Fatal("no state")
	}
	if !strings.HasPrefix(st.WS, "ws://127.0.0.1:") || st.Focus == "" {
		t.Fatalf("state: %+v", st)
	}
	conn, err := dialCDP(ctx, st.WS)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := conn.attach(cctx, st.Focus)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.call(cctx, session, "Page.enable", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.call(cctx, session, "Emulation.setDeviceMetricsOverride", map[string]any{"width": 400, "height": 300, "deviceScaleFactor": 1, "mobile": false}, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.call(cctx, session, "Page.startScreencast", map[string]any{"format": "jpeg", "quality": 60, "maxWidth": 400, "maxHeight": 300}, nil); err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Data      string `json:"data"`
		SessionID int    `json:"sessionId"`
	}
	for frame.Data == "" {
		select {
		case msg, ok := <-conn.Events():
			if !ok {
				t.Fatal("connection closed")
			}
			if msg.Method == "Page.screencastFrame" && msg.SessionID == session {
				json.Unmarshal(msg.Params, &frame)
			}
		case <-cctx.Done():
			t.Fatal("no screencast frame")
		}
	}
	if raw, err := base64.StdEncoding.DecodeString(frame.Data); err != nil || len(raw) < 100 {
		t.Fatalf("frame: %d bytes, %v", len(raw), err)
	}
	conn.detach(cctx, session)
	conn.Close()
	// The agent's tab is still there and usable.
	snap, err := s.Snapshot(ctx, "")
	if err != nil || !strings.Contains(snap.Text, "Sign up") {
		t.Fatalf("after the viewer left: %v %q", err, snap.Text)
	}
	_ = chromedp.Run // the session package's own driver stays in charge
}

// A new full frame is placed before the old one is deleted, so the pane
// never shows nothing in between.
func TestDrawReplacesFullFramesWithoutBlanking(t *testing.T) {
	v := newViewer(kitty{})
	v.cols, v.rows, v.cellW, v.cellH, v.scale = 10, 12, 10, 20, 1 // 100x200 px
	run := func(img *image.RGBA) string {
		cmd := v.draw(toRGB(img))
		if cmd == nil {
			return ""
		}
		return cmd().(tea.RawMsg).Msg.(string)
	}
	first := run(image.NewRGBA(image.Rect(0, 0, 100, 200)))
	if !strings.Contains(first, ",i=1,") || strings.Contains(first, "d=A") {
		t.Fatalf("first frame: %.120q", first)
	}
	changed := image.NewRGBA(image.Rect(0, 0, 100, 200))
	changed.Set(5, 5, color.RGBA{255, 0, 0, 255})
	if patch := run(changed); !strings.Contains(patch, ",i=3,") || strings.Contains(patch, "a=d") {
		t.Fatalf("a small change should be a patch: %.120q", patch)
	}
	all := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for i := range all.Pix {
		all.Pix[i] = 200
	}
	second := run(all)
	show, del := strings.Index(second, ",i=2,"), strings.Index(second, "a=d,d=I,i=1,")
	if show < 0 || del < show || !strings.Contains(second, "a=d,d=I,i=3,") || strings.Contains(second, "d=A") {
		t.Fatalf("second full frame must be shown before the first and its patch go: %.200q", second)
	}
}

func TestKittyShowThroughFile(t *testing.T) {
	var out bytes.Buffer
	f := rgbFrame{w: 2, h: 1, pix: []byte{1, 2, 3, 4, 5, 6}}
	if err := (kitty{w: &out, files: true}).Show(f, placement{id: 1}); err != nil {
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
	if err != nil || !bytes.Equal(data, f.pix) || !strings.Contains(filepath.Base(string(name)), "tty-graphics-protocol") {
		t.Fatalf("frame file %s: %v %v", name, data, err)
	}
}

func TestYCbCrConversionMatchesStdlib(t *testing.T) {
	img := image.NewYCbCr(image.Rect(0, 0, 130, 70), image.YCbCrSubsampleRatio420)
	rng := rand.New(rand.NewPCG(3, 4))
	for i := range img.Y {
		img.Y[i] = byte(rng.Uint32())
	}
	for i := range img.Cb {
		img.Cb[i], img.Cr[i] = byte(rng.Uint32()), byte(rng.Uint32())
	}
	f := toRGB(img)
	for _, p := range []image.Point{{0, 0}, {129, 69}, {64, 33}, {7, 50}} {
		r, g, b, _ := img.At(p.X, p.Y).RGBA()
		o := (p.Y*f.w + p.X) * 3
		if f.pix[o] != byte(r>>8) || f.pix[o+1] != byte(g>>8) || f.pix[o+2] != byte(b>>8) {
			t.Errorf("%v: got %v, want %d %d %d", p, f.pix[o:o+3], r>>8, g>>8, b>>8)
		}
	}
}

// Closing the browser pane closes Chrome too; the next call starts it again.
func TestClosingThePaneStopsChrome(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	if _, err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path, err := s.Serve(dir)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); s.viewers() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the viewer never connected")
		}
	}
	conn.Close() // the user closes the pane
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		s.mu.Lock()
		down := s.root == nil
		s.mu.Unlock()
		if down {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Chrome is still running after the pane closed")
		}
	}
	if notes := strings.Join(s.takeNotes(), " "); !strings.Contains(notes, "closed the browser pane") {
		t.Errorf("the model should be told: %q", notes)
	}
	if _, err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("the next call should start Chrome again: %v", err)
	}
}

func TestAddressBarEditing(t *testing.T) {
	var a addressBar
	a.begin("https://old.example/")
	a.key(tea.Key{Code: 'n', Text: "n"}) // typing replaces the selection
	a.insert("ews.example")
	if got := string(a.text); got != "news.example" || a.cursor != len(a.text) {
		t.Fatalf("after typing: %q at %d", got, a.cursor)
	}
	a.key(tea.Key{Code: tea.KeyHome})
	a.key(tea.Key{Code: tea.KeyDelete})
	a.key(tea.Key{Code: tea.KeyEnd})
	a.key(tea.Key{Code: tea.KeyBackspace})
	a.key(tea.Key{Code: tea.KeyLeft})
	a.insert("X")
	if got := string(a.text); got != "ews.exampXl" {
		t.Fatalf("after editing: %q", got)
	}
	a.begin("abc")
	a.key(tea.Key{Code: tea.KeyBackspace})
	if len(a.text) != 0 {
		t.Fatalf("backspace on a selection clears it: %q", string(a.text))
	}
	if a.key(tea.Key{Code: tea.KeyF5}) {
		t.Fatal("a key that doesn't edit is not taken")
	}
}

func TestToolbarLayout(t *testing.T) {
	v := newViewer(kitty{})
	defer v.close()
	v.cols, v.tab = 80, "T"
	v.targets["T"] = targetInfo{URL: "https://example.com/a/very/long/path/that/goes/on/and/on/and/on/forever/and/ever"}
	v.state.Driving = "explore"
	for x, want := range map[int]toolbarHit{0: hitBack, 2: hitBack, 3: hitForward, 7: hitReload, 12: hitAddress, 79: hitNone} {
		if got := v.hitToolbar(x); got != want {
			t.Errorf("column %d: hit %v, want %v", x, got, want)
		}
	}
	line := v.toolbarLine()
	if w := ansi.StringWidth(line); w != 80 {
		t.Errorf("toolbar is %d cells, want 80: %q", w, ansi.Strip(line))
	}
	if plain := ansi.Strip(line); !strings.HasPrefix(plain, " ←  →  ↻  │ https://example.com/") || !strings.Contains(plain, "explore is driving") {
		t.Errorf("toolbar: %q", plain)
	}
	v.editing = true
	v.address.begin("abc")
	if w := ansi.StringWidth(v.toolbarLine()); w != 80 {
		t.Errorf("while editing the toolbar is %d cells", w)
	}
}

func TestInputQueueMergesMovesAndWheel(t *testing.T) {
	q := &inputQueue{kick: make(chan struct{}, 1)} // no sender: items stay queued
	move := func(x float64) inputItem {
		return inputItem{kind: inputMove, session: "S", method: "Input.dispatchMouseEvent", params: map[string]any{"type": "mouseMoved", "x": x}}
	}
	wheel := func(dy float64) inputItem {
		return inputItem{kind: inputWheel, session: "S", method: "Input.dispatchMouseEvent", params: map[string]any{"deltaX": 0.0, "deltaY": dy, "x": 1.0, "y": 1.0}}
	}
	q.push(move(1))
	q.push(move(2))
	q.push(inputItem{session: "S", method: "Input.dispatchMouseEvent", params: map[string]any{"type": "mousePressed"}})
	q.push(wheel(40))
	q.push(wheel(40))
	q.push(move(3))
	if len(q.items) != 4 {
		t.Fatalf("%d items, want move, press, one wheel, move: %+v", len(q.items), q.items)
	}
	if x := q.items[0].params.(map[string]any)["x"]; x != 2.0 {
		t.Errorf("the queued move should be the latest position, got %v", x)
	}
	if dy := q.items[2].params.(map[string]any)["deltaY"]; dy != 80.0 {
		t.Errorf("wheel ticks should add up, got %v", dy)
	}
}

func TestWheelAxisLock(t *testing.T) {
	q := &inputQueue{}
	now := time.Now()
	tick := func(b tea.MouseButton, shift bool) (float64, float64, bool) {
		now = now.Add(10 * time.Millisecond)
		return q.wheelDelta(b, shift, now)
	}
	if _, dy, ok := tick(tea.MouseWheelDown, false); !ok || dy != wheelStep {
		t.Fatal("a vertical tick scrolls")
	}
	if _, _, ok := tick(tea.MouseWheelRight, false); ok {
		t.Fatal("a sideways wobble in a vertical swipe is dropped")
	}
	now = now.Add(time.Second) // a new gesture
	if _, _, ok := tick(tea.MouseWheelRight, false); ok {
		t.Fatal("one sideways tick waits to see if the swipe goes sideways")
	}
	if dx, _, ok := tick(tea.MouseWheelRight, false); !ok || dx != 2*wheelStep {
		t.Fatalf("a second sideways tick scrolls, with the first: %v %v", dx, ok)
	}
	if _, _, ok := tick(tea.MouseWheelDown, false); ok {
		t.Fatal("once sideways, a vertical wobble is dropped")
	}
	now = now.Add(time.Second)
	if dx, dy, ok := tick(tea.MouseWheelDown, true); !ok || dx != wheelStep || dy != 0 {
		t.Fatalf("shift turns the wheel sideways: %v %v %v", dx, dy, ok)
	}
}

// As a panel the viewer draws at its place on screen, and deletes only its
// own images: the host may have others.
func TestEmbeddedViewerDrawsAtItsOrigin(t *testing.T) {
	v := newViewer(kitty{})
	defer v.close()
	v.embedded = true
	v.layout(40, 3, 30, 12)
	v.cellW, v.cellH, v.scale = 10, 20, 1 // 300x200 px of page
	run := func(f rgbFrame) string {
		cmd := v.draw(f)
		if cmd == nil {
			return ""
		}
		return cmd().(tea.RawMsg).Msg.(string)
	}
	full := run(rgbFrame{w: 300, h: 200, pix: make([]byte, 300*200*3)})
	// Row 3 + 2 header rows, column 40: 1-based "6;41".
	if !strings.Contains(full, "\x1b[6;41H") {
		t.Fatalf("full frame not at the origin: %.60q", full)
	}
	changed := rgbFrame{w: 300, h: 200, pix: make([]byte, 300*200*3)}
	o := (150*300 + 250) * 3 // pixel (250,150): cell (25,7) of the page
	changed.pix[o] = 255
	patch := run(changed)
	if !strings.Contains(patch, "\x1b[") || strings.Contains(patch, "\x1b[6;41H") {
		t.Fatalf("patch: %.60q", patch)
	}
	// Tile (192,128)-(256,192) starts at cell 19,6 of the page: screen 59,11.
	if !strings.Contains(patch, "\x1b[12;60H") {
		t.Errorf("patch not offset by the origin: %.60q", patch)
	}
	clear := v.clearImages()().(tea.RawMsg).Msg.(string)
	if strings.Contains(clear, "d=A") || !strings.Contains(clear, "d=I,i=1") || !strings.Contains(clear, "d=I,i=3") {
		t.Errorf("clearing must delete only the panel's images: %q", clear)
	}
	if m := v.local(tea.Mouse{X: 45, Y: 10}); m.X != 5 || m.Y != 7 {
		t.Errorf("a screen cell maps to %d,%d in the panel", m.X, m.Y)
	}
	if cmd := v.closeCmd(); cmd == nil {
		t.Fatal("closing a panel must not quit the host")
	} else if _, ok := cmd().(PanelClosedMsg); !ok {
		t.Fatal("closing a panel reports PanelClosedMsg")
	}
}

func TestWrapMarksPanelMessages(t *testing.T) {
	cmd := wrap(tea.Batch(func() tea.Msg { return noteMsg("x") }, tea.Raw("esc")))
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("a batch stays a batch: %#v", cmd())
	}
	if m, ok := batch[0]().(PanelMsg); !ok || m.msg != noteMsg("x") {
		t.Errorf("a panel message is wrapped: %#v", batch[0]())
	}
	if _, ok := batch[1]().(tea.RawMsg); !ok {
		t.Errorf("an escape for the terminal passes through")
	}
	if wrap(nil) != nil {
		t.Error("no command stays no command")
	}
}

// A quick second or third click on the same spot is a double or triple
// click; anywhere else, or later, starts over.
func TestCountClick(t *testing.T) {
	v := newViewer(kitty{})
	defer v.close()
	now := time.Now()
	at := func(x, y int, after time.Duration) int {
		now = now.Add(after)
		return v.countClick(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}, now)
	}
	if n := at(5, 5, 0); n != 1 {
		t.Fatalf("first click %d", n)
	}
	if n := at(5, 5, 100*time.Millisecond); n != 2 {
		t.Fatalf("double click %d", n)
	}
	if n := at(6, 5, 100*time.Millisecond); n != 3 {
		t.Fatalf("triple click, a cell over %d", n)
	}
	if n := at(6, 5, 100*time.Millisecond); n != 1 {
		t.Fatalf("a fourth click starts over: %d", n)
	}
	if n := at(6, 5, time.Second); n != 1 {
		t.Fatalf("a slow click is a first one: %d", n)
	}
	if n := at(20, 5, 50*time.Millisecond); n != 1 {
		t.Fatalf("a click elsewhere is a first one: %d", n)
	}
}

// A drag that starts on the page and lets go outside it still releases
// the button in the page, at its edge.
func TestReleaseOffThePageEndsTheDrag(t *testing.T) {
	v := newViewer(kitty{})
	defer v.close()
	v.inputs.close()
	v.inputs = &inputQueue{kick: make(chan struct{}, 1), done: make(chan struct{})} // nothing sends
	v.session = "s"
	v.layout(40, 3, 30, 12)
	v.cellW, v.cellH, v.scale = 10, 20, 1
	v.Update(tea.MouseClickMsg{X: 50, Y: 8, Button: tea.MouseLeft})
	if !v.pressed {
		t.Fatal("a press on the page is held")
	}
	v.Update(tea.MouseReleaseMsg{X: 5, Y: 1, Button: tea.MouseLeft})
	if v.pressed {
		t.Fatal("the release ends the press")
	}
	items := v.inputs.items
	if len(items) != 2 {
		t.Fatalf("press and release go to the page: %d items", len(items))
	}
	p := items[1].params.(map[string]any)
	if p["type"] != "mouseReleased" || p["x"].(float64) != 5 || p["y"].(float64) != 10 {
		t.Errorf("release clamped to the page's top-left: %v", p)
	}
	if v.Update(tea.MouseReleaseMsg{X: 5, Y: 1}); len(v.inputs.items) != 2 {
		t.Error("a release with nothing pressed, off the page, goes nowhere")
	}
}

func TestNotesExpire(t *testing.T) {
	v := newViewer(kitty{})
	defer v.close()
	_, cmd := v.Update(noteMsg("can't reach it"))
	if cmd == nil || v.note == "" {
		t.Fatal("a note shows, with a timer to end it")
	}
	gen := v.noteGen
	v.Update(noteMsg("another"))
	v.Update(noteEndMsg{gen})
	if v.note != "another" {
		t.Fatalf("an old timer leaves a newer note: %q", v.note)
	}
	v.Update(noteEndMsg{v.noteGen})
	if v.note != "" {
		t.Fatalf("the note ends: %q", v.note)
	}
}

// While the user works in a tab, an agent using another doesn't pull the
// view away.
func TestUserHoldsTheTab(t *testing.T) {
	v := newViewer(kitty{})
	defer v.close()
	if v.holding() != 0 {
		t.Fatal("no tab, nothing to hold")
	}
	v.tab = "a"
	if v.holding() != 0 {
		t.Fatal("untouched, the view follows the agent")
	}
	v.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if d := v.holding(); d <= 0 || d > holdFor {
		t.Fatalf("typing holds the tab: %v", d)
	}
	v.touched = time.Now().Add(-holdFor - time.Second)
	if v.holding() != 0 {
		t.Fatal("the hold runs out")
	}
}

func TestPanelHeaderShowsFocus(t *testing.T) {
	v := newViewer(kitty{})
	defer v.close()
	v.embedded, v.cols, v.focusHint = true, 100, "f3"
	h := v.header()
	if !strings.Contains(h[1], "f3 or click to use the browser") || !strings.HasPrefix(h[1], "\x1b[2m") {
		t.Errorf("unfocused: dim, with the way in: %q", h[1])
	}
	v.focused = true
	h = v.header()
	if !strings.Contains(h[1], "f3 chat") || strings.HasPrefix(h[1], "\x1b[2m") {
		t.Errorf("focused: bright, with the way back: %q", h[1])
	}
}
