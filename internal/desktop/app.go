// Package desktop is Larik's desktop window: every session from every
// project in a sidebar, grouped by the directory it ran in, with what each
// is doing, and a tab per open session. Each tab is a terminal running the
// larik CLI, so the window adds to the terminal app rather than being a
// second front end. It draws with MyGo's native UI and its terminal
// plugin; what sessions are doing comes from their status files, so
// sessions running in other terminals show too.
package desktop

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

// Options configure the window.
type Options struct {
	Bin     string // the larik executable each tab runs
	DataDir string // where sessions live
	Cwd     string // the directory larik desktop was started in
	Version string // larik's, for About Larik
	// Host runs the tabs' programs in the host, where they go on when the
	// window closes (see host.go), and StartHost starts it, in a process of
	// its own, when it isn't running.
	Host      bool
	StartHost func() error
}

// App is the window's state. The view reads it on the main thread;
// goroutines change it through update.
type App struct {
	opts Options
	// update runs fn on the main thread and draws a new frame, without
	// waiting.
	update func(fn func())
	done   chan struct{}
	kick   chan struct{} // asks the index loop to look again now

	projects []session.Project
	open     map[string]*bool // project root → its sidebar section is open
	showAll  map[string]bool  // project root → all its sessions show
	// hidden are the sessions hidden from the sidebar, by transcript path,
	// and showHidden the projects showing theirs anyway.
	hidden     map[string]bool
	showHidden map[string]bool
	// showArchived are the projects listing their archived sessions, and
	// trashItem moves a file to the Trash (see lifecycle.go and Run).
	showArchived map[string]bool
	trashItem    func(path string) error
	// pinned are the sessions pinned above the projects, in the order
	// pinned, by transcript path.
	pinned []string
	// prompts are the sessions' first prompts, by transcript path, kept
	// for search and tooltips where a name given to one (session.SetName)
	// shows instead.
	prompts map[string]string
	// renaming is the session renamed in the sidebar, newName what its
	// field says, focusRename that the field is to take the keyboard, and
	// renameFocused that it has had it.
	renaming, newName          string
	focusRename, renameFocused bool
	// dragging says a session is being dragged from the sidebar.
	dragging bool
	tabs     []*tab
	// layout is the panes showing tabs, nil with none (see panes.go);
	// focus is the one with the keyboard, which the sidebar, the title bar
	// and the keys follow, and zoomed shows it alone.
	layout    *pane
	focus     *pane
	zoomed    bool
	changes   *changes // the changes pane, beside the panes when open
	changesP  float32  // the panes' share of the width beside the changes, in percent
	planP     float32  // the terminal's share beside a plan waiting for review
	plans     map[string]*planPreview
	focusNext *tab   // the tab whose terminal takes the keyboard next frame
	saved     string // the state last saved, as JSON
	saves     int    // how many times the state was saved
	// writeMu orders the writes of the saved state, and written is the
	// number of the save last written, so an older one never lands last;
	// Close waits for writing.
	writeMu sync.Mutex
	written int
	writing sync.WaitGroup
	query   string // the sidebar's search
	pick    int    // the match of the search Enter opens
	// conv finds sessions by what was said in them, off the main thread:
	// convAsked is the query last asked of it, and convHits what it
	// found for convQuery, by transcript path, with the passage.
	conv        convIndex
	convAsked   string
	convQuery   string
	convHits    map[string]string
	focusSearch bool                 // the search field takes the keyboard next frame
	restored    bool                 // the last launch's state was read
	seen        map[string]time.Time // when each session was last looked at
	started     time.Time            // what's older than this is read
	sidebarW    float32
	noSidebar   bool     // the sidebar is hidden
	closed      []string // the sessions of tabs closed, the last closed last
	fontSize    float32  // the terminals' text size, 0 for fontSize
	// closing are the tabs waiting on confirming, the question whether
	// to close them while they work, and closeKeep the tab to show after.
	closing    []*tab
	closeKeep  *tab
	confirming bool
	// confirmingLeave asks whether to close the window, or quit when
	// quitting, while tabs work; leaving is the answer yes, and leave
	// closes or quits (see Run).
	confirmingLeave bool
	quitting        bool
	leaving         bool
	// ending says quitting ends the programs the host runs too, which
	// otherwise go on (see quitEnding).
	ending bool
	leave  func(quit bool)
	theme  [2]*ui.Theme       // light, dark
	termTh [2]*terminal.Theme // light, dark
	notice string
	host   string
	// stopHosted is overridden by tests to hold a host close at its reply.
	stopHosted func(id string) error
	// git is the branch and changes of each project root, and of each
	// worktree a session runs in, and ports the ports each session's
	// programs listen on, by transcript path (see metaLoop).
	git   map[string]gitMeta
	ports map[string][]int
	// events are the notifications, the newest last, under the toolbar's
	// bell while showEvents; looked is when you last had each session's
	// tab focused, by transcript path (see attention.go).
	events     []event
	showEvents bool
	// paletteOpen shows the command palette, paletteQuery is what's typed
	// in it, palettePick the line Enter runs, paletteScroll that the list
	// is to scroll to it, and focusPalette that its field is to take the
	// keyboard (see palette.go).
	paletteOpen   bool
	paletteQuery  string
	palettePick   int
	paletteScroll bool
	focusPalette  bool
	looked        map[string]time.Time
	// answered are the sessions answered from the window, by transcript
	// path, until the session moves on or the window looks whether the
	// tab took the answer (see answer.go).
	answered map[string]pending
	// asked is the question confirmAsk is putting, while it's up.
	asked     *confirmation
	askedOpen bool

	// states is each session's state as of the last index, to see what
	// changed; alert and badge tell the user, off the window (see Run).
	states map[string]status.State
	alert  func(s session.Indexed)
	badge  func(n int)
	// dock sets the Dock icon's menu, docked as last set (see Run).
	dock   func(items []command)
	docked []command
	// redraw asks for a frame, for what changes outside the window's
	// input, as a search finding more (see Run).
	redraw func()
	// copyText, showFile and openPath put text on the clipboard, show a
	// file in the file manager, and open a folder there (see Run).
	copyText func(s string)
	showFile func(path string)
	openPath func(path string)
	// pickDir asks for a folder, starting in start, and gives it to then
	// on the window's frame (see Run).
	pickDir func(start string, then func(dir string))
	// ask asks a question in a sheet of the window's, as macOS asks: yes
	// is the button that goes ahead, beside Cancel, and then gets the
	// answer on the window's frame (see Run). Without it, the window asks
	// in a dialog of its own drawing; asking is set while it asks.
	ask    func(title, msg, yes string, then func(yes bool))
	asking bool
	// setMenu sets the menu bar again, for the Window menu's tabs as
	// menuTabs last said them, and openExternal opens a web page (see Run).
	setMenu      func()
	about        func(mygo.AboutPanelOptions) // shows the About panel
	menuSaid     string
	openExternal func(url string)
	showSettings func()
	// newWebView makes a browser tab's web view, loading url (see Run);
	// sites are what you let pages use, or didn't, by siteKey.
	newWebView func(url string) (*mygo.WebView, error)
	sites      map[string]bool
	// lastBrowser is the browser tab focused last, which page commands go
	// to when no other is named or focused.
	lastBrowser *tab
	// post posts a notification a command asked for, and raise brings
	// the window forward (see Run).
	post  func(title, body string)
	raise func()
	// ping tells you, from another app, what a program in a terminal tab
	// posted (see Run).
	ping func(tab, title, body string)
	// setTitle sets the window's title, titled as last set (see Run).
	setTitle func(title string)
	titled   string
}

