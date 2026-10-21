package termbrowser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/chromedp/cdproto/target"
	"golang.org/x/sys/unix"
)

// The viewer is `larik browser view`, run in a pane beside Larik's. It
// follows the tab the agent works in, draws it with the kitty graphics
// protocol and sends the user's clicks, scrolling and typing to it, so the
// user can watch the agent and take over (to sign in, say) in the same
// window. Two rows at the top work like a browser's toolbar (toolbar.go).

const (
	headerRows = 2
	// maxPatches is how many patch images may sit over the last full
	// frame before a full frame replaces them all.
	maxPatches = 48
	// Full frames alternate between two image ids, so a new one is on
	// screen before the old one is deleted and nothing blinks; patches
	// take the ids after them.
	firstPatchID = 3
	resizeSettle = 150 * time.Millisecond
	// frameQuality is the JPEG quality Chrome encodes frames at.
	frameQuality = 90
)

// RunViewer runs the viewer for the session listening at socket until the
// user quits it or the session ends.
func RunViewer(socket string) error {
	ok, tmux := kittyTerminal(os.Getenv)
	if !ok && os.Getenv("LARIK_FORCE_KITTY") == "" {
		return errors.New("this terminal does not show kitty graphics (kitty, Ghostty, WezTerm, cmux or Konsole); set LARIK_FORCE_KITTY=1 to try anyway")
	}
	ctl, err := DialControl(socket)
	if err != nil {
		return err
	}
	defer ctl.Close()
	v := newViewer(kitty{tmux: tmux})
	v.socket, v.out, v.control = socket, os.Stdout, ctl
	p := tea.NewProgram(v)
	v.send = p.Send
	go func() {
		err := ctl.Run(func(st State) { p.Send(stateMsg(st)) })
		p.Send(sessionGoneMsg{err})
	}()
	_, err = p.Run()
	v.close()
	return err
}

type viewer struct {
	socket  string
	control *Control    // the session's socket, for answers only the user gives
	inputs  *inputQueue // the user's input to the page, in order
	send    func(tea.Msg)
	kitty   kitty
	out     io.Writer // the terminal, for the last clean-up after Bubble Tea exits

	state   State
	conn    *cdpConn
	ws      string // what conn is connected to
	session target.SessionID
	tab     string // the tab session is attached to
	targets map[string]targetInfo

	cols, rows   int
	cellW, cellH int
	scale        int // device pixels per CSS pixel
	resizeGen    int

	diff    frameDiff
	base    int   // id of the full frame on screen, 0 for none
	patches []int // ids of the patches over it
	z       int   // stacking order of the last image placed

	// frames carries encoded frames to the decoder, newest only: one that
	// arrives while the last is still decoding replaces any waiting.
	frames chan encodedFrame
	probed bool          // the temporary-file transfer probe was sent
	done   chan struct{} // closed by close

	measured bool        // the terminal reported its cell size in pixels
	frame    image.Point // size of the last frame drawn, for LARIK_BROWSER_DEBUG
	drawn    []time.Time // when recent frames were drawn, for the frame rate

	editing  bool // the address field has the keyboard
	address  addressBar
	hist     history            // the shown tab's back/forward list
	prompts  []permissionPrompt // pages asking for a permission, oldest first
	signin   *signIn            // the username and password a site asks for
	hooked   target.SessionID   // the session the permission script is in
	toast    string             // the last notification a page showed
	toastGen int
	loading  bool   // the shown tab's page is loading
	note     string // the last error or notice, shown in the header
	quit     bool
}

type targetInfo struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type (
	stateMsg       State
	sessionGoneMsg struct{ err error }
	connectedMsg   struct {
		conn *cdpConn
		ws   string
		err  error
	}
	attachedMsg struct {
		tab     string
		session target.SessionID
		err     error
	}
	frameMsg struct {
		session target.SessionID
		frame   rgbFrame
	}
	targetMsg struct {
		id   string
		info targetInfo
	}
	detachedMsg struct{ session target.SessionID }
	// pageMsg is a navigation or load event on a tab's main frame.
	pageMsg struct {
		session target.SessionID
		frame   string
		loading *bool // set by load events
		moved   bool  // the frame navigated: history changed
	}
	encodedFrame struct {
		session target.SessionID
		data    string // base64 JPEG
	}
	noteMsg     string
	resizedMsg  struct{ gen int }
	connLostMsg struct{}
)

