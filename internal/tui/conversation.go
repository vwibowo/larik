package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// maxConversationBytes bounds the printed conversation kept for scrolling;
// the oldest output is dropped past it. The session file keeps everything.
const maxConversationBytes = 2 << 20

// appendOutput adds printed output to the conversation. Output is wrapped
// once here, so rendering a frame only touches the visible lines.
func (m *model) appendOutput(s string) {
	m.outputs = append(m.outputs, s)
	m.outputBytes += len(s)
	if m.outputBytes > maxConversationBytes {
		drop := 0
		for m.outputBytes > maxConversationBytes*3/4 && drop < len(m.outputs)-1 {
			m.outputBytes -= len(m.outputs[drop])
			drop++
		}
		m.outputs = append([]string(nil), m.outputs[drop:]...)
		m.rewrapConversation()
		return
	}
	m.convLines = append(m.convLines, wrapOutput(s, m.convWidth)...)
	m.setConversation()
}

// rewrapConversation re-wraps all output for the current width.
func (m *model) rewrapConversation() {
	m.rewrapConversationWidth(m.width)
}

func (m *model) rewrapConversationWidth(width int) {
	m.convWidth = width
	m.convLines = m.convLines[:0]
	for _, s := range m.outputs {
		m.convLines = append(m.convLines, wrapOutput(s, m.convWidth)...)
	}
	m.setConversation()
}

func (m *model) resetConversation() {
	m.outputs, m.outputBytes, m.convLines = nil, 0, nil
	m.setConversation()
}

func (m *model) setConversation() {
	atBottom := m.view.AtBottom()
	// Clip capacity so later appends never write into the viewport's slice.
	m.view.SetContentLines(m.convLines[:len(m.convLines):len(m.convLines)])
	if atBottom {
		m.view.GotoBottom()
	}
}

func wrapOutput(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, max(width, 1), ""), "\n")
}
