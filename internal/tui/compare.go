package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/llm"
)

// pageComparedMsg is a finished /compare.
type pageComparedMsg struct {
	note  string
	image llm.Block
}

// compareCommand is /compare: the page before the edits beside the page now.
func (m *model) compareCommand() tea.Cmd {
	fail := func(s string) tea.Cmd { return m.println(m.st.err.Render("compare: " + s)) }
	if m.opts.App == nil || m.opts.App.Browser == nil {
		return fail("the browser tools are off")
	}
	if !m.inlineOn() {
		return fail("this needs inline images; ask the agent to use browser_compare")
	}
	b := m.opts.App.Browser
	return tea.Sequence(m.println(m.st.dim.Render("comparing…")), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := b.ComparePage(ctx)
		if err != nil {
			return outputMsg(m.st.err.Render("compare: " + err.Error()))
		}
		if c.PNG == nil {
			return outputMsg(m.st.dim.Render("no visible change since before the edits"))
		}
		note := fmt.Sprintf("%.0f%% of the window changed", c.Share*100)
		if c.Moving > 0 {
			note += fmt.Sprintf(", %.0f%% left out for moving by itself", c.Moving*100)
		}
		return pageComparedMsg{note, llm.Block{Type: llm.BlockImage, MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(c.PNG)}}
	})
}
