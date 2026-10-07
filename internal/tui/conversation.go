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
	// prompt is set when the output shows one of your prompts, so a click
	// on it can open the prompt menu.
	prompt *promptRef
	// firstLine and lines place the output in convLines.
	firstLine, lines int
}

// appendOutput adds already-rendered output to the conversation. Output is
// wrapped once here, so rendering a frame only touches the visible lines.
func (m *model) appendOutput(s string) {
	m.appendConversationOutput(conversationOutput{text: s, sourceSize: len(s), size: len(s)})
}

// appendRenderedOutput retains enough source to lay structured output such as
// markdown out again when the conversation column changes width.
func (m *model) appendRenderedOutput(text string, renderedWidth, size int, render conversationRenderer, prompt *promptRef) {
	if renderedWidth != m.currentConversationWidth() {
		text = render(m, m.currentConversationWidth())
	}
	m.appendConversationOutput(conversationOutput{text: text, sourceSize: size, render: render, prompt: prompt})
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
	m.outputs[len(m.outputs)-1].place(&m.convLines, m.convWidth)
	m.setConversation()
}

// place wraps the output onto the end of lines and records where it sits.
func (o *conversationOutput) place(lines *[]string, width int) {
	wrapped := wrapOutput(o.text, width)
	o.firstLine, o.lines = len(*lines), len(wrapped)
	*lines = append(*lines, wrapped...)
}

// outputAt returns the output shown on conversation line n, or nil.
func (m *model) outputAt(n int) *conversationOutput {
	for i := len(m.outputs) - 1; i >= 0; i-- {
		o := &m.outputs[i]
		if n >= o.firstLine && n < o.firstLine+o.lines {
			return o
		}
		if n >= o.firstLine+o.lines {
			return nil
		}
	}
	return nil
}

// restyleOutput renders one output again in place, for a change that
// keeps its size, such as highlighting a prompt; anything else re-wraps
// the whole conversation.
func (m *model) restyleOutput(o *conversationOutput) {
	if o == nil || o.render == nil {
		return
	}
	m.outputBytes -= o.size
	o.renderAt(m, m.convWidth)
	m.outputBytes += o.size
	wrapped := wrapOutput(o.text, m.convWidth)
	if len(wrapped) != o.lines || o.firstLine+o.lines > len(m.convLines) {
		m.rewrapConversation()
		return
	}
	copy(m.convLines[o.firstLine:], wrapped)
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
		m.outputs[i].place(&m.convLines, width)
	}
	m.setConversation()
}

func (m *model) resetConversation() {
	m.outputs, m.outputBytes, m.convLines = nil, 0, nil
	m.promptMenu = nil  // its prompt is no longer on screen
	m.clearSuggestion() // it followed from what was on screen
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
