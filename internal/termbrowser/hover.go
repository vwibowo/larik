package termbrowser

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/chromedp/cdproto/target"
)

// While the pointer rests on a link, the second row says where it goes,
// as a desktop browser's status bubble does: the page is only a picture,
// so nothing else would tell the user before they click.

// hoverDelay is how long the pointer rests before the viewer asks the page
// what is under it: asking on every move would be a call per cell.
const hoverDelay = 150 * time.Millisecond

type (
	// hoverAskMsg is the pointer having rested at CSS pixel x, y.
	hoverAskMsg struct {
		gen  int
		x, y float64
	}
	// hoverMsg is the link under the pointer, or "".
	hoverMsg struct {
		gen     int
		session target.SessionID
		href    string
	}
)

// hoverJS finds the link at a point, through open shadow roots.
const hoverJS = `((x, y) => {
	let e = document.elementFromPoint(x, y);
	while (e && e.shadowRoot) {
		const inner = e.shadowRoot.elementFromPoint(x, y);
		if (!inner || inner === e) break;
		e = inner;
	}
	const a = e && e.closest && e.closest('a[href]');
	return a ? String(a.href) : '';
})(%v, %v)`

// pointerAt notes that the pointer moved to x, y on the page (ok) or off
// it, and asks about it once it rests.
func (v *viewer) pointerAt(x, y float64, ok bool) tea.Cmd {
	v.hoverGen++
	if !ok {
		v.hover = ""
		return nil
	}
	gen := v.hoverGen
	return tea.Tick(hoverDelay, func(time.Time) tea.Msg { return hoverAskMsg{gen, x, y} })
}

// askHover asks the page for the link under the resting pointer.
func (v *viewer) askHover(msg hoverAskMsg) tea.Cmd {
	conn, session := v.conn, v.session
	if msg.gen != v.hoverGen || conn == nil || session == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = conn.call(ctx, session, "Runtime.evaluate", map[string]any{
			"expression": fmt.Sprintf(hoverJS, msg.x, msg.y), "returnByValue": true,
		}, &res)
		return hoverMsg{msg.gen, session, res.Result.Value}
	}
}
