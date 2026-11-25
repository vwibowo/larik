package desktop

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	browserpane "larik/internal/desktop/panes/browser"
)

// What a browser tab's page may use, and what it downloads. A page
// asking for the camera, the microphone, your location or to notify you
// gets nothing at first, and a bar over it asks you: Allow lets that
// site have it from then on and reloads the page, which asks again and
// gets it, and Don't Allow keeps it from asking again. The answers are
// kept with the window's state. A download goes to the Downloads
// folder, as Safari's do, and a bar says when it's done, with Show in
// Finder.

type siteAsk = browserpane.SiteAsk
type pageDownload = browserpane.PageDownload

// siteKey names what a site may use, as the window keeps it.
func siteKey(origin string, p mygo.Permission) string { return browserpane.SiteKey(origin, p) }

// permissionText says what a page asks for, as "your camera and microphone".
func permissionText(perms []mygo.Permission) string { return browserpane.PermissionText(perms) }

// devicesAllowed reports whether macOS lets the window use the camera,
// the microphone and your location: it ends an app that asks for them
// without saying why in its Info.plist, as a Larik app installed before
// browser tabs doesn't. Out of an app, the terminal that started the
// window asks, which says why.
func devicesAllowed() bool { return browserpane.DevicesAllowed() }

// needsDevice reports whether perms need what devicesAllowed guards.
func needsDevice(perms []mygo.Permission) bool { return browserpane.NeedsDevice(perms) }

// permitted decides what t's page asked for: what you answered before,
// or nothing yet while the bar asks you.
func (a *App) permitted(t *tab, req mygo.PermissionRequest) bool {
	if needsDevice(req.Permissions) && !devicesAllowed() {
		a.notice = "A page asked for " + permissionText(req.Permissions) + ": run larik desktop --install again to let browser panes ask macOS for it."
		return false
	}
	return t.browserState().Permitted(req, a.sites)
}

// answerSite answers what t's page asked for, and on yes reloads it to
// ask again.
func (a *App) answerSite(t *tab, allow bool) {
	if t.browserState().Asking == nil {
		return
	}
	if a.sites == nil {
		a.sites = map[string]bool{}
	}
	if t.browserState().AnswerPermission(a.sites, allow) {
		t.browserState().Web.Page().Reload()
	}
}

// watchSite decides what t's page may use, and follows its downloads.
func (a *App) watchSite(t *tab) {
	p := t.browserState().Web.Page()
	p.SetPermissionHandler(func(req mygo.PermissionRequest) bool {
		// On the main thread, which draws the bar the next frame.
		ok := a.permitted(t, req)
		a.update(func() {})
		return ok
	})
	t.browserState().Observers = append(t.browserState().Observers,
		p.OnWillDownload(func(e *mygo.DownloadEvent) {
			name, path := e.SuggestedName, e.Path
			a.update(func() { t.browserState().Download = &pageDownload{Name: name, Path: path} })
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
	title := t.browserState().FinishDownload(path, msg)
	a.record(event{tab: t.id, title: title, body: msg, read: t == a.focused()})
}

// siteBars are a browser tab's bars over its page: what it asks to use,
// and its download.
func (a *App) siteBars(c *ui.Context, theme *ui.Theme, tb *tab) {
	browserpane.SiteBars(c, theme, browserpane.SiteBarsOptions{
		State: tb.browserState(), ErrorIcon: iconError, CheckIcon: iconCheck,
		Answer: func(allow bool) { a.answerSite(tb, allow) }, ShowFile: a.showFile,
		CloseButton: func(label string, fn func()) { a.closeButton(c, theme, label, fn) },
	})
}

// showInFolder is what showing a file in the file manager is called here.
func showInFolder() string { return browserpane.ShowInFolder() }
