package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// savedState is what the window remembers across launches: the sessions
// open as tabs, in order, how the panes showed them, and the sidebar.
type savedState struct {
	Tabs   []string `json:"tabs"`              // transcript paths
	Active int      `json:"active"`            // index into Tabs, -1 for none
	Right  int      `json:"right"`             // the split's tab, -1 for none
	SplitP float32  `json:"split,omitempty"`   // the main pane's share, in percent
	Width  float32  `json:"sidebar,omitempty"` // the sidebar's width
	Hidden bool     `json:"no_sidebar,omitempty"`
	Font   float32  `json:"font_size,omitempty"` // the terminals' text size
	// Open says, for each project's root, whether its section is open.
	Open map[string]bool `json:"open,omitempty"`
	// Hidden are the sessions hidden from the sidebar.
	HiddenSessions []string `json:"hidden,omitempty"`
	// Pinned are the sessions pinned at the top, in order.
	Pinned []string `json:"pinned,omitempty"`
	// Names are the names given to sessions, by transcript path.
	Names map[string]string `json:"names,omitempty"`
}

func (a *App) statePath() string {
	return filepath.Join(a.opts.DataDir, "desktop", "state.json")
}

// state is what to save now. Tabs that haven't started a session yet
// have nothing to resume, so they are left out.
func (a *App) state() savedState {
	st := savedState{Active: -1, Right: -1, SplitP: a.splitP, Width: a.sidebarW, Hidden: a.noSidebar, Font: a.fontSize, Open: map[string]bool{}}
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
	for path, name := range a.names {
		if _, _, ok := a.find(path); ok {
			if st.Names == nil {
				st.Names = map[string]string{}
			}
			st.Names[path] = name
		}
	}
	for _, t := range a.tabs {
		if t.path == "" {
			continue
		}
		if t == a.active {
			st.Active = len(st.Tabs)
		}
		if t == a.right {
			st.Right = len(st.Tabs)
		}
		st.Tabs = append(st.Tabs, t.path)
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

// restore reopens the tabs of the last launch, resuming their sessions,
// and reports whether it opened any. A session that is gone, or open in
// another terminal now, is skipped.
func (a *App) restore() bool {
	data, err := os.ReadFile(a.statePath())
	if a.opts.DataDir == "" {
		err = os.ErrNotExist
	}
	a.restored = true
	if err != nil {
		return false
	}
	var st savedState
	if json.Unmarshal(data, &st) != nil {
		return false
	}
	a.saved = string(data)
	if st.SplitP > 0 {
		a.splitP = st.SplitP
	}
	if st.Width > 0 {
		a.sidebarW = st.Width
	}
	a.noSidebar = st.Hidden
	for _, path := range st.HiddenSessions {
		a.hidden[path] = true
	}
	a.pinned = st.Pinned
	for path, name := range st.Names {
		a.names[path] = name
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
		s, _, ok := a.find(path)
		if !ok || s.Running {
			continue
		}
		if t := a.startTab(s.Cwd, s.ID, path); t != nil {
			opened[i] = t
		}
	}
	if len(opened) == 0 {
		return false
	}
	a.active = opened[st.Active]
	if a.active == nil {
		a.active = a.tabs[0]
	}
	if r := opened[st.Right]; r != nil && r != a.active {
		a.right = r
	}
	a.focusNext = a.active
	return true
}
