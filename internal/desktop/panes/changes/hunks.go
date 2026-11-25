package changespane

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui/editor"
)

func ApplyHunk(old, new string, h editor.DiffHunk, pick func(editor.DiffSide, int) bool, forward bool) (string, error) {
	crlf := strings.Contains(old, "\r\n") || strings.Contains(new, "\r\n")
	o, n := DiffLines(old), DiffLines(new)
	if h.OldStart < 0 || h.OldStart > h.OldEnd || h.OldEnd > len(o) || h.NewStart < 0 || h.NewStart > h.NewEnd || h.NewEnd > len(n) {
		return "", fmt.Errorf("the change isn't in the file as it is now")
	}
	picked := func(side editor.DiffSide, i int) bool { return pick == nil || pick(side, i) }
	var out []string
	if forward {
		out = append(out, o[:h.OldStart]...)
		for i := h.OldStart; i < h.OldEnd; i++ {
			if !picked(editor.DiffOld, i) {
				out = append(out, o[i]) // a deletion not taken stays
			}
		}
		for j := h.NewStart; j < h.NewEnd; j++ {
			if picked(editor.DiffNew, j) {
				out = append(out, n[j])
			}
		}
		out = append(out, o[h.OldEnd:]...)
	} else {
		out = append(out, n[:h.NewStart]...)
		for i := h.OldStart; i < h.OldEnd; i++ {
			if picked(editor.DiffOld, i) {
				out = append(out, o[i]) // a deletion taken back
			}
		}
		for j := h.NewStart; j < h.NewEnd; j++ {
			if !picked(editor.DiffNew, j) {
				out = append(out, n[j]) // an addition not taken back stays
			}
		}
		out = append(out, n[h.NewEnd:]...)
	}
	s := strings.Join(out, "\n")
	if crlf {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s, nil
}

func LinesMatch(b *editor.Buffer, text string) bool {
	return text == "" || b.LineCount() == len(DiffLines(text))
}

func DiffLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

func HunkPick(d *editor.DiffState, h editor.DiffHunk) func(editor.DiffSide, int) bool {
	sel, ok := d.SelectedLines()
	if !ok {
		return nil
	}
	in := func(side editor.DiffSide, i int) bool { return sel.Side == side && i >= sel.Start && i <= sel.End }
	for i := h.OldStart; i < h.OldEnd; i++ {
		if in(editor.DiffOld, i) {
			return in
		}
	}
	for j := h.NewStart; j < h.NewEnd; j++ {
		if in(editor.DiffNew, j) {
			return in
		}
	}
	return nil
}

func SetIndex(ctx context.Context, root, path, was, text string) error {
	now, err := Git(ctx, root, "show", ":"+path)
	if err != nil {
		return fmt.Errorf("git show :%s: %v", path, err)
	}
	if string(now) != was {
		return fmt.Errorf("%s changed in the index since it was read: look again, and try once more", path)
	}
	entry, err := GitDo(ctx, root, "ls-files", "-s", "--", path)
	mode, _, ok := strings.Cut(entry, " ")
	if err != nil || !ok {
		return fmt.Errorf("%s isn't in the index", path)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "hash-object", "-w", "--stdin", "--no-filters")
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git hash-object: %v", err)
	}
	_, err = GitDo(ctx, root, "update-index", "--cacheinfo", mode+","+strings.TrimSpace(string(out))+","+path)
	return err
}

func WriteKept(path, was, text string) error {
	now, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(now, []byte(was)) {
		return fmt.Errorf("%s changed since it was read: look again, and try once more", filepath.Base(path))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), fi.Mode().Perm())
}