// tab is a terminal running larik in a session's directory, or, for a
// terminal tab, your shell or a program (see shell.go).
type tab struct {
	id     string // in the process's environment as status.TabEnv
	term   *terminal.Terminal
	dir    string
	path   string // the session it has open, once it has one
	title  string // what larik, or the shell, set as the terminal's title
	exited bool
	code   int
	// cmd is what a terminal tab runs, nil for larik's tabs; run is the
	// command it was given, "" for the shell; and alerted is when its
	// program last rang or posted a notification while you looked
	// elsewhere.
	cmd       []string
	run       string
	alerted   time.Time
	program   status.Report // OSC 7501 from a terminal tab's program
	records   statusRecords
	programAt time.Time
	// hosted says the host runs its program, and reattach that the next
	// run is to attach to it there, not run one (see host.go).
	hosted, reattach bool

	// The find bar: open, what it looks for, how the search goes, and
	// whether its field takes the keyboard next frame.
	finding   bool
	find      string
	search    terminal.SearchStatus
	focusFind bool

	// A browser tab's (see browser.go): its web view, nil without a
	// window; the address of its page, and what its address bar says;
	// that the bar takes the keyboard next frame; whether its page loads,
	// and can go back and forward; and what stops following its page.
	browser   bool
	web       *mygo.WebView
	url, addr string
	focusAddr bool
	loading   bool
	back, fwd bool
	off       []func()
	// asking is what its page asked to use and waits on you for, and
	// download its last download (see browser.go).
	asking   *siteAsk
	download *pageDownload
	// found is how its find bar's search of its page went.
	found mygo.FindResult
}

