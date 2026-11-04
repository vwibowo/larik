package desktop

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

type closedTab struct {
	Kind       string          `json:"kind"`
	Path       string          `json:"path,omitempty"`
	Dir        string          `json:"dir,omitempty"`
	URL        string          `json:"url,omitempty"`
	RemotePort savedRemotePort `json:"remote_port,omitzero"`
	Anchor     string          `json:"anchor,omitempty"`
	Below      bool            `json:"below,omitempty"`
	Before     bool            `json:"before,omitempty"`
}

func (a *App) rememberClosed(t *tab) {
	r := closedTab{Path: t.path, Dir: t.dir, URL: t.url}
	if t.remotePort.port != 0 {
		r.RemotePort = savedRemotePort{t.remotePort.name, t.remotePort.port}
		r.URL = blankPage
	}
	switch {
	case t.path != "":
		r.Kind = "larik"
	case t.browser:
		r.Kind = "browser"
	case t.files != nil:
		r.Kind = "files"
	case t.shell():
		r.Kind = "terminal"
	default:
		return
	}
	if p := a.paneOf(t); p != nil && p.parent != nil {
		r.Below = p.parent.below
		r.Before = p.parent.first == p
		sibling := p.parent.first
		if sibling == p {
			sibling = p.parent.second
		}
		if leaves := sibling.leaves(); len(leaves) > 0 {
			r.Anchor = leaves[0].tab.id
		}
	}
	a.recent = append(a.recent, r)
	if len(a.recent) > closedKept {
		a.recent = a.recent[len(a.recent)-closedKept:]
	}
}

func (a *App) reopenRecent(r closedTab) bool {
	var t *tab
	switch r.Kind {
	case "larik":
		if r.Path == "" {
			return false
		}
		if existing := a.tabOf(r.Path); existing != nil {
			a.activate(existing)
			return true
		}
		s, _, ok := a.find(r.Path)
		if !ok || s.Running {
			return false
		}
		t = a.startTab(s.Cwd, s.ID, s.Path)
		a.closed = slices.DeleteFunc(a.closed, func(path string) bool { return path == r.Path })
	case "terminal":
		t = a.startShell(r.Dir, "")
	case "browser":
		t = a.startBrowser(r.URL)
		if t != nil && r.RemotePort.Port > 0 {
			t.dir = r.Dir
			t.remotePort = remotePortKey{r.RemotePort.Name, r.RemotePort.Port}
			t.remotePortLost = true
		}
	case "files":
		t = a.startFiles(r.Dir)
	}
	if t == nil {
		return false
	}
	if anchor := a.tabByID(r.Anchor); anchor != nil {
		if p := a.paneOf(anchor); p != nil {
			a.splitAt(p, t, r.Below, r.Before)
			if t.remotePortLost {
				a.startRemoteForward(t.remotePort, false, t, "")
			}
			return true
		}
	}
	a.activate(t)
	if t.remotePortLost {
		a.startRemoteForward(t.remotePort, false, t, "")
	}
	return true
}

func (r closedTab) label() string {
	switch r.Kind {
	case "larik":
		return "Larik · " + filepath.Base(r.Path)
	case "browser":
		if r.RemotePort.Port > 0 {
			return "Browser · " + r.RemotePort.Name + ":" + strconv.Itoa(r.RemotePort.Port)
		}
		return "Browser · " + r.URL
	case "files":
		return "Files · " + r.Dir
	case "terminal":
		return "Terminal · " + r.Dir
	}
	return r.Kind
}

func (a *App) recentDialog(c *ui.Context) {
	if !a.recentOpen {
		return
	}
	ui.Modal(c.Key("recent-tabs"), &a.recentOpen, func() {
		ui.Column(c).Width(480).Gap(8).Children(func() {
			ui.Text(c, "Recently Closed Tabs").FontSize(16).Bold()
			ui.SearchField(c.Key("search"), &a.recentQuery).Placeholder("Search closed tabs").FillWidth()
			shown := 0
			for i := len(a.recent) - 1; i >= 0 && shown < 20; i-- {
				r := a.recent[i]
				label := r.label()
				if !strings.Contains(strings.ToLower(label), strings.ToLower(a.recentQuery)) {
					continue
				}
				shown++
				if ui.Text(c.Key(strconv.Itoa(i)), label).Cursor(ui.CursorPointer).Clicked() {
					if a.reopenRecent(r) {
						a.recent = slices.Delete(a.recent, i, i+1)
						a.recentOpen = false
					} else {
						a.notice = "That tab is no longer available."
					}
				}
			}
			if shown == 0 {
				ui.Text(c, "No matching closed tabs.").FontSize(12).TextColor(c.Theme().TextMuted)
			}
		})
	}).Label("Recently Closed Tabs")
}
