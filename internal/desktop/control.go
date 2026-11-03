package desktop

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"larik/internal/session"
	"larik/internal/status"
)

// Commands drive the window from outside it, as a script or an agent in
// one of its tabs would: list the tabs, open a session, type into a tab,
// read what it shows, wait for its session to finish, and post a
// notification. larik desktop list, open, send, read, wait, focus, close
// and notify send them (see Call).

// Request is a command for the window.
type Request struct {
	Cmd string `json:"cmd"` // list, open, send, read, wait, focus, close or notify
	// Tab is the tab a command acts on: its id, as LARIK_DESKTOP_TAB
	// says it in the tab, or the id of the session it has open, or
	// enough of one to tell. "focused" is the tab with the keyboard.
	Tab string `json:"tab,omitempty"`
	// Session is the session open opens, or wait waits on when it runs
	// outside the window, by id or enough of one to tell.
	Session string `json:"session,omitempty"`
	Cwd     string `json:"cwd,omitempty"`    // where open starts a new session
	Prompt  string `json:"prompt,omitempty"` // what open's session starts with
	Split   bool   `json:"split,omitempty"`  // open beside the main pane
	// Shell opens a terminal tab in Cwd, running Run through your shell,
	// or the shell itself when Run is empty.
	Shell bool   `json:"shell,omitempty"`
	Run   string `json:"run,omitempty"`
	// Background opens a tab without showing it, leaving the panes as
	// they are.
	Background bool   `json:"background,omitempty"`
	Text       string `json:"text,omitempty"`  // what send types
	Enter      bool   `json:"enter,omitempty"` // send presses Return after
	Lines      int    `json:"lines,omitempty"` // the last lines read returns, 0 for all
	// State is what wait waits for: one state or several, comma
	// separated; done when empty.
	State   string        `json:"state,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"` // how long wait waits, 0 for as long as it takes
	Force   bool          `json:"force,omitempty"`   // close a tab that's working
	Always  bool          `json:"always,omitempty"`  // allow answers the prompt's second option
	Title   string        `json:"title,omitempty"`   // notify's
	Body    string        `json:"body,omitempty"`    // notify's
}

// Response is the window's answer.
type Response struct {
	Error string    `json:"error,omitempty"`
	Tabs  []TabInfo `json:"tabs,omitempty"` // list's
	Tab   *TabInfo  `json:"tab,omitempty"`  // the tab acted on
	Text  string    `json:"text,omitempty"` // read's
	State string    `json:"state,omitempty"`
}

