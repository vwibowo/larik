package termbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/chromedp/cdproto/target"

	"larik/internal/osc"
)

// Headless Chrome has no permission prompts: it denies notifications and
// location at once. While the user watches, the viewer puts a script into
// the shown tab that asks the pane first, the way a desktop browser would,
// and on "Allow" grants the permission in Chrome before the page's own
// request goes through. Notifications the page then shows are passed on as
// desktop notifications. Sign-in requests (HTTP authentication) come from
// the session instead (auth.go), because only it sees them.

// askBinding is the function the script calls to reach the viewer.
const askBinding = "__larikAsk"

// permissionHook wraps the permission and notification APIs of the top
// frame. Requests from frames are left to Chrome (denied): the prompt names
// the tab's own site, which a frame of another site must not borrow.
const permissionHook = `(() => {
  if (window !== window.top || window.__larikHooked || typeof window.` + askBinding + ` !== 'function') return;
  window.__larikHooked = true;
  const ask = window.` + askBinding + `;
  const waiting = new Map(), asked = new Set();
  let n = 0;
  window.__larikAnswer = (id, ok) => { const r = waiting.get(id); if (r) { waiting.delete(id); r(ok); } };
  const request = (kind) => new Promise((r) => { const id = ++n; waiting.set(id, r); ask(JSON.stringify({type: 'permission', id, kind})); });
  const tell = (title, body) => ask(JSON.stringify({type: 'notification', title: String(title ?? ''), body: String(body ?? '')}));
  if (window.Notification) {
    const N = window.Notification, original = N.requestPermission.bind(N);
    N.requestPermission = function (cb) {
      let p;
      if (N.permission === 'granted' || asked.has('notifications')) p = original();
      else { asked.add('notifications'); p = request('notifications').then(() => original()); }
      if (typeof cb === 'function') p.then(cb);
      return p;
    };
    window.Notification = new Proxy(N, { construct(t, args) { tell(args[0], (args[1] || {}).body); return Reflect.construct(t, args); } });
  }
  if (window.ServiceWorkerRegistration) {
    const show = ServiceWorkerRegistration.prototype.showNotification;
    ServiceWorkerRegistration.prototype.showNotification = function (title, opts) { tell(title, (opts || {}).body); return show.apply(this, arguments); };
  }
  if (navigator.geolocation) {
    const g = navigator.geolocation;
    const gate = (fn) => function (...args) {
      navigator.permissions.query({name: 'geolocation'}).then((st) => {
        if (st.state === 'granted' || asked.has('geolocation')) return fn.apply(g, args);
        asked.add('geolocation');
        request('geolocation').then(() => fn.apply(g, args));
      });
      return 0;
    };
    g.getCurrentPosition = gate(g.getCurrentPosition);
    g.watchPosition = gate(g.watchPosition);
  }
})()`

// permissionPrompt is a page asking for something only the user grants.
type permissionPrompt struct {
	session target.SessionID
	context int    // execution context the answer goes back to
	id      int    // the script's own request number
	kind    string // notifications, geolocation
	origin  string
}

func (p permissionPrompt) question() string {
	what := map[string]string{"notifications": "show notifications", "geolocation": "know your location"}[p.kind]
	return hostOf(p.origin) + " wants to " + firstNonEmpty(what, p.kind)
}

func hostOf(origin string) string {
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		return u.Host
	}
	return origin
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// bindingMsg is a call from the page script.
type bindingMsg struct {
	session target.SessionID
	context int
	payload string
}

// toastMsg ends a notification shown in the pane.
type toastMsg struct{ gen int }

