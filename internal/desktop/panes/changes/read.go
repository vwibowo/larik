package changespane

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"larik/internal/worktree"
)

const (
	MaxChangedFiles = 60
	MaxDiffBytes    = 512 << 10
)

type FileRead struct {
	Path, From, Code string
	Adds, Dels       int
	Note             string
	Old, New         string
	Index            string
	Hunks            bool
}

type Read struct {
	Against            int
	Root, Branch, Base string
	Remote, GH         bool
	Snaps              []FileRead
	More               int
	Err                error
}

type HeadCache map[string]string

func ReadChanges(dir string, heads HeadCache, against int) (r Read) {
	r.Against = against
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := Git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		r.Err = fmt.Errorf("%s is not in a git repository", dir)
		return r
	}
	r.Root = strings.TrimSpace(string(out))
	root := r.Root
	branch, _ := Git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	r.Branch = strings.TrimSpace(string(branch))
	r.Base = FindBase(ctx, root, r.Branch)
	remotes, _ := Git(ctx, root, "remote")
	r.Remote = strings.TrimSpace(string(remotes)) != ""
	_, err = exec.LookPath("gh")
	r.GH = err == nil

	status, err := Git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		r.Err = err
		return r
	}
	entries := ParseStatus(status)
	head, _ := Git(ctx, root, "rev-parse", "HEAD") // none before the first commit
	since := strings.TrimSpace(string(head))
	if against == CompareBase && r.Base != "" {
		mb, err := Git(ctx, root, "merge-base", "HEAD", r.Base)
		if err == nil {
			since = strings.TrimSpace(string(mb))
			entries = SinceEntries(ctx, root, since, entries)
		}
	}
	counts := Numstat(ctx, root, cmp.Or(since, "HEAD"))
	var index map[string]string // path → its blob in the index, for files with staged changes
	used := map[string]bool{}
	if len(entries) > MaxChangedFiles {
		r.More = len(entries) - MaxChangedFiles
		entries = entries[:MaxChangedFiles]
	}
	for _, e := range entries {
		s := FileRead{Path: e.Path, Code: e.Code}
		if e.From != e.Path {
			s.From = e.From
		}
		if c, ok := counts[e.Path]; ok {
			s.Adds, s.Dels = c[0], c[1]
		}
		if e.Code != "??" && e.Code[0] != 'A' && since != "" {
			key := since + ":" + e.From
			old, ok := heads[key]
			if !ok {
				out, _ := Git(ctx, root, "show", since+":"+e.From)
				old = string(out)
				heads[key] = old
			}
			used[key] = true
			s.Old = old
		}
		if data, err := os.ReadFile(filepath.Join(root, e.Path)); err == nil {
			s.New = string(data)
			if e.Code == "??" {
				s.Adds = strings.Count(s.New, "\n")
			}
		}
		switch {
		case len(s.Old) > MaxDiffBytes || len(s.New) > MaxDiffBytes:
			s.Note, s.Old, s.New = "too large to show", "", ""
		case IsBinary(s.Old) || IsBinary(s.New):
			s.Note, s.Old, s.New = "binary", "", ""
		case against == CompareCommit && Hunkable(s):
			// What's in the Index: HEAD's text, with nothing staged.
			s.Hunks, s.Index = true, s.Old
			if s.Code[0] != ' ' {
				if index == nil {
					index = IndexBlobs(ctx, root)
				}
				blob, ok := index[s.Path]
				if !ok {
					s.Hunks = false
					break
				}
				key := "blob:" + blob
				text, ok := heads[key]
				if !ok {
					out, _ := Git(ctx, root, "cat-file", "blob", blob)
					text = string(out)
					heads[key] = text
				}
				used[key] = true
				s.Index = text
			}
		}
		r.Snaps = append(r.Snaps, s)
	}
	for key := range heads {
		if !used[key] {
			delete(heads, key)
		}
	}
	return r
}

func Hunkable(s FileRead) bool {
	x, y := s.Code[0], s.Code[1]
	return s.From == "" && strings.IndexByte(" MA", x) >= 0 && strings.IndexByte(" M", y) >= 0
}

func FindBase(ctx context.Context, root, branch string) string {
	base := ""
	if strings.HasPrefix(branch, worktree.BranchPrefix) {
		if common, err := Git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			out, _ := Git(ctx, filepath.Dir(strings.TrimSpace(string(common))), "symbolic-ref", "--quiet", "--short", "HEAD")
			base = strings.TrimSpace(string(out))
		}
	}
	if base == "" {
		if out, err := Git(ctx, root, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
			base = strings.TrimSpace(string(out))
		}
	}
	for _, b := range []string{"main", "master"} {
		if base != "" {
			break
		}
		if _, err := Git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			base = b
		}
	}
	if base == branch || strings.TrimPrefix(base, "origin/") == branch {
		return ""
	}
	return base
}
