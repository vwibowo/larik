package chooserpane

import "larik/internal/desktop/panes"

// Selection tracks the keyboard choice separately from the last hovered row.
type Selection struct {
	Pick  int
	Hover int
}

func NewSelection() Selection { return Selection{Hover: -1} }

func (s *Selection) Clamp(count int) {
	if count == 0 {
		s.Pick = 0
		return
	}
	s.Pick = min(max(s.Pick, 0), count-1)
}

func (s *Selection) Move(count, by int) {
	if count == 0 {
		return
	}
	s.Pick = (s.Pick + by%count + count) % count
}

func (s *Selection) SetHover(index int) {
	if index == s.Hover {
		return
	}
	s.Hover = index
	if index >= 0 {
		s.Pick = index
	}
}

func (*Selection) Kind() panes.Kind { return panes.Chooser }
func (*Selection) Close()           {}
