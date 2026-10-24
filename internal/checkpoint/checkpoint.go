// Package checkpoint snapshots files before the agent modifies them so a
// turn's changes can be undone.
//
// Changes are kept in undo units. Each user prompt is one (a turn, begun
// with BeginTurn); an operation that runs alongside the turns, such as a
// subagent, gets its own (Begin, through a Unit handle), so its changes
// never land in whichever turn happens to be current when it writes. A unit
// that ends while the unit that started it is still open joins it: a
// subagent that finished within its turn is undone with that turn. One that
// outlived it stays a unit of its own.
//
// Undo never reverts a file another unit may have changed since: when a
// unit that is not being restored changed the same file while, or after,
// the restored units did, it refuses with an error naming that unit.
//
// Each unit lives in its own directory (<dir>/<n>/) with a manifest, so a
// resumed session can undo turns from earlier runs. Prune removes units
// older than a retention period.
package checkpoint

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"larik/internal/pathpolicy"
)

type snapshot struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Blob    string `json:"blob,omitempty"` // file under the turn dir holding the original bytes
	Mode    uint32 `json:"mode,omitempty"`
	// Seq orders snapshots across units; 0 in manifests saved before units.
	Seq uint64 `json:"seq,omitempty"`
	// Recorded marks a state noticed after the change (a shell command's):
	// the change may have happened at any time since the unit began.
	Recorded bool `json:"recorded,omitempty"`
}

type turn struct {
	N int `json:"n"`
	// Time is when the turn first changed a file. Rewinding a conversation
	// to an earlier prompt restores every turn that began changing files
	// after that prompt was sent.
	Time  time.Time  `json:"time,omitempty"`
	Files []snapshot `json:"files"`

	// Owner names the operation a unit belongs to; "" for a turn.
	Owner string `json:"owner,omitempty"`
	// Begin and End bound, in the store's sequence, when the unit could
	// change files. Both are 0 in manifests saved before units.
	Begin uint64 `json:"begin,omitempty"`
	End   uint64 `json:"end,omitempty"`
	// Join is the unit this one was undone with: the unit that started it,
	// when it ended while that was still open.
	Join int `json:"join,omitempty"`

	open   bool            // may still change files: the current turn, or a unit not ended
	gone   bool            // undone
	parent *turn           // the unit that was open when this one began
	seen   map[string]bool // paths captured in this unit
}

// task reports whether the unit belongs to an operation rather than a turn.
func (t *turn) task() bool { return t.Owner != "" }

// running reports whether an operation is still changing files under the
// unit. An open turn is not running: it is what the user undoes.
func (t *turn) running() bool { return t.open && t.task() }

// end is the last point in the sequence at which the unit could have
// changed a file.
func (t *turn) end() uint64 {
	if t.open {
		return math.MaxUint64
	}
	return t.End
}

// from is the earliest point at which the state f holds could have been
// taken: a recorded state predates the record, back to the unit's start.
func (t *turn) from(f snapshot) uint64 {
	if f.Recorded {
		return min(t.Begin, f.Seq)
	}
	return f.Seq
}

func (t *turn) who() string {
	if t.task() {
		return fmt.Sprintf("task %q", t.Owner)
	}
	return "another turn"
}

// Store keeps one snapshot per file per unit, taken at the first write.
type Store struct {
	dir  string
	root string

	mu    sync.Mutex
	turns []*turn // by N
	cur   *turn   // the current turn, nil before the first in this run
	seq   uint64  // last sequence number handed out
	lastN int     // highest unit number used, so none is reused in a run
}

// Unit is the handle of one operation's undo unit. Its Capture and Record
// put changes in that unit whatever turn is current. End it when the
// operation finishes.
type Unit struct {
	s *Store
	t *turn
}

// New opens the store in dir, loading turns saved by earlier runs of the
// same session.
func New(dir, projectRoot string) *Store {
	s := &Store{dir: dir, root: projectRoot}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		manifest := filepath.Join(dir, e.Name(), "manifest.json")
		data, err := os.ReadFile(manifest)
		if err != nil {
			continue
		}
		var t turn
		if json.Unmarshal(data, &t) != nil || t.N != n || len(t.Files) == 0 {
			continue
		}
		if t.Time.IsZero() { // saved before turns were timed
			if fi, err := os.Stat(manifest); err == nil {
				t.Time = fi.ModTime()
			}
		}
		t.seen = map[string]bool{}
		for _, f := range t.Files {
			t.seen[f.Path] = true
			s.seq = max(s.seq, f.Seq)
		}
		s.seq = max(s.seq, t.Begin, t.End)
		s.lastN = max(s.lastN, t.N)
		s.turns = append(s.turns, &t)
	}
	sort.Slice(s.turns, func(i, j int) bool { return s.turns[i].N < s.turns[j].N })
	// Whatever wrote these has stopped. A unit whose end wasn't saved (the
	// last turn of a run, or a run that crashed) ended by now at the latest.
	for _, t := range s.turns {
		if t.End == 0 && t.Begin != 0 {
			t.End = s.seq
		}
	}
	return s
}

