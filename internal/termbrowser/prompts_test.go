package termbrowser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// promptSession is a browser with a viewer attached, on a server that
// asks for a password at /auth.
func promptSession(t *testing.T, opts Options) (*Session, *httptest.Server, *loop) {
	t.Helper()
	path := chrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/auth" {
			if u, p, ok := r.BasicAuth(); !ok || u != "ann" || p != "s3cret" {
				w.Header().Set("WWW-Authenticate", `Basic realm="staff"`)
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			w.Write([]byte("<title>Authed</title>"))
			return
		}
		w.Write([]byte("<title>Page</title>"))
	}))
	t.Cleanup(srv.Close)
	profile, err := os.MkdirTemp("", "larik-browser-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(profile) })
	opts.ChromePath, opts.ProfileDir = path, profile
	s := New(opts)
	s.startupTimeout = testStartupTimeout
	t.Cleanup(s.Close)
	if _, err := s.Navigate(context.Background(), srv.URL+"/"); err != nil {
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
	ctl, err := DialControl(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	l := &loop{t: t, msgs: make(chan tea.Msg, 256)}
	l.v = newViewer(kitty{})
	l.v.control = ctl
	l.v.send = func(m tea.Msg) { l.msgs <- m }
	t.Cleanup(l.v.close)
	go ctl.Run(func(st State) { l.msgs <- stateMsg(st) })
	l.msgs <- tea.WindowSizeMsg{Width: 120, Height: 40}
	l.until("the tab", func() bool { return l.v.session != "" && l.v.tab != "" && l.v.hooked == l.v.session })
	return s, srv, l
}

func typeKeys(l *loop, s string) {
	for _, r := range s {
		l.msgs <- tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})
	}
}

func TestSignInThroughThePane(t *testing.T) {
	s, srv, l := promptSession(t, Options{})
	done := make(chan error, 1)
	go func() { _, err := s.Navigate(context.Background(), srv.URL+"/auth"); done <- err }()
	l.until("the sign-in form", func() bool { return l.v.signin != nil })
	if line, _ := l.v.promptLine(120); !strings.Contains(line, "sign in to 127.0.0.1") || !strings.Contains(line, "(staff)") {
		t.Errorf("prompt: %q", line)
	}
	typeKeys(l, "ann")
	l.msgs <- tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	typeKeys(l, "s3cret")
	l.until("the password", func() bool { return l.v.signin != nil && string(l.v.signin.password) == "s3cret" })
	if line, _ := l.v.promptLine(120); strings.Contains(line, "s3cret") || !strings.Contains(line, "••••••") {
		t.Errorf("the password must be masked: %q", line)
	}
	l.msgs <- tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	l.until("the page", func() bool {
		// A short check: the loop must keep handing the viewer its keys.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		title, _ := s.Eval(ctx, "document.title")
		return title == `"Authed"`
	})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(s.takeNotes(), " ")
	if !strings.Contains(notes, "signed in") || strings.Contains(notes, "s3cret") {
		t.Errorf("notes for the model: %q", notes)
	}
}

func TestSignInWithoutAViewerIsCancelled(t *testing.T) {
	s, srv := newSession(t)
	_ = srv
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer authSrv.Close()
	_, _ = s.Navigate(context.Background(), authSrv.URL)
	if notes := strings.Join(s.takeNotes(), " "); !strings.Contains(notes, "browser_show") {
		t.Errorf("the model should be told the user can sign in through the pane: %q", notes)
	}
}

// evalAsync runs a script that waits on the user, without blocking the loop.
func evalAsync(s *Session, js string) <-chan string {
	out := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := s.Eval(ctx, js)
		if err != nil {
			r = "error: " + err.Error()
		}
		out <- r
	}()
	return out
}

func waitResult(t *testing.T, l *loop, ch <-chan string) string {
	t.Helper()
	var got string
	l.until("the page's answer", func() bool {
		select {
		case got = <-ch:
			return true
		default:
			return false
		}
	})
	return got
}

