package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// savedState is what the window remembers across launches: the sessions
// open as tabs, in order, how the panes showed them, and the sidebar.
type savedState struct {
	Version int         `json:"version,omitempty"`
	Windows []string    `json:"windows,omitempty"` // other OS windows, in creation order
	Recent  []closedTab `json:"recent,omitempty"`
	Tabs    []string    `json:"tabs"` // transcript paths, "" for a terminal or browser tab
	// Terminals are the terminal tabs, by their index in Tabs.
	Terminals map[int]savedTerminal `json:"terminals,omitempty"`
	// Browsers are the browser tabs' pages, by their index in Tabs, and
	// Sites what you let their pages use, or didn't, by siteKey.
	Browsers      map[int]string            `json:"browsers,omitempty"`
	BrowserDirs   map[int]string            `json:"browser_dirs,omitempty"`
	RemotePorts   map[int]savedRemotePort   `json:"remote_ports,omitempty"`
	Agents        map[int]agentResume       `json:"agents,omitempty"`
	Files         map[int]string            `json:"files,omitempty"`     // Files tab roots by tab index
	Databases     map[int]string            `json:"databases,omitempty"` // Database pane project roots by tab index
	DatabaseViews map[int]savedDatabaseView `json:"database_views,omitempty"`
	Sites         map[string]bool           `json:"sites,omitempty"`
	// Hosted are the tabs the host runs the programs of, their ids by their
	// index in Tabs, to attach to again (see host.go).
	Hosted map[int]string `json:"hosted,omitempty"`
	Active int            `json:"active"` // the focused pane's tab, by index into Tabs, -1 for none
	// Panes are how the panes show the tabs, and Zoomed that the focused
	// one shows alone.
	Panes  *savedPane `json:"panes,omitempty"`
	Zoomed bool       `json:"zoomed,omitempty"`
	// Right and SplitP are the one split of earlier versions, the tab
	// beside the main one and the main one's share, read when there are no
	// Panes.
	Right  int     `json:"right,omitempty"`
	SplitP float32 `json:"split,omitempty"`
	Width  float32 `json:"sidebar,omitempty"` // the sidebar's width
	Hidden bool    `json:"no_sidebar,omitempty"`
	// NoChooser makes a new tab start larik rather than ask what to open.
	NoChooser bool `json:"no_chooser,omitempty"`
	// Appearance is "light" or "dark", and Accent an accent colour,
	// "#rrggbb"; none follows the system.
	Appearance string `json:"appearance,omitempty"`
	Accent     string `json:"accent,omitempty"`
	// Keys are the keys Settings gave commands, by their menu label, ""
	// for none.
	Keys map[string]string `json:"keys,omitempty"`
	// Servers are the machines connected to, by SSH destination, to
	// connect to again; their tabs come back from their hosts.
	Servers         []string `json:"servers,omitempty"`
	Font            float32  `json:"font_size,omitempty"`        // the terminals' text size
	PaneOpacity     *float32 `json:"pane_opacity,omitempty"`     // nil is the solid default
	TerminalOpacity *float32 `json:"terminal_opacity,omitempty"` // read older Desktop state
	// Open says, for each project's root, whether its section is open.
	Open map[string]bool `json:"open,omitempty"`
	// Hidden are the sessions hidden from the sidebar.
	HiddenSessions []string `json:"hidden,omitempty"`
	// Pinned are the sessions pinned at the top, in order.
	Pinned []string `json:"pinned,omitempty"`
	// Names are the names given to sessions in the window, by transcript
	// path, as earlier versions kept them, read once to put them beside
	// the transcripts (session.SetName).
	Names map[string]string `json:"names,omitempty"`
}

// savedTerminal is a terminal tab as the window remembers it: where it
// ran, and what. It comes back as a shell there: running a program again
// as the window opens, as a dev server or a script, isn't for the window
// to decide.
type savedTerminal struct {
	Dir string `json:"dir"`
	Run string `json:"run,omitempty"`
}

