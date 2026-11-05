package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type projectLayout struct {
	Version int         `json:"version"`
	Tabs    []layoutTab `json:"tabs"`
	Panes   *savedPane  `json:"panes,omitempty"`
	Active  int         `json:"active"`
}

type layoutTab struct {
	ID         string          `json:"id,omitempty"`
	Kind       string          `json:"kind"`
	Dir        string          `json:"dir"`
	Path       string          `json:"path,omitempty"`
	URL        string          `json:"url,omitempty"`
	Agent      agentResume     `json:"agent,omitzero"`
	RemotePort savedRemotePort `json:"remote_port,omitzero"`
}

func projectLayoutsPath(dataDir string) string {
	return filepath.Join(dataDir, "desktop", "layouts.json")
}

func readProjectLayouts(dataDir string) map[string]map[string]projectLayout {
	layouts := map[string]map[string]projectLayout{}
	if dataDir == "" {
		return layouts
	}
	data, err := os.ReadFile(projectLayoutsPath(dataDir))
	if err == nil {
		_ = json.Unmarshal(data, &layouts)
	}
	return layouts
}

func (a *App) writeProjectLayouts() error {
	if a.opts.DataDir == "" {
		return errors.New("Desktop has no data folder")
	}
	path := projectLayoutsPath(a.opts.DataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a.layouts, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func validLayoutName(name string) bool {
	return len(name) > 0 && len(name) <= 64 && !strings.ContainsAny(name, "/\\\r\n") && strings.TrimSpace(name) == name
}

func (a *App) saveProjectLayout(root, name string) error {
	if !validLayoutName(name) {
		return errors.New("layout name must be 1–64 characters without a slash")
	}
	if root == "" {
		return errors.New("choose a project first")
	}
	layout := projectLayout{Version: 1, Active: -1}
	index := map[*tab]int{}
	for _, t := range a.tabs {
		if !filesWithinRoot(root, t.dir) {
			continue
		}
		item := layoutTab{ID: t.id, Dir: t.dir, Path: t.path, URL: t.url, Agent: t.agent}
		if t.remotePort.port != 0 {
			item.RemotePort = savedRemotePort{t.remotePort.name, t.remotePort.port}
			item.URL = blankPage
		}
		switch {
		case t.path != "":
			item.Kind = "larik"
		case t.browser:
			item.Kind = "browser"
		case t.files != nil:
			item.Kind = "files"
		case t.shell():
			item.Kind = "terminal"
		default:
			continue
		}
		if a.focused() == t {
			layout.Active = len(layout.Tabs)
		}
		index[t] = len(layout.Tabs)
		layout.Tabs = append(layout.Tabs, item)
	}
	if len(layout.Tabs) == 0 {
		return errors.New("the project has no open tabs to save")
	}
	layout.Panes = savePanes(a.layout, index)
	if a.layouts[root] == nil {
		a.layouts[root] = map[string]projectLayout{}
	}
	a.layouts[root][name] = layout
	return a.writeProjectLayouts()
}

func (a *App) openProjectLayout(root, name string) error {
	layout, ok := a.layouts[root][name]
	if !ok {
		return fmt.Errorf("no layout %q in this project", name)
	}
	opened := map[int]*tab{}
	for i, item := range layout.Tabs {
		if t := a.tabByID(item.ID); t != nil {
			opened[i] = t
			continue
		}
		if !isRemote(item.Dir) {
			if info, err := os.Stat(item.Dir); err != nil || !info.IsDir() {
				continue
			}
		}
		var t *tab
		switch item.Kind {
		case "larik":
			if t = a.tabOf(item.Path); t == nil {
				s, _, ok := a.find(item.Path)
				if !ok || s.Running {
					continue
				}
				t = a.startTab(s.Cwd, s.ID, s.Path)
			}
		case "terminal":
			t = a.startShell(item.Dir, "")
			if t != nil && item.Agent.valid() {
				item.Agent.Tab = t.id
				t.agent = agentForTab(t, item.Agent)
				t.agentOffered = t.agent.valid()
			}
		case "browser":
			t = a.startBrowser(item.URL)
			if t != nil {
				t.dir = item.Dir
				if item.RemotePort.Port > 0 {
					t.remotePort = remotePortKey{item.RemotePort.Name, item.RemotePort.Port}
					t.remotePortLost = true
				}
			}
		case "files":
			t = a.startFiles(item.Dir)
		}
		if t != nil {
			opened[i] = t
		}
	}
	if len(opened) == 0 {
		return errors.New("none of the layout's folders or sessions is available")
	}
	if panes := restorePanes(layout.Panes, opened, map[*tab]bool{}); panes != nil {
		a.layout = panes
		for _, t := range a.tabs {
			if a.paneOf(t) == nil {
				a.splitPane(a.layout.leaves()[len(a.layout.leaves())-1], t, false)
			}
		}
		a.focus = a.paneOf(opened[layout.Active])
		if a.focus == nil {
			a.focus = panes.leaves()[0]
		}
		a.focusPane(a.focus)
	} else {
		for i := range layout.Tabs {
			if t := opened[i]; t != nil {
				a.activate(t)
				break
			}
		}
	}
	for _, t := range opened {
		if t.remotePortLost {
			a.startRemoteForward(t.remotePort, false, t, "")
		}
	}
	return nil
}
