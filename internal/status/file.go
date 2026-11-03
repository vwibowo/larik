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
	Updated time.Time `json:"updated"` // when the file was last written
	// Reported is when the report last changed, as against the usage
	// alone; files from before it have Updated only (see Since).
	Reported time.Time `json:"reported,omitzero"`
	// Usage is what the session has used, as its process last said.
	Usage Usage `json:"usage,omitzero"`
}

// Usage is what a session runs on and has used so far: its model and
// permission mode, what it has cost, and how much of the model's context
// its conversation takes. It goes in the status file, beside the report,
// rather than in OSC 7501, which has no place for it.
type Usage struct {
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Mode     string `json:"mode,omitempty"`
	// CostUSD is what the session has cost, as larik's footer counts it;
	// 0 for a model without a price, as one paid for by subscription.
	CostUSD float64 `json:"cost_usd,omitempty"`
	Tokens  int     `json:"tokens,omitempty"` // the conversation's context
	Window  int     `json:"window,omitempty"` // the model's context window
}

// ContextPercent is how much of the context window the conversation
// takes, or -1 when the window isn't known.
func (u Usage) ContextPercent() int {
	if u.Window <= 0 {
		return -1
	}
	return min(max(u.Tokens, 0)*100/u.Window, 100)
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

// Since is when f's report last changed: a newer one says the session
// moved on, where a newer Updated may only say what it used.
func (f File) Since() time.Time {
	if f.Reported.IsZero() {
		return f.Updated
	}
	return f.Reported
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

	mu       sync.Mutex
	last     Report
	reported time.Time
	usage    Usage
	set      bool
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
	w.last, w.set, w.reported = r, true, time.Now().UTC()
	w.write(os.Getpid())
}

// SetUsage records what the session has used, if it differs from the
// last; it shows from the next report on, or at once once there's one.
func (w *Writer) SetUsage(u Usage) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if u == w.usage {
		return
	}
	w.usage = u
	if w.set {
		w.write(os.Getpid())
	}
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
	data, err := json.Marshal(File{Report: w.last, PID: pid, FrontEnd: w.frontEnd, Tab: w.tab, Updated: time.Now().UTC(), Reported: w.reported, Usage: w.usage})
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
