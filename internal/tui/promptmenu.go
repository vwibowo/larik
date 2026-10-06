package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/session"
)

// promptRef names one of your prompts on screen: its number as /rewind
// counts them, and its text. The number is a hint; actions find the prompt
// in the session file by number and text together.
type promptRef struct {
	n    int
	text string
}

// promptMenu is the Copy / Fork / Rewind menu opened by clicking a prompt
// or with /prompt.
type promptMenu struct {
	ref    *promptRef
	picker *picker
}

const (
	promptCopy   = "copy"
	promptFork   = "fork"
	promptRewind = "rewind"
)

// promptMenuItems are the menu's rows; labels are also what a click is
// matched against.
var promptMenuItems = []pickItem{
	{label: "1. Copy", detail: "put this prompt on the clipboard", value: promptCopy},
	{label: "2. Fork", detail: "new session that keeps this turn and its reply", value: promptFork},
	{label: "3. Rewind", detail: "branch off before it and put it back in the input to edit", value: promptRewind},
}

func (m *model) openPromptMenu(ref *promptRef) {
	if m.promptMenu != nil {
		m.closePromptMenu()
	}
	p := &picker{items: promptMenuItems}
	p.home()
	m.promptMenu = &promptMenu{ref: ref, picker: p}
	m.restyleOutput(m.outputFor(ref)) // highlight the box
}

func (m *model) closePromptMenu() {
	if m.promptMenu == nil {
		return
	}
	ref := m.promptMenu.ref
	m.promptMenu = nil
	m.restyleOutput(m.outputFor(ref))
}

// outputFor returns the output showing ref, or nil.
func (m *model) outputFor(ref *promptRef) *conversationOutput {
	for i := len(m.outputs) - 1; i >= 0; i-- {
		if m.outputs[i].prompt == ref {
			return &m.outputs[i]
		}
	}
	return nil
}

// handleClick opens the prompt menu on a clicked prompt, or picks a row of
// the open menu.
func (m *model) handleClick(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	if m.promptMenu != nil && msg.Y >= m.panelTop && msg.Y < m.panelTop+m.panelRows {
		return m.clickPromptMenu(msg.Y - m.panelTop)
	}
	// Other panels and prompts keep the keyboard; don't open over them.
	if m.perm != nil || m.showKeys || m.hasPickerPanel() && m.promptMenu == nil {
		return nil
	}
	if msg.Y < 0 || msg.Y >= m.viewRows || msg.X >= m.viewWidth {
		return nil
	}
	line := m.view.YOffset() + msg.Y
	o := m.outputAt(line)
	if o == nil || o.prompt == nil || strings.TrimSpace(ansi.Strip(m.convLines[line])) == "" {
		m.closePromptMenu() // a click elsewhere in the conversation
		return nil
	}
	if m.promptMenu != nil && m.promptMenu.ref == o.prompt {
		m.closePromptMenu() // a second click closes it
		return nil
	}
	m.openPromptMenu(o.prompt)
	return nil
}

// clickPromptMenu picks the menu row on panel row row. A panel squeezed
// into fewer rows than it has scrolls, so clicks are left to the keyboard
// then.
func (m *model) clickPromptMenu(row int) tea.Cmd {
	lines := strings.Split(m.promptMenuView(), "\n")
	if m.panelRows != len(lines) || row < 2 || row >= len(lines) {
		return nil
	}
	text := ansi.Strip(lines[row])
	for _, it := range promptMenuItems {
		if strings.Contains(text, it.label) {
			return m.runPromptAction(it.value.(string))
		}
	}
	return nil
}

func (m *model) handlePromptMenuKey(msg tea.KeyPressMsg) tea.Cmd {
	pm := m.promptMenu
	switch k := msg.String(); k {
	case "esc", "ctrl+c":
		m.closePromptMenu()
		return nil
	case "1", "2", "3":
		i, _ := strconv.Atoi(k)
		return m.runPromptAction(promptMenuItems[i-1].value.(string))
	}
	if pm.picker.handleKey(msg) {
		it, _ := pm.picker.selected()
		return m.runPromptAction(it.value.(string))
	}
	return nil
}