// openFind opens the find bar of the tab with the keyboard, or gives the
// bar the keyboard again.
func (a *App) openFind() {
	if t := a.focused(); t != nil && (t.term != nil || t.browser) {
		t.finding, t.focusFind = true, true
	}
}

// closeFind closes t's find bar, ending its search, and gives the
// keyboard back to its terminal.
func (a *App) closeFind(t *tab) {
	t.finding, t.find = false, ""
	if t.browser {
		a.endPageFind(t)
	} else {
		t.term.EndSearch()
	}
	a.focusNext = t
}

// New makes the window's state. Call Start once update is set.
func New(opts Options) *App {
	host, _ := os.Hostname()
	return &App{
		opts:         opts,
		update:       func(fn func()) { fn() },
		done:         make(chan struct{}),
		kick:         make(chan struct{}, 1),
		open:         map[string]*bool{},
		showAll:      map[string]bool{},
		hidden:       map[string]bool{},
		prompts:      map[string]string{},
		showHidden:   map[string]bool{},
		showArchived: map[string]bool{},
		seen:         map[string]time.Time{},
		looked:       map[string]time.Time{},
		answered:     map[string]pending{},
		states:       map[string]status.State{},
		started:      time.Now(),
		sidebarW:     280,
		changesP:     60,
		planP:        55,
		plans:        map[string]*planPreview{},
		host:         host,
	}
}

// Start loads the index, opens the newest session of the starting
// directory, and keeps the index current.
func (a *App) Start() {
	projects, _ := session.Index(a.opts.DataDir, session.IndexOptions{})
	a.update(func() {
		a.setProjects(projects)
		if !a.restore() {
			a.selectStart()
		}
	})
	go a.indexLoop()
	go a.metaLoop()
}

// Close ends the index loop and every tab's larik, but those the host
// runs, which go on.
func (a *App) Close() {
	select {
	case <-a.done:
		return
	default:
	}
	a.saveState() // as it is as the window goes
	close(a.done)
	for _, t := range a.tabs {
		if t.hosted && a.ending {
			closeHostTab(a.opts.DataDir, t.id)
		}
	}
	for _, t := range a.tabs {
		t.close()
	}
	a.writing.Wait() // the last state, so quitting doesn't lose it
}

// indexLoop reads the index again every second or so, or when kicked:
// status files are how the window learns what each session is doing.
func (a *App) indexLoop() {
	tick := time.NewTicker(1500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.done:
			return
		case <-tick.C:
		case <-a.kick:
		}
		projects, err := session.Index(a.opts.DataDir, session.IndexOptions{})
		if err == nil {
			// Saved here too, as frames save it: a window out of sight may
			// not draw, and the tabs the host runs are to be found again.
			a.update(func() { a.setProjects(projects); a.saveState() })
		}
	}
}

// refreshIndex asks the index loop to look again soon.
func (a *App) refreshIndex() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *App) setProjects(ps []session.Project) {
	a.projects = ps
	a.refreshPlans(ps)
	a.convAsked = "" // a session may have said more
	a.prompts = map[string]string{}
	a.applyNames()
	for _, p := range ps {
		if a.open[p.Root] == nil {
			open := len(a.open) < 3 // the newest few start open
			a.open[p.Root] = &open
		}
	}
	a.followSessions()
	for _, t := range a.shownTabs() {
		if t.path != "" {
			a.seen[t.path] = time.Now()
		}
	}
	if f := a.focused(); f != nil {
		a.lookTab(f)
	}
	a.attend(ps)
}

// followSessions has each tab follow its larik to whichever session it
// has open: the one a new tab started, or one it switched to.
func (a *App) followSessions() {
	for _, t := range a.tabs {
		if t.shell() {
			t.path = "" // a larik in it has a session only while it runs
		}
		var newest time.Time
		for _, p := range a.projects {
			for _, s := range p.Sessions {
				if s.Tab == t.id && s.Modified.After(newest) {
					t.path, newest = s.Path, s.Modified
				}
			}
		}
	}
}

// attend raises what started waiting for the user since the last index:
// a session now blocked, or a turn that just finished or failed. The
// first index raises nothing, being what was there before the window.
func (a *App) attend(ps []session.Project) {
	first := len(a.states) == 0
	waiting := 0
	for _, p := range ps {
		for _, s := range p.Sessions {
			cur := s.Status.State
			prev, known := a.states[s.Path]
			a.states[s.Path] = cur
			if cur == status.Blocked || a.unread(s) {
				waiting++
			}
			if first || cur == prev {
				continue
			}
			if cur == status.Blocked || (known && prev == status.Working && (cur == status.Done || cur == status.Error)) {
				a.recordSession(s)
				if a.alert != nil {
					a.alert(s)
				}
			}
		}
	}
	if a.badge != nil {
		a.badge(waiting)
	}
	if a.dock != nil {
		if items := a.dockMenu(); !slices.EqualFunc(items, a.docked, func(x, y command) bool { return x.label == y.label }) {
			a.docked = items
			a.dock(items)
		}
	}
}

