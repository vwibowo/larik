package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// windowGroup keeps one command socket and process host while each native
// window owns its own App, pane tree, and saved state.
type windowGroup struct {
	mu       sync.RWMutex
	main     *App
	active   *App
	children map[string]*windowEntry
	ids      []string
	quitting bool
}

type windowEntry struct {
	app *App
	win *mygo.Window
}

func newWindowGroup(a *App) *windowGroup {
	g := &windowGroup{main: a, active: a, children: map[string]*windowEntry{}}
	data, err := os.ReadFile(a.statePath())
	if err == nil {
		var st savedState
		if json.Unmarshal(data, &st) == nil {
			g.ids = slices.Clone(st.Windows)
		}
	}
	a.windowKeys = func() []string {
		g.mu.RLock()
		defer g.mu.RUnlock()
		return slices.Clone(g.ids)
	}
	a.ownedHostTabs = map[string]bool{}
	for _, id := range g.ids {
		for _, tabID := range savedWindowHosted(a.opts.DataDir, id) {
			a.ownedHostTabs[tabID] = true
		}
	}
	return g
}

func savedWindowHosted(dataDir, id string) []string {
	data, err := os.ReadFile(filepath.Join(dataDir, "desktop", "state-"+id+".json"))
	if err != nil {
		return nil
	}
	var st savedState
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	var ids []string
	for _, tabID := range st.Hosted {
		ids = append(ids, tabID)
	}
	return ids
}

func (g *windowGroup) apps() []*App {
	g.mu.RLock()
	defer g.mu.RUnlock()
	apps := []*App{g.main}
	for _, id := range g.ids {
		if e := g.children[id]; e != nil {
			apps = append(apps, e.app)
		}
	}
	return apps
}

func (g *windowGroup) activeApp() *App {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.active
}

func (g *windowGroup) appForNotification(id string) *App {
	for _, a := range g.apps() {
		if a.tabByID(strings.TrimPrefix(id, "larik-tab-")) != nil {
			return a
		}
		for _, p := range a.projects {
			for _, s := range p.Sessions {
				if "larik-"+s.ID == id && a.tabOf(s.Path) != nil {
					return a
				}
			}
		}
	}
	return g.main
}

func (g *windowGroup) windowFor(a *App) *mygo.Window {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, e := range g.children {
		if e.app == a {
			return e.win
		}
	}
	return nil
}

func (g *windowGroup) setActive(a *App) {
	g.mu.Lock()
	g.active = a
	g.mu.Unlock()
}

func (g *windowGroup) removeChild(id string, b *App) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.children, id)
	if !g.quitting {
		g.ids = slices.DeleteFunc(g.ids, func(x string) bool { return x == id })
	}
	if g.active == b {
		g.active = g.main
	}
	return g.quitting
}

func (g *windowGroup) windowPosition(a *App) (int, int) {
	apps := g.apps()
	for i, candidate := range apps {
		if candidate == a {
			return i + 1, len(apps)
		}
	}
	return 1, len(apps)
}

func (g *windowGroup) refreshWindows() {
	for _, a := range g.apps() {
		if a.redraw != nil {
			a.redraw()
		}
	}
}

func (g *windowGroup) navigateWindow(from *App, by int) {
	apps := g.apps()
	if len(apps) < 2 {
		return
	}
	i := slices.Index(apps, from)
	if i < 0 {
		i = 0
	}
	target := apps[(i+by+len(apps))%len(apps)]
	g.setActive(target)
	if target.raise != nil {
		target.raise()
	}
	if target.setMenu != nil {
		target.setMenu()
	}
}

func (g *windowGroup) controlIn(a *App, req Request) Response {
	res := a.control(req)
	if req.Cmd != "open" || res.Tab == nil {
		return res
	}
	for _, owner := range g.apps() {
		var info *TabInfo
		owner.onMain(func() {
			if t := owner.tabByID(res.Tab.ID); t != nil {
				found := owner.tabInfo(t)
				info = &found
			}
		})
		if info != nil {
			res.Tab = info
			return res
		}
	}
	return res
}