// begin starts a unit. The caller holds s.mu.
func (s *Store) begin(owner string, parent *turn) *turn {
	s.lastN = max(s.lastN, s.nextN()-1) + 1
	s.seq++
	t := &turn{N: s.lastN, Owner: owner, Begin: s.seq, open: true, parent: parent, seen: map[string]bool{}}
	s.turns = append(s.turns, t)
	return t
}

// nextN numbers a new unit after the last one, so units without file
// changes (which leave no directory) never cause a number to be reused.
func (s *Store) nextN() int {
	if len(s.turns) == 0 {
		return 1
	}
	return s.turns[len(s.turns)-1].N + 1
}

// BeginTurn starts a new turn, the undo unit of one user prompt, and ends
// the previous one.
func (s *Store) BeginTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeTurn()
	s.cur = s.begin("", nil)
}

// closeTurn ends the current turn. The caller holds s.mu.
func (s *Store) closeTurn() {
	t := s.cur
	if t == nil {
		return
	}
	s.cur = nil
	t.open = false
	s.seq++
	t.End = s.seq
	if len(t.Files) == 0 {
		s.remove(t) // nothing to undo; units it started stay grouped by its number
		return
	}
	_ = s.saveManifest(t) // best effort: a missing end is taken as the load time
}

// current returns the current turn, starting one if this run has none yet.
// The caller holds s.mu.
func (s *Store) current() *turn {
	if s.cur == nil {
		s.cur = s.begin("", nil)
	}
	return s.cur
}

// Begin starts an undo unit for an operation that runs alongside the turns
// (a subagent), named owner, inside the current turn.
func (s *Store) Begin(owner string) *Unit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &Unit{s: s, t: s.begin(ownerName(owner), s.cur)}
}

// Begin starts a unit for an operation that u's operation started.
func (u *Unit) Begin(owner string) *Unit {
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	return &Unit{s: u.s, t: u.s.begin(ownerName(owner), u.t)}
}

func ownerName(owner string) string {
	if owner == "" {
		return "task"
	}
	return owner
}

// Owner is the name the unit was begun with.
func (u *Unit) Owner() string { return u.t.Owner }

// Capture records a file's current state in u if not already captured.
func (u *Unit) Capture(path string) error {
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	return u.s.capture(u.t, path)
}

// Record is Store.Record for u.
func (u *Unit) Record(path string, existed bool, data []byte, mode os.FileMode) error {
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	return u.s.record(u.t, path, existed, data, mode)
}

// End finishes the unit. If the unit that was open when it began still is,
// the two are undone together from now on; otherwise it stays a unit of
// its own. Ending twice does nothing. A change recorded after End still
// lands in the unit.
func (u *Unit) End() error {
	if u == nil {
		return nil
	}
	s := u.s
	s.mu.Lock()
	defer s.mu.Unlock()
	t := u.t
	if !t.open {
		return nil
	}
	t.open = false
	s.seq++
	t.End = s.seq
	if p := t.parent; p != nil && p.open && !p.gone {
		root := p.N
		if p.Join != 0 {
			root = p.Join
		}
		t.Join = root
		// Units that had joined this one go with it, keeping groups flat.
		for _, o := range s.turns {
			if o.Join == t.N {
				o.Join = root
				if len(o.Files) > 0 && !o.gone {
					_ = s.saveManifest(o)
				}
			}
		}
	}
	switch {
	case t.gone:
		return nil
	case len(t.Files) == 0:
		s.remove(t) // nothing to undo; units joined to it keep its number as their group
		return nil
	}
	return s.saveManifest(t)
}

// Capture records a file's current state in the current turn if not
// already captured there.
func (s *Store) Capture(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capture(s.current(), path)
}

// capture is Capture into t. The caller holds s.mu.
func (s *Store) capture(t *turn, path string) error {
	rel, err := pathpolicy.WritePath(s.root, path)
	if err != nil {
		return err
	}
	s.live(t)
	if t.seen[path] {
		return nil
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if os.IsNotExist(err) {
		return s.add(t, snapshot{Path: path}, nil)
	}
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return err
	}
	return s.add(t, snapshot{Path: path, Existed: true, Mode: uint32(fi.Mode().Perm())}, data)
}