// installHooks puts the permission script into the shown tab, for the
// document there now and every one after; detaching removes it again.
func (v *viewer) installHooks() tea.Cmd {
	conn, session := v.conn, v.session
	if conn == nil || session == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Binding calls are Runtime events, sent once the domain is on.
		if err := conn.call(ctx, session, "Runtime.enable", nil, nil); err != nil {
			return nil
		}
		if err := conn.call(ctx, session, "Runtime.addBinding", map[string]any{"name": askBinding}, nil); err != nil {
			return nil
		}
		_ = conn.call(ctx, session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": permissionHook, "runImmediately": true}, nil)
		_ = conn.call(ctx, session, "Runtime.evaluate", map[string]any{"expression": permissionHook}, nil)
		_ = conn.call(ctx, session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": selectHook, "runImmediately": true}, nil)
		_ = conn.call(ctx, session, "Runtime.evaluate", map[string]any{"expression": selectHook}, nil)
		return hooksMsg{session}
	}
}

// hooksMsg says the permission script is in the tab of session.
type hooksMsg struct{ session target.SessionID }

// onBinding handles the page script's call.
func (v *viewer) onBinding(msg bindingMsg) tea.Cmd {
	if msg.session != v.session {
		return nil
	}
	var p struct {
		Type, Kind, Title, Body string
		ID                      int
	}
	if json.Unmarshal([]byte(msg.payload), &p) != nil {
		return nil
	}
	origin := originOf(v.targets[v.tab].URL)
	switch p.Type {
	case "permission":
		if origin == "" || (p.Kind != "notifications" && p.Kind != "geolocation") {
			return v.answerPage(permissionPrompt{session: msg.session, context: msg.context, id: p.ID}, false)
		}
		v.prompts = append(v.prompts, permissionPrompt{session: msg.session, context: msg.context, id: p.ID, kind: p.Kind, origin: origin})
	case "select":
		v.openChoice(msg)
	case "notification":
		text := strings.TrimSpace(p.Title + ": " + p.Body)
		text = strings.TrimSuffix(text, ":")
		v.toast = "🔔 " + hostOf(origin) + " · " + text
		v.toastGen++
		gen := v.toastGen
		cmds := []tea.Cmd{tea.Tick(8*time.Second, func(time.Time) tea.Msg { return toastMsg{gen} })}
		if osc.SupportsNotify() {
			seq := osc.Notify(hostOf(origin) + ": " + text)
			if v.kitty.tmux {
				seq = string(tmuxWrap([]byte(seq)))
			}
			cmds = append(cmds, tea.Raw(seq))
		}
		return tea.Batch(cmds...)
	}
	return nil
}

// decide answers the first permission prompt.
func (v *viewer) decide(allow bool) tea.Cmd {
	if len(v.prompts) == 0 {
		return nil
	}
	p := v.prompts[0]
	v.prompts = v.prompts[1:]
	if allow && p.kind == "geolocation" && v.state.Location == nil {
		v.note = "no location to share: set browser.location (latitude and longitude) in your settings"
		allow = false
	}
	conn := v.conn
	setting := map[bool]string{true: "granted", false: "denied"}[allow]
	loc := v.state.Location
	answer := v.answerPage(p, allow)
	return func() tea.Msg {
		if conn == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := conn.call(ctx, "", "Browser.setPermission", map[string]any{
			"permission": map[string]any{"name": p.kind}, "setting": setting, "origin": p.origin,
		}, nil); err != nil {
			return noteMsg("couldn't set the permission: " + err.Error())
		}
		if allow && p.kind == "geolocation" && loc != nil {
			_ = conn.call(ctx, p.session, "Emulation.setGeolocationOverride", map[string]any{
				"latitude": loc.Latitude, "longitude": loc.Longitude, "accuracy": max(loc.Accuracy, 1),
			}, nil)
		}
		if answer != nil {
			return answer()
		}
		return nil
	}
}

// answerPage tells the page script how its request went, so the page's
// own call goes on to Chrome.
func (v *viewer) answerPage(p permissionPrompt, ok bool) tea.Cmd {
	conn := v.conn
	if conn == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.call(ctx, p.session, "Runtime.evaluate", map[string]any{
			"expression": fmt.Sprintf("window.__larikAnswer && window.__larikAnswer(%d, %v)", p.id, ok),
			"contextId":  p.context,
		}, nil)
		return nil
	}
}

