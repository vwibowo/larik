package tools

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// quietEnv is env (the current environment when nil) set up so commands
// don't page or color their output, which the model can't use.
func quietEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	return append(env[:len(env):len(env)], "NO_COLOR=1", "PAGER=cat", "GIT_PAGER=cat")
}

// ansiSeq matches terminal escape sequences: CSI (colors, cursor moves),
// OSC (titles, hyperlinks), charset selection and two-byte escapes.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[()][0-9A-Za-z]|\x1b[@-Z\\-_=>]`)

// cleanTerminal removes what only makes sense on a terminal: escape
// sequences, and lines redrawn in place with \r (progress bars), of
// which only the final state is kept.
func cleanTerminal(s string) string {
	if !strings.ContainsAny(s, "\x1b\r") {
		return s
	}
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = ansiSeq.ReplaceAllString(s, "")
	}
	if strings.IndexByte(s, '\r') < 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if j := strings.LastIndexByte(line, '\r'); j >= 0 {
			// The last non-empty redraw is what the terminal showed.
			segs := strings.Split(line, "\r")
			line = ""
			for k := len(segs) - 1; k >= 0; k-- {
				if segs[k] != "" {
					line = segs[k]
					break
				}
			}
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// captureBytes is how much of each end of a command's output is kept.
const captureBytes = 64 * 1024

// capBuffer keeps the first and last captureBytes written and counts the
// rest, so a command that prints gigabytes holds bounded memory.
type capBuffer struct {
	head, tail []byte
	total      int64
}

func (c *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	c.total += int64(n)
	if room := captureBytes - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > 2*captureBytes {
			c.tail = append(c.tail[:0], c.tail[len(c.tail)-captureBytes:]...)
		}
	}
	return n, nil
}

// String is everything written when it all fit. Otherwise it is the head
// and tail, cleaned for the model and laid out like Truncate's output
// within MaxOutputBytes, around the count of bytes left out.
func (c *capBuffer) String() string {
	tail := c.tail
	if len(tail) > captureBytes {
		tail = tail[len(tail)-captureBytes:]
	}
	if c.total == int64(len(c.head)+len(tail)) {
		return string(c.head) + string(tail)
	}
	budget := MaxOutputBytes - 100 // the marker fits, so Truncate leaves it be
	head := safeCut(cleanTerminal(string(c.head)), budget*2/3)
	t := cleanTerminal(string(tail))
	t = t[max(0, len(t)-(budget-len(head))):]
	for !utf8.ValidString(t) && len(t) > 0 {
		t = t[1:]
	}
	omitted := c.total - int64(len(head)+len(t))
	return fmt.Sprintf("%s\n\n... [%d bytes truncated] ...\n\n%s", head, omitted, t)
}