// nextWaiting shows the next session waiting for you, after the focused
// one: those blocked first, then those that failed or finished unread.
func (a *App) nextWaiting() {
	var list []string
	for _, s := range a.waiting() {
		list = append(list, s.Path)
	}
	if len(list) == 0 {
		a.notice = "Nothing is waiting for you."
		return
	}
	next := list[0]
	if f := a.focused(); f != nil {
		if i := slices.Index(list, f.path); i >= 0 {
			next = list[(i+1)%len(list)]
		}
	}
	a.reveal(next)
}

// waiting are the sessions waiting for you, across projects: those
// blocked first, then those that failed or finished unread.
func (a *App) waiting() []session.Indexed {
	var blocked, unread []session.Indexed
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			switch {
			case s.Status.State == status.Blocked:
				blocked = append(blocked, s)
			case a.unread(s):
				unread = append(unread, s)
			}
		}
	}
	return append(blocked, unread...)
}

// selectStart opens the newest session of the directory larik desktop
// was started in, unless it is open in another terminal. Started in the
// home directory, as from the launcher, it opens none: that is where
// larik desktop starts, not a project you are in.
func (a *App) selectStart() {
	if a.layout != nil || a.opts.Cwd == "" {
		return
	}
	if home, err := os.UserHomeDir(); err == nil && a.opts.Cwd == home {
		return
	}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if s.Cwd == a.opts.Cwd || p.Root == a.opts.Cwd {
				*a.open[p.Root] = true
				if !s.Running {
					a.selectSession(s.Path)
				}
				return
			}
		}
	}
}

// find returns the indexed session at path and its project.
func (a *App) find(path string) (session.Indexed, session.Project, bool) {
	if path == "" {
		return session.Indexed{}, session.Project{}, false
	}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if s.Path == path {
				return s, p, true
			}
		}
	}
	return session.Indexed{}, session.Project{}, false
}

// tabOf returns the tab that has the session at path open.
func (a *App) tabOf(path string) *tab {
	for _, t := range a.tabs {
		if path != "" && t.path == path {
			return t
		}
	}
	return nil
}

// ours reports whether the tab with id is this window's.
func (a *App) ours(id string) bool {
	return id != "" && slices.ContainsFunc(a.tabs, func(t *tab) bool { return t.id == id })
}

// selectSession shows the session at path in the focused pane.
func (a *App) selectSession(path string) {
	if t := a.sessionTab(path); t != nil {
		a.activate(t)
	}
}

// sessionTab returns the session's tab, starting one that resumes it if
// it has none. A session open in another terminal stays there, as only
// one larik can have it: that returns nil, with a notice saying so.
func (a *App) sessionTab(path string) *tab {
	a.notice = ""
	if t := a.tabOf(path); t != nil {
		return t
	}
	s, p, ok := a.find(path)
	if !ok {
		return nil
	}
	if s.Running && !a.ours(s.Tab) {
		title := s.Title
		if title == "" {
			title = "This session"
		}
		a.notice = fmt.Sprintf("%q is open in another terminal. Only one larik can have a session at a time: switch to that terminal, or quit larik there to open it here.", title)
		return nil
	}
	dir := s.Cwd
	if _, err := os.Stat(dir); err != nil && a.inWorktree(dir) {
		dir = p.Root // its worktree was merged, or removed: back where it came from
	}
	return a.startTab(dir, s.ID, path)
}

// newTab starts larik in dir, resuming the session id unless it is
// empty, and shows it in the focused pane.
func (a *App) newTab(dir, id, path string) {
	if t := a.startTab(dir, id, path); t != nil {
		a.activate(t)
	}
}

// openFolder asks for a folder and starts a session in it.
func (a *App) openFolder() {
	if a.pickDir != nil {
		a.pickDir(a.currentDir(), func(dir string) { a.newTab(dir, "", "") })
	}
}

// openSettings opens the desktop's settings window.
func (a *App) openSettings() {
	if a.showSettings != nil {
		a.showSettings()
	}
}

// openAdvancedSettings opens larik's full config editor in a terminal tab,
// preserving its personal and project settings behavior.
func (a *App) openAdvancedSettings() {
	if t := a.startTab(a.currentDir(), "", "", "/config"); t != nil {
		a.activate(t)
	}
}

// dropDirs starts a session in each folder dropped on the sidebar, or in
// the folder of a file dropped there, showing the last.
func (a *App) dropDirs(paths []string) {
	var dirs []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			p = filepath.Dir(p)
		}
		if !slices.Contains(dirs, p) {
			dirs = append(dirs, p)
		}
	}
	for _, dir := range dirs {
		a.newTab(dir, "", "")
	}
}

