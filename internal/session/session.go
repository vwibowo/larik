// Package session persists conversations as append-only JSONL files.
//
// Every entry has an id and a parent id. Branches are separate files: Fork
// copies a prefix of a session into a new one that records its origin.
package session

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"larik/internal/llm"
)

type EntryType string

const (
	EntryMeta       EntryType = "meta"
	EntryMessage    EntryType = "message"
	EntryCompaction EntryType = "compaction"
	EntryUsage      EntryType = "usage" // spend not tied to a message, e.g. subagents
)

type Entry struct {
	ID       string       `json:"id"`
	ParentID string       `json:"parent_id,omitempty"`
	Type     EntryType    `json:"type"`
	Time     time.Time    `json:"ts"`
	Message  *llm.Message `json:"message,omitempty"`
	Usage    *llm.Usage   `json:"usage,omitempty"`
	Summary  string       `json:"summary,omitempty"`
	Model    string       `json:"model,omitempty"` // EntryUsage
	Meta     *Meta        `json:"meta,omitempty"`
}

type Meta struct {
	Cwd      string `json:"cwd"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ForkOf is the session this one branched from, and ForkAt how many of
	// its messages were kept.
	ForkOf string `json:"fork_of,omitempty"`
	ForkAt int    `json:"fork_at,omitempty"`
}

type Session struct {
	ID   string
	Path string

	mu  sync.Mutex
	f   *os.File
	tip string
	seq int
}

// Dir returns a collision-resistant sessions directory for a working directory.
func Dir(dataDir, cwd string) string {
	canonical := filepath.Clean(cwd)
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	hash := sha256.Sum256([]byte(canonical))
	return filepath.Join(dataDir, "sessions", fmt.Sprintf("%s-%x", filepath.Base(canonical), hash[:8]))
}

func newID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Create starts a new session file.
func Create(dir string, meta Meta) (*Session, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	id := newID()
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	s := &Session{ID: id, Path: path, f: f}
	return s, s.append(Entry{Type: EntryMeta, Meta: &meta})
}

// State is what a loaded session reconstructs.
type State struct {
	Meta     Meta
	Messages []llm.Message // active context (after the latest compaction)
	All      []llm.Message // full history for display
	Usage    llm.Usage
	Cost     float64
	ByModel  map[string]llm.Usage // usage per model that served it
}

func (st *State) addUsage(model string, u llm.Usage) {
	st.Usage.Add(u)
	st.Cost += llm.Lookup(model).Cost(u)
	if st.ByModel == nil {
		st.ByModel = map[string]llm.Usage{}
	}
	mu := st.ByModel[model]
	mu.Add(u)
	st.ByModel[model] = mu
}

// Open loads a session and reopens it for appending.
func Open(path string) (*Session, *State, error) {
	s := &Session{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Path: path}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, nil, err
	}
	s.f = f
	st, err := s.read()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return s, st, nil
}

func lockFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("session %s is already open for writing in another process", f.Name())
		}
		return fmt.Errorf("locking session %s: %w", f.Name(), err)
	}
	return nil
}

// Load reads a session without opening it for writing, e.g. to show the
// history of a session another process (or Session) is appending to.
func Load(path string) (*State, error) {
	return (&Session{Path: path}).read()
}

func (s *Session) read() (*State, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st := &State{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // tolerate a torn final line after a crash
		}
		s.tip = e.ID
		s.seq++
		switch e.Type {
		case EntryMeta:
			if e.Meta != nil {
				st.Meta = *e.Meta
			}
		case EntryMessage:
			if e.Message == nil {
				continue
			}
			st.Messages = llm.Append(st.Messages, *e.Message)
			st.All = append(st.All, *e.Message)
			if e.Usage != nil {
				st.addUsage(e.Message.Model, *e.Usage)
			}
		case EntryUsage:
			if e.Usage != nil {
				st.addUsage(e.Model, *e.Usage)
			}
		case EntryCompaction:
			st.Messages = []llm.Message{CompactionMessage(e.Summary, s.Path)}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return st, nil
}

// CompactionMessage is how a summary re-enters the context. When the
// transcript path is known, the model is told where to look up details
// the summary left out.
func CompactionMessage(summary, transcript string) llm.Message {
	text := "This session continues from an earlier conversation that was summarized to save context. Summary:\n\n" + summary
	if transcript != "" {
		text += "\n\nThe full transcript, including everything before this summary, is in " + transcript +
			" (JSONL, one entry per line). If you need an exact detail the summary left out, such as a command, an error message or a file path, search it with grep instead of guessing."
	}
	return llm.UserText(text)
}

func (s *Session) AppendMessage(m llm.Message, usage *llm.Usage) error {
	return s.append(Entry{Type: EntryMessage, Message: &m, Usage: usage})
}

// AppendUsage records spend that isn't part of this transcript.
func (s *Session) AppendUsage(model string, u llm.Usage) error {
	return s.append(Entry{Type: EntryUsage, Model: model, Usage: &u})
}

func (s *Session) AppendCompaction(summary string) error {
	return s.append(Entry{Type: EntryCompaction, Summary: summary})
}

func (s *Session) append(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return errors.New("session closed")
	}
	s.seq++
	e.ID = fmt.Sprintf("%s-%d", s.ID, s.seq)
	e.ParentID = s.tip
	e.Time = time.Now()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return err
	}
	s.tip = e.ID
	return nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

type Info struct {
	ID       string
	Path     string
	Modified time.Time
	Title    string // first user prompt
	ForkOf   string // parent session id for branches
}

// List returns sessions in dir, newest first.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		title, forkOf := scan(path)
		out = append(out, Info{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Path: path, Modified: fi.ModTime(), Title: title, ForkOf: forkOf})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// Find resolves a session id (or unique prefix) in dir.
func Find(dir, id string) (string, error) {
	infos, err := List(dir)
	if err != nil {
		return "", err
	}
	var match []Info
	for _, in := range infos {
		if in.ID == id {
			return in.Path, nil
		}
		if strings.HasPrefix(in.ID, id) {
			match = append(match, in)
		}
	}
	switch len(match) {
	case 0:
		return "", fmt.Errorf("no session %q in %s", id, dir)
	case 1:
		return match[0].Path, nil
	}
	return "", fmt.Errorf("session id %q is ambiguous", id)
}

// scan reads a session's title (first prompt) and fork origin.
func scan(path string) (title, forkOf string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for i := 0; sc.Scan() && i < 50; i++ {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Type == EntryMeta && e.Meta != nil {
			forkOf = e.Meta.ForkOf
		}
		if e.Type == EntryMessage && e.Message != nil && IsPrompt(*e.Message) {
			t := strings.ReplaceAll(strings.TrimSpace(e.Message.Text()), "\n", " ")
			if len(t) > 80 {
				t = t[:77] + "..."
			}
			return t, forkOf
		}
	}
	return "", forkOf
}

// IsPrompt reports whether m starts a turn: a user message with text and
// no tool results. Cutting a transcript just before one never leaves a
// tool call without its result.
func IsPrompt(m llm.Message) bool {
	if m.Role != llm.RoleUser || strings.TrimSpace(m.Text()) == "" {
		return false
	}
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolResult {
			return false
		}
	}
	return true
}

// Prompts returns the indexes of the prompts in msgs.
func Prompts(msgs []llm.Message) []int {
	var out []int
	for i, m := range msgs {
		if IsPrompt(m) {
			out = append(out, i)
		}
	}
	return out
}

// Fork creates a new session in dir holding the first keep messages of
// the session at src (all of them if keep < 0). keep must fall on a turn
// boundary: the end, or the index of a prompt. Usage is not copied, so
// each branch reports only its own spend.
func Fork(dir, src string, keep int) (*Session, *State, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var (
		meta    Meta
		entries []Entry
		msgs    []llm.Message
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case EntryMeta:
			if e.Meta != nil {
				meta = *e.Meta
			}
		case EntryMessage:
			if e.Message != nil {
				msgs = append(msgs, *e.Message)
				entries = append(entries, e)
			}
		case EntryCompaction:
			entries = append(entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if keep < 0 {
		keep = len(msgs)
	}
	if keep > len(msgs) || (keep < len(msgs) && !IsPrompt(msgs[keep])) {
		return nil, nil, fmt.Errorf("can only branch before a prompt or at the end (message %d of %d)", keep, len(msgs))
	}

	meta.ForkOf = strings.TrimSuffix(filepath.Base(src), ".jsonl")
	meta.ForkAt = keep
	s, err := Create(dir, meta)
	if err != nil {
		return nil, nil, err
	}
	n := 0
	for _, e := range entries {
		if e.Type == EntryMessage {
			if n == keep {
				break
			}
			n++
			err = s.append(Entry{Type: EntryMessage, Message: e.Message})
		} else {
			err = s.append(Entry{Type: EntryCompaction, Summary: e.Summary})
		}
		if err != nil {
			s.Close()
			os.Remove(s.Path)
			return nil, nil, err
		}
	}
	path := s.Path
	s.Close()
	return Open(path)
}
