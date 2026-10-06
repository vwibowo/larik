package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// maxConversationBytes bounds the printed conversation kept for scrolling;
// the oldest output is dropped past it. The session file keeps everything.
const maxConversationBytes = 2 << 20

type conversationRenderer func(*model, int) string

type conversationOutput struct {
	text       string
	sourceSize int
	size       int
	render     conversationRenderer
}

// appendOutput adds already-rendered output to the conversation. Output is
// wrapped once here, so rendering a frame only touches the visible lines.
func (m *model) appendOutput(s string) {
	m.appendConversationOutput(conversationOutput{text: s, sourceSize: len(s), size: len(s)})
}

// appendRenderedOutput retains enough source to lay structured output such as
// markdown out again when the conversation column changes width.
func (m *model) appendRenderedOutput(text string, renderedWidth, size int, render conversationRenderer) {
	if renderedWidth != m.currentConversationWidth() {
		text = render(m, m.currentConversationWidth())
	}
	m.appendConversationOutput(conversationOutput{text: text, sourceSize: size, render: render})
}

func (m *model) appendConversationOutput(out conversationOutput) {
	if width := m.currentConversationWidth(); m.convWidth != width {
		m.rewrapConversationWidth(width)
	}
	out.measure()
	m.outputs = append(m.outputs, out)
	m.outputBytes += out.size
	if m.outputBytes > maxConversationBytes {
		drop := 0
		for m.outputBytes > maxConversationBytes*3/4 && drop < len(m.outputs)-1 {
			m.outputBytes -= m.outputs[drop].size
			drop++
		}
		m.outputs = append([]conversationOutput(nil), m.outputs[drop:]...)
		m.rewrapConversation()
		return
	}
	m.convLines = append(m.convLines, wrapOutput(out.text, m.convWidth)...)
	m.setConversation()
}

func (o *conversationOutput) renderAt(m *model, width int) string {
	if o.render != nil {
		o.text = o.render(m, width)
	}
	o.measure()
	return o.text
}

func (o *conversationOutput) measure() {
	o.size = max(o.sourceSize, len(o.text))
}

// rewrapConversation re-wraps all output for the current layout.
func (m *model) rewrapConversation() {
	m.rewrapConversationWidth(m.currentConversationWidth())
}

func (m *model) rewrapConversationWidth(width int) {
	m.convWidth = width
	m.outputBytes = 0
	for i := range m.outputs {
		m.outputs[i].renderAt(m, width)
		m.outputBytes += m.outputs[i].size
	}
	if m.outputBytes > maxConversationBytes {
		drop := 0
		for m.outputBytes > maxConversationBytes*3/4 && drop < len(m.outputs)-1 {
			m.outputBytes -= m.outputs[drop].size
			drop++
		}
		m.outputs = append([]conversationOutput(nil), m.outputs[drop:]...)
	}
	m.convLines = m.convLines[:0]
	for i := range m.outputs {
		m.convLines = append(m.convLines, wrapOutput(m.outputs[i].text, width)...)
	}
	m.setConversation()
}

func (m *model) resetConversation() {
	m.outputs, m.outputBytes, m.convLines = nil, 0, nil
	m.setConversation()
	m.view.GotoTop() // a scrolled-up viewport would sit past the empty content
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
