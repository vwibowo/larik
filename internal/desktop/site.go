package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// What a browser tab's page may use, and what it downloads. A page
// asking for the camera, the microphone, your location or to notify you
// gets nothing at first, and a bar over it asks you: Allow lets that
// site have it from then on and reloads the page, which asks again and
// gets it, and Don't Allow keeps it from asking again. The answers are
// kept with the window's state. A download goes to the Downloads
// folder, as Safari's do, and a bar says when it's done, with Show in
// Finder.

// siteAsk is what a page asked to use that waits on you.
type siteAsk struct {
	origin string
	perms  []mygo.Permission
}

// pageDownload is a browser tab's download, going or done.
type pageDownload struct {
	name, path string
	done       bool
	err        string
}

// siteKey names what a site may use, as the window keeps it.
func siteKey(origin string, p mygo.Permission) string { return origin + " " + string(p) }

// permissionText says what a page asks for, as "your camera and microphone".
func permissionText(perms []mygo.Permission) string {
	var words []string
	for _, p := range perms {
		switch p {
		case mygo.PermissionCamera:
			words = append(words, "camera")
		case mygo.PermissionMicrophone:
			words = append(words, "microphone")
		case mygo.PermissionGeolocation:
			words = append(words, "location")
		case mygo.PermissionNotifications:
			words = append(words, "notifications")
		default:
			words = append(words, string(p))
		}
	}
	switch len(words) {
	case 0:
		return "something"
	case 1:
		return "your " + words[0]
	}
	return "your " + strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// devicesAllowed reports whether macOS lets the window use the camera,
// the microphone and your location: it ends an app that asks for them
// without saying why in its Info.plist, as a Larik app installed before
// browser tabs doesn't. Out of an app, the terminal that started the
// window asks, which says why.
var devicesAllowed = sync.OnceValue(func() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	contents, ok := strings.CutSuffix(filepath.Dir(exe), string(filepath.Separator)+"MacOS")
	if !ok || !strings.HasSuffix(contents, ".app"+string(filepath.Separator)+"Contents") {
		return true
	}
	plist, err := os.ReadFile(filepath.Join(contents, "Info.plist"))
	return err == nil && strings.Contains(string(plist), "NSCameraUsageDescription")
})

// needsDevice reports whether perms need what devicesAllowed guards.
func needsDevice(perms []mygo.Permission) bool {
	return slices.ContainsFunc(perms, func(p mygo.Permission) bool { return p != mygo.PermissionNotifications })
}

// permitted decides what t's page asked for: what you answered before,
// or nothing yet while the bar asks you.
func (a *App) permitted(t *tab, req mygo.PermissionRequest) bool {
	if needsDevice(req.Permissions) && !devicesAllowed() {
		a.notice = "A page asked for " + permissionText(req.Permissions) + ": run larik desktop --install again to let browser tabs ask macOS for it."
		return false
	}
	all := true
	for _, p := range req.Permissions {
		allowed, answered := a.sites[siteKey(req.Origin, p)]
		if answered && !allowed {
			return false // you said no
		}
		all = all && answered
	}
	if all {
		return true
	}
	t.asking = &siteAsk{origin: req.Origin, perms: req.Permissions}
	return false
}

// answerSite answers what t's page asked for, and on yes reloads it to
// ask again.
func (a *App) answerSite(t *tab, allow bool) {
	ask := t.asking
	if ask == nil {
		return
	}
	t.asking = nil
	if a.sites == nil {
		a.sites = map[string]bool{}
	}
	for _, p := range ask.perms {
		a.sites[siteKey(ask.origin, p)] = allow
	}
	if allow && t.web != nil {
		t.web.Page().Reload()
	}
}

// watchSite decides what t's page may use, and follows its downloads.
func (a *App) watchSite(t *tab) {
	p := t.web.Page()
	p.SetPermissionHandler(func(req mygo.PermissionRequest) bool {
		// On the main thread, which draws the bar the next frame.
		ok := a.permitted(t, req)
		a.update(func() {})
		return ok
	})
	t.off = append(t.off,
		p.OnWillDownload(func(e *mygo.DownloadEvent) {
			name, path := e.SuggestedName, e.Path
			a.update(func() { t.download = &pageDownload{name: name, path: path} })
		}),
		p.OnDownloadDone(func(d *mygo.Download) {
			path, msg := d.Path, ""
			if d.Err != nil {
				msg = d.Err.Error()
			}
			a.update(func() { a.downloaded(t, path, msg) })
		}),
	)
}

// downloaded notes that t's download to path ended, failing with msg.
func (a *App) downloaded(t *tab, path, msg string) {
	d := t.download
	if d == nil {
		d = &pageDownload{name: filepath.Base(path)}
		t.download = d
	}
	if path != "" {
		d.path, d.name = path, filepath.Base(path)
	}
	d.done, d.err = true, msg
	title := "Downloaded " + d.name
	if msg != "" {
		title = "Couldn't download " + d.name
	}
	a.record(event{tab: t.id, title: title, body: msg, read: t == a.focused()})
}

// siteBars are a browser tab's bars over its page: what it asks to use,
// and its download.
func (a *App) siteBars(c *ui.Context, t *ui.Theme, tb *tab) {
	if ask := tb.asking; ask != nil {
		ui.Row(c.Key("asking")).Gap(8).Padding(6, 10, 6, 16).AlignItems(ui.Center).Background(t.Accent.Alpha(0.08)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, pageHost(ask.origin)+" wants to use "+permissionText(ask.perms)+".").FontSize(12).Grow(1).Shrink(1)
			if ui.Button(c.Key("deny"), "Don't Allow").Clicked() {
				a.answerSite(tb, false)
			}
			if ui.Button(c.Key("allow"), "Allow").Clicked() {
				a.answerSite(tb, true)
			}
		})
	}
	if d := tb.download; d != nil {
		ui.Row(c.Key("download")).Gap(8).Padding(6, 10, 6, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			switch {
			case !d.done:
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, "Downloading "+d.name+"…").FontSize(12).Grow(1).Shrink(1)
			case d.err != "":
				ui.Icon(c, iconError).FontSize(13).TextColor(t.Danger)
				ui.Text(c, "Couldn't download "+d.name+": "+d.err).FontSize(12).Grow(1).Shrink(1)
			default:
				ui.Icon(c, iconCheck).FontSize(13).TextColor(t.Success)
				ui.Text(c, "Downloaded "+d.name).FontSize(12).Grow(1).Shrink(1)
				if a.showFile != nil && ui.Button(c.Key("show"), showInFolder()).Clicked() {
					a.showFile(d.path)
				}
			}
			a.closeButton(c, t, "Dismiss", func() { tb.download = nil })
		})
	}
}

// showInFolder is what showing a file in the file manager is called here.
func showInFolder() string {
	if runtime.GOOS == "darwin" {
		return "Show in Finder"
	}
	return "Show in Folder"
}
