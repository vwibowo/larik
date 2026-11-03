package terminal

import "strings"

// statusOSC observes a byte stream without changing what the emulator sees.
// OSC strings can span reads and end in BEL or ST (ESC backslash).
type statusOSC struct {
	state byte
	buf   []byte
}

type oscEvent struct {
	kind    byte // S: status, P: shell prompt, R: full reset
	payload string
}

func (s *statusOSC) scan(p []byte) []oscEvent {
	var found []oscEvent
	for _, b := range p {
		switch s.state {
		case 0:
			if b == '\x1b' {
				s.state = 1
			}
		case 1:
			if b == ']' {
				s.state, s.buf = 2, s.buf[:0]
			} else if b == 'c' {
				s.state = 0
				found = append(found, oscEvent{kind: 'R'})
			} else if b != '\x1b' {
				s.state = 0
			}
		case 2:
			switch b {
			case '\x07':
				if v, ok := s.finish(); ok {
					found = append(found, v)
				}
			case '\x1b':
				s.state = 3
			default:
				s.append(b)
			}
		case 3:
			if b == '\\' {
				if v, ok := s.finish(); ok {
					found = append(found, v)
				}
			} else {
				s.state = 0 // invalid OSC; the next ESC can start one
				if b == '\x1b' {
					s.state = 1
				}
			}
		}
	}
	return found
}

func (s *statusOSC) append(b byte) {
	if len(s.buf) >= 4096 {
		s.state = 0
		s.buf = s.buf[:0]
		return
	}
	s.buf = append(s.buf, b)
}

func (s *statusOSC) finish() (oscEvent, bool) {
	s.state = 0
	v := string(s.buf)
	s.buf = s.buf[:0]
	if strings.HasPrefix(v, "7501;") {
		return oscEvent{kind: 'S', payload: strings.TrimPrefix(v, "7501;")}, true
	}
	if v == "133;A" {
		return oscEvent{kind: 'P'}, true
	}
	return oscEvent{}, false
}
