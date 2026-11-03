package desktop

import (
	"encoding/base64"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"larik/internal/status"
)

// statusRecords holds OSC 7501 reports for one terminal. A blocked child
// takes precedence over a working parent, as in the protocol's examples.
type statusRecords struct {
	byID  map[string]status.Report
	order []string
}

func (s *statusRecords) current() status.Report {
	best, score := status.Report{State: status.Idle}, -1
	for _, id := range s.order {
		r, ok := s.byID[id]
		if !ok {
			continue
		}
		p := map[status.State]int{status.Idle: 0, status.Done: 1, status.Error: 2, status.Working: 3, status.Blocked: 4}[r.State]
		if p >= score {
			best, score = r, p
		}
	}
	return best
}

func (s *statusRecords) apply(payload string) (status.Report, bool) {
	id, r, clear, ok := parseProgramStatus(payload)
	if !ok {
		return s.current(), false
	}
	old := s.current()
	if clear {
		for key := range s.byID {
			if id == "" || key == id || strings.HasPrefix(key, id+"/") {
				delete(s.byID, key)
			}
		}
		s.order = slices.DeleteFunc(s.order, func(key string) bool { _, exists := s.byID[key]; return !exists })
	} else {
		if s.byID == nil {
			s.byID = make(map[string]status.Report)
		}
		if _, exists := s.byID[id]; !exists && len(s.byID) >= 256 {
			delete(s.byID, s.order[0])
			s.order = s.order[1:]
		}
		s.byID[id] = r
		s.order = slices.DeleteFunc(s.order, func(key string) bool { return key == id })
		s.order = append(s.order, id)
	}
	now := s.current()
	return now, now != old
}

func (s *statusRecords) dropLive() (status.Report, bool) {
	old := s.current()
	for id, r := range s.byID {
		if r.State.Live() {
			delete(s.byID, id)
		}
	}
	now := s.current()
	return now, now != old
}

func (s *statusRecords) clearAll() (status.Report, bool) {
	old := s.current()
	s.byID, s.order = nil, nil
	now := s.current()
	return now, now != old
}

func parseProgramStatus(payload string) (string, status.Report, bool, bool) {
	if len(payload) > 4087 || payload == "?" {
		return "", status.Report{}, false, false
	}
	values := map[string]string{}
	badID := false
	for pair := range strings.SplitSeq(payload, ":") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || len(key) == 0 || len(key) > 16 {
			continue
		}
		valid := true
		for _, b := range []byte(key) {
			if b < 'a' || b > 'z' {
				valid = false
			}
		}
		for _, b := range []byte(value) {
			if !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.ContainsRune("_.,+/=-", rune(b))) {
				valid = false
			}
		}
		if key == "id" && !valid {
			badID = true
		}
		if valid {
			values[key] = value
		}
	}
	id := values["id"]
	if badID || !validStatusID(id) {
		return "", status.Report{}, false, false
	}
	state := status.State(values["state"])
	if state == "clear" {
		return id, status.Report{}, true, true
	}
	switch state {
	case status.Idle, status.Working, status.Done, status.Blocked, status.Error:
	default:
		return "", status.Report{}, false, false
	}
	title, ok := statusText(values["title"], 256, 192)
	if !ok {
		return "", status.Report{}, false, false
	}
	msg, ok := statusText(values["msg"], 2732, 2048)
	if !ok {
		return "", status.Report{}, false, false
	}
	r := status.Report{State: state, Title: title, Msg: msg}
	if state == status.Blocked {
		switch status.Kind(values["kind"]) {
		case status.Permission, status.Question, status.Auth:
			r.Kind = status.Kind(values["kind"])
		}
	}
	return id, r, false, true
}

func validStatusID(id string) bool {
	if len(id) > 128 || id == "" {
		return id == ""
	}
	parts := strings.Split(id, "/")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 32 {
			return false
		}
		for _, b := range []byte(part) {
			if !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.ContainsRune("_.+-", rune(b))) {
				return false
			}
		}
	}
	return true
}

func statusText(encoded string, maxEncoded, maxDecoded int) (string, bool) {
	if len(encoded) > maxEncoded {
		return "", false
	}
	if encoded == "" {
		return "", true
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(decoded) > maxDecoded || !utf8.Valid(decoded) {
		return "", false
	}
	for _, r := range string(decoded) {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", false
		}
	}
	return string(decoded), true
}