// Record stores a file's original state as the caller found it, for a
// change noticed only after it happened (a shell command's). Like Capture,
// it keeps the first state recorded for a path in a turn.
func (s *Store) Record(path string, existed bool, data []byte, mode os.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record(s.current(), path, existed, data, mode)
}

// record is Record into t. The caller holds s.mu.
func (s *Store) record(t *turn, path string, existed bool, data []byte, mode os.FileMode) error {
	if _, err := pathpolicy.WritePath(s.root, path); err != nil {
		return err
	}
	s.live(t)
	if t.seen[path] {
		return nil
	}
	snap := snapshot{Path: path, Existed: existed, Mode: uint32(mode.Perm()), Recorded: true}
	return s.add(t, snap, data)
}

// Shell tracking observes all concurrent changes, not their author. Never
// infer ownership from equal preimages: a shell may have written after another
// unit captured that same preimage. Keep both snapshots and refuse isolated
// undo when ownership is ambiguous; rewinding both units remains safe.

// live readies t for a change. A unit dropped while its operation could
// still write (undone, or ended with no changes, before a command
// interrupted earlier finished) comes back empty as a unit of its own, so
// the late change is still undoable. The caller holds s.mu.
func (s *Store) live(t *turn) {
	if !t.gone {
		return
	}
	t.gone, t.Join = false, 0
	t.Files, t.seen, t.Time = nil, map[string]bool{}, time.Time{}
	s.turns = append(s.turns, t)
	sort.Slice(s.turns, func(i, j int) bool { return s.turns[i].N < s.turns[j].N })
}

// add saves one snapshot in t. The caller holds s.mu.
func (s *Store) add(t *turn, snap snapshot, data []byte) error {
	s.seq++
	snap.Seq = s.seq
	if snap.Existed {
		tdir := filepath.Join(s.dir, fmt.Sprint(t.N))
		if err := os.MkdirAll(tdir, 0o700); err != nil {
			return err
		}
		snap.Blob = filepath.Join(tdir, fmt.Sprintf("%d.bin", len(t.Files)))
		if err := os.WriteFile(snap.Blob, data, 0o600); err != nil {
			return err
		}
	} else {
		snap.Mode = 0
	}
	first, end := len(t.Files) == 0, t.End
	if first {
		t.Time = time.Now()
	}
	if !t.open {
		t.End = s.seq // a change after End: the operation was still writing
	}
	t.Files = append(t.Files, snap)
	if err := s.saveManifest(t); err != nil {
		t.Files = t.Files[:len(t.Files)-1]
		t.End = end
		if first {
			t.Time = time.Time{}
		}
		return err
	}
	t.seen[snap.Path] = true
	return nil
}

func (s *Store) saveManifest(t *turn) error {
	tdir := filepath.Join(s.dir, fmt.Sprint(t.N))
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tdir, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(tdir, "manifest.json"))
}

// remove drops t from the store and deletes its directory. The caller
// holds s.mu.
func (s *Store) remove(t *turn) {
	s.turns = slices.DeleteFunc(s.turns, func(o *turn) bool { return o == t })
	t.gone = true
	if s.cur == t {
		s.cur = nil
	}
	if len(t.Files) > 0 {
		_ = os.RemoveAll(filepath.Join(s.dir, fmt.Sprint(t.N)))
	}
}

// group is the number of the unit t is undone with (its own if none).
func (t *turn) group() int {
	if t.Join != 0 {
		return t.Join
	}
	return t.N
}

// Undo reverts the most recent turn that changed files, with the units
// that joined it, and returns the restored paths. Restoration is refused
// while any operation is still running, or if it would discard changes
// made by a unit outside the selected group.
func (s *Store) Undo() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.activeError("undo"); err != nil {
		return nil, err
	}
	pick := 0
	for _, t := range s.turns {
		if len(t.Files) > 0 {
			pick = max(pick, t.group())
		}
	}
	if pick == 0 {
		return nil, fmt.Errorf("nothing to undo")
	}
	var sel []*turn
	for _, t := range s.turns {
		if t.group() == pick {
			sel = append(sel, t)
		}
	}
	return s.revert(sel, "undo")
}

// since selects the units that first changed a file at or after since.
// The caller holds s.mu.
func (s *Store) since(since time.Time) []*turn {
	var sel []*turn
	for _, t := range s.turns {
		if len(t.Files) > 0 && !t.Time.Before(since) {
			sel = append(sel, t)
		}
	}
	return sel
}

// Since lists the files UndoSince(since) would restore, newest change
// first, without touching anything.
func (s *Store) Since(since time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range plan(s.since(since)) {
		out = append(out, r.snap.Path)
	}
	return out
}

