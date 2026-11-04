package desktop

import (
	"fmt"
	"slices"
	"time"
)

type PaneInfo struct {
	ID      string `json:"id"`
	Tab     string `json:"tab"`
	Focused bool   `json:"focused,omitempty"`
}

type NotificationInfo struct {
	At    time.Time `json:"at"`
	Tab   string    `json:"tab,omitempty"`
	Path  string    `json:"path,omitempty"`
	Title string    `json:"title"`
	Body  string    `json:"body,omitempty"`
	Read  bool      `json:"read,omitempty"`
}

func (a *App) paneRef(id string) *pane {
	for _, p := range a.panes() {
		if p.id == "" {
			p.id = newTabID()
		}
		if p.id == id {
			return p
		}
	}
	return nil
}

func (a *App) controlPane(req Request) Response {
	switch req.Op {
	case "list":
		var out []PaneInfo
		for _, p := range a.panes() {
			if p.id == "" {
				p.id = newTabID()
			}
			out = append(out, PaneInfo{ID: p.id, Tab: p.tab.id, Focused: a.focus == p})
		}
		return Response{Panes: out}
	case "focus":
		if p := a.paneRef(req.Ref); p != nil {
			a.focusPane(p)
			return Response{Panes: []PaneInfo{{ID: p.id, Tab: p.tab.id, Focused: true}}}
		}
		return Response{Error: "no such pane"}
	case "move":
		p := a.paneRef(req.Ref)
		if p == nil {
			return Response{Error: "no such pane"}
		}
		t, err := a.tabRef(req.Tab)
		if err != "" {
			return Response{Error: err}
		}
		a.show(p, t)
		a.focusPane(p)
		return Response{Panes: []PaneInfo{{ID: p.id, Tab: t.id, Focused: true}}}
	case "layout":
		layouts := map[string]layout{"columns": layoutColumns, "rows": layoutRows, "grid": layoutGrid, "main-stack": layoutMainStack}
		l, ok := layouts[req.State]
		if !ok {
			return Response{Error: "pane layout must be columns, rows, grid or main-stack"}
		}
		a.arrange(l)
		return a.controlPane(Request{Op: "list"})
	}
	return Response{Error: fmt.Sprintf("unknown pane operation %q", req.Op)}
}

func (a *App) controlNotifications(req Request) Response {
	switch req.Op {
	case "list":
		out := make([]NotificationInfo, 0, len(a.events))
		for _, e := range a.events {
			out = append(out, NotificationInfo{At: e.at, Tab: e.tab, Path: e.path, Title: e.title, Body: e.body, Read: e.read})
		}
		return Response{Notifications: out}
	case "clear":
		a.events = nil
		return Response{}
	}
	return Response{Error: "notifications needs list or clear"}
}

func (a *App) controlProjectLayout(req Request) Response {
	if req.Project == "" {
		return Response{Error: "project-layout needs --project"}
	}
	switch req.Op {
	case "list":
		var names []string
		for name := range a.layouts[req.Project] {
			names = append(names, name)
		}
		slices.Sort(names)
		return Response{Layouts: names}
	case "save":
		if err := a.saveProjectLayout(req.Project, req.Name); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Layouts: []string{req.Name}}
	case "open":
		if err := a.openProjectLayout(req.Project, req.Name); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Layouts: []string{req.Name}}
	}
	return Response{Error: "project-layout needs list, save or open"}
}