// applyNames titles the sessions given names with them, and the others
// with their first prompts.
func (a *App) applyNames() {
	for i := range a.projects {
		for _, list := range [][]session.Indexed{a.projects[i].Sessions, a.projects[i].Archived} {
			for j := range list {
				s := &list[j]
				prompt, ok := a.prompts[s.Path]
				if !ok {
					prompt = s.Title
					a.prompts[s.Path] = prompt
				}
				s.Title = cmp.Or(s.Name, prompt)
			}
		}
	}
}

// rename names the session at path, beside its transcript, where the
// command line's /resume finds it too, or gives it its first prompt back
// for a blank name.
func (a *App) rename(path, name string) {
	name = strings.Join(strings.Fields(name), " ")
	if name == a.prompts[path] {
		name = ""
	}
	if err := session.SetName(path, name); err != nil {
		a.notice = "Can't rename the session: " + err.Error()
		return
	}
	for i := range a.projects {
		for j := range a.projects[i].Sessions {
			if s := &a.projects[i].Sessions[j]; s.Path == path {
				s.Name = name
			}
		}
	}
	a.applyNames()
}

// askRename renames the session at path in its sidebar row, showing the
// row: the sidebar, the project's section, and the session among its
// project's, hidden or not.
func (a *App) askRename(path string) {
	s, p, ok := a.find(path)
	if !ok {
		return
	}
	a.noSidebar, a.query = false, ""
	*a.open[p.Root] = true
	a.showAll[p.Root] = true
	if a.hidden[path] {
		a.showHidden[p.Root] = true
	}
	a.renaming, a.newName, a.focusRename = path, s.Title, true
}

// endRename names the session being renamed with what its field says,
// or leaves it as it was.
func (a *App) endRename(keep bool) {
	if a.renaming == "" {
		return
	}
	if keep {
		a.rename(a.renaming, a.newName)
	}
	a.renaming, a.newName, a.renameFocused = "", "", false
}

// pin pins the session at the top of the sidebar, or unpins it. A
// pinned session shows nowhere else, and one hidden shows again.
func (a *App) pin(path string, on bool) {
	a.pinned = slices.DeleteFunc(a.pinned, func(p string) bool { return p == path })
	if on {
		a.pinned = append(a.pinned, path)
		delete(a.hidden, path)
	}
}

// movePin pins the session at path before the pinned one at before, or
// last for "", moving it there if it's pinned already.
func (a *App) movePin(path, before string) {
	if path == before {
		return
	}
	a.pin(path, true)
	if i := slices.Index(a.pinned, before); i >= 0 {
		a.pinned = slices.Insert(a.pinned[:len(a.pinned)-1], i, path)
	}
}

// pinnedSessions are the pinned sessions the index still has, in the
// order pinned.
func (a *App) pinnedSessions() []session.Indexed {
	var out []session.Indexed
	for _, path := range a.pinned {
		if s, _, ok := a.find(path); ok {
			out = append(out, s)
		}
	}
	return out
}

// sessionDrag is a session dragged from the sidebar, by its transcript.
type sessionDrag string

// dropSession shows the session dropped on a pane in that pane, which a
// pane showing it already swaps with; with no pane, in a new one.
func (a *App) dropSession(path string, p *pane) {
	t := a.sessionTab(path)
	switch {
	case t == nil:
		return
	case p == nil:
		a.activate(t)
		return
	}
	a.show(p, t)
	a.focusPane(p)
}

// windowTitle names the window after the focused tab's session, for the
// Window menu, Mission Control and the app switcher.
func (a *App) windowTitle() string {
	if t := a.focused(); t != nil {
		return a.tabTitle(t) + " — Larik"
	}
	return "Larik"
}

// fork starts a new session from the one at path, in a new tab, leaving
// the original as it is: larik --resume <id> --fork.
func (a *App) fork(path string) {
	s, _, ok := a.find(path)
	if !ok {
		return
	}
	if t := a.startTab(s.Cwd, s.ID, "", "--fork"); t != nil {
		a.activate(t)
	}
}

// matches are the sessions the sidebar's search finds: every word of it
// in the title, the project, the subfolder or the id, newest project
// first.
func (a *App) matches() []session.Indexed {
	words := strings.Fields(strings.ToLower(a.query))
	var out, said []session.Indexed
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			hay := strings.ToLower(s.Title + " " + a.prompts[s.Path] + " " + p.Name + " " + s.Subdir + " " + s.ID)
			switch {
			case !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }):
				out = append(out, s)
			case a.saidIn(s.Path) != "":
				said = append(said, s)
			}
		}
	}
	// Those whose names match first, then those whose conversations do.
	return append(out, said...)
}