func newViewer(k kitty) *viewer {
	v := &viewer{kitty: k, targets: map[string]targetInfo{}, frames: make(chan encodedFrame, 1), done: make(chan struct{})}
	v.inputs = newInputQueue(func(msg string) {
		if v.send != nil {
			v.send(noteMsg(msg))
		}
	})
	go v.decode()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if v.kitty.files {
					sweepFrameFiles()
				}
			case <-v.done:
				return
			}
		}
	}()
	return v
}

func (v *viewer) Init() tea.Cmd {
	if os.Getenv("LARIK_BROWSER_TRANSFER") == "direct" {
		return nil
	}
	// Ask whether frames can travel as temporary files; the answer comes
	// back as a KittyGraphicsEvent, and until then they go inline.
	var out bytes.Buffer
	k := v.kitty
	k.w = &out
	if k.probeFiles() != nil {
		return nil
	}
	v.probed = true
	return tea.Raw(out.String())
}

func (v *viewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case stateMsg:
		v.state = State(msg)
		v.syncAuth()
		return v, v.follow()
	case bindingMsg:
		return v, v.onBinding(msg)
	case hooksMsg:
		v.hooked = msg.session
		return v, nil
	case toastMsg:
		if msg.gen == v.toastGen {
			v.toast = ""
		}
		return v, nil
	case sessionGoneMsg:
		v.quit = true
		return v, tea.Quit
	case connectedMsg:
		if msg.err != nil {
			v.note = "can't reach the browser: " + msg.err.Error()
			return v, nil
		}
		if v.conn != nil {
			v.conn.Close()
		}
		v.conn, v.ws, v.session, v.tab = msg.conn, msg.ws, "", ""
		v.inputs.use(msg.conn)
		go v.pump(msg.conn)
		return v, tea.Batch(v.discover(), v.follow())
	case attachedMsg:
		if msg.err != nil {
			v.note = "can't show the tab: " + msg.err.Error()
			return v, nil
		}
		v.session, v.tab = msg.session, msg.tab
		v.diff = frameDiff{} // the next frame is a full one
		v.editing, v.loading, v.hist, v.prompts = false, false, history{}, nil
		return v, tea.Batch(v.clearImages(), v.startScreencast(), v.fetchHistory(), v.installHooks())
	case pageMsg:
		// A tab's main frame has the tab's own id.
		if msg.session != v.session || msg.frame != v.tab {
			return v, nil
		}
		if msg.loading != nil {
			v.loading = *msg.loading
		}
		if msg.moved {
			return v, v.fetchHistory()
		}
		return v, nil
	case historyMsg:
		if msg.session == v.session {
			v.hist = msg.hist
		}
		return v, nil
	case frameMsg:
		if msg.session != v.session {
			return v, nil
		}
		return v, v.draw(msg.frame)
	case uv.KittyGraphicsEvent:
		if msg.Options.ID == probeID && v.probed {
			v.kitty.files = string(msg.Payload) == "OK"
		}
		return v, nil
	case targetMsg:
		v.targets[msg.id] = msg.info
		return v, nil
	case detachedMsg:
		if msg.session == v.session {
			v.session, v.tab = "", ""
			return v, tea.Batch(v.clearImages(), v.follow())
		}
		return v, nil
	case connLostMsg:
		v.conn, v.ws, v.session, v.tab = nil, "", "", ""
		return v, tea.Batch(v.clearImages(), v.follow())
	case noteMsg:
		v.note = string(msg)
		return v, nil
	case tea.WindowSizeMsg:
		v.cols, v.rows = msg.Width, msg.Height
		v.measureCells()
		v.resizeGen++
		gen := v.resizeGen
		return v, tea.Batch(v.clearImages(), tea.Tick(resizeSettle, func(time.Time) tea.Msg { return resizedMsg{gen} }))
	case resizedMsg:
		if msg.gen != v.resizeGen || v.session == "" {
			return v, nil
		}
		v.diff = frameDiff{}
		return v, v.startScreencast()
	case tea.KeyPressMsg:
		return v, v.key(msg)
	case tea.PasteMsg:
		if f := v.signin; f != nil {
			if f.onPassword {
				f.password = append(f.password, []rune(msg.Content)...)
			} else {
				f.user = append(f.user, []rune(msg.Content)...)
			}
			return v, nil
		}
		if v.editing {
			v.address.insert(msg.Content)
			return v, nil
		}
		v.typeIn("Input.insertText", map[string]any{"text": msg.Content})
		return v, nil
	case tea.MouseClickMsg:
		if msg.Y == 0 && msg.Button == tea.MouseLeft {
			return v, v.clickToolbar(msg.X)
		}
		if msg.Y == 1 && msg.Button == tea.MouseLeft {
			return v, v.clickPrompt(msg.X)
		}
		if msg.Y >= headerRows {
			v.editing = false // clicking the page leaves the address field
		}
		return v, v.mouse("mousePressed", tea.Mouse(msg), 1)
	case tea.MouseReleaseMsg:
		return v, v.mouse("mouseReleased", tea.Mouse(msg), 1)
	case tea.MouseMotionMsg:
		return v, v.mouse("mouseMoved", tea.Mouse(msg), 0)
	case tea.MouseWheelMsg:
		return v, v.wheel(tea.Mouse(msg))
	}
	return v, nil
}

