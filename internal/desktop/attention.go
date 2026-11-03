package desktop

import (
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

// What needs you while the window is in front: alerts tell you only
// from another app, and a session in a tab you aren't looking at can
// start waiting for you, or finish, while you work in another. Such a
// tab, and such a pane in the split, takes a ring in its status colour
// until you look at it; and each alert is kept in a list of notifications,
// under the toolbar's bell, with what larik desktop notify posted, where
// Shift+Command+U opens the newest you haven't seen.

// event is a notification: a session that started waiting for you,
// finished or failed, or what larik desktop notify posted.
type event struct {
	at    time.Time
	path  string // the session's transcript, or "" for a posted one
	tab   string // the terminal tab whose program posted it, if one did
	title string
	body  string
	state status.State // what the session did, or "" for a posted one
	read  bool
}

// eventsKept is how many notifications the list keeps, the oldest going.
const eventsKept = 100

// record keeps a notification, read already when it's about the session
// you're looking at.
func (a *App) record(e event) {
	if e.at.IsZero() {
		e.at = time.Now()
	}
	if f := a.focused(); f != nil && (e.path != "" && f.path == e.path || e.tab != "" && f.id == e.tab) {
		e.read = true
	}
	a.events = append(a.events, e)
	if len(a.events) > eventsKept {
		a.events = slices.Delete(a.events, 0, len(a.events)-eventsKept)
	}
}

// recordSession keeps the notification for a session that started
// waiting for you, finished or failed.
func (a *App) recordSession(s session.Indexed) {
	title, body := alertText(s)
	a.record(event{path: s.Path, title: title, body: body, state: s.Status.State})
}

// alertText is what a notification says about a session: its title, and
// what it did.
func alertText(s session.Indexed) (title, body string) {
	title = s.Title
	if title == "" {
		title = "Larik session"
	}
	switch s.Status.State {
	case status.Blocked:
		return title, "Larik " + s.Status.Msg
	case status.Error:
		return title, "Failed"
	}
	return title, "Finished"
}

// lookTab says you looked at t just now: at its session, and, for a
// terminal tab, at what its program posted.
func (a *App) lookTab(t *tab) {
	a.look(t.path)
	if !t.shell() {
		return
	}
	a.looked[tabKey(t)] = time.Now()
	for i := range a.events {
		if a.events[i].tab == t.id {
			a.events[i].read = true
		}
	}
}

// look says you looked at the session at path just now: its tab's ring
// goes, and its notifications are read.
func (a *App) look(path string) {
	if path == "" {
		return
	}
	a.looked[path] = time.Now()
	for i := range a.events {
		if a.events[i].path == path {
			a.events[i].read = true
		}
	}
}

// needsLook reports whether a tab you aren't looking at has something for
// you: its session waits for you, or finished or failed since you last
// looked at it (or since the window opened).
func (a *App) needsLook(tb *tab) bool {
	if tb == a.focused() {
		return false
	}
	s, _, ok := a.find(tb.path)
	if !ok {
		// A terminal tab's program rang or posted since you looked.
		looked := a.looked[tabKey(tb)]
		if looked.Before(a.started) {
			looked = a.started
		}
		return tb.shell() && tb.alerted.After(looked)
	}
	switch s.Status.State {
	case status.Blocked:
		return true
	case status.Done, status.Error:
		looked := a.looked[s.Path]
		if looked.Before(a.started) {
			looked = a.started
		}
		return s.Modified.After(looked)
	}
	return false
}

// ringColor is the colour of the ring on a tab that needs a look: its
// session's status colour, or the accent for what a terminal tab's
// program posted.
func (a *App) ringColor(t *ui.Theme, tb *tab) ui.Color {
	switch st := a.tabStatus(tb); st.State {
	case status.Blocked, status.Done, status.Error:
		return stateColor(t, st)
	}
	return t.Accent
}

// unreadEvents counts the notifications you haven't seen.
func (a *App) unreadEvents() int {
	n := 0
	for _, e := range a.events {
		if !e.read {
			n++
		}
	}
	return n
}

// latestUnread shows what the newest notification you haven't seen is
// about: its session, or what it said, in the notice bar.
func (a *App) latestUnread() {
	for i := len(a.events) - 1; i >= 0; i-- {
		if !a.events[i].read {
			a.openEvent(i)
			return
		}
	}
	a.notice = "No unread notifications."
}

// openEvent marks the notification read and shows its session, or for
// one posted, what it said.
func (a *App) openEvent(i int) {
	e := &a.events[i]
	e.read = true
	if _, _, ok := a.find(e.path); ok {
		a.showEvents = false
		a.reveal(e.path)
		return
	}
	if t := a.tabByID(e.tab); t != nil {
		a.showEvents = false
		a.activate(t)
		return
	}
	a.notice = e.title
	if e.body != "" {
		a.notice += ": " + e.body
	}
}

// markAllRead marks every notification read.
func (a *App) markAllRead() {
	for i := range a.events {
		a.events[i].read = true
	}
}

// bell is the toolbar's notifications button, with how many you haven't
// seen, opening their list.
func (a *App) bell(c *ui.Context, t *ui.Theme) {
	n := a.unreadEvents()
	tip := "Notifications (" + keyLabel(true, "U") + " opens the newest)"
	if n > 0 {
		tip = fmt.Sprintf("%d unread notifications (%s opens the newest)", n, keyLabel(true, "U"))
	}
	b := ui.Row(c.Key("bell")).Padding(6).Radius(6).Gap(3).AlignItems(ui.Center).Label(tip).Tooltip(tip).Children(func() {
		ui.Icon(c, iconBell).FontSize(15).TextColor(t.Text.Alpha(0.75))
		if n > 0 {
			ui.Text(c, fmt.Sprint(n)).FontSize(10).Bold().TextColor(ui.Hex("#ffffff")).Padding(0, 5).Radius(999).Background(t.Accent)
		}
	})
	switch {
	case a.showEvents:
		b.Background(t.Text.Alpha(0.1))
	case b.Hovered():
		b.Background(t.Text.Alpha(0.06))
	}
	if b.Clicked() {
		a.showEvents = !a.showEvents
	}
	ui.Popover(c.Key("events"), b, &a.showEvents, func() { a.eventList(c, t) })
}

// eventsShown is how many notifications the list shows, newest first.
const eventsShown = 50

// eventList is the notifications, newest first, each opening its
// session; those you haven't seen marked.
func (a *App) eventList(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(340).Gap(4).Children(func() {
		ui.Row(c).Padding(2, 4, 6, 4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Notifications").Bold().FontSize(13)
			ui.Spacer(c)
			if a.unreadEvents() > 0 && ui.Text(c.Key("all-read"), "Mark All Read").FontSize(12).TextColor(t.Accent).Cursor(ui.CursorPointer).Clicked() {
				a.markAllRead()
			}
		})
		if len(a.events) == 0 {
			ui.Text(c, "Sessions that need you, finish or fail show here, as does what larik desktop notify posts.").FontSize(12).TextColor(t.TextMuted).Padding(4, 4, 8, 4)
			return
		}
		ui.Scroll(c).MaxHeight(420).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				shown := 0
				answers := map[string]bool{} // under the newest of each session's only
				for i := len(a.events) - 1; i >= 0 && shown < eventsShown; i, shown = i-1, shown+1 {
					a.eventRow(c, t, i)
					if e := a.events[i]; e.state == status.Blocked && !answers[e.path] {
						answers[e.path] = true
						if s, _, ok := a.find(e.path); ok {
							a.answerButtons(c, t, s, 27)
						}
					}
				}
			})
		})
	})
}

// eventRow is one notification in the list.
func (a *App) eventRow(c *ui.Context, t *ui.Theme, i int) {
	e := a.events[i]
	row := ui.Row(c.Key(fmt.Sprint("event-", i, e.at.UnixNano()))).Gap(8).Padding(6, 6).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(e.title + ": " + e.body).Children(func() {
		icon, color := iconBell, t.Accent
		switch e.state {
		case status.Blocked:
			icon, color = iconPause, t.Warning
		case status.Error:
			icon, color = iconError, t.Danger
		case status.Done:
			icon, color = iconCheck, t.Success
		}
		ui.Icon(c, icon).FontSize(13).TextColor(color)
		ui.Column(c).Gap(1).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			title := ui.Text(c, e.title).FontSize(12).SingleLine()
			if !e.read {
				title.Bold()
			}
			ui.Text(c, e.body).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		})
		ui.Text(c, age(time.Since(e.at))).FontSize(11).TextColor(t.TextMuted).SingleLine().Tooltip(e.at.Format("Mon 2 Jan 2006, 15:04"))
		dot := ui.Box(c).Size(7, 7).Radius(4)
		if !e.read {
			dot.Background(t.Accent)
		}
	})
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	if row.Clicked() {
		a.openEvent(i)
	}
}