// saidIn is the passage of the session's conversation the search found,
// if it found one for the query as it is.
func (a *App) saidIn(path string) string {
	if a.convQuery != a.query {
		return ""
	}
	return a.convHits[path]
}

// searchConversations looks for the query in what was said in every
// session, once per query and again when the sessions change, off the
// main thread; matches shows what it found when it has. A word or two
// of fewer than three letters would find nearly every session.
func (a *App) searchConversations() {
	if a.query == a.convAsked || a.update == nil {
		return
	}
	a.convAsked = a.query
	words := strings.Fields(strings.ToLower(a.query))
	if utf8.RuneCountInString(strings.Join(words, "")) < 3 {
		a.convQuery, a.convHits = a.query, nil
		return
	}
	var paths []string
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			paths = append(paths, s.Path)
		}
	}
	query := a.query
	go func() {
		a.conv.refresh(paths)
		hits := a.conv.search(words)
		a.update(func() {
			if a.query == query {
				a.convQuery, a.convHits = query, hits
			}
		})
	}()
}

// startTab starts larik in dir, resuming the session id unless it is
// empty, with flags after, and adds its tab.
func (a *App) startTab(dir, id, path string, flags ...string) *tab {
	a.notice = ""
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		a.notice = fmt.Sprintf("Can't start larik in %s: %v", dir, err)
		return nil
	}
	t := &tab{id: newTabID(), dir: dir, path: path}
	if !a.run(t, id, flags...) {
		return nil
	}
	a.tabs = append(a.tabs, t)
	return t
}

// run starts larik in t's terminal, resuming the session id unless it is
// empty, with flags after.
func (a *App) run(t *tab, id string, flags ...string) bool {
	if t.shell() && !t.reattach {
		t.program, t.records = status.Report{}, statusRecords{}
	}
	args := []string{a.opts.Bin}
	if id != "" {
		args = append(args, "--resume", id)
	}
	args = append(args, flags...)
	if t.shell() {
		args = t.cmd
	}
	// The terminal's own end, not one a restart replaced.
	var term *terminal.Terminal
	opts := terminal.Options{
		Font:         a.termFont(),
		Theme:        a.terminalTheme(false),
		DarkTheme:    a.terminalTheme(true),
		Padding:      terminal.Padding{X: 14, Y: 10},
		PaddingColor: terminal.PaddingExtend,
		OnTitle:      func(title string) { a.update(func() { t.title = title }) },
		OnZoom:       func(z terminal.Zoom) bool { a.zoom(z); return true },
		OnBell:       func() { a.update(func() { a.rang(t) }) },
		OnNotify:     func(title, body string) { a.update(func() { a.programNotify(t, title, body) }) },
		OnStatus: func(payload string) {
			if t.shell() {
				a.update(func() {
					if r, changed := t.records.apply(payload); changed {
						a.setProgramStatus(t, r)
					}
				})
			}
		},
		OnPrompt: func() {
			if t.shell() {
				a.update(func() {
					if r, changed := t.records.dropLive(); changed {
						a.setProgramStatus(t, r)
					}
				})
			}
		},
		OnReset: func() {
			if t.shell() {
				a.update(func() {
					if r, changed := t.records.clearAll(); changed {
						a.setProgramStatus(t, r)
					}
				})
			}
		},
		OnSearch: func(st terminal.SearchStatus) {
			t.search = st
			if a.redraw != nil {
				a.redraw()
			}
		},
	}
	hc, err := a.openHosted(t, args)
	switch {
	case err != nil && t.reattach:
		a.notice = "Can't reach the tab in the host: " + err.Error()
		return false
	case err != nil:
		a.notice = "The host isn't running, so this tab's program ends with the window: " + err.Error()
	}
	t.reattach = false
	if hc != nil {
		// The host's terminal rings and posts, and how it ended is the
		// host's to say.
		hc.onBell, hc.onNotify = opts.OnBell, opts.OnNotify
		hc.onStatus = func(r status.Report) { a.update(func() { a.setProgramStatus(t, r) }) }
		opts.OnBell, opts.OnNotify, opts.OnStatus, opts.OnPrompt, opts.OnReset = nil, nil, nil, nil, nil
		opts.Conn, opts.ResizeByConn = hc, true
	} else {
		opts.Command, opts.Dir, opts.Env = args, t.dir, []string{status.TabEnv + "=" + t.id}
	}
	opts.OnExit = func(code int) {
		if hc != nil {
			code = hc.exitCode()
		}
		a.update(func() {
			if t.term == term {
				a.exited(t, code)
			}
		})
	}
	term, err = terminal.New(opts)
	if err != nil {
		if hc != nil {
			hc.Close()
		}
		a.notice = "Can't open a terminal: " + err.Error()
		return false
	}
	if hc != nil {
		hc.attached(term)
	}
	t.term, t.hosted = term, hc != nil
	return true
}

