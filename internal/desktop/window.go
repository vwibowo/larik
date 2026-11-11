package desktop

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"larik/internal/session"
)

// Prepare loads the terminal library the tabs need, downloading it into
// the user's cache on first use (checking its SHA-256), unless
// $MYGO_GHOSTTY_VT names one.
func Prepare() error {
	if err := terminal.Load(); err != nil {
		return fmt.Errorf("larik desktop needs Ghostty's terminal library: %w", err)
	}
	return nil
}

// Run opens the window and returns once it has been closed, after its
// tabs' larik processes have ended.
func Run(opts Options) error {
	if runtime.GOOS == "darwin" {
		mygo.App.DisablePressAndHold()
	}
	if err := Prepare(); err != nil {
		return err
	}
	a := New(opts)
	group := newWindowGroup(a)
	defer group.close()
	var settingsWin *mygo.Window
	// win is set once the window opens; a directory handed over before
	// then waits for it.
	var (
		mu      sync.Mutex
		opened  *mygo.Window
		pending []string
	)
	ln, err := listen(opts.DataDir, func(cwd string) {
		mu.Lock()
		defer mu.Unlock()
		if opened == nil {
			pending = append(pending, cwd)
			return
		}
		show(opened, a, cwd)
	}, func(req Request) Response {
		mu.Lock()
		ready := opened != nil
		mu.Unlock()
		if !ready {
			return Response{Error: "larik desktop is still opening: try again in a moment"}
		}
		return group.control(req)
	})
	if errors.Is(err, errRunning) {
		Handover(opts.DataDir, opts.Cwd)
		return nil
	}
	if err != nil {
		return err
	}
	if ln != nil {
		defer ln.Close()
	}
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:         "Larik",
			Width:         1240,
			Height:        800,
			MinWidth:      720,
			MinHeight:     480,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			// Centered in the toolbar, as a macOS window with a unified
			// toolbar puts them: AppKit's default suits a 28-point title bar.
			TrafficLightPosition: trafficLights,
			Transparent:          true,
			Vibrancy:             mygo.VibrancySidebar,
			Content:              ui.View(a.view),
		})
		fmt.Fprintf(os.Stderr, "larik desktop: notifications %s\n", map[bool]string{true: "on", false: "off (macOS shows them only for an app bundle: larik desktop --install)"}[mygo.NotificationsSupported()])
		a.update = win.Update
		a.redraw = func() { win.Update(func() {}) }
		a.contentSize = win.ContentSize
		a.fillWindow = win.Maximize
		a.centerWindow = win.Center
		a.overflow = func(t *tab) bool { return group.overflow(a, t) }
		a.activateWindow = func() { group.setActive(a) }
		a.navigateWindow = func(by int) { group.navigateWindow(a, by) }
		a.windowPosition = func() (int, int) { return group.windowPosition(a) }
		a.quitAll = group.quitEnding
		a.appear = func(v string) { mygo.Theme.SetSource(mygo.ThemeSource(cmp.Or(v, string(mygo.ThemeSystem)))) }
		a.showSettings = func() {
			if settingsWin != nil {
				a.refreshSettings()
				settingsWin.Show()
				settingsWin.Focus()
				return
			}
			settingsWin = mygo.NewWindow(mygo.WindowOptions{
				Title: "Larik Settings", Width: 960, Height: 680,
				MinWidth: 760, MinHeight: 500, StateKey: "settings",
				TitleBarStyle:        mygo.TitleBarHidden,
				TrafficLightPosition: trafficLights,
				Content:              ui.View(a.settingsView),
			})
			settingsWin.OnFocus(func() {
				a.refreshSettings()
				settingsWin.Update(func() {})
			})
			settingsWin.OnClosed(func() { settingsWin = nil })
			settingsWin.Show()
		}
		a.badge = mygo.App.SetBadgeCount
		a.copyText = func(s string) { _ = mygo.Clipboard.Write(transfer.TextData(s)) }
		a.saveDatabaseCSV = databaseCSVDialog(win, a)
		a.showFile = mygo.Shell.ShowItemInFolder
		a.trashItem = mygo.Shell.TrashItem
		a.openPath = func(path string) { _ = mygo.Shell.OpenPath(path) }
		a.openExternal = func(url string) { _ = mygo.Shell.OpenExternal(url) }
		a.newWebView = func(url string) (*mygo.WebView, error) { return win.NewWebView(webViewOptions(url)) }
		a.newFileWebView = func() (*mygo.WebView, error) {
			return win.NewWebView(mygo.WebViewOptions{Page: mygo.PageOptions{Untrusted: true}})
		}
		a.about = mygo.App.ShowAboutPanel
		a.alert = func(s session.Indexed) {
			target := group.main
			for _, app := range group.apps() {
				if app.tabOf(s.Path) != nil {
					target = app
					break
				}
			}
			notify(win, s, target.notifyActions(s))
		}
		a.post = func(title, body string) { post(win, title, body) }
		a.ping = func(id, title, body string) { notifyText(win, "larik-tab-"+id, title, body, "") }
		a.raise = func() { raise(win) }
		a.ask = func(title, msg, yes string, then func(bool)) {
			// The sheet waits for the user; the window keeps drawing.
			go func() {
				res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: win, Type: mygo.MessageWarning, Message: title, Detail: msg, Buttons: []string{yes, "Cancel"}, DefaultButton: 0, CancelButton: 1})
				win.Update(func() { then(err == nil && res.Button == 0) })
			}()
		}
		// Set off the frame, the last title last.
		var titleMu sync.Mutex
		var title string
		a.setTitle = func(s string) {
			titleMu.Lock()
			title = s
			titleMu.Unlock()
			go func() {
				titleMu.Lock()
				defer titleMu.Unlock()
				win.SetTitle(title)
			}()
		}
		a.pickDir = func(start string, then func(string)) {
			// The dialog waits for the user; the window keeps drawing.
			go func() {
				dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "Open Folder", Message: "Start a larik session in a folder", DefaultPath: start, ButtonLabel: "Start Session", Directory: true, CreateDirectories: true})
				if err == nil && len(dirs) > 0 {
					win.Update(func() { then(dirs[0]) })
				}
			}()
		}
		if runtime.GOOS == "darwin" {
			mygo.App.SetMenu(a.menu(win.Update))
			a.setMenu = func() { mygo.App.SetMenu(a.menu(win.Update)) }
			// Its own icon in the Dock, which otherwise shows a program's
			// generic one when larik desktop runs from a terminal.
			if png, err := iconPNG(512); err == nil {
				_ = mygo.App.Dock.SetIcon(png)
			}
			a.dock = func(items []command) {
				mygo.App.Dock.SetMenu(mygo.NewMenu(menuItems(items, win.Update, win.Focus)))
			}
		}
		// Quitting closes the window, so its OnClose asks for both; a
		// quit asked for first knows it's one.
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
			group.mu.Lock()
			group.quitting = true
			group.mu.Unlock()
			for _, app := range group.apps() {
				app.quitting = true
			}
		})
		win.OnClose(func(e *mygo.CloseEvent) {
			if !a.windowClosing() {
				e.PreventDefault()
				win.Update(func() {})
			}
		})
		win.OnFocus(func() {
			group.setActive(a)
			if a.setMenu != nil {
				a.setMenu()
			}
		})
		win.OnClosed(func() {
			if settingsWin != nil {
				settingsWin.Close()
			}
			mygo.App.Quit()
		})
		a.leave = func(quit bool) {
			if quit {
				mygo.App.Quit()
			} else {
				win.Close()
			}
		}
		// A button of a session's notification answers its prompt, and
		// leaves the window where it is.
		mygo.App.OnNotificationAction(func(id, action string) {
			win.Update(func() { group.appForNotification(id).answerNotification(id, action) })
		})
		mygo.App.OnNotificationClick(func(id string) {
			win.Update(func() {
				target := group.appForNotification(id)
				for _, p := range target.projects {
					for _, s := range p.Sessions {
						if "larik-"+s.ID == id {
							target.reveal(s.Path)
						}
					}
				}
				if t := target.tabByID(strings.TrimPrefix(id, "larik-tab-")); t != nil {
					target.activate(t)
				}
				if w := group.windowFor(target); w != nil {
					w.Focus()
				} else {
					win.Focus()
				}
			})
		})
		go a.versionLoop()
		go func() {
			a.Start()
			win.Update(func() { group.restoreChildren() })
			// What was handed over while the window opened, once the
			// first index is in.
			mu.Lock()
			defer mu.Unlock()
			for _, cwd := range pending {
				show(win, a, cwd)
			}
			pending = nil
			opened = win
		}()
	})
	return mygo.App.Run()
}