type savedRemotePort struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type savedDatabaseView struct {
	Profile        string  `json:"profile,omitempty"`
	BrowserWidth   float32 `json:"browser_width,omitempty"`
	InspectorWidth float32 `json:"inspector_width,omitempty"`
	EditorHeight   float32 `json:"editor_height,omitempty"` // legacy percentage
	EditorHeightPx float32 `json:"editor_height_px,omitempty"`
	ActiveResult   int     `json:"active_result,omitempty"`
	PageSize       int     `json:"page_size,omitempty"`
	ShowBrowser    bool    `json:"show_browser"`
	ShowDetails    bool    `json:"show_details"`
}

func saveDatabaseView(d *databaseState) savedDatabaseView {
	return savedDatabaseView{Profile: d.profile, BrowserWidth: d.browserWidth, InspectorWidth: d.inspectorWidth, EditorHeightPx: d.editorHeight, ActiveResult: d.activeResult, PageSize: d.pageSize, ShowBrowser: d.showBrowser, ShowDetails: d.showDetails}
}
func restoreDatabaseView(d *databaseState, v savedDatabaseView) {
	if v == (savedDatabaseView{}) {
		return
	}
	d.profile = v.Profile
	if v.BrowserWidth > 0 {
		d.browserWidth = v.BrowserWidth
	}
	if v.InspectorWidth > 0 {
		d.inspectorWidth = v.InspectorWidth
	}
	if v.EditorHeightPx > 0 {
		d.editorHeight = v.EditorHeightPx
	} else if v.EditorHeight > 0 {
		d.legacyEditorPercent = v.EditorHeight
	}
	if v.ActiveResult >= databaseData && v.ActiveResult <= databaseAgent {
		d.activeResult = v.ActiveResult
	}
	if v.PageSize == 25 || v.PageSize == 50 || v.PageSize == 100 || v.PageSize == 250 || v.PageSize == 500 {
		d.pageSize = v.PageSize
	}
	d.showBrowser, d.showDetails = v.ShowBrowser, v.ShowDetails
}

func (a *App) statePath() string {
	if a.windowID != "" {
		return filepath.Join(a.opts.DataDir, "desktop", "state-"+a.windowID+".json")
	}
	return filepath.Join(a.opts.DataDir, "desktop", "state.json")
}

// state is what to save now. Tabs that haven't started a session yet
// have nothing to resume, so they are left out.
func (a *App) state() savedState {
	st := savedState{Version: 4, Recent: a.recent, Active: -1, Zoomed: a.zoomed, Width: a.sidebarW, Hidden: a.noSidebar, NoChooser: a.noChooser, Appearance: a.appearance, Accent: a.accent, Keys: a.keys, Font: a.fontSize, Open: map[string]bool{}}
	if a.windowKeys != nil {
		st.Windows = a.windowKeys()
	}
	if a.paneOpacity != 1 {
		st.PaneOpacity = &a.paneOpacity
	}
	for root, open := range a.open {
		st.Open[root] = *open
	}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if a.hidden[s.Path] {
				st.HiddenSessions = append(st.HiddenSessions, s.Path)
			}
		}
	}
	for _, s := range a.pinnedSessions() {
		st.Pinned = append(st.Pinned, s.Path)
	}
	for _, srv := range a.servers {
		st.Servers = append(st.Servers, srv.name)
	}
	index := map[*tab]int{}
	for _, t := range a.tabs {
		if isRemote(t.dir) && t.files == nil && !t.browser {
			continue // the server's host has it, and the next connection shows it
		}
		path := t.path
		switch {
		case t.shell() && (!t.exited || t.agent.valid()):
			if st.Terminals == nil {
				st.Terminals = map[int]savedTerminal{}
			}
			st.Terminals[len(st.Tabs)] = savedTerminal{Dir: t.dir, Run: t.run}
			if t.agent.valid() {
				if st.Agents == nil {
					st.Agents = map[int]agentResume{}
				}
				st.Agents[len(st.Tabs)] = t.agent
			}
			path = ""
		case t.browser:
			if st.Browsers == nil {
				st.Browsers = map[int]string{}
			}
			if st.BrowserDirs == nil {
				st.BrowserDirs = map[int]string{}
			}
			st.BrowserDirs[len(st.Tabs)] = t.dir
			st.Browsers[len(st.Tabs)] = t.url
			if t.remotePort.port != 0 {
				if st.RemotePorts == nil {
					st.RemotePorts = map[int]savedRemotePort{}
				}
				st.RemotePorts[len(st.Tabs)] = savedRemotePort{t.remotePort.name, t.remotePort.port}
				st.Browsers[len(st.Tabs)] = blankPage
			}
			path = ""
		case t.files != nil:
			if st.Files == nil {
				st.Files = map[int]string{}
			}
			st.Files[len(st.Tabs)] = t.dir
			path = ""
		case t.database != nil:
			if st.Databases == nil {
				st.Databases = map[int]string{}
			}
			if st.DatabaseViews == nil {
				st.DatabaseViews = map[int]savedDatabaseView{}
			}
			st.Databases[len(st.Tabs)] = t.dir
			st.DatabaseViews[len(st.Tabs)] = saveDatabaseView(t.database)
			path = ""
		case path == "":
			continue
		}
		if t.hosted && !t.exited {
			if st.Hosted == nil {
				st.Hosted = map[int]string{}
			}
			st.Hosted[len(st.Tabs)] = t.id
		}
		if t == a.focused() {
			st.Active = len(st.Tabs)
		}
		index[t] = len(st.Tabs)
		st.Tabs = append(st.Tabs, path)
	}
	st.Panes = savePanes(a.layout, index)
	if len(a.sites) > 0 {
		st.Sites = a.sites
	}
	return st
}

