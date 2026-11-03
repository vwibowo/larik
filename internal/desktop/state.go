package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// savedState is what the window remembers across launches: the sessions
// open as tabs, in order, how the panes showed them, and the sidebar.
type savedState struct {
	Tabs []string `json:"tabs"` // transcript paths, "" for a terminal or browser tab
	// Terminals are the terminal tabs, by their index in Tabs.
	Terminals map[int]savedTerminal `json:"terminals,omitempty"`
	// Browsers are the browser tabs' pages, by their index in Tabs, and
	// Sites what you let their pages use, or didn't, by siteKey.
	Browsers map[int]string  `json:"browsers,omitempty"`
	Sites    map[string]bool `json:"sites,omitempty"`
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
	NoChooser bool    `json:"no_chooser,omitempty"`
	Font      float32 `json:"font_size,omitempty"` // the terminals' text size
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

func (a *App) statePath() string {
	return filepath.Join(a.opts.DataDir, "desktop", "state.json")
}

// state is what to save now. Tabs that haven't started a session yet
// have nothing to resume, so they are left out.
func (a *App) state() savedState {
	st := savedState{Active: -1, Zoomed: a.zoomed, Width: a.sidebarW, Hidden: a.noSidebar, NoChooser: a.noChooser, Font: a.fontSize, Open: map[string]bool{}}
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
	index := map[*tab]int{}
	for _, t := range a.tabs {
		path := t.path
		switch {
		case t.shell() && !t.exited:
			if st.Terminals == nil {
				st.Terminals = map[int]savedTerminal{}
			}
			st.Terminals[len(st.Tabs)] = savedTerminal{Dir: t.dir, Run: t.run}
			path = ""
		case t.browser:
			if st.Browsers == nil {
				st.Browsers = map[int]string{}
			}
			st.Browsers[len(st.Tabs)] = t.url
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
	if st.Width > 0 {
		a.sidebarW = st.Width
	}
	a.noSidebar = st.Hidden
	a.noChooser = st.NoChooser
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
				opened[i] = t
			}
			continue
		}
		if term, ok := st.Terminals[i]; ok {
			if t := a.startShell(term.Dir, ""); t != nil {
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
	a.focus = a.paneOf(opened[st.Active])
	if a.focus == nil {
		a.focus = a.panes()[0]
	}
	a.zoomed = st.Zoomed && a.split()
	a.focusNext = a.focus.tab
	return true
}

// showAdopted shows the first of the tabs adopted, when nothing else
// opened, and reports whether there was one.
func (a *App) showAdopted(adopted []*tab) bool {
	if len(adopted) == 0 {
		return false
	}
	a.activate(adopted[0])
	return true
}