func (g *windowGroup) control(req Request) Response {
	if req.Cmd == "list" {
		var out Response
		active := g.activeApp()
		for _, a := range g.apps() {
			listed := a.control(req).Tabs
			if a != active {
				for i := range listed {
					listed[i].Focused = false
				}
			}
			out.Tabs = append(out.Tabs, listed...)
		}
		return out
	}
	if req.Tab != "" && req.Tab != "focused" {
		for _, a := range g.apps() {
			var found bool
			if a.onMain(func() { _, err := a.tabRef(req.Tab); found = err == "" }) && found {
				return g.controlIn(a, req)
			}
		}
	}
	if req.Cmd == "open" && req.Session != "" {
		for _, a := range g.apps() {
			var found bool
			if a.onMain(func() {
				s, err := a.sessionRef(req.Session)
				found = err == "" && a.tabOf(s.Path) != nil
			}) && found {
				return g.controlIn(a, req)
			}
		}
	}
	if req.Cmd == "pane" && req.Ref != "" {
		for _, a := range g.apps() {
			var found bool
			if a.onMain(func() { found = a.paneRef(req.Ref) != nil }) && found {
				return g.controlIn(a, req)
			}
		}
	}
	return g.controlIn(g.activeApp(), req)
}

// restoreChildren runs after the primary window restored its own panes.
func (g *windowGroup) restoreChildren() {
	g.mu.RLock()
	ids := slices.Clone(g.ids)
	g.mu.RUnlock()
	for _, id := range ids {
		if _, err := os.Stat(filepath.Join(g.main.opts.DataDir, "desktop", "state-"+id+".json")); err != nil {
			g.mu.Lock()
			g.ids = slices.DeleteFunc(g.ids, func(x string) bool { return x == id })
			g.mu.Unlock()
			continue
		}
		if e := g.create(id, false); e != nil {
			go e.app.Start()
		}
	}
}

func (g *windowGroup) create(id string, background bool) *windowEntry {
	b := New(g.main.opts)
	b.windowID, b.skipAdoption = id, true
	e := &windowEntry{app: b}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title: "Larik", Width: 1240, Height: 800, MinWidth: 720, MinHeight: 480,
		StateKey: "main-" + id, TitleBarStyle: mygo.TitleBarHidden,
		TrafficLightPosition: trafficLights, Transparent: true, Vibrancy: mygo.VibrancySidebar,
		Content: ui.View(b.view),
	})
	e.win = win
	g.mu.Lock()
	g.children[id] = e
	g.mu.Unlock()
	b.update = win.Update
	b.redraw = func() { win.Update(func() {}) }
	b.contentSize = win.ContentSize
	b.overflow = func(t *tab) bool { return g.overflow(b, t) }
	b.activateWindow = func() { g.setActive(b) }
	b.navigateWindow = func(by int) { g.navigateWindow(b, by) }
	b.windowPosition = func() (int, int) { return g.windowPosition(b) }
	b.quitAll = g.quitEnding
	b.appear = g.main.appear
	b.showSettings = g.main.showSettings
	b.badge = nil // the primary window owns the app-wide badge
	b.copyText = func(s string) { _ = mygo.Clipboard.Write(transfer.TextData(s)) }
	b.showFile, b.trashItem = g.main.showFile, g.main.trashItem
	b.openPath, b.openExternal = g.main.openPath, g.main.openExternal
	b.newWebView = func(url string) (*mygo.WebView, error) { return win.NewWebView(webViewOptions(url)) }
	b.about = g.main.about
	b.post, b.ping = g.main.post, g.main.ping
	b.raise = func() { raise(win) }
	b.ask = func(title, msg, yes string, then func(bool)) {
		go func() {
			res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: win, Type: mygo.MessageWarning, Message: title, Detail: msg, Buttons: []string{yes, "Cancel"}, DefaultButton: 0, CancelButton: 1})
			win.Update(func() { then(err == nil && res.Button == 0) })
		}()
	}
	var titleMu sync.Mutex
	b.setTitle = func(s string) {
		titleMu.Lock()
		defer titleMu.Unlock()
		win.SetTitle(s)
	}
	b.pickDir = func(start string, then func(string)) {
		go func() {
			dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "Open Folder", Message: "Start a larik session in a folder", DefaultPath: start, ButtonLabel: "Start Session", Directory: true, CreateDirectories: true})
			if err == nil && len(dirs) > 0 {
				win.Update(func() { then(dirs[0]) })
			}
		}()
	}
	b.setMenu = func() { mygo.App.SetMenu(b.menu(win.Update)) }
	b.dock = nil
	b.leave = func(quit bool) {
		if quit {
			mygo.App.Quit()
		} else {
			win.Close()
		}
	}
	ignoreInitialFocus := background
	win.OnFocus(func() {
		if ignoreInitialFocus {
			ignoreInitialFocus = false
			return
		}
		g.setActive(b)
		b.setMenu()
	})
	win.OnClose(func(ev *mygo.CloseEvent) {
		g.mu.RLock()
		quitting := g.quitting
		g.mu.RUnlock()
		if !quitting {
			b.ending = true // closing this window closes its panes
		}
		if !b.windowClosing() {
			ev.PreventDefault()
			win.Update(func() {})
		}
	})
	win.OnClosed(func() {
		b.Close()
		quitting := g.removeChild(id, b)
		if !quitting {
			_ = os.Remove(b.statePath())
			g.main.saveState()
			g.refreshWindows()
		}
	})
	win.Show()
	return e
}