func (v *viewer) View() tea.View {
	info := v.targets[v.tab]
	title := firstNonEmpty(info.Title, info.URL, "waiting for a page…")
	second, hints := " "+title, "alt+←/→ back/forward · ctrl+l address · ctrl+r reload · ctrl+q close browser"
	switch {
	case v.editing:
		second, hints = " type an address", "enter go · esc cancel"
	case os.Getenv("LARIK_BROWSER_DEBUG") != "":
		w, h := v.pagePixels()
		guess := ""
		if !v.measured {
			guess = " (guessed: the terminal didn't report pixels)"
		}
		transfer := "inline"
		if v.kitty.files {
			transfer = "file"
		}
		second, hints = fmt.Sprintf(" cell %dx%d px%s · %dx scale · pane %dx%d px · frame %dx%d · %s · %d fps", v.cellW, v.cellH, guess, v.scale, w, h, v.frame.X, v.frame.Y, transfer, v.fps()), ""
	case v.note != "":
		second, hints = " "+v.note, ""
	case v.tab == "":
		hints = "ctrl+q close browser"
	}
	width := max(v.cols, 10)
	if line, _ := v.promptLine(width); line != "" {
		second, hints = line, ""
	} else if v.toast != "" && !v.editing {
		second, hints = " "+v.toast, ""
	}
	// The hints go at the right of the second row, as room allows.
	if room := width - ansi.StringWidth(second) - 2; hints != "" && room >= 12 {
		hints = ansi.Truncate(hints, room, "…")
		second = padRight(second, width-ansi.StringWidth(hints)-1) + hints
	}
	lines := []string{
		ansi.Truncate(v.toolbarLine(), width, "") + sgrReset,
		"\x1b[2m" + ansi.Truncate(second, width, "…") + "\x1b[0m",
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeAllMotion
	view.WindowTitle = "larik browser · " + title
	return view
}

func padRight(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// follow connects to the browser and attaches to the tab the session
// says to show, whichever of those is not yet done.
func (v *viewer) follow() tea.Cmd {
	st := v.state
	switch {
	case st.WS == "":
		return nil
	case v.conn == nil || v.ws != st.WS:
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := dialCDP(ctx, st.WS)
			return connectedMsg{conn, st.WS, err}
		}
	case st.Focus != "" && st.Focus != v.tab:
		conn, old, focus := v.conn, v.session, st.Focus
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if old != "" {
				conn.detach(ctx, old)
			}
			session, err := conn.attach(ctx, focus)
			if err == nil {
				err = conn.call(ctx, session, "Page.enable", nil, nil)
			}
			return attachedMsg{focus, session, err}
		}
	}
	return nil
}

// discover asks for every tab's title and address as they change.
func (v *viewer) discover() tea.Cmd {
	conn := v.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil)
		return nil
	}
}

