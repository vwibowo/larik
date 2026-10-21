package termbrowser

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"

	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "browser.log")
	s := New(Options{LogFile: path})
	s.logger()("hello %d", 7)
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "hello 7") {
		t.Fatalf("log: %q %v", data, err)
	}
	New(Options{}).logger()("dropped") // no file: nothing happens
}

// chromedp reports DOM events it doesn't know, such as the one a modal
// dialog sends, and by default prints them on stderr: over the TUI. They
// must go to the log file instead.
func TestChromedpMessagesStayOffStderr(t *testing.T) {
	path := chrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><title>Dialog</title><dialog id=d>Hi</dialog>
<div popover id=p>Pop</div>
<script>setTimeout(() => { d.showModal(); p.showPopover(); }, 100)</script>`)
	}))
	defer srv.Close()
	profile, err := os.MkdirTemp("", "larik-browser-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profile)
	logFile := filepath.Join(t.TempDir(), "browser.log")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	captured := make(chan string)
	go func() { b, _ := io.ReadAll(r); captured <- string(b) }()

	s := New(Options{ChromePath: path, ProfileDir: profile, LogFile: logFile})
	s.startupTimeout = testStartupTimeout
	ctx := context.Background()
	_, navErr := s.Navigate(ctx, srv.URL)
	if navErr == nil {
		time.Sleep(500 * time.Millisecond)
		_, navErr = s.Snapshot(ctx, "")
	}
	s.Close()
	os.Stderr = stderr
	w.Close()
	out := <-captured
	if navErr != nil {
		t.Fatal(navErr)
	}
	if strings.Contains(out, "unhandled") || strings.Contains(out, "ERROR") {
		t.Fatalf("chromedp wrote to stderr: %q", out)
	}
	if logged, _ := os.ReadFile(logFile); !strings.Contains(string(logged), "unhandled node event") {
		t.Logf("the page did not make chromedp report anything this time: %q", logged)
	} else {
		t.Logf("routed to the log: %d bytes", len(logged))
	}
}

// Chrome runs muted unless sound is on: an agent browsing unwatched must
// not start playing a video's sound.
func TestChromeIsMutedUnlessSoundIsOn(t *testing.T) {
	path := chrome(t)
	if runtime.GOOS == "windows" {
		t.Skip("reads the process list with ps")
	}
	for _, sound := range []bool{false, true} {
		profile, err := os.MkdirTemp("", "larik-browser-test")
		if err != nil {
			t.Fatal(err)
		}
		s := New(Options{ChromePath: path, ProfileDir: profile, Sound: sound})
		s.startupTimeout = testStartupTimeout
		if _, err := s.Tab(context.Background()); err != nil {
			t.Fatal(err)
		}
		pid := profileLockPID(profile)
		out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
		s.Close()
		os.RemoveAll(profile)
		if pid == 0 || err != nil {
			t.Fatalf("finding Chrome: pid %d: %v", pid, err)
		}
		if muted := strings.Contains(string(out), "--mute-audio"); muted == sound {
			t.Errorf("sound=%v: Chrome started as %s", sound, out)
		}
	}
}
