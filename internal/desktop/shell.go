package desktop

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"larik/internal/session"
	"larik/internal/status"
)

// Terminal tabs run your shell, or a program such as lazygit or a dev
// server, beside the sessions, as Rex and cmux keep them: the window is
// a terminal, and not only larik's. A program in one shows what it
// posts as a notification (OSC 9, OSC 777) and rings its bell on its
// tab. OSC 7501 reports from programs in terminal tabs show their live
// status, and a larik started in one can also use its status file, as the
// tab's id is in its environment.

// shellCommand is what a terminal tab runs: your login shell, or run
// through it, as an interactive shell reads your profile and its PATH.
func shellCommand(run string) []string {
	if runtime.GOOS == "windows" {
		sh := os.Getenv("COMSPEC")
		if sh == "" {
			sh = "cmd.exe"
		}
		if run == "" {
			return []string{sh}
		}
		return []string{sh, "/c", run}
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	if run == "" {
		return []string{sh, "-l"}
	}
	return []string{sh, "-ilc", run}
}

// startShell starts a terminal tab in dir, running run through the
// shell, or the shell itself for "", and adds it.
func (a *App) startShell(dir, run string) *tab {
	a.notice = ""
	if _, err := os.Stat(dir); err != nil && !isRemote(dir) {
		a.notice = "Can't open a terminal in " + dir + ": " + err.Error()
		return nil
	}
	t := &tab{id: newTabID(), dir: dir, run: run, cmd: shellCommand(run)}
	if !a.run(t, "") {
		return nil
	}
	a.tabs = append(a.tabs, t)
	return t
}

// openShell opens a terminal tab in the focused tab's directory.
func (a *App) openShell() {
	if t := a.startShell(a.currentDir(), ""); t != nil {
		a.activate(t)
	}
}

// shell reports whether t is a terminal tab rather than larik's.
func (t *tab) shell() bool { return t.cmd != nil }

// setProgramStatus updates the tab and keeps an in-app alert when a program
// starts waiting, finishes, or fails while another tab has the focus.
func (a *App) setProgramStatus(t *tab, r status.Report) {
	old := t.program
	if old == r {
		return
	}
	t.program, t.programAt = r, time.Now()
	if slices.Contains(a.tabs, t) && t.path == "" && r.State != old.State && (r.State == status.Blocked || r.State == status.Done || r.State == status.Error) {
		a.record(event{tab: t.id, title: a.tabTitle(t), body: r.Msg, state: r.State})
	}
}

// programNotify keeps what a program in a terminal tab posted (OSC 9,
// OSC 777) as a notification, rings the tab while you look elsewhere,
// and tells you from another app. larik's own tabs say what their
// sessions do through their status files instead.
func (a *App) programNotify(t *tab, title, body string) {
	if !t.shell() || t.path != "" || !slices.Contains(a.tabs, t) {
		return
	}
	if title == "" {
		title = a.tabTitle(t)
	}
	a.record(event{tab: t.id, title: title, body: body})
	if t != a.focused() {
		t.alerted = time.Now()
	}
	if a.ping != nil {
		a.ping(t.id, title, body)
	}
}

// rang rings a terminal tab you aren't looking at.
func (a *App) rang(t *tab) {
	if t.shell() && t != a.focused() {
		t.alerted = time.Now()
	}
}

// tabByID is the open tab with id.
func (a *App) tabByID(id string) *tab {
	for _, t := range a.tabs {
		if id != "" && t.id == id {
			return t
		}
	}
	return nil
}

// tabKey names a terminal or browser tab in the sidebar, where sessions go by
// their transcripts.
func tabKey(t *tab) string { return "tab:" + t.id }

// selectKey shows what the sidebar's key names: a terminal tab, or a
// session.
func (a *App) selectKey(key string) {
	if a.selectDetached(key) {
		return
	}
	if id, ok := strings.CutPrefix(key, "bookmark:"); ok {
		for _, b := range a.bookmarks.all() {
			if b.key() == id {
				a.openSavedBookmark(b)
				return
			}
		}
		return
	}
	if id, ok := strings.CutPrefix(key, "tab:"); ok {
		if t := a.tabByID(id); t != nil {
			a.activate(t)
		}
		return
	}
	if session.IsArchived(key) {
		a.openArchived(key)
		return
	}
	a.selectSession(key)
}