// restart runs larik again in a tab whose larik failed, resuming its
// session, in the same place.
func (a *App) restart(t *tab) {
	if !t.exited || !slices.Contains(a.tabs, t) {
		return
	}
	a.notice = ""
	id := ""
	if s, _, ok := a.find(t.path); ok {
		id = s.ID
	}
	old := t.term
	if !a.run(t, id) {
		return
	}
	t.exited, t.code = false, 0
	if old != nil {
		old.Close()
	}
	a.activate(t)
	a.refreshIndex()
}

// The terminals' text size, and how far zooming goes.
const (
	fontSize         = 13
	minFont, maxFont = 8, 32
)

func (a *App) termFont() terminal.Font {
	size := a.fontSize
	if size == 0 {
		size = fontSize
	}
	return terminal.Font{Family: mono, Size: size}
}

// zoom sizes every tab's text at once, a step at a time (Command and +, -
// or 0, or the wheel with Command held), rather than the one terminal
// zoomed, so that tabs and splits match.
func (a *App) zoom(z terminal.Zoom) {
	size := a.termFont().Size
	switch {
	case z.Reset:
		size = fontSize
	default:
		size = min(max(size+float32(z.Steps), minFont), maxFont)
	}
	a.fontSize = size
	if size == fontSize {
		a.fontSize = 0
	}
	for _, t := range a.tabs {
		if t.term != nil {
			t.term.SetFont(a.termFont())
		}
	}
}

// close ends the tab's larik, or leaves the host running it.
func (t *tab) close() {
	if t.term != nil {
		t.term.Close()
	}
	if t.browser {
		t.closeBrowser()
	}
}

func newTabID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// exited handles a tab's larik ending: a clean exit closes the tab, as
// it would close a terminal's; a failure stays, for what it printed.
func (a *App) exited(t *tab, code int) {
	select {
	case <-a.done:
		return // the window is closing, and ending every tab
	default:
	}
	t.exited, t.code = true, code
	if t.shell() && !t.hosted {
		t.program, _ = t.records.dropLive()
	}
	if code == 0 {
		a.closeTab(t)
	}
	a.refreshIndex()
}

// closeTab closes t, ending its larik; the session stays on disk.
func (a *App) closeTab(t *tab) <-chan error {
	i := slices.Index(a.tabs, t)
	if i < 0 {
		return nil
	}
	a.tabs = slices.Delete(a.tabs, i, i+1)
	t.close()
	var stopped chan error
	if t.hosted {
		stopped = make(chan error, 1)
		stop := a.stopHosted
		if stop == nil {
			stop = func(id string) error { return closeHostTab(a.opts.DataDir, id) }
		}
		go func() { stopped <- stop(t.id) }() // its program ends too
	}
	if t.path != "" {
		a.closed = append(slices.DeleteFunc(a.closed, func(p string) bool { return p == t.path }), t.path)
		if len(a.closed) > closedKept {
			a.closed = a.closed[1:]
		}
	}
	a.unshow(t, i)
	if a.layout == nil {
		a.closeChanges() // nothing left for them to be beside
	}
	a.refreshIndex()
	return stopped
}

// closedKept is how many closed tabs reopenClosed can bring back.
const closedKept = 20

// reopenClosed opens the session of the tab closed last, and of the one
// before on the next call. One that's gone, or open again, is skipped.
func (a *App) reopenClosed() {
	for len(a.closed) > 0 {
		path := a.closed[len(a.closed)-1]
		a.closed = a.closed[:len(a.closed)-1]
		if _, _, ok := a.find(path); ok && a.tabOf(path) == nil {
			a.selectSession(path)
			return
		}
	}
	a.notice = "No closed tab to reopen."
}

// listed reports whether the sidebar lists s under its project: unless
// it's hidden, and even then while it's open in a tab or needs you.
func (a *App) listed(s session.Indexed) bool {
	return !a.hidden[s.Path] || a.tabOf(s.Path) != nil || s.Status.State == status.Blocked
}

// moveTab moves t to where to is in the title bar.
func (a *App) moveTab(t, to *tab) {
	i, j := slices.Index(a.tabs, t), slices.Index(a.tabs, to)
	if i < 0 || j < 0 || i == j {
		return
	}
	a.tabs = slices.Insert(slices.Delete(a.tabs, i, i+1), j, t)
}

