package termbrowser

import (
	"errors"
	"os"

	tea "charm.land/bubbletea/v2"
)

// A Panel is the viewer as a part of another Bubble Tea program: Larik
// shows the browser beside its conversation instead of in a pane of its
// own. It connects to the session exactly as the pane does, so it counts
// as a viewer (prompts reach it, closing it closes Chrome) and the host
// only has to give it a rectangle, its messages and, while it has the
// focus, the user's keys and mouse.

// PanelMsg carries the panel's own messages through the host's Update.
type PanelMsg struct{ msg tea.Msg }

// PanelClosedMsg says the panel has ended: the user closed it (ctrl+q) or
// the session it showed went away. The host drops it and calls Close.
type PanelClosedMsg struct{}

// GraphicsTerminal reports whether this terminal can show the browser.
func GraphicsTerminal() bool {
	ok, _ := kittyTerminal(os.Getenv)
	return ok || os.Getenv("LARIK_FORCE_KITTY") != ""
}

// Panel is a viewer embedded in a host program.
type Panel struct {
	v   *viewer
	ctl *Control
}

// NewPanel connects a panel to the session listening at socket. send is
// the host program's Send; the panel's messages arrive through it as
// PanelMsg.
func NewPanel(socket string, send func(tea.Msg)) (*Panel, error) {
	if !GraphicsTerminal() {
		return nil, errors.New("this terminal does not show kitty graphics")
	}
	ctl, err := DialControl(socket)
	if err != nil {
		return nil, err
	}
	_, tmux := kittyTerminal(os.Getenv)
	v := newViewer(kitty{tmux: tmux})
	v.embedded, v.socket, v.control = true, socket, ctl
	v.send = func(m tea.Msg) { send(PanelMsg{m}) }
	go func() {
		err := ctl.Run(func(st State) { v.send(stateMsg(st)) })
		v.send(sessionGoneMsg{err})
	}()
	return &Panel{v: v, ctl: ctl}, nil
}

// Init starts the panel; the host runs the command.
func (p *Panel) Init() tea.Cmd { return wrap(p.v.Init()) }

// Layout places the panel at screen cell x, y, cols by rows. It returns
// nil when nothing changed, so the host can call it after every update.
func (p *Panel) Layout(x, y, cols, rows int) tea.Cmd {
	return wrap(p.v.layout(x, y, cols, rows))
}

// Update handles the panel's own messages, the terminal's answer to its
// graphics probe, and the user's input while the host gives it the focus.
// Mouse coordinates are the screen's.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(PanelMsg); ok {
		msg = m.msg
	}
	if _, ok := msg.(tea.WindowSizeMsg); ok {
		return nil // the host sizes the panel with Layout
	}
	_, cmd := p.v.Update(msg)
	return wrap(cmd)
}

// Header is the panel's top two rows, as wide as the panel.
func (p *Panel) Header() [2]string { return p.v.header() }

// Title is the shown page's title.
func (p *Panel) Title() string { return p.v.title() }

// Clear is the escape that removes the panel's images from the screen,
// for the host to print when it takes the panel away.
func (p *Panel) Clear() tea.Cmd { return p.v.clearImages() }

// Close lets go of Chrome and the session. Closing the last viewer also
// shuts Chrome down, as closing the pane does.
func (p *Panel) Close() {
	p.v.close()
	p.ctl.Close()
}

// wrap marks the messages a panel command produces as the panel's, so
// the host can hand them back. Escapes for the terminal pass through.
func wrap(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch m := cmd().(type) {
		case nil:
			return nil
		case tea.BatchMsg:
			out := make(tea.BatchMsg, len(m))
			for i, c := range m {
				out[i] = wrap(c)
			}
			return out
		case tea.RawMsg, PanelClosedMsg, PanelMsg:
			return m
		default:
			return PanelMsg{m}
		}
	}
}
