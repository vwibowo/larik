package termbrowser

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// loop plays the Bubble Tea runtime for a viewer: it runs commands, feeds
// their messages back to Update, and keeps what the viewer wrote raw.
type loop struct {
	t    *testing.T
	v    *viewer
	msgs chan tea.Msg
	mu   sync.Mutex
	raw  strings.Builder
}

func (l *loop) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() { l.deliver(cmd()) }()
}

func (l *loop) deliver(msg tea.Msg) {
	switch m := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range m {
			l.run(c)
		}
	case tea.RawMsg:
		l.mu.Lock()
		l.raw.WriteString(m.Msg.(string))
		l.mu.Unlock()
	default:
		// tea.Sequence's message is an unexported slice of commands.
		if rv := reflect.ValueOf(msg); rv.Kind() == reflect.Slice && rv.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			for i := 0; i < rv.Len(); i++ {
				if c := rv.Index(i).Interface().(tea.Cmd); c != nil {
					l.deliver(c())
				}
			}
			return
		}
		l.msgs <- msg
	}
}

// until handles messages until ok holds.
func (l *loop) until(what string, ok func() bool) {
	l.t.Helper()
	deadline := time.After(20 * time.Second)
	for !ok() {
		select {
		case msg := <-l.msgs:
			_, cmd := l.v.Update(msg)
			l.run(cmd)
		case <-deadline:
			l.t.Fatalf("timed out waiting for %s", what)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (l *loop) output() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.String()
}

func TestViewerDrawsAndTakesInput(t *testing.T) {
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
	l := &loop{t: t, msgs: make(chan tea.Msg, 256)}
	l.v = newViewer(kitty{})
	l.v.send = func(m tea.Msg) { l.msgs <- m }
	defer l.v.close()
	go Subscribe(path, func(st State) { l.msgs <- stateMsg(st) })
	l.msgs <- tea.WindowSizeMsg{Width: 100, Height: 40} // 10x20 cells off a tty: 1000x760 px

	l.until("a full frame", func() bool { return strings.Contains(l.output(), "a=T,q=2,f=24,s=1000,v=760,i=1,") })
	if v := l.v.View(); !strings.Contains(v.Content, "Test form") {
		t.Errorf("the header should show the tab's title: %q", v.Content)
	}

	// Click the "Log" button where it is on screen, through the viewer.
	var rect struct{ X, Y float64 }
	if out, err := s.Eval(ctx, `(() => { const b = [...document.querySelectorAll('button')].find(b => b.textContent === 'Log'); const r = b.getBoundingClientRect(); return {x: r.x + r.width/2, y: r.y + r.height/2}; })()`); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal([]byte(out), &rect); err != nil {
		t.Fatal(err)
	}
	cell := tea.Mouse{X: int(rect.X / 10), Y: headerRows + int(rect.Y/20), Button: tea.MouseLeft}
	l.msgs <- tea.MouseClickMsg(cell)
	l.msgs <- tea.MouseReleaseMsg(cell)
	l.until("the click", func() bool {
		title, _ := s.Eval(ctx, "document.title")
		return strings.Contains(title, "Clicked")
	})

	// Type into the focused name field.
	if _, err := s.Eval(ctx, "document.getElementById('name').focus()"); err != nil {
		t.Fatal(err)
	}
	for _, r := range "hi" {
		l.msgs <- tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})
	}
	l.until("the typing", func() bool {
		v, _ := s.Eval(ctx, "document.getElementById('name').value")
		return strings.Contains(v, "hi")
	})
	// The page changed, so frames keep coming, now as patches or a new frame.
	before := strings.Count(l.output(), "\x1b_Ga=T")
	l.until("more frames", func() bool { return strings.Count(l.output(), "\x1b_Ga=T") > before })
}

// On a Retina-class screen the page renders at 2x, and what reaches the
// terminal must be full resolution: Chrome's own screencast frames are
// half size there, and stretching them is what made text look blocky.
func TestViewerHiDPIFramesAreFullResolution(t *testing.T) {
	old := cellPixels
	cellPixels = func() (int, int, bool) { return 20, 40, true }
	t.Cleanup(func() { cellPixels = old })
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
	l := &loop{t: t, msgs: make(chan tea.Msg, 256)}
	l.v = newViewer(kitty{})
	l.v.send = func(m tea.Msg) { l.msgs <- m }
	defer l.v.close()
	go Subscribe(path, func(st State) { l.msgs <- stateMsg(st) })
	l.msgs <- tea.WindowSizeMsg{Width: 50, Height: 20} // 1000x720 device px at 2x
	l.until("a full-resolution frame", func() bool { return strings.Contains(l.output(), ",s=1000,v=720,") })
	if strings.Contains(l.output(), ",c=") {
		t.Error("no frame should need the terminal to scale it")
	}
}