// saveState writes the state when it changed since the last write. It
// runs after every frame, from the one after the last launch's state was
// read; once the window is closing, the tabs ending are not the user
// closing them, and nothing more is written.
func (a *App) saveState() {
	if !a.restored || a.opts.DataDir == "" {
		return
	}
	select {
	case <-a.done:
		return
	default:
	}
	data, err := json.Marshal(a.state())
	if err != nil || string(data) == a.saved {
		return
	}
	a.saved = string(data)
	a.saves++
	n, path := a.saves, a.statePath()
	a.writing.Add(1)
	go func() {
		defer a.writing.Done()
		a.writeMu.Lock()
		defer a.writeMu.Unlock()
		if n < a.written {
			return // a newer state was written already
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return
		}
		tmp := path + ".tmp"
		if os.WriteFile(tmp, data, 0o600) == nil && os.Rename(tmp, path) == nil {
			a.written = n
		}
	}()
}

// restore reopens the tabs of the last launch, attaching to those whose
// programs the host still runs and resuming the others' sessions, then
// attaches to what else the host runs, and reports whether it opened
// any. A session that is gone, or open in another terminal now, is
// skipped.
func (a *App) restore() bool {
	a.restoring = true
	defer func() { a.restoring = false }()
	data, err := os.ReadFile(a.statePath())
	if a.opts.DataDir == "" {
		err = os.ErrNotExist
	}
	a.restored = true
	live := a.hostedTabs()
	st := savedState{Right: -1}
	if err != nil || json.Unmarshal(data, &st) != nil {
		return a.showAdopted(a.adoptHosted(live))
	}
	a.saved = string(data)
	a.recent = st.Recent
	if st.Width > 0 {
		a.sidebarW = st.Width
	}
	a.noSidebar = st.Hidden
	a.noChooser = st.NoChooser
	if st.PaneOpacity != nil {
		a.paneOpacity = min(max(*st.PaneOpacity, 0), 1)
	} else if st.TerminalOpacity != nil {
		a.paneOpacity = min(max(*st.TerminalOpacity, 0), 1)
	}
	a.appearance, a.accent, a.keys = st.Appearance, st.Accent, st.Keys
	if st.Version < 3 {
		for old, renamed := range map[string]string{
			"New Tab": "New Pane", "New Tab in Split": "New Pane Beside",
			"New Tab in Split Below": "New Pane Below", "Close Tab": "Close Pane",
			"Reopen Closed Tab": "Reopen Closed Pane", "New Browser Tab": "New Browser Pane",
			"New Files Tab": "New Files Pane", "Show Next Tab": "Focus Next Pane",
			"Show Previous Tab": "Focus Previous Pane",
		} {
			if key, ok := a.keys[old]; ok {
				if _, set := a.keys[renamed]; !set {
					a.keys[renamed] = key
				}
				delete(a.keys, old)
			}
		}
	}
	for _, name := range st.Servers {
		a.connectServer(name)
	}
	if a.appearance != "" && a.appear != nil {
		a.appear(a.appearance)
	}
	for _, path := range st.HiddenSessions {
		a.hidden[path] = true
	}
	a.pinned = st.Pinned
	if st.Sites != nil {
		a.sites = st.Sites
	}
	for path, name := range st.Names {
		if s, _, ok := a.find(path); ok && s.Name == "" {
			a.rename(path, name)
		}
	}
	a.applyNames()
	if st.Font >= minFont && st.Font <= maxFont {
		a.fontSize = st.Font
	}
	for root, open := range st.Open {
		if p := a.open[root]; p != nil {
			*p = open
		}
	}
	opened := map[int]*tab{}
	for i, path := range st.Tabs {
		if h, ok := live[st.Hosted[i]]; ok {
			if t := a.attachTab(h, path); t != nil {
				opened[i] = t
				continue
			}
		}
		if u, ok := st.Browsers[i]; ok {
			if t := a.startBrowser(u); t != nil {
				t.focusAddr = false
				if dir := st.BrowserDirs[i]; dir != "" {
					t.dir = dir
				}
				if remote, ok := st.RemotePorts[i]; ok && remote.Name != "" && remote.Port > 0 && remote.Port <= 65535 {
					if !isRemote(t.dir) {
						t.dir = remotePath(remote.Name, "")
					}
					t.remotePort, t.remotePortLost = remotePortKey{remote.Name, remote.Port}, true
				}
				opened[i] = t
			}
			continue
		}
		if dir, ok := st.Files[i]; ok {
			opened[i] = a.startFiles(dir)
			continue
		}
		if dir, ok := st.Databases[i]; ok {
			opened[i] = a.startDatabase(dir)
			if view, ok := st.DatabaseViews[i]; ok {
				restoreDatabaseView(opened[i].database, view)
				if opened[i].database.profile != "" {
					a.loadDatabaseSchema(opened[i])
				}
			}
			continue
		}
		if term, ok := st.Terminals[i]; ok {
			if t := a.startShell(term.Dir, ""); t != nil {
				if agent := st.Agents[i]; agent.valid() {
					agent.Tab = t.id
					if mapped := agentForTab(t, agent); mapped.valid() {
						t.agent, t.agentOffered = mapped, true
					}
				}
				opened[i] = t
			}
			continue
		}
		s, _, ok := a.find(path)
		if !ok || s.Running {
			continue
		}
		if t := a.startTab(s.Cwd, s.ID, path); t != nil {
			opened[i] = t
		}
	}
	adopted := a.adoptHosted(live)
	if len(opened) == 0 {
		return a.showAdopted(adopted)
	}
	if st.Panes == nil {
		// An earlier version's: the main pane, and the split beside it.
		st.Panes = &savedPane{Tab: st.Active}
		if _, ok := opened[st.Right]; ok && st.Right != st.Active {
			st.Panes = &savedPane{Tab: -1, Percent: st.SplitP, Panes: []savedPane{{Tab: st.Active}, {Tab: st.Right}}}
		}
	}
	a.layout = restorePanes(st.Panes, opened, map[*tab]bool{})
	if a.layout == nil {
		a.layout = &pane{tab: a.tabs[0]}
	}
	// Older layouts could keep open items outside the pane tree. Give each
	// one a pane on the first launch with the pane-only model.
	for _, t := range a.tabs {
		if a.paneOf(t) == nil {
			a.splitPane(a.layout.leaves()[len(a.layout.leaves())-1], t, false)
		}
	}
	a.focus = a.paneOf(opened[st.Active])
	if a.focus == nil {
		a.focus = a.panes()[0]
	}
	a.zoomed = st.Zoomed && a.split()
	a.focusNext = a.focus.tab
	for _, srv := range a.servers {
		if a.dialOf(srv) != nil {
			a.reconnectRemotePorts(srv)
		}
	}
	if a.focus.tab.files == nil {
		a.followFiles(a.focus.tab.dir)
	}
	return true
}

// showAdopted shows the first of the tabs adopted, when nothing else
// opened, and reports whether there was one.
func (a *App) showAdopted(adopted []*tab) bool {
	if len(adopted) == 0 {
		return false
	}
	for _, t := range adopted {
		a.activate(t)
	}
	return true
}
