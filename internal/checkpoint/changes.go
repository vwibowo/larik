package checkpoint

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"time"

	"larik/internal/pathpolicy"
)

// maxChangeBytes is the largest version of a file Changes will compare.
const maxChangeBytes = 2 << 20

// Change is what one undo unit did to one file. After is the state the next
// unit to touch the file started from, or the file on disk when there is no
// such unit (Latest).
type Change struct {
	Path            string
	Before, After   []byte
	Existed, Exists bool
	Mode            os.FileMode
	Latest          bool
	// Large and Binary mark files that can be listed but not compared.
	Large, Binary bool
}

// ChangeSet is the changes of one turn, with the subagents that joined it.
type ChangeSet struct {
	Group   int
	Time    time.Time
	Owner   string // the operation, for a unit that isn't a turn
	Changes []Change
}

// Changes lists what each unit changed, newest first, leaving out files that
// ended as they began.
func (s *Store) Changes() []ChangeSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := map[int][]*turn{}
	var order []int
	for _, t := range s.turns {
		if len(t.Files) == 0 {
			continue
		}
		g := t.group()
		if groups[g] == nil {
			order = append(order, g)
		}
		groups[g] = append(groups[g], t)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(order)))
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil
	}
	defer root.Close()
	var out []ChangeSet
	next := map[string]snapshot{} // what the unit after holds for a path
	for _, g := range order {
		set := ChangeSet{Group: g}
		for _, t := range groups[g] {
			if t.N == g {
				set.Owner = t.Owner
			}
			if set.Time.IsZero() || !t.Time.IsZero() && t.Time.Before(set.Time) {
				set.Time = t.Time
			}
		}
		for _, r := range plan(groups[g]) {
			if c, ok := s.change(root, r.snap, next); ok {
				set.Changes = append(set.Changes, c)
			}
		}
		for _, r := range plan(groups[g]) {
			next[r.snap.Path] = r.snap
		}
		if len(set.Changes) > 0 {
			sort.Slice(set.Changes, func(i, j int) bool { return set.Changes[i].Path < set.Changes[j].Path })
			out = append(out, set)
		}
	}
	return out
}

// change compares snap with the state that followed it. ok is false when
// there is nothing to show. The caller holds s.mu.
func (s *Store) change(root *os.Root, snap snapshot, next map[string]snapshot) (Change, bool) {
	c := Change{Path: snap.Path, Existed: snap.Existed, Mode: os.FileMode(snap.Mode)}
	var err error
	if c.Before, err = readSnapshot(snap); errors.Is(err, errTooLarge) {
		c.Large = true
	} else if err != nil {
		return c, false
	}
	if after, ok := next[snap.Path]; ok {
		c.Exists = after.Existed
		c.After, err = readSnapshot(after)
	} else {
		c.Latest = true
		c.After, c.Exists, err = s.readCurrent(root, snap.Path)
	}
	if errors.Is(err, errTooLarge) {
		c.Large = true
	} else if err != nil {
		return c, false
	}
	if c.Large {
		c.Before, c.After = nil, nil
		return c, true
	}
	c.Binary = bytes.IndexByte(c.Before, 0) >= 0 || bytes.IndexByte(c.After, 0) >= 0
	return c, c.Existed != c.Exists || !bytes.Equal(c.Before, c.After)
}

var errTooLarge = errors.New("file too large to compare")

func readSnapshot(snap snapshot) ([]byte, error) {
	if !snap.Existed {
		return nil, nil
	}
	fi, err := os.Stat(snap.Blob)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxChangeBytes {
		return nil, errTooLarge
	}
	return os.ReadFile(snap.Blob)
}

func (s *Store) readCurrent(root *os.Root, path string) (data []byte, exists bool, err error) {
	rel, err := pathpolicy.WritePath(s.root, path)
	if err != nil {
		return nil, false, err
	}
	fi, err := root.Stat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if fi.Size() > maxChangeBytes {
		return nil, true, errTooLarge
	}
	data, err = root.ReadFile(rel)
	return data, err == nil, err
}

// Replace puts content in c's file, or removes the file, as long as it is
// still what c shows. The unit keeps its snapshot, so undoing it still
// restores the file as it was before the unit.
func (s *Store) Replace(c Change, content []byte, remove bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.activeError("reject a change"); err != nil {
		return err
	}
	name := s.display(c.Path)
	if !c.Latest {
		return fmt.Errorf("a later change touched %s; reject that one first", name)
	}
	rel, err := pathpolicy.WritePath(s.root, c.Path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer root.Close()
	current, exists, err := s.readCurrent(root, c.Path)
	if err != nil {
		return err
	}
	if exists != c.Exists || !bytes.Equal(current, c.After) {
		return fmt.Errorf("%s changed after it was shown; look at it again", name)
	}
	if remove {
		return root.Remove(rel)
	}
	mode := c.Mode
	if fi, err := root.Stat(rel); err == nil {
		mode = fi.Mode().Perm()
	}
	if mode == 0 {
		mode = 0o644
	}
	return restoreFile(root, rel, slices.Clone(content), mode)
}
