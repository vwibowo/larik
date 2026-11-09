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
	Tab       string `json:"tab,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Window    string `json:"window,omitempty"`
	Position  int    `json:"position,omitempty"`
	// Session is the session open opens, or wait waits on when it runs
	// outside the window, by id or enough of one to tell.
	Session string `json:"session,omitempty"`
	Cwd     string `json:"cwd,omitempty"`    // where open starts a new session
	Prompt  string `json:"prompt,omitempty"` // what open's session starts with
	// Split opens it in a new pane beside the focused one, or with Below
	// under it.
	Split bool `json:"split,omitempty"`
	Below bool `json:"below,omitempty"`
	// Shell opens a terminal tab in Cwd, running Run through your shell,
	// or the shell itself when Run is empty.
	Shell    bool   `json:"shell,omitempty"`
	Files    bool   `json:"files,omitempty"`
	Database bool   `json:"database,omitempty"`
	Run      string `json:"run,omitempty"`
	// Worktree opens the tab in a new worktree of the repository Cwd is
	// in, on the branch larik/<Branch>, or one named for it.
	Worktree bool   `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Project  string `json:"project,omitempty"`
	Name     string `json:"name,omitempty"`
	// URL opens a browser tab on the page, as a web address is typed, or
	// is where page's navigate goes.
	URL string `json:"url,omitempty"`
	// Op is what page does to a browser tab's page (see pagectl.go), and
	// Ref the element of its outline it clicks or types into; Text is
	// what type types or eval runs, and Enter submits what was typed.
	Op     string `json:"op,omitempty"`
	SQL    string `json:"sql,omitempty"`
	Schema string `json:"schema,omitempty"`
	Table  string `json:"table,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Ref    string `json:"ref,omitempty"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	// Background opens a pane without taking focus from the current one.
	Background bool   `json:"background,omitempty"`
	Text       string `json:"text,omitempty"`  // what send types
	Enter      bool   `json:"enter,omitempty"` // send presses Return after
	Lines      int    `json:"lines,omitempty"` // the last lines read returns, 0 for all
	// State is what wait waits for: one state or several, comma
	// separated; done when empty.
	State    string        `json:"state,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`   // how long wait waits, 0 for as long as it takes
	Force    bool          `json:"force,omitempty"`     // close a tab that's working
	Always   bool          `json:"always,omitempty"`    // allow answers the prompt's second option
	Title    string        `json:"title,omitempty"`     // notify's
	Body     string        `json:"body,omitempty"`      // notify's
	StatusID string        `json:"status_id,omitempty"` // status record within a terminal tab
	Kind     string        `json:"kind,omitempty"`      // blocked status kind
	Message  string        `json:"message,omitempty"`   // status message
	Agent    *agentResume  `json:"agent,omitempty"`     // native agent identity, when a hook supplies it
}

// Response is the window's answer.
type Response struct {
	Error         string             `json:"error,omitempty"`
	Tabs          []TabInfo          `json:"tabs,omitempty"` // list's
	Tab           *TabInfo           `json:"tab,omitempty"`  // the tab acted on
	Text          string             `json:"text,omitempty"` // read's
	State         string             `json:"state,omitempty"`
	Page          *PageResult        `json:"page,omitempty"` // page's
	Database      *DatabaseResponse  `json:"database,omitempty"`
	Panes         []PaneInfo         `json:"panes,omitempty"`
	Notifications []NotificationInfo `json:"notifications,omitempty"`
	Layouts       []string           `json:"layouts,omitempty"`
	Workspaces    []WorkspaceInfo    `json:"workspaces,omitempty"`
}