// The toolbar's buttons and address field drive the shown tab.
func TestViewerToolbar(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	for _, path := range []string{"/", "/other"} {
		if _, err := s.Navigate(ctx, srv.URL+path); err != nil {
			t.Fatal(err)
		}
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
	l := &loop{t: t, msgs: make(chan tea.Msg, 256)}
	l.v = newViewer(kitty{})
	l.v.send = func(m tea.Msg) { l.msgs <- m }
	defer l.v.close()
	go Subscribe(path, func(st State) { l.msgs <- stateMsg(st) })
	l.msgs <- tea.WindowSizeMsg{Width: 100, Height: 40}
	l.until("the history", func() bool { return len(l.v.hist.ids) >= 2 && l.v.hist.index == len(l.v.hist.ids)-1 })
	url := func() string { u, _ := s.Eval(ctx, "location.pathname"); return u }
	click := func(x int) { l.msgs <- tea.MouseClickMsg(tea.Mouse{X: x, Y: 0, Button: tea.MouseLeft}) }

	click(1) // back
	l.until("going back", func() bool { return url() == `"/"` })
	l.until("forward enabled", func() bool { return l.v.hist.index < len(l.v.hist.ids)-1 })
	click(4) // forward
	l.until("going forward", func() bool { return url() == `"/other"` })

	click(20) // the address field
	l.until("editing", func() bool { return l.v.editing })
	for _, r := range srv.URL + "/done?name=Ann" {
		l.msgs <- tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})
	}
	l.msgs <- tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	l.until("the typed address", func() bool { return url() == `"/done"` })
	if title, _ := s.Eval(ctx, "document.title"); !strings.Contains(title, "Done") {
		t.Errorf("title after navigating: %s", title)
	}
	click(7) // reload: the page is still there afterwards
	l.until("the reload", func() bool { return !l.v.loading && url() == `"/done"` })
}

// Headless Chrome draws no popup for a <select>, so clicking one offers its
// options in the header, and the choice reaches the page with its events.
func TestViewerChoosesFromASelect(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	if _, err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(ctx, `(() => {
		document.body.innerHTML = '<select id=c style="position:fixed;left:0;top:0;width:200px;height:40px">' +
			'<option>Austria</option><option disabled>Belgium</option><option>Canada</option><option>Indonesia</option></select>' +
			'<a href="/next" style="position:fixed;left:0;top:200px;width:300px;height:40px;display:block">next</a>';
		document.getElementById('c').addEventListener('change', (e) => { document.title = 'changed ' + e.target.value; });
	})()`); err != nil {
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
	l := &loop{t: t, msgs: make(chan tea.Msg, 256)}
	l.v = newViewer(kitty{})
	l.v.send = func(m tea.Msg) { l.msgs <- m }
	defer l.v.close()
	go Subscribe(path, func(st State) { l.msgs <- stateMsg(st) })
	l.msgs <- tea.WindowSizeMsg{Width: 100, Height: 40}
	l.until("the hooks", func() bool { return l.v.hooked != "" && l.v.hooked == l.v.session })

	cell := tea.Mouse{X: 5, Y: headerRows + 1, Button: tea.MouseLeft}
	l.msgs <- tea.MouseClickMsg(cell)
	l.msgs <- tea.MouseReleaseMsg(cell)
	l.until("the options", func() bool { return l.v.choosing != nil })
	if h := l.v.header(); !strings.Contains(h[1], "Austria") || !strings.Contains(h[1], "1/4") {
		t.Errorf("the header offers the options: %q", h[1])
	}
	l.msgs <- tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}) // skips the disabled Belgium
	l.msgs <- tea.KeyPressMsg(tea.Key{Code: 'i', Text: "i"})
	l.until("the search", func() bool { return l.v.choosing != nil && l.v.choosing.index == 3 })
	l.msgs <- tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	l.until("the change", func() bool {
		title, _ := s.Eval(ctx, "document.title")
		return strings.Contains(title, "changed Indonesia")
	})

	// Resting on the link shows where it goes.
	l.msgs <- tea.MouseMotionMsg(tea.Mouse{X: 5, Y: headerRows + 10})
	l.until("the link", func() bool { return strings.HasSuffix(l.v.hover, "/next") })
	if h := l.v.header(); !strings.Contains(h[1], "/next") {
		t.Errorf("the header shows the link: %q", h[1])
	}
	l.msgs <- tea.MouseMotionMsg(tea.Mouse{X: 5, Y: 0}) // onto the toolbar
	l.until("the link to go", func() bool { return l.v.hover == "" })
}
