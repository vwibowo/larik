package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// replayCommand is /replay: the filmstrip in the browser panel, or a GIF of it.
func (m *model) replayCommand(arg string) tea.Cmd {
	fail := func(s string) tea.Cmd { return m.println(m.st.err.Render("replay: " + s)) }
	if m.opts.App == nil || m.opts.App.Browser == nil {
		return fail("the browser tools are off")
	}
	b := m.opts.App.Browser
	fields := strings.Fields(arg)
	switch {
	case len(fields) == 0:
	case fields[0] == "gif" && len(fields) <= 2:
		path := "larik-replay-" + time.Now().Format("20060102-150405") + ".gif"
		if len(fields) == 2 {
			path = fields[1]
		}
		// A long strip takes a while to encode.
		return tea.Sequence(m.println(m.st.dim.Render("making the GIF…")), func() tea.Msg {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return outputMsg(m.st.err.Render("replay: " + err.Error()))
			}
			defer f.Close()
			n, err := b.WriteGIF(f)
			if err != nil {
				os.Remove(path)
				return outputMsg(m.st.err.Render("replay: " + err.Error()))
			}
			size := int64(0)
			if fi, err := f.Stat(); err == nil {
				size = fi.Size()
			}
			return outputMsg(m.st.dim.Render(fmt.Sprintf("saved %s (%d steps, %s)", path, n, sizeOf(size))))
		})
	default:
		return fail("usage: /replay [gif [file]]")
	}
	if len(b.Film()) == 0 {
		return fail("nothing recorded yet: the filmstrip fills as an agent uses the browser")
	}
	if !m.panelAllowed() {
		return fail("this needs the browser panel; /replay gif exports it instead")
	}
	init, err := m.prepareBrowserPanel()
	if err != nil {
		return fail(err.Error())
	}
	show := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := b.ShowReplay(ctx); err != nil {
			return outputMsg(m.st.err.Render("replay: " + err.Error()))
		}
		return nil
	}
	return tea.Batch(init, show)
}
