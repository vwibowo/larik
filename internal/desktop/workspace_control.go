package desktop

import (
	"cmp"
	"fmt"
)

type WorkspaceInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Window   string `json:"window"`
	Position int    `json:"position"`
	Active   bool   `json:"active"`
	Panes    int    `json:"panes"`
}

func (a *App) workspaceInfo(w *workspace) WorkspaceInfo {
	a.currentWorkspace()
	for i, candidate := range a.workspaces {
		if candidate == w {
			return WorkspaceInfo{ID: w.id, Name: w.name, Window: cmp.Or(a.windowID, "main"), Position: i + 1, Active: w == a.activeWorkspace, Panes: len(w.layout.leaves())}
		}
	}
	return WorkspaceInfo{}
}

func (a *App) controlWorkspace(req Request) Response {
	if req.Op == "list" {
		out := Response{}
		for _, w := range a.workspaces {
			out.Workspaces = append(out.Workspaces, a.workspaceInfo(w))
		}
		return out
	}
	w := a.workspaceByID(req.Workspace)
	if req.Op != "new" && w == nil {
		return Response{Error: "no such workspace"}
	}
	switch req.Op {
	case "new":
		if req.Name != "" {
			if err := a.renameWorkspace(&workspace{}, req.Name); err != nil {
				return Response{Error: err.Error()}
			}
		}
		w = a.newWorkspace(req.Name)
	case "focus":
		a.switchWorkspace(w)
		if a.raise != nil {
			a.raise()
		}
		if a.activateWindow != nil {
			a.activateWindow()
		}
	case "rename":
		if err := a.renameWorkspace(w, req.Name); err != nil {
			return Response{Error: err.Error()}
		}
	case "reorder":
		if err := a.reorderWorkspace(w, req.Position); err != nil {
			return Response{Error: err.Error()}
		}
	case "move-pane":
		t, err := a.tabRef(req.Tab)
		if err != "" {
			return Response{Error: err}
		}
		if err := a.movePaneToWorkspace(t, w); err != nil {
			return Response{Error: err.Error()}
		}
	case "close":
		if a.confirming {
			return Response{Error: "a close confirmation is already open"}
		}
		if req.Force {
			if len(a.workspaces) < 2 {
				return Response{Error: "the last workspace cannot be closed"}
			}
			a.currentWorkspace()
			a.closing = nil
			a.closingWorkspace = w
			for _, p := range w.layout.leaves() {
				a.closing = append(a.closing, p.tab)
			}
			a.closeNow()
			return Response{}
		}
		if err := a.closeWorkspace(w); err != nil {
			return Response{Error: err.Error()}
		}
		if a.confirming {
			return Response{State: "confirmation"}
		}
		return Response{}
	default:
		return Response{Error: fmt.Sprintf("unknown workspace operation %q", req.Op)}
	}
	return Response{Workspaces: []WorkspaceInfo{a.workspaceInfo(w)}}
}
