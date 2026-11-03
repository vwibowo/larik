package desktop

import (
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
	if err := Prepare(); err != nil {
		return err
	}
	a := New(opts)
	defer a.Close()
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
		return a.control(req)
	})
	if errors.Is(err, errRunning) {
		Handover(opts.DataDir, opts.Cwd)
		return nil
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
			Vibrancy:      mygo.VibrancySidebar,
			Content:       ui.View(a.view),
		})
		fmt.Fprintf(os.Stderr, "larik desktop: notifications %s\n", map[bool]string{true: "on", false: "off (macOS shows them only for an app bundle: larik desktop --install)"}[mygo.NotificationsSupported()])
		a.update = win.Update
		a.redraw = func() { win.Update(func() {}) }
		a.badge = mygo.App.SetBadgeCount
		a.copyText = func(s string) { _ = mygo.Clipboard.Write(transfer.TextData(s)) }
		a.showFile = mygo.Shell.ShowItemInFolder
		a.openPath = func(path string) { _ = mygo.Shell.OpenPath(path) }
		a.openExternal = func(url string) { _ = mygo.Shell.OpenExternal(url) }
		a.about = mygo.App.ShowAboutPanel
		a.alert = func(s session.Indexed) { notify(win, s) }
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
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quitting = true })
		win.OnClose(func(e *mygo.CloseEvent) {
			if !a.windowClosing() {
				e.PreventDefault()
				win.Update(func() {})
			}
		})
		a.leave = func(quit bool) {
			if quit {
				mygo.App.Quit()
			} else {
				win.Close()
			}
		}
		mygo.App.OnNotificationClick(func(id string) {
			win.Update(func() {
				for _, p := range a.projects {
					for _, s := range p.Sessions {
						if "larik-"+s.ID == id {
							a.reveal(s.Path)
						}
					}
				}
				if t := a.tabByID(strings.TrimPrefix(id, "larik-tab-")); t != nil {
					a.activate(t)
				}
			})
			win.Focus()
		})
		go func() {
			a.Start()
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
// macOS only in an app bundle, as larik desktop --install makes), and a
// bounce of the Dock icon.
func notify(win *mygo.Window, s session.Indexed) {
	if win.IsFocused() {
		return
	}
	title, body := alertText(s)
	notifyText(win, "larik-"+s.ID, title, body, s.Cwd)
}

// notifyText posts a notification with id, while the window isn't
// focused, and bounces the Dock icon.
func notifyText(win *mygo.Window, id, title, body, group string) {
	if win.IsFocused() {
		return
	}
	if runtime.GOOS == "darwin" {
		mygo.App.Dock.Bounce(false)
	}
	if mygo.NotificationsSupported() {
		n := mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body, Group: group})
		go func() { _ = n.Show() }()
	}
}