// UndoSince reverts every unit that first changed a file at or after
// since, so each file ends as it was before the earliest of them. It
// returns the restored paths. It refuses, changing nothing, while one of
// those units' operations is still running, or when reverting would
// discard a change made by a unit it does not revert. On an error while
// restoring, the units all stay, so the call can be retried.
func (s *Store) UndoSince(since time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.activeError("restore files"); err != nil {
		return nil, err
	}
	sel := s.since(since)
	if len(sel) == 0 {
		return nil, nil
	}
	return s.revert(sel, "restore files")
}

// restore is one file to put back: the earliest state any of the units
// being reverted holds for it.
type restore struct {
	snap snapshot
	from uint64 // earliest point the state could have been taken
}

// plan picks, per path, the earliest state among sel, newest first.
func plan(sel []*turn) []restore {
	byPath := map[string]restore{}
	for _, t := range sel {
		for _, f := range t.Files {
			r := restore{snap: f, from: t.from(f)}
			if old, ok := byPath[f.Path]; !ok || r.from < old.from || r.from == old.from && f.Seq < old.snap.Seq {
				byPath[f.Path] = r
			}
		}
	}
	out := make([]restore, 0, len(byPath))
	for _, r := range byPath {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].snap.Seq != out[j].snap.Seq {
			return out[i].snap.Seq > out[j].snap.Seq
		}
		return out[i].snap.Path > out[j].snap.Path
	})
	return out
}

// revert restores the files of sel and drops those units. It changes
// nothing if another unit may have changed one of the files after the
// state restored was taken, or if a snapshot can't be read. The caller
// holds s.mu.
func (s *Store) revert(sel []*turn, verb string) ([]string, error) {
	todo := plan(sel)
	for _, r := range todo {
		for _, o := range s.turns {
			if slices.Contains(sel, o) || !o.seen[r.snap.Path] || o.end() <= r.from {
				continue
			}
			name := s.display(r.snap.Path)
			if o.running() {
				return nil, fmt.Errorf("cannot %s: %s is still running and has changed %s; wait for it to finish or stop it", verb, o.who(), name)
			}
			return nil, fmt.Errorf("cannot %s: %s also changed %s, and restoring it would discard those changes; rewind to before both to restore them together", verb, o.who(), name)
		}
	}
	data := make([][]byte, len(todo))
	for i, r := range todo {
		if r.snap.Existed {
			b, err := os.ReadFile(r.snap.Blob)
			if err != nil {
				return nil, err
			}
			data[i] = b
		}
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var restored []string
	for i, r := range todo {
		f := r.snap
		rel, err := pathpolicy.WritePath(s.root, f.Path)
		if err != nil {
			return restored, err
		}
		if !f.Existed {
			if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
				return restored, err
			}
		} else if err := restoreFile(root, rel, data[i], os.FileMode(f.Mode)); err != nil {
			return restored, err
		}
		restored = append(restored, f.Path)
	}
	for _, t := range sel {
		s.remove(t)
	}
	return restored, nil
}

// activeError refuses restoration while a task could still write, even if
// it has not captured a path yet. Shell changes are discovered only after
// execution, so snapshots alone cannot prove a running task is disjoint.
// The caller holds s.mu.
func (s *Store) activeError(verb string) error {
	for _, t := range s.turns {
		if t.running() {
			detail := ""
			if len(t.Files) > 0 {
				detail = " and has changed " + s.display(t.Files[0].Path)
			}
			return fmt.Errorf("cannot %s: %s is still running%s; wait for it to finish or stop it", verb, t.who(), detail)
		}
	}
	return nil
}

// display names path for an error message.
func (s *Store) display(path string) string {
	if rel, err := filepath.Rel(s.root, path); err == nil && filepath.IsLocal(rel) {
		return rel
	}
	return path
}

// restoreFile replaces the directory entry atomically. A symlink installed at
// the target after capture is replaced rather than followed.
func restoreFile(root *os.Root, rel string, data []byte, mode os.FileMode) error {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	// A shell command may have removed the directory along with the file.
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := filepath.Join(filepath.Dir(rel), ".larik-undo-"+hex.EncodeToString(nonce[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, rel)
}

// Prune deletes units under root (one directory per session) whose last
// change is older than maxAge, then any session directory left empty.
func Prune(root string, maxAge time.Duration) error {
	sessions, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, sess := range sessions {
		if !sess.IsDir() {
			continue
		}
		dir := filepath.Join(root, sess.Name())
		turns, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, t := range turns {
			if fi, err := t.Info(); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.RemoveAll(filepath.Join(dir, t.Name()))
			}
		}
		_ = os.Remove(dir) // only succeeds when empty
	}
	return nil
}
