package termbrowser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// inputSession is a viewer on a page wider and taller than the pane, with
// a text field at the top.
func inputSession(t *testing.T) (*Session, *loop) {
	t.Helper()
	path := chrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><title>Big</title><style>body{margin:0;width:6000px;height:20000px}</style><input id=f style="position:fixed;top:0;left:0">`))
	}))
	t.Cleanup(srv.Close)
	profile, err := os.MkdirTemp("", "larik-browser-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(profile) })
	s := New(Options{ChromePath: path, ProfileDir: profile})
	s.startupTimeout = testStartupTimeout
	t.Cleanup(s.Close)
	if _, err := s.Navigate(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock, err := s.Serve(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := &loop{t: t, msgs: make(chan tea.Msg, 512)}
	l.v = newViewer(kitty{})
	l.v.send = func(m tea.Msg) { l.msgs <- m }
	t.Cleanup(l.v.close)
	go Subscribe(sock, func(st State) { l.msgs <- stateMsg(st) })
	l.msgs <- tea.WindowSizeMsg{Width: 100, Height: 40}
	l.until("the tab", func() bool { return l.v.session != "" && l.v.hooked == l.v.session })
	return s, l
}

func pageNumber(s *Session, js string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, _ := s.Eval(ctx, js)
	n, _ := strconv.Atoi(strings.Trim(out, `"`))
	return n
}

// A trackpad swipe down sends mostly vertical wheel events with a few
// sideways ones mixed in; the page must scroll down, not drift sideways.
func TestTrackpadScrollStaysVertical(t *testing.T) {
	s, l := inputSession(t)
	at := tea.Mouse{X: 50, Y: 20}
	for i := 0; i < 30; i++ {
		m := at
		m.Button = tea.MouseWheelDown
		if i%4 == 3 {
			m.Button = tea.MouseWheelRight
		}
		l.msgs <- tea.MouseWheelMsg(m)
	}
	l.until("the scroll", func() bool { return pageNumber(s, "Math.round(scrollY)") > 0 })
	settle := time.Now().Add(700 * time.Millisecond)
	l.until("the scroll to settle", func() bool { return time.Now().After(settle) })
	if x := pageNumber(s, "Math.round(scrollX)"); x != 0 {
		t.Errorf("a vertical swipe scrolled %dpx sideways", x)
	}
	y := pageNumber(s, "Math.round(scrollY)")
	if y < 400 || y > 2000 {
		t.Errorf("23 wheel ticks down scrolled %dpx; want a few lines each", y)
	}

	// A sideways swipe on its own still scrolls sideways.
	time.Sleep(400 * time.Millisecond)
	for i := 0; i < 6; i++ {
		l.msgs <- tea.MouseWheelMsg(tea.Mouse{X: 50, Y: 20, Button: tea.MouseWheelRight})
	}
	l.until("a sideways scroll", func() bool { return pageNumber(s, "Math.round(scrollX)") > 0 })
}

// Fast typing arrives in order.
func TestTypingStaysInOrder(t *testing.T) {
	s, l := inputSession(t)
	if _, err := s.Eval(context.Background(), "document.getElementById('f').focus()"); err != nil {
		t.Fatal(err)
	}
	const text = "the quick brown fox jumps over the lazy dog 0123456789"
	for _, r := range text {
		l.msgs <- tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})
	}
	var got string
	l.until("the typing", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		got, _ = s.Eval(ctx, "document.getElementById('f').value")
		return len(got) >= len(text)+2
	})
	if got != `"`+text+`"` {
		t.Errorf("typed %s", got)
	}
}