// pump reads browser events: frames are acked at once (Chrome sends the
// next only then) and decoded here, off the UI goroutine.
func (v *viewer) pump(conn *cdpConn) {
	for msg := range conn.Events() {
		switch msg.Method {
		case "Page.screencastFrame":
			var ev struct {
				Data      string `json:"data"`
				SessionID int    `json:"sessionId"`
			}
			if json.Unmarshal(msg.Params, &ev) != nil {
				continue
			}
			session := msg.SessionID
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = conn.call(ctx, session, "Page.screencastFrameAck", map[string]any{"sessionId": ev.SessionID}, nil)
			}()
			v.queueFrame(encodedFrame{session, ev.Data})
		case "Target.targetInfoChanged", "Target.targetCreated":
			var ev struct {
				TargetInfo struct {
					TargetID string `json:"targetId"`
					URL      string `json:"url"`
					Title    string `json:"title"`
				} `json:"targetInfo"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil {
				v.send(targetMsg{ev.TargetInfo.TargetID, targetInfo{ev.TargetInfo.URL, ev.TargetInfo.Title}})
			}
		case "Page.frameStartedLoading", "Page.frameStoppedLoading":
			var ev struct {
				FrameID string `json:"frameId"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil {
				loading := msg.Method == "Page.frameStartedLoading"
				v.send(pageMsg{session: msg.SessionID, frame: ev.FrameID, loading: &loading})
			}
		case "Page.frameNavigated":
			var ev struct {
				Frame struct {
					ID string `json:"id"`
				} `json:"frame"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil {
				v.send(pageMsg{session: msg.SessionID, frame: ev.Frame.ID, moved: true})
			}
		case "Page.navigatedWithinDocument":
			var ev struct {
				FrameID string `json:"frameId"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil {
				v.send(pageMsg{session: msg.SessionID, frame: ev.FrameID, moved: true})
			}
		case "Runtime.bindingCalled":
			var ev struct {
				Name      string `json:"name"`
				Payload   string `json:"payload"`
				ContextID int    `json:"executionContextId"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil && ev.Name == askBinding {
				v.send(bindingMsg{session: msg.SessionID, context: ev.ContextID, payload: ev.Payload})
			}
		case "Target.detachedFromTarget":
			var ev struct {
				SessionID target.SessionID `json:"sessionId"`
			}
			if json.Unmarshal(msg.Params, &ev) == nil {
				v.send(detachedMsg{ev.SessionID})
			}
		}
	}
	v.send(connLostMsg{})
}

// cellPixels is the terminal's cell size in pixels, from the window size
// the kernel keeps for it; tests replace it.
var cellPixels = func() (w, h int, ok bool) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0, false
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row), true
}

// queueFrame hands a frame to the decoder, dropping one still waiting:
// only the page as it is now matters.
func (v *viewer) queueFrame(f encodedFrame) {
	for {
		select {
		case v.frames <- f:
			return
		default:
		}
		select {
		case <-v.frames:
		default:
		}
	}
}

// decode turns frames into pixels off the UI goroutine, so a frame being
// decoded never holds up a click.
func (v *viewer) decode() {
	for f := range v.frames {
		raw, err := base64.StdEncoding.DecodeString(f.data)
		if err != nil {
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			continue
		}
		v.send(frameMsg{f.session, toRGB(img)})
	}
}

// measureCells reads the terminal's cell size in pixels, which the page
// is sized and clicks are placed by.
func (v *viewer) measureCells() {
	v.cellW, v.cellH = 10, 20 // a guess, for terminals that don't say
	w, h, ok := cellPixels()
	if ok {
		v.cellW, v.cellH = w, h
	}
	v.measured = ok
	// Retina-class screens have tall cells; render at 2x there so text
	// stays a readable size instead of a desktop page shrunk to fit.
	v.scale = 1
	if v.cellH >= 28 {
		v.scale = 2
	}
}

// pagePixels is the size of the page area in device pixels, rounded down
// to whole CSS pixels so frames match it exactly.
func (v *viewer) pagePixels() (w, h int) {
	scale := max(v.scale, 1)
	w = max(v.cols, 1) * v.cellW / scale * scale
	h = max(v.rows-headerRows, 1) * v.cellH / scale * scale
	return w, h
}

// startScreencast sizes the page to the pane and asks for frames of it.
func (v *viewer) startScreencast() tea.Cmd {
	conn, session := v.conn, v.session
	if conn == nil || session == "" || v.cols == 0 {
		return nil
	}
	w, h := v.pagePixels()
	scale := v.scale
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.call(ctx, session, "Page.stopScreencast", nil, nil)
		err := conn.call(ctx, session, "Emulation.setDeviceMetricsOverride", map[string]any{
			"width": w / scale, "height": h / scale, "deviceScaleFactor": scale, "mobile": false,
			// Headless Chrome reports a 400x300 screen otherwise, which
			// some pages size their layout by.
			"screenWidth": w / scale, "screenHeight": h / scale,
		}, nil)
		if err == nil {
			err = conn.call(ctx, session, "Page.startScreencast", map[string]any{
				"format": "jpeg", "quality": frameQuality, "maxWidth": w, "maxHeight": h, "everyNthFrame": 1,
			}, nil)
		}
		if err != nil {
			return noteMsg("can't stream the tab: " + err.Error())
		}
		return noteMsg("")
	}
}

// draw puts a frame on screen: the parts that changed as patches over the
// last full frame, or the whole frame.
func (v *viewer) draw(img rgbFrame) tea.Cmd {
	if v.cols == 0 || v.rows <= headerRows {
		return nil
	}
	var out bytes.Buffer
	k := v.kitty
	k.w = &out
	w, h := v.pagePixels()
	v.frame = image.Pt(img.w, img.h)
	fits := img.w <= w && img.h <= h
	rects, full := v.diff.update(img)
	if v.base == 0 || !fits || len(v.patches)+len(rects) > maxPatches {
		full = true
	}
	switch {
	case full:
		// The new frame goes on top first, then what it covers is deleted,
		// so there is never a moment with nothing on screen.
		id := 1
		if v.base == 1 {
			id = 2
		}
		v.z++
		p := placement{id: id, z: v.z, row: headerRows}
		if !fits {
			p.cols, p.rows = v.cols, v.rows-headerRows // too big: let the terminal shrink it
		}
		k.Show(img, p)
		if v.base != 0 {
			k.Delete(v.base)
		}
		for _, pid := range v.patches {
			k.Delete(pid)
		}
		v.base, v.patches = id, v.patches[:0]
	case len(rects) > 0:
		for _, r := range rects {
			id := firstPatchID + len(v.patches)
			v.patches = append(v.patches, id)
			v.z++
			k.Show(img.sub(r), placement{
				id: id, z: v.z,
				col: r.Min.X / v.cellW, row: headerRows + r.Min.Y/v.cellH,
				x: r.Min.X % v.cellW, y: r.Min.Y % v.cellH,
			})
		}
	default:
		return nil
	}
	now := time.Now()
	v.drawn = append(v.drawn, now)
	for len(v.drawn) > 0 && now.Sub(v.drawn[0]) > time.Second {
		v.drawn = v.drawn[1:]
	}
	return tea.Raw(out.String())
}

// fps is how many frames were drawn in the last second.
func (v *viewer) fps() int {
	n := 0
	for _, t := range v.drawn {
		if time.Since(t) <= time.Second {
			n++
		}
	}
	return n
}

// clearImages removes everything the viewer drew; the next frame is full.
func (v *viewer) clearImages() tea.Cmd {
	var out bytes.Buffer
	k := v.kitty
	k.w = &out
	k.Delete(0)
	v.base, v.patches, v.z, v.diff = 0, nil, 0, frameDiff{}
	return tea.Raw(out.String())
}

// key handles the viewer's own shortcuts and sends every other key to the
// page.
func (v *viewer) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.Key()
	if msg.String() == "ctrl+q" {
		v.quit = true
		return tea.Quit
	}
	if v.signin != nil {
		return v.signInKey(msg)
	}
	if len(v.prompts) > 0 {
		switch msg.String() {
		case "y", "enter":
			return v.decide(true)
		case "n", "esc":
			return v.decide(false)
		}
		return nil // an open question keeps the keyboard
	}
	if v.editing {
		switch {
		case k.Code == tea.KeyEnter:
			v.editing = false
			return v.navigate(normalizeAddress(string(v.address.text)))
		case k.Code == tea.KeyEscape:
			v.editing = false
		case msg.String() == "ctrl+q":
			v.quit = true
			return tea.Quit
		default:
			v.address.key(k)
		}
		return nil
	}
	switch msg.String() {
	case "ctrl+q":
		v.quit = true
		return tea.Quit
	case "ctrl+l":
		v.editing = true
		v.address.begin(v.targets[v.tab].URL)
		return nil
	case "ctrl+r", "f5":
		return v.reloadOrStop()
	case "alt+left":
		return v.goHistory(-1)
	case "alt+right":
		return v.goHistory(+1)
	}
	for _, ev := range keyEvents(k) {
		v.typeIn("Input.dispatchKeyEvent", ev)
	}
	return nil
}

// normalizeAddress turns what was typed into a URL: a bare host or
// localhost:port gets a scheme.
func normalizeAddress(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "", strings.Contains(s, "://"), strings.HasPrefix(s, "about:"):
		return s
	case strings.HasPrefix(s, "localhost"), strings.HasPrefix(s, "127."):
		return "http://" + s
	}
	return "https://" + s
}

// navigate opens url in the shown tab, through the session's own request
// guard like every other request the tab makes.
func (v *viewer) navigate(url string) tea.Cmd {
	if url == "" {
		return nil
	}
	return v.input("Page.navigate", map[string]any{"url": url})
}

// input sends one command to the shown tab in the background.
func (v *viewer) input(method string, params any) tea.Cmd {
	conn, session := v.conn, v.session
	if conn == nil || session == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := conn.call(ctx, session, method, params, nil); err != nil {
			return noteMsg(err.Error())
		}
		return nil
	}
}

// pagePoint maps a terminal cell to the CSS pixel at its center, or false
// when the cell is in the header.
func (v *viewer) pagePoint(m tea.Mouse) (x, y float64, ok bool) {
	if m.Y < headerRows || v.cellW == 0 || v.scale == 0 {
		return 0, 0, false
	}
	x = (float64(m.X) + 0.5) * float64(v.cellW) / float64(v.scale)
	y = (float64(m.Y-headerRows) + 0.5) * float64(v.cellH) / float64(v.scale)
	return x, y, true
}

func mouseButton(b tea.MouseButton) string {
	switch b {
	case tea.MouseLeft:
		return "left"
	case tea.MouseRight:
		return "right"
	case tea.MouseMiddle:
		return "middle"
	}
	return "none"
}

func (v *viewer) mouse(kind string, m tea.Mouse, clicks int) tea.Cmd {
	x, y, ok := v.pagePoint(m)
	if !ok || v.session == "" {
		return nil
	}
	item := inputItem{kind: inputOther, session: v.session, method: "Input.dispatchMouseEvent"}
	button := mouseButton(m.Button)
	if kind == "mouseMoved" {
		clicks, item.kind = 0, inputMove
	}
	item.params = map[string]any{
		"type": kind, "x": x, "y": y, "button": button, "clickCount": clicks, "modifiers": modifiers(m.Mod),
	}
	v.inputs.push(item)
	return nil
}

func (v *viewer) wheel(m tea.Mouse) tea.Cmd {
	x, y, ok := v.pagePoint(m)
	if !ok || v.session == "" {
		return nil
	}
	dx, dy, ok := v.inputs.wheelDelta(m.Button, m.Mod.Contains(tea.ModShift), time.Now())
	if !ok {
		return nil
	}
	v.inputs.push(inputItem{kind: inputWheel, session: v.session, method: "Input.dispatchMouseEvent", params: map[string]any{
		"type": "mouseWheel", "x": x, "y": y, "deltaX": dx, "deltaY": dy, "modifiers": modifiers(m.Mod),
	}})
	return nil
}

// typeIn queues keyboard input for the shown tab.
func (v *viewer) typeIn(method string, params any) {
	if v.session != "" {
		v.inputs.push(inputItem{session: v.session, method: method, params: params})
	}
}

// close stops the screencast and lets go of Chrome, leaving every tab.
func (v *viewer) close() {
	select {
	case <-v.done:
	default:
		close(v.done)
	}
	v.inputs.close()
	if v.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if v.session != "" {
		_ = v.conn.call(ctx, v.session, "Page.stopScreencast", nil, nil)
		_ = v.conn.call(ctx, v.session, "Emulation.clearDeviceMetricsOverride", nil, nil)
		v.conn.detach(ctx, v.session)
	}
	v.conn.Close()
	if v.out != nil {
		k := v.kitty
		k.w = v.out
		k.Delete(0)
	}
}