// askClose closes tabs, first asking when a Larik turn or terminal program
// would be stopped. keep, when set, is the tab to show after.
func (a *App) askClose(tabs []*tab, keep *tab) {
	a.closing, a.closeKeep = tabs, keep
	if len(a.closingWorking()) > 0 || len(a.closingTerminals()) > 0 {
		a.confirming = true
		return
	}
	a.closeNow()
}

// closeOthers closes every tab but keep.
func (a *App) closeOthers(keep *tab) {
	var others []*tab
	for _, t := range a.tabs {
		if t != keep {
			others = append(others, t)
		}
	}
	a.askClose(others, keep)
}

// windowClosing decides whether the window may close, and with it every
// tab's program but those the host runs: at once when none that ends is
// working, else after asking.
func (a *App) windowClosing() bool {
	if a.leaving || len(a.stopping()) == 0 {
		return true
	}
	a.confirmingLeave = true
	return false
}

// stopping are the live terminals and working sessions that leaving stops:
// those the host doesn't run, or all when quitting ends those too.
func (a *App) stopping() []*tab {
	var out []*tab
	for _, t := range a.tabs {
		if (!t.hosted || a.ending) && ((t.shell() && !t.exited) || a.tabStatus(t).State == status.Working) {
			out = append(out, t)
		}
	}
	return out
}

// quitEnding quits, ending the programs the host runs as well, after
// asking when any is working.
func (a *App) quitEnding() {
	a.ending, a.quitting = true, true
	if a.windowClosing() {
		a.leaving = true
		if a.leave != nil {
			a.leave(true)
		}
	}
}

// workingTabs are the tabs whose larik is in a turn.
func (a *App) workingTabs() []*tab {
	var working []*tab
	for _, t := range a.tabs {
		if !t.shell() && a.tabStatus(t).State == status.Working {
			working = append(working, t)
		}
	}
	return working
}

// closingWorking are the tabs asked to close whose larik is working.
func (a *App) closingWorking() []*tab {
	var working []*tab
	for _, t := range a.closing {
		if !t.shell() && a.tabStatus(t).State == status.Working {
			working = append(working, t)
		}
	}
	return working
}

// closingTerminals are the terminal tabs whose live shell or command
// would be ended by closing the tab. The terminal library does not expose
// whether a shell has a foreground child, so every live shell is covered.
func (a *App) closingTerminals() []*tab {
	var terminals []*tab
	for _, t := range a.closing {
		if t.shell() && !t.exited {
			terminals = append(terminals, t)
		}
	}
	return terminals
}

// closeNow closes the tabs askClose was given.
func (a *App) closeNow() {
	for _, t := range a.closing {
		a.closeTab(t)
	}
	if a.closeKeep != nil && slices.Contains(a.tabs, a.closeKeep) {
		a.activate(a.closeKeep)
	}
	a.cancelClose()
}

func (a *App) cancelClose() {
	a.closing, a.closeKeep, a.confirming = nil, nil, false
}

// tabStatus is what a tab's larik is doing: its session's status, or an
// error once larik failed.
func (a *App) tabStatus(t *tab) status.Report {
	if t.exited && t.code != 0 {
		return status.Report{State: status.Error, Msg: fmt.Sprintf("larik exited with status %d", t.code)}
	}
	if s, _, ok := a.find(t.path); ok {
		return s.Status
	}
	if t.shell() && t.program.State != "" {
		return t.program
	}
	return status.Report{State: status.Idle}
}

// tabTitle names a tab: its session's title, else what larik titled the
// terminal, else what it is.
func (a *App) tabTitle(t *tab) string {
	if s, _, ok := a.find(t.path); ok && s.Title != "" {
		return s.Title
	}
	if t.shell() && t.program.Title != "" {
		return t.program.Title
	}
	if t.title != "" {
		return t.title
	}
	switch {
	case t.browser && t.url != blankPage:
		return pageHost(t.url)
	case t.browser:
		return "New browser tab"
	case t.run != "":
		return t.run
	case t.shell():
		return "Terminal"
	}
	return "New session"
}

// unread reports whether the session finished or failed since the user
// last looked at it.
func (a *App) unread(s session.Indexed) bool {
	if st := s.Status.State; st != status.Done && st != status.Error || a.hidden[s.Path] {
		return false
	}
	for _, t := range a.shownTabs() {
		if t.path == s.Path {
			return false
		}
	}
	seen := a.seen[s.Path]
	if seen.Before(a.started) {
		seen = a.started
	}
	return s.Modified.After(seen)
}

// currentDir is where a new session starts: the focused tab's
// directory, else the directory larik desktop started in.
func (a *App) currentDir() string {
	if t := a.focused(); t != nil {
		return t.dir
	}
	return a.opts.Cwd
}
