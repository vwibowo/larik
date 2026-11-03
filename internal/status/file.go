package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// File is a session's status as other processes see it: what the process
// that has the session open last reported.
type File struct {
	Report
	// PID is the process that has the session open, or 0 once it let go.
	PID      int    `json:"pid,omitempty"`
	FrontEnd string `json:"front_end,omitempty"` // "tui", "headless" or "serve"
	// Tab is the larik desktop tab the process runs in, from TabEnv, so
	// the window knows which session a new tab went on to open.
	Tab     string    `json:"tab,omitempty"`
	Updated time.Time `json:"updated"`
}

// TabEnv names, in a process larik desktop started, the tab it runs in.
const TabEnv = "LARIK_DESKTOP_TAB"

// Running reports whether the process that wrote f still has the session.
func (f File) Running() bool { return f.PID > 0 && alive(f.PID) }

// Current is f's report as of now. Working and blocked mean something only
// while their process runs; from one that has exited they read as idle,
// the rule OSC 7501 gives terminals. Done and error stay.
func (f File) Current() Report {
	if f.State.Live() && !f.Running() {
		return Report{State: Idle, Title: f.Title}
	}
	return f.Report
}

// Path is where a session's status file sits: beside its transcript.
func Path(sessionPath string) string {
	return strings.TrimSuffix(sessionPath, ".jsonl") + ".status.json"
}

// Read loads a session's status file. ok is false when there is none or
// it can't be read; the session is then idle as far as anyone knows.
func Read(sessionPath string) (f File, ok bool) {
	data, err := os.ReadFile(Path(sessionPath))
	if err != nil || json.Unmarshal(data, &f) != nil || f.State == "" {
		return File{}, false
	}
	return f, true
}

// Writer keeps one session's status file up to date for this process.
type Writer struct {
	path     string
	frontEnd string
	tab      string

	mu   sync.Mutex
	last Report
	set  bool
}

// NewWriter writes the status of the session at sessionPath on behalf of
// frontEnd. It writes nothing until the first Set. Without a session it
// returns nil, which writes nothing at all.
func NewWriter(sessionPath, frontEnd string) *Writer {
	if sessionPath == "" {
		return nil
	}
	return &Writer{path: Path(sessionPath), frontEnd: frontEnd, tab: os.Getenv(TabEnv)}
}

// Set records r if it differs from the last report. Errors are dropped:
// the status file is a courtesy to other processes, never a reason to
// stop a session.
func (w *Writer) Set(r Report) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.set && r == w.last {
		return
	}
	w.last, w.set = r, true
	w.write(os.Getpid())
}

// Release says this process no longer has the session open, keeping the
// last report, so a done or failed turn still shows.
func (w *Writer) Release() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.set {
		w.write(0)
	}
}

// write replaces the file in one rename, so a reader never sees half of it.
func (w *Writer) write(pid int) {
	data, err := json.Marshal(File{Report: w.last, PID: pid, FrontEnd: w.frontEnd, Tab: w.tab, Updated: time.Now().UTC()})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".status-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil || os.Rename(tmp.Name(), w.path) != nil {
		os.Remove(tmp.Name())
	}
}