// TabInfo is a tab as commands see it.
type TabInfo struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`              // larik, or terminal
	Run     string `json:"run,omitempty"`     // a terminal tab's program
	Session string `json:"session,omitempty"` // its session's id, once it has one
	Path    string `json:"path,omitempty"`    // its session's transcript
	Dir     string `json:"dir"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Wants   string `json:"wants,omitempty"` // what a blocked session waits for
	Msg     string `json:"msg,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Ports   []int  `json:"ports,omitempty"`
	Shown   bool   `json:"shown,omitempty"`   // in the main pane or the split
	Focused bool   `json:"focused,omitempty"` // has the keyboard
	Exited  bool   `json:"exited,omitempty"`
	Code    int    `json:"code,omitempty"`
}

// control answers a request, off the main thread: wait waits there, and
// the others run on the main thread and wait for it.
func (a *App) control(req Request) Response {
	switch req.Cmd {
	case "wait":
		return a.wait(req)
	case "allow", "deny":
		return a.controlAnswer(req)
	case "list", "open", "send", "read", "focus", "close", "notify":
	default:
		return Response{Error: fmt.Sprintf("unknown command %q", req.Cmd)}
	}
	var res Response
	if !a.onMain(func() { res = a.command(req) }) {
		return Response{Error: "larik desktop is closing"}
	}
	return res
}

// onMain runs fn on the main thread and waits for it, reporting whether
// it ran: not when the window closes first.
func (a *App) onMain(fn func()) bool {
	ran := make(chan struct{})
	a.update(func() {
		defer close(ran)
		fn()
	})
	select {
	case <-ran:
		return true
	case <-a.done:
		return false
	}
}

// command runs every command but wait, on the main thread.
func (a *App) command(req Request) Response {
	switch req.Cmd {
	case "list":
		var tabs []TabInfo
		for _, t := range a.tabs {
			tabs = append(tabs, a.tabInfo(t))
		}
		return Response{Tabs: tabs}
	case "open":
		return a.controlOpen(req)
	case "notify":
		if req.Body == "" && req.Title == "" {
			return Response{Error: "notify needs something to say"}
		}
		title := req.Title
		if title == "" {
			title = "Larik"
		}
		a.notice = strings.TrimSuffix(title+": "+req.Body, ": ")
		a.record(event{title: title, body: req.Body})
		if a.post != nil {
			a.post(title, req.Body)
		}
		return Response{}
	}
	t, err := a.tabRef(req.Tab)
	if err != "" {
		return Response{Error: err}
	}
	switch req.Cmd {
	case "send":
		if t.exited || t.term == nil {
			return Response{Error: "the tab's larik has exited"}
		}
		if strings.ContainsAny(req.Text, "\r\n") {
			t.term.Paste(req.Text) // as one, not a line at a time
		} else {
			t.term.Send([]byte(req.Text))
		}
		if req.Enter {
			t.term.Send([]byte("\r"))
		}
	case "read":
		if t.term == nil {
			return Response{Error: "the tab has no terminal"}
		}
		info := a.tabInfo(t)
		return Response{Tab: &info, Text: lastLines(t.term.Text(), req.Lines)}
	case "focus":
		a.activate(t)
		if a.raise != nil {
			a.raise()
		}
	case "close":
		if !req.Force && a.tabStatus(t).State == status.Working {
			return Response{Error: "the tab is working: close it with --force to stop it mid-turn"}
		}
		a.closeTab(t)
		info := a.tabInfo(t)
		return Response{Tab: &info}
	}
	info := a.tabInfo(t)
	return Response{Tab: &info}
}

// controlOpen opens a session in a tab: one that exists, by id, or a new
// one in a directory, starting with a prompt when there is one.
func (a *App) controlOpen(req Request) Response {
	var t *tab
	switch {
	case req.Session != "":
		s, err := a.sessionRef(req.Session)
		if err != "" {
			return Response{Error: err}
		}
		if t = a.tabOf(s.Path); t == nil && req.Prompt != "" {
			if s.Running && !a.ours(s.Tab) {
				return Response{Error: "the session is open in another terminal"}
			}
			t = a.startTab(s.Cwd, s.ID, s.Path, "--", req.Prompt)
		} else {
			if t == nil {
				t = a.sessionTab(s.Path)
			}
			if t != nil && req.Prompt != "" && !t.exited {
				t.term.Send([]byte(req.Prompt + "\r"))
			}
		}
	case req.Shell || req.Run != "":
		if req.Cwd == "" {
			return Response{Error: "open needs a directory for the terminal"}
		}
		t = a.startShell(req.Cwd, req.Run)
	case req.Cwd != "":
		var flags []string
		if req.Prompt != "" {
			flags = []string{"--", req.Prompt}
		}
		t = a.startTab(req.Cwd, "", "", flags...)
	default:
		return Response{Error: "open needs a directory or a session"}
	}
	if t == nil {
		msg := a.notice
		if msg == "" {
			msg = "couldn't open a tab"
		}
		return Response{Error: msg}
	}
	switch {
	case req.Background:
	case req.Split:
		a.showInSplit(t)
	default:
		a.activate(t)
	}
	info := a.tabInfo(t)
	return Response{Tab: &info}
}

// wait waits for a tab's session, or a session running elsewhere, to be
// in one of the states asked for. Done and error say a turn ended, so
// only those reported after the wait began count; the others count as
// they are, as a session already waiting for you is.
func (a *App) wait(req Request) Response {
	want := map[status.State]bool{}
	for _, s := range strings.Split(cmp.Or(req.State, string(status.Done)), ",") {
		st := status.State(strings.TrimSpace(s))
		switch st {
		case status.Idle, status.Working, status.Done, status.Blocked, status.Error:
			want[st] = true
		default:
			return Response{Error: fmt.Sprintf("unknown state %q: idle, working, done, blocked or error", s)}
		}
	}
	start := time.Now()
	var deadline <-chan time.Time
	if req.Timeout > 0 {
		timer := time.NewTimer(req.Timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	poll := time.NewTicker(400 * time.Millisecond)
	defer poll.Stop()
	var path string // the session's transcript, once known
	for {
		var (
			info   TabInfo
			gone   bool
			errMsg string
		)
		ok := a.onMain(func() {
			if req.Session != "" && req.Tab == "" {
				s, err := a.sessionRef(req.Session)
				path, errMsg = s.Path, err
				return
			}
			t, err := a.tabRef(req.Tab)
			switch {
			case err != "" && path == "":
				errMsg = err
			case err != "":
				gone = true // closed: its session's status still says
			default:
				info = a.tabInfo(t)
				if info.Path != "" {
					path = info.Path
				}
			}
		})
		switch {
		case !ok:
			return Response{Error: "larik desktop is closing"}
		case errMsg != "":
			return Response{Error: errMsg}
		case info.Exited && info.Code != 0 && want[status.Error]:
			return Response{Tab: &info, State: string(status.Error)}
		case info.Exited && info.Code != 0:
			return Response{Tab: &info, State: string(status.Error), Error: fmt.Sprintf("larik exited with status %d", info.Code)}
		}
		if path != "" {
			if f, ok := status.Read(path); ok {
				cur := f.Current()
				ended := cur.State == status.Done || cur.State == status.Error
				if want[cur.State] && (!ended || f.Updated.After(start)) {
					if info.ID == "" {
						return Response{State: string(cur.State)}
					}
					info.State, info.Wants, info.Msg = string(cur.State), string(cur.Kind), cur.Msg
					return Response{Tab: &info, State: string(cur.State)}
				}
			}
		}
		if gone && path == "" {
			return Response{Error: "the tab closed"}
		}
		select {
		case <-poll.C:
		case <-deadline:
			return Response{Error: fmt.Sprintf("timed out after %s waiting for %s", req.Timeout, cmp.Or(req.State, string(status.Done)))}
		case <-a.done:
			return Response{Error: "larik desktop is closing"}
		}
	}
}

// controlAnswer answers the prompt a tab's session waits on, and returns
// once the session has gone on from it, with what it does now, so that a
// wait after doesn't find the prompt answered still waiting; or says the
// tab didn't take the answer.
func (a *App) controlAnswer(req Request) Response {
	an := deny
	switch {
	case req.Cmd == "allow" && req.Always:
		an = allowAlways
	case req.Cmd == "allow":
		an = allowOnce
	}
	var (
		info   TabInfo
		before status.File
		errMsg string
	)
	if !a.onMain(func() {
		t, err := a.tabRef(req.Tab)
		switch {
		case err != "":
			errMsg = err
		case t.path == "":
			errMsg = "the tab has no session waiting for an answer"
		default:
			info = a.tabInfo(t)
			before, _ = status.Read(t.path)
			errMsg = a.answerSession(t.path, an)
		}
	}) {
		return Response{Error: "larik desktop is closing"}
	}
	if errMsg != "" {
		return Response{Error: errMsg}
	}
	deadline := time.Now().Add(answerChecked)
	for time.Now().Before(deadline) {
		if f, ok := status.Read(info.Path); ok && f.Updated.After(before.Updated) {
			cur := f.Current()
			info.State, info.Wants, info.Msg = string(cur.State), string(cur.Kind), cur.Msg
			return Response{Tab: &info, State: string(cur.State)}
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-a.done:
			return Response{Error: "larik desktop is closing"}
		}
	}
	return Response{Tab: &info, Error: "the tab didn't take the answer: something in it has the keys, as the diff or a reason being typed"}
}

// tabRef finds the tab ref names (see Request.Tab), or says why not.
func (a *App) tabRef(ref string) (*tab, string) {
	switch ref {
	case "":
		return nil, "which tab? Give its id, or its session's, with --tab"
	case "focused":
		if t := a.focused(); t != nil {
			return t, ""
		}
		return nil, "no tab is open"
	}
	var found []*tab
	for _, t := range a.tabs {
		if t.id == ref {
			return t, ""
		}
		if s, _, ok := a.find(t.path); ok && strings.HasPrefix(s.ID, ref) {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Sprintf("no tab %q is open", ref)
	case 1:
		return found[0], ""
	}
	return nil, fmt.Sprintf("%q is the start of %d tabs' sessions: give more of it", ref, len(found))
}

// sessionRef finds the session ref names, by id or the start of one.
func (a *App) sessionRef(ref string) (session.Indexed, string) {
	var found []session.Indexed
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if s.ID == ref {
				return s, ""
			}
			if strings.HasPrefix(s.ID, ref) {
				found = append(found, s)
			}
		}
	}
	switch len(found) {
	case 0:
		return session.Indexed{}, fmt.Sprintf("no session %q", ref)
	case 1:
		return found[0], ""
	}
	return session.Indexed{}, fmt.Sprintf("%q is the start of %d sessions: give more of it", ref, len(found))
}

// tabInfo describes t for commands.
func (a *App) tabInfo(t *tab) TabInfo {
	st := a.tabStatus(t)
	info := TabInfo{
		ID: t.id, Path: t.path, Dir: t.dir, Title: a.tabTitle(t),
		Kind: "larik", Run: t.run,
		State: string(st.State), Wants: string(st.Kind), Msg: st.Msg,
		Ports:   a.ports[t.path],
		Shown:   t == a.active || t == a.right,
		Focused: t == a.focused() && slices.Contains(a.tabs, t),
		Exited:  t.exited, Code: t.code,
	}
	if t.shell() {
		info.Kind = "terminal"
	}
	if s, _, ok := a.find(t.path); ok {
		info.Session = s.ID
		info.Branch = a.git[s.Cwd].Branch
	}
	if info.Branch == "" {
		info.Branch = a.git[t.dir].Branch
	}
	return info
}

// lastLines is text's last n lines, all for 0, without the blank ones
// under what the screen shows.
func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, " \n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
