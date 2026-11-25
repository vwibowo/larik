package terminalpane

import (
	"github.com/egoist/mygo/plugins/terminal"
	"larik/internal/desktop/panes"
)

// State owns the terminal emulator for a Larik session or shell pane.
type State struct {
	PaneKind panes.Kind
	Terminal *terminal.Terminal
}

func (s *State) Kind() panes.Kind { return s.PaneKind }

func (s *State) Close() {
	if s.Terminal != nil {
		s.Terminal.Close()
	}
}