func TestPermissionPrompts(t *testing.T) {
	s, _, l := promptSession(t, Options{Location: &Location{Latitude: -6.2, Longitude: 106.8, Accuracy: 50}})

	// Notifications: allowed in the pane, then shown.
	res := evalAsync(s, "Notification.requestPermission()")
	l.until("the notification prompt", func() bool { return len(l.v.prompts) == 1 })
	if line, buttons := l.v.promptLine(120); !strings.Contains(line, "127.0.0.1") || !strings.Contains(line, "show notifications") || len(buttons) != 2 {
		t.Errorf("prompt: %q %v", line, buttons)
	}
	typeKeys(l, "y")
	if got := waitResult(t, l, res); got != `"granted"` {
		t.Fatalf("after Allow: %s", got)
	}
	if _, err := s.Eval(context.Background(), "new Notification('Build done', {body: 'all green'}); 1"); err != nil {
		t.Fatal(err)
	}
	l.until("the notification", func() bool { return strings.Contains(l.v.toast, "Build done: all green") })

	// Location: allowed, and the configured position is what the page gets.
	res = evalAsync(s, `new Promise((ok) => navigator.geolocation.getCurrentPosition((p) => ok(p.coords.latitude.toFixed(1) + ',' + p.coords.longitude.toFixed(1)), (e) => ok('error ' + e.code), {timeout: 10000}))`)
	l.until("the location prompt", func() bool { return len(l.v.prompts) == 1 && l.v.prompts[0].kind == "geolocation" })
	// Click "Allow" on the second row.
	_, buttons := l.v.promptLine(120)
	l.msgs <- tea.MouseClickMsg(tea.Mouse{X: buttons[0].start + 1, Y: 1, Button: tea.MouseLeft})
	if got := waitResult(t, l, res); got != `"-6.2,106.8"` {
		t.Fatalf("location after Allow: %s", got)
	}
}

func TestBlockedPermission(t *testing.T) {
	s, _, l := promptSession(t, Options{})
	res := evalAsync(s, "Notification.requestPermission()")
	l.until("the prompt", func() bool { return len(l.v.prompts) == 1 })
	typeKeys(l, "n")
	if got := waitResult(t, l, res); got != `"denied"` {
		t.Fatalf("after Block: %s", got)
	}
	// Without a configured location, Allow can't share one, and says so.
	res = evalAsync(s, `new Promise((ok) => navigator.geolocation.getCurrentPosition(() => ok('position'), (e) => ok('error ' + e.code), {timeout: 5000}))`)
	l.until("the location prompt", func() bool { return len(l.v.prompts) == 1 })
	typeKeys(l, "y")
	if got := waitResult(t, l, res); got != `"error 1"` {
		t.Fatalf("location with none set: %s", got)
	}
	if !strings.Contains(l.v.note, "browser.location") {
		t.Errorf("the pane should say how to share a location: %q", l.v.note)
	}
}

// With a host that can show the browser itself, browser_show uses it and
// splits nothing.
func TestShowUsesTheHostsPanel(t *testing.T) {
	s, _ := newSession(t)
	asked := 0
	s.SetShower(func() (string, bool) { asked++; return "opened the panel", true })
	msg, err := s.Show(context.Background())
	if err != nil || msg != "opened the panel" || asked != 1 {
		t.Fatalf("Show: %q %v (asked %d)", msg, err, asked)
	}
	s.SetShower(func() (string, bool) { return "", false })
	var launched []string
	s.paneEnv = func(key string) string {
		if key == "CMUX_SURFACE_ID" {
			return "surface:test"
		}
		return ""
	}
	s.paneRun = func(_ context.Context, args []string) error {
		launched = append([]string(nil), args...)
		return nil
	}
	if msg, err := s.Show(context.Background()); err != nil || !strings.Contains(msg, "new cmux pane") || len(launched) == 0 {
		t.Errorf("a host that declines uses the fake pane launcher: %q %v (%v)", msg, err, launched)
	}
}
