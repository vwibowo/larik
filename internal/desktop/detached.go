package desktop

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// detachedTerminal is a host-owned terminal without a Desktop pane.
// source is empty for this machine, or the connected SSH destination.
type detachedTerminal struct {
	source string
	hostTabInfo
}

func (d detachedTerminal) key() string { return "detached:" + d.source + ":" + d.ID }

func (d detachedTerminal) label() string {
	if d.Title != "" {
		return d.Title
	}
	if d.Run != "" {
		return d.Run
	}
	return "Terminal"
}

func (d detachedTerminal) detail() string {
	place := d.Dir
	if d.source != "" {
		place = d.source + ":" + d.Dir
	}
	state := "Running"
	if d.Exited {
		state = fmt.Sprintf("Exited (%d)", d.Code)
	}
	return state + " · " + place
}

func (a *App) canDetach(t *tab) bool {
	if t == nil || !t.shell() || !t.hosted || t.disconnected {
		return false
	}
	if name, _, remote := splitRemote(t.dir); remote {
		srv := a.serverNamed(name)
		return srv != nil && a.dialOf(srv) != nil && slices.Contains(srv.features, "detached-terminals")
	}
	return a.opts.Host
}

// detachPane asks the host to keep the terminal before removing its pane.
// A failed request leaves the pane and process alone.
func (a *App) detachPane(t *tab) {
	if !a.canDetach(t) {
		a.notice = "This terminal cannot be detached. Connect to its host or update Larik there."
		return
	}
	go func() {
		var err error
		if name, _, remote := splitRemote(t.dir); remote {
			srv := a.serverNamed(name)
			conn, _, _, e := askHostVia(a.dialOf(srv), hostRequest{Op: "detach", ID: t.id})
			err = e
			if conn != nil {
				conn.Close()
			}
		} else {
			conn, _, _, e := askHost(a.opts.DataDir, hostRequest{Op: "detach", ID: t.id})
			err = e
			if conn != nil {
				conn.Close()
			}
		}
		a.update(func() {
			if err != nil {
				a.notice = "Can't detach terminal: " + err.Error()
				return
			}
			if a.tabByID(t.id) == t {
				a.forgetTab(t)
			}
			a.refreshDetached()
		})
	}()
}

func (a *App) refreshDetached() {
	a.refreshIndex()
	if a.detachedChanged != nil {
		a.detachedChanged()
	}
}

// readDetached reads both the local host and connected remote hosts off
// the UI thread. Hosts own the detached marker and final terminal screen.
func (a *App) readDetached() []detachedTerminal {
	var out []detachedTerminal
	add := func(source string, tabs []hostTabInfo) {
		for _, h := range tabs {
			if h.Detached && h.Shell {
				out = append(out, detachedTerminal{source, h})
			}
		}
	}
	if a.opts.Host {
		if tabs, err := hostTabs(a.opts.DataDir); err == nil {
			add("", tabs)
		}
	}
	a.serversMu.Lock()
	dials := map[string]dialer{}
	for _, srv := range a.servers {
		if srv.dial != nil {
			dials[srv.name] = srv.dial
		}
	}
	a.serversMu.Unlock()
	for name, dial := range dials {
		conn, _, rep, err := askHostVia(dial, hostRequest{Op: "list"})
		if err == nil {
			conn.Close()
			add(name, rep.Tabs)
		}
	}
	slices.SortFunc(out, func(x, y detachedTerminal) int { return y.Started.Compare(x.Started) })
	return out
}

func (a *App) openDetached(d detachedTerminal) {
	if a.openDetachedInGroup != nil {
		a.openDetachedInGroup(d)
		return
	}
	a.reattachDetached(d)
}

func (a *App) reattachDetached(d detachedTerminal) {
	if t := a.tabByID(d.ID); t != nil {
		a.activate(t)
		return
	}
	h := d.hostTabInfo
	if d.source != "" {
		srv := a.serverNamed(d.source)
		if srv == nil || a.dialOf(srv) == nil {
			a.notice = "Connect to " + d.source + " to reattach this terminal."
			return
		}
		h.Dir = remotePath(d.source, h.Dir)
	}
	if t := a.attachTab(h, ""); t != nil {
		a.activate(t)
		a.refreshDetached()
	} else if a.notice == "" {
		a.notice = "Can't reattach terminal " + d.ID
	}
}

func (a *App) endDetached(d detachedTerminal) {
	var err error
	if d.source == "" {
		err = closeHostTab(a.opts.DataDir, d.ID)
	} else if srv := a.serverNamed(d.source); srv == nil || a.dialOf(srv) == nil {
		err = fmt.Errorf("connect to %s first", d.source)
	} else {
		conn, _, _, e := askHostVia(a.dialOf(srv), hostRequest{Op: "close", ID: d.ID})
		err = e
		if conn != nil {
			conn.Close()
		}
	}
	if err != nil {
		a.notice = "Can't end terminal: " + err.Error()
		return
	}
	a.detached = slices.DeleteFunc(a.detached, func(x detachedTerminal) bool { return x.key() == d.key() })
	a.refreshDetached()
}

func (a *App) detachedSection(c *ui.Context, t *ui.Theme) {
	if len(a.detached) == 0 {
		return
	}
	ui.SidebarSection(c.Key("detached-terminals"), "Detached Terminals", nil, func() {
		for _, d := range a.detached {
			d := d
			label := d.label()
			ui.SidebarItem(c, d.key(), iconTerm, label).Tooltip(d.detail()).
				ContextMenu(func(m *ui.Menu) {
					if m.Item("Reattach Terminal").Chosen() {
						a.openDetached(d)
					}
					if m.Item("End Terminal").Chosen() {
						a.endDetached(d)
					}
				})
			ui.Text(c, d.detail()).FontSize(11).TextColor(t.TextMuted).Padding(0, 12, 4, 34)
		}
	})
}

func (a *App) selectDetached(key string) bool {
	if !strings.HasPrefix(key, "detached:") {
		return false
	}
	for _, d := range a.detached {
		if d.key() == key {
			a.openDetached(d)
			return true
		}
	}
	return true
}