func (m *model) promptMenuView() string {
	pm := m.promptMenu
	w := max(m.width-6, 20)
	title := m.st.accent.Render(fmt.Sprintf("Prompt %d", pm.ref.n))
	head := spread(title, m.st.dim.Render(oneLine(pm.ref.text, max(w-lipgloss.Width(title)-4, 10))), w)
	hint := "1–3 or ↑/↓ + enter choose · click an item · esc close"
	return m.st.modal.Width(max(m.width-2, 10)).Render(head + "\n" + pm.picker.view(m.st, w) + "\n" + m.st.dim.Render(hint))
}

// runPromptAction closes the menu and does what was picked on its prompt.
func (m *model) runPromptAction(action string) tea.Cmd {
	ref := m.promptMenu.ref
	m.closePromptMenu()
	info := func(s string) tea.Cmd { return m.println(m.st.dim.Render(s)) }
	fail := func(s string) tea.Cmd { return m.println(m.st.err.Render(s)) }
	if action == promptCopy {
		return m.copyText(strings.TrimSpace(ref.text), fmt.Sprintf("prompt %d", ref.n))
	}
	name := strings.ToUpper(action[:1]) + action[1:]
	if !m.idle() {
		return fail(name + " is unavailable while " + m.busyWhat() + m.interruptHint())
	}
	if m.opts.App == nil || m.sess == nil {
		return fail(name + " is not available here")
	}
	st, err := session.Load(m.sess.Path)
	if err != nil {
		return fail(err.Error())
	}
	prompts := session.Prompts(st.All)
	n := findPrompt(st, prompts, ref)
	if n == 0 {
		return fail("can't find this prompt in the session")
	}
	switch action {
	case promptFork:
		o, current := m.sessionOptions()
		o.Model, o.ResumeID, o.Fork = current, m.sess.ID, true
		if n < len(prompts) { // keep this turn, up to the next prompt
			at := prompts[n]
			o.ForkAt = &at
		}
		return m.open(o, fmt.Sprintf("branched from %s after prompt %d", m.sess.ID, n), "")
	case promptRewind:
		return m.rewindTo(st, prompts, n, "", info, fail)
	}
	return nil
}

// findPrompt returns the number (1-based) of ref's prompt among prompts,
// or 0. The number on screen is used when its text matches; otherwise the
// nearest prompt with the same text, as numbering on screen can drift
// from the file (a prompt saved under a skill's expansion, say).
func findPrompt(st *session.State, prompts []int, ref *promptRef) int {
	want := strings.TrimSpace(ref.text)
	text := func(n int) string { return session.PromptText(st.All[prompts[n-1]]) }
	if ref.n >= 1 && ref.n <= len(prompts) && text(ref.n) == want {
		return ref.n
	}
	best := 0
	for n := 1; n <= len(prompts); n++ {
		if text(n) == want && (best == 0 || abs(n-ref.n) < abs(best-ref.n)) {
			best = n
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// promptCommand handles /prompt [n]: the prompt menu for prompt n, as
// /rewind numbers them, or for the last prompt.
func (m *model) promptCommand(args []string, info, fail func(string) tea.Cmd) tea.Cmd {
	if m.sess == nil {
		return fail("/prompt is not available here")
	}
	st, err := session.Load(m.sess.Path)
	if err != nil {
		return fail(err.Error())
	}
	prompts := session.Prompts(st.All)
	if len(prompts) == 0 {
		return info("no prompts yet")
	}
	n := len(prompts)
	if len(args) > 0 {
		if n, err = strconv.Atoi(args[0]); err != nil || n < 1 || n > len(prompts) {
			return fail(fmt.Sprintf("pick a prompt number from 1 to %d (see /rewind)", len(prompts)))
		}
	}
	text := session.PromptText(st.All[prompts[n-1]])
	ref := &promptRef{n: n, text: text}
	// Highlight the prompt if it is on screen.
	for i := len(m.outputs) - 1; i >= 0; i-- {
		if p := m.outputs[i].prompt; p != nil && p.n == n && strings.TrimSpace(p.text) == text {
			ref = p
			break
		}
	}
	m.openPromptMenu(ref)
	return nil
}
