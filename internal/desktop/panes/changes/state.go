package changespane

import "sync/atomic"

// State owns the lifetime and presentation state of the Git Changes side pane.
// F is the host's diff entry, so this package does not import desktop.
type State[F any] struct {
	Dir, Root     string
	Files         []F
	More          int
	Err           string
	Loaded        bool
	Stop          chan struct{}
	Kick          chan struct{}
	Against       int
	Mode          atomic.Int32
	Branch, Base  string
	Remote, GH    bool
	Busy, Message string
}

func NewState[F any](dir string) State[F] {
	return State[F]{Dir: dir, Stop: make(chan struct{}), Kick: make(chan struct{}, 1)}
}

func (s *State[F]) Close() {
	if s.Stop != nil {
		close(s.Stop)
	}
}