// TabInfo is a tab as commands see it.
type TabInfo struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace,omitempty"`
	Kind      string `json:"kind"`              // larik, terminal, browser, files, database, or chooser
	Run       string `json:"run,omitempty"`     // a terminal tab's program
	URL       string `json:"url,omitempty"`     // a browser tab's page
	Session   string `json:"session,omitempty"` // its session's id, once it has one
	Path      string `json:"path,omitempty"`    // its session's transcript
	Dir       string `json:"dir"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Wants     string `json:"wants,omitempty"` // what a blocked session waits for
	Msg       string `json:"msg,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Ports     []int  `json:"ports,omitempty"`
	// What its session runs on and has used: Context is how full its
	// context is, in percent, and CostUSD 0 for a model without a price.
	Model        string  `json:"model,omitempty"`
	Mode         string  `json:"mode,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Context      *int    `json:"context,omitempty"`
	Tokens       int     `json:"tokens,omitempty"`
	Shown        bool    `json:"shown,omitempty"`   // in a pane
	Focused      bool    `json:"focused,omitempty"` // has the keyboard
	Exited       bool    `json:"exited,omitempty"`
	Code         int     `json:"code,omitempty"`
	Disconnected bool    `json:"disconnected,omitempty"`
}

// control answers a request, off the main thread: wait waits there, and
// the others run on the main thread and wait for it.
func (a *App) control(req Request) Response {
	switch req.Cmd {
	case "wait":
		return a.wait(req)
	case "page":
		return a.pageControl(req)
	case "database":
		return a.databaseControl(req)
	case "status":
		return a.controlStatus(req)
	case "allow", "deny":
		return a.controlAnswer(req)
	case "open":
		if req.Worktree {
			// Off the main thread: git checks the files out.
			if req.Session != "" {
				return Response{Error: "open --worktree starts a new session; it can't open one"}
			}
			if req.Cwd == "" {
				return Response{Error: "open --worktree needs the repository's directory"}
			}
			w, err := a.makeWorktree(req.Cwd, req.Branch)
			if err != nil {
				return Response{Error: "can't make a worktree: " + err.Error()}
			}
			req.Cwd = w.Path
		}
	case "trash":
		return a.controlTrash(req)
	case "list", "send", "read", "focus", "close", "notify", "archive", "unarchive", "pane", "notifications", "project-layout", "workspace":
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
	case "pane":
		return a.controlPane(req)
	case "notifications":
		return a.controlNotifications(req)
	case "project-layout":
		return a.controlProjectLayout(req)
	case "workspace":
		return a.controlWorkspace(req)
	case "archive":
		s, err := a.sessionRef(req.Session)
		if err != "" {
			return Response{Error: err}
		}
		if !a.archive(s.Path) {
			return Response{Error: a.notice}
		}
		return Response{}
	case "unarchive":
		s, err := a.archivedRef(req.Session)
		if err != "" {
			return Response{Error: err}
		}
		if a.unarchive(s.Path) == "" {
			return Response{Error: a.notice}
		}
		return Response{}
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
	case "send", "read":
		if t.browser {
			return Response{Error: "a browser tab has no terminal"}
		}
		if t.files != nil {
			return Response{Error: "a Files tab has no terminal"}
		}
		if t.database != nil {
			return Response{Error: "a Database tab has no terminal"}
		}
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
		if a.activateWindow != nil {
			a.activateWindow()
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
	var target *workspace
	if req.Workspace != "" {
		target = a.workspaceByID(req.Workspace)
		if target == nil {
			return Response{Error: "no such workspace"}
		}
	}
	previousWorkspace, previous := a.activeWorkspace, a.focused()
	var t *tab
	switch {
	case req.Session != "":
		s, err := a.sessionRef(req.Session)
		if err != "" {
			return Response{Error: err}
		}
		if existing := a.tabOf(s.Path); existing != nil && target != nil && a.workspaceOf(existing) != target {
			return Response{Error: "pane belongs to another workspace; use workspace move-pane to move it"}
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
	case req.Files:
		if req.Cwd == "" {
			return Response{Error: "open --files needs a directory"}
		}
		t = a.startFiles(req.Cwd)
	case req.Database:
		if req.Cwd == "" {
			return Response{Error: "open --database needs a directory"}
		}
		t = a.startDatabase(req.Cwd)
	case req.URL != "":
		u, ok := normalizeURL(req.URL)
		if !ok {
			return Response{Error: fmt.Sprintf("%q isn't a web address", req.URL)}
		}
		t = a.startBrowser(u)
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
	if target != nil {
		if owner := a.workspaceOf(t); owner != nil && owner != target {
			return Response{Error: "pane belongs to another workspace; use workspace move-pane to move it"}
		}
		a.switchWorkspace(target)
	}
	switch {
	case req.Background:
		a.backgroundOpen = true
		a.activate(t)
		a.backgroundOpen = false
		if previousWorkspace != a.activeWorkspace {
			a.switchWorkspace(previousWorkspace)
		}
		if previous != nil && previous != t {
			if p := a.paneOf(previous); p != nil {
				a.focusPane(p)
			}
		}
	case req.Split || req.Below:
		a.showInSplit(t, req.Below)
	default:
		a.activate(t)
	}
	if !req.Background && slices.Contains(a.tabs, t) {
		if a.raise != nil {
			a.raise()
		}
		if a.activateWindow != nil {
			a.activateWindow()
		}
	}
	info := a.tabInfo(t)
	return Response{Tab: &info}
}

// wait waits for a tab's session, or a session running elsewhere, to be
// in one of the states asked for. Done and error say a turn ended, so
// only those reported after the wait began count; the others count as
// they are, as a session already waiting for you is.
func (a *App) wait(req Request) Response {
	want, err := wantStates(req.State)
	if err != "" {
		return Response{Error: err}
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
			if cur, ok := reached(path, want, start); ok {
				if info.ID == "" {
					return Response{State: string(cur.State)}
				}
				info.State, info.Wants, info.Msg = string(cur.State), string(cur.Kind), cur.Msg
				return Response{Tab: &info, State: string(cur.State)}
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

// wantStates are the states wait waits for: one or several, comma
// separated, done when none; or it says why not.
func wantStates(states string) (map[status.State]bool, string) {
	want := map[status.State]bool{}
	for _, s := range strings.Split(cmp.Or(states, string(status.Done)), ",") {
		st := status.State(strings.TrimSpace(s))
		switch st {
		case status.Idle, status.Working, status.Done, status.Blocked, status.Error:
			want[st] = true
		default:
			return nil, fmt.Sprintf("unknown state %q: idle, working, done, blocked or error", s)
		}
	}
	return want, ""
}

// reached reports whether the session at path is in one of the states
// wanted, with its status: done and error only when reported after
// start, as a turn that ended then.
func reached(path string, want map[status.State]bool, start time.Time) (status.Report, bool) {
	f, ok := status.Read(path)
	if !ok {
		return status.Report{}, false
	}
	cur := f.Current()
	ended := cur.State == status.Done || cur.State == status.Error
	return cur, want[cur.State] && (!ended || f.Since().After(start))
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
		if f, ok := status.Read(info.Path); ok && f.Since().After(before.Since()) {
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

// controlTrash moves a session to the Trash, archived or not, without
// asking: the command is the asking.
func (a *App) controlTrash(req Request) Response {
	var folder, path, errMsg string
	if !a.onMain(func() {
		s, err := a.sessionRef(req.Session)
		if err != "" {
			var archErr string
			if s, archErr = a.archivedRef(req.Session); archErr != "" {
				// What neither list has is no session at all.
				errMsg = err
				if !strings.HasPrefix(archErr, "no ") {
					errMsg = archErr
				}
				return
			}
		}
		path = s.Path
		if errMsg = a.unmovable(path); errMsg != "" {
			return
		}
		if a.trashItem == nil {
			errMsg = "there's no Trash to move it to here"
			return
		}
		var gerr error
		if folder, gerr = session.Gather(path); gerr != nil {
			errMsg = gerr.Error()
			return
		}
		a.forget(path)
		a.moved(path, "", false)
	}) {
		return Response{Error: "larik desktop is closing"}
	}
	if errMsg != "" {
		return Response{Error: "can't move the session to the Trash: " + errMsg}
	}
	if err := a.trashItem(folder); err != nil {
		msg := "can't move the session to the Trash: " + err.Error() + "; it's where it was"
		if uerr := session.Ungather(path, folder); uerr != nil {
			msg = fmt.Sprintf("can't move the session to the Trash: %v, and it stayed in %s: %v", err, folder, uerr)
		}
		a.refreshIndex()
		return Response{Error: msg}
	}
	return Response{}
}

// archivedRef finds the archived session ref names, by id or the start
// of one.
func (a *App) archivedRef(ref string) (session.Indexed, string) {
	var found []session.Indexed
	for _, p := range a.projects {
		for _, s := range p.Archived {
			if s.ID == ref {
				return s, ""
			}
			if ref != "" && strings.HasPrefix(s.ID, ref) {
				found = append(found, s)
			}
		}
	}
	switch len(found) {
	case 0:
		return session.Indexed{}, fmt.Sprintf("no archived session %q", ref)
	case 1:
		return found[0], ""
	}
	return session.Indexed{}, fmt.Sprintf("%q is the start of %d archived sessions: give more of it", ref, len(found))
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
		Shown:   a.shown(t),
		Focused: t == a.focused() && slices.Contains(a.tabs, t),
		Exited:  t.exited, Code: t.code, Disconnected: t.disconnected,
	}
	if w := a.workspaceOf(t); w != nil {
		info.Workspace = w.id
	}
	if t.shell() {
		info.Kind = "terminal"
	}
	if t.browser {
		info.Kind, info.URL = "browser", t.url
	}
	if t.files != nil {
		info.Kind = "files"
	}
	if t.database != nil {
		info.Kind = "database"
	}
	if t.chooser {
		info.Kind = "chooser"
	}
	if s, _, ok := a.find(t.path); ok {
		info.Session = s.ID
		info.Branch = a.git[s.Cwd].Branch
		u := s.Usage
		info.Model, info.Mode, info.CostUSD, info.Tokens = cmp.Or(u.Model, s.Model), u.Mode, u.CostUSD, u.Tokens
		if pct := u.ContextPercent(); pct >= 0 {
			info.Context = &pct
		}
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
