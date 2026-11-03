package desktop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// convIndex keeps what was said in each session, its prompts and the
// replies, so the search finds a session by its conversation and not
// only its title. Prompts and replies are a sliver of a transcript, most
// of which is tool output. Transcripts only grow, so it reads what each
// gained since it last looked, and one that shrank from the start.
type convIndex struct {
	mu    sync.Mutex
	files map[string]*convFile
}

type convFile struct {
	read  int64    // how far the transcript has been read
	msgs  []string // its prompts and replies
	lower []string // msgs, lowercased
}

// convLine is what the index reads of a transcript's line.
type convLine struct {
	Message *struct {
		Role   string `json:"role"`
		Blocks []struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			Attachment string `json:"attachment"`
		} `json:"blocks"`
	} `json:"message"`
}

// refresh reads what the transcripts at paths gained, and forgets those
// no longer listed.
func (x *convIndex) refresh(paths []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.files == nil {
		x.files = map[string]*convFile{}
	}
	listed := make(map[string]bool, len(paths))
	for _, path := range paths {
		listed[path] = true
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		f := x.files[path]
		if f == nil || fi.Size() < f.read {
			f = &convFile{}
			x.files[path] = f
		}
		if fi.Size() > f.read {
			f.readFrom(path)
		}
	}
	for path := range x.files {
		if !listed[path] {
			delete(x.files, path)
		}
	}
}

// readFrom reads the transcript's whole lines after those read already;
// a line still being written waits for the next look.
func (f *convFile) readFrom(path string) {
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	if _, err := fh.Seek(f.read, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReader(fh)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return // io.EOF leaves a partial line unread
		}
		f.read += int64(len(line))
		if !bytes.Contains(line, []byte(`"text"`)) {
			continue // tool calls and results, usage, and the like
		}
		var l convLine
		if json.Unmarshal(line, &l) != nil || l.Message == nil || (l.Message.Role != "user" && l.Message.Role != "assistant") {
			continue
		}
		for _, b := range l.Message.Blocks {
			// An attached file's text isn't what was said.
			if b.Type == "text" && b.Attachment == "" && strings.TrimSpace(b.Text) != "" {
				f.msgs = append(f.msgs, b.Text)
				f.lower = append(f.lower, strings.ToLower(b.Text))
			}
		}
	}
}

// search finds the sessions whose conversation has every one of the
// lowercase words, each with a passage around the first word in the
// message that has the most of them.
func (x *convIndex) search(words []string) map[string]string {
	x.mu.Lock()
	defer x.mu.Unlock()
	hits := map[string]string{}
	for path, f := range x.files {
		if snippet, ok := f.find(words); ok {
			hits[path] = snippet
		}
	}
	return hits
}

func (f *convFile) find(words []string) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	for _, w := range words {
		if !hasWord(f.lower, w) {
			return "", false
		}
	}
	best, most := -1, 0
	for i, l := range f.lower {
		n := 0
		for _, w := range words {
			if strings.Contains(l, w) {
				n++
			}
		}
		if n > most {
			best, most = i, n
		}
	}
	for _, w := range words {
		if i := strings.Index(f.lower[best], w); i >= 0 {
			return snippet(f.msgs[best], f.lower[best], i, len(w)), true
		}
	}
	return "", false
}

func hasWord(msgs []string, w string) bool {
	for _, m := range msgs {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// snippet is the passage of msg around the match at i, n bytes long in
// lower, on one line. Lowercasing that changes a message's length moves
// its offsets, so such a message shows lowercased.
func snippet(msg, lower string, i, n int) string {
	src := msg
	if len(lower) != len(msg) {
		src = lower
	}
	const before, after = 30, 90
	start, end := max(i-before, 0), min(i+n+after, len(src))
	for start > 0 && !utf8.RuneStart(src[start]) {
		start++
	}
	for end < len(src) && !utf8.RuneStart(src[end]) {
		end--
	}
	s := strings.Join(strings.Fields(src[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(src) {
		s += "…"
	}
	return s
}