// signIn is the username and password being typed for an AuthPrompt.
type signIn struct {
	id             int
	user, password []rune
	onPassword     bool // which field has the keyboard
}

// syncAuth starts or drops the sign-in form as the session's state says.
func (v *viewer) syncAuth() {
	a := v.state.Auth
	switch {
	case a == nil:
		v.signin = nil
	case v.signin == nil || v.signin.id != a.ID:
		v.signin = &signIn{id: a.ID}
		v.editing = false
	}
}

// signInKey types into the sign-in form; enter on the password sends it.
func (v *viewer) signInKey(msg tea.KeyPressMsg) tea.Cmd {
	f, k := v.signin, msg.Key()
	field := &f.user
	if f.onPassword {
		field = &f.password
	}
	switch {
	case k.Code == tea.KeyEscape:
		return v.sendAuth(ViewerMessage{Op: "auth", ID: f.id, Cancel: true})
	case k.Code == tea.KeyTab, k.Code == tea.KeyUp, k.Code == tea.KeyDown:
		f.onPassword = !f.onPassword
	case k.Code == tea.KeyEnter:
		if !f.onPassword {
			f.onPassword = true
			return nil
		}
		return v.sendAuth(ViewerMessage{Op: "auth", ID: f.id, User: string(f.user), Password: string(f.password)})
	case k.Code == tea.KeyBackspace:
		if len(*field) > 0 {
			*field = (*field)[:len(*field)-1]
		}
	case k.Text != "":
		*field = append(*field, []rune(k.Text)...)
	}
	return nil
}

func (v *viewer) sendAuth(m ViewerMessage) tea.Cmd {
	ctl := v.control
	v.signin = nil
	if ctl == nil {
		return nil
	}
	return func() tea.Msg {
		if err := ctl.Send(m); err != nil {
			return noteMsg("couldn't send the sign-in: " + err.Error())
		}
		return nil
	}
}

// promptLine is the second row while something waits for the user, or "".
// It also returns where its buttons are, for clicks.
func (v *viewer) promptLine(width int) (line string, buttons []promptButton) {
	switch {
	case v.choosing != nil:
		line, _, _ = v.choiceLine()
		return line, nil
	case v.signin != nil && v.state.Auth != nil:
		a, f := v.state.Auth, v.signin
		who := hostOf(a.Origin)
		if a.Proxy {
			who = "proxy " + who
		}
		if a.Realm != "" {
			who += " (" + a.Realm + ")"
		}
		field := func(label string, text string, active bool) string {
			if active {
				return label + "\x1b[0;7m" + text + " \x1b[0m"
			}
			return label + "\x1b[0;4m" + text + " \x1b[0m"
		}
		line = " 🔑 sign in to " + who + "  " +
			field("user ", string(f.user), !f.onPassword) + "  " +
			field("password ", strings.Repeat("•", len(f.password)), f.onPassword) +
			"\x1b[2m  enter ↵ · tab switch · esc cancel\x1b[0m"
		return line, nil
	case len(v.prompts) > 0:
		q := " " + v.prompts[0].question() + "  "
		allow, block := "[ Allow ]", "[ Block ]"
		x := ansi.StringWidth(q)
		buttons = []promptButton{{x, x + len(allow), true}, {x + len(allow) + 1, x + len(allow) + 1 + len(block), false}}
		line = q + "\x1b[0;7m" + allow + "\x1b[0m " + "\x1b[0;7m" + block + "\x1b[0m" + "\x1b[2m  y / n\x1b[0m"
		return line, buttons
	}
	return "", nil
}

// promptButton is a clickable answer on the second row.
type promptButton struct {
	start, end int
	allow      bool
}

// clickPrompt answers a permission prompt clicked on the second row.
func (v *viewer) clickPrompt(x int) tea.Cmd {
	if v.choosing != nil {
		return v.clickChoice(x)
	}
	_, buttons := v.promptLine(v.cols)
	for _, b := range buttons {
		if x >= b.start && x < b.end {
			return v.decide(b.allow)
		}
	}
	return nil
}