// show brings the window forward on cwd's project, which a later larik
// desktop handed over; the check a window makes for another sends none.
func show(win *mygo.Window, a *App, cwd string) {
	if cwd == "" {
		return
	}
	win.Update(func() { a.openDir(cwd) })
	raise(win)
}

// raise brings the window forward, from the Dock when minimized.
func raise(win *mygo.Window) {
	if win.IsMinimized() {
		win.Restore()
	}
	win.Show()
	win.Focus()
	mygo.App.Focus()
}

// post posts a notification larik desktop notify asked for, focused or
// not, as it was asked for, and bounces the Dock icon while the window
// isn't focused.
func post(win *mygo.Window, title, body string) {
	if runtime.GOOS == "darwin" && !win.IsFocused() {
		mygo.App.Dock.Bounce(false)
	}
	if mygo.NotificationsSupported() {
		n := mygo.NewNotification(mygo.NotificationOptions{ID: "larik-notify-" + newTabID(), Title: title, Body: body})
		go func() { _ = n.Show() }()
	}
}

// notify tells the user, while the window isn't focused, that a session
// needs them or finished: a notification where the system allows one (on
// macOS only in an app bundle, as larik desktop --install makes), with
// buttons that answer a prompt the window can answer, and a bounce of
// the Dock icon.
func notify(win *mygo.Window, s session.Indexed, actions []mygo.NotificationAction) {
	if win.IsFocused() {
		return
	}
	title, body := alertText(s)
	notifyText(win, "larik-"+s.ID, title, body, s.Cwd, actions...)
}

// notifyText posts a notification with id, and actions, while the window
// isn't focused, and bounces the Dock icon.
func notifyText(win *mygo.Window, id, title, body, group string, actions ...mygo.NotificationAction) {
	if win.IsFocused() {
		return
	}
	if runtime.GOOS == "darwin" {
		mygo.App.Dock.Bounce(false)
	}
	if mygo.NotificationsSupported() {
		n := mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body, Group: group, Actions: actions})
		go func() { _ = n.Show() }()
	}
}