func (g *windowGroup) overflow(src *App, t *tab) bool {
	if t == nil || isRemote(t.dir) || t.remotePort.port != 0 || t.exited {
		return false
	}
	// Non-hosted terminal processes cannot be moved between windows without
	// restarting them. Keep those in the existing grid.
	if t.term != nil && !t.hosted {
		return false
	}
	var e *windowEntry
	g.mu.RLock()
	for i := len(g.ids) - 1; i >= 0; i-- {
		candidate := g.children[g.ids[i]]
		if candidate != nil && candidate.app != src && !candidate.app.panesTooSmall(len(candidate.app.panes())+1) {
			e = candidate
			break
		}
	}
	g.mu.RUnlock()
	created := e == nil
	if created {
		e = g.create(newTabID(), src.backgroundOpen)
		if e == nil {
			return false
		}
	}
	b := e.app
	if created {
		b.setProjects(src.projects)
		b.restored = true
		b.noChooser, b.sidebarW, b.fontSize = src.noChooser, src.sidebarW, src.fontSize
		go b.indexLoop()
		go b.metaLoop()
	}
	var opened *tab
	switch {
	case t.chooser:
		opened = b.newChooser(t.dir)
	case t.browser:
		opened = b.startBrowser(t.url)
		if opened != nil {
			opened.dir = t.dir
		}
	case t.files != nil:
		opened = b.startFiles(t.dir)
	case t.hosted:
		if h, ok := src.hostedTabs()[t.id]; ok {
			opened = b.attachTab(h, t.path)
		}
	}
	if opened == nil {
		if created {
			e.win.Close()
		}
		return false
	}
	opened.id = t.id // CLI pane references stay valid after crossing windows.
	src.tabs = slices.DeleteFunc(src.tabs, func(x *tab) bool { return x == t })
	t.close() // detaches from the host; the process continues in b
	b.activate(opened)
	if !src.backgroundOpen {
		e.win.Focus()
	}
	g.mu.Lock()
	if !src.backgroundOpen {
		g.active = b
	}
	if created {
		g.ids = append(g.ids, b.windowID)
	}
	g.mu.Unlock()
	if created {
		g.main.saveState()
		g.refreshWindows()
	}
	src.saveState()
	b.saveState()
	return true
}

func (g *windowGroup) close() {
	g.mu.RLock()
	children := make([]*windowEntry, 0, len(g.children))
	for _, e := range g.children {
		children = append(children, e)
	}
	g.mu.RUnlock()
	for _, e := range children {
		e.app.Close()
	}
	g.main.Close()
}

func (g *windowGroup) quitEnding() {
	apps := g.apps()
	working := 0
	for _, a := range apps {
		a.ending, a.quitting = true, true
		working += len(a.stopping())
	}
	finish := func(yes bool) {
		if yes {
			mygo.App.Quit()
			return
		}
		for _, a := range apps {
			a.ending, a.quitting = false, false
		}
	}
	if working == 0 {
		finish(true)
		return
	}
	active := g.activeApp()
	if active.ask != nil {
		active.ask("Quit and end panes?", fmt.Sprintf("This ends %d running or working panes across every window.", working), "Quit and End", finish)
	} else {
		finish(false)
	}
}

func (g *windowGroup) activeWindow() *mygo.Window {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, e := range g.children {
		if e.app == g.active {
			return e.win
		}
	}
	return nil
}
