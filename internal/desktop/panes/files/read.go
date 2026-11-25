package filespane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

const (
	MaxFilePreview  = 512 << 10
	MaxImagePreview = 10 << 20
	MaxFileMatches  = 500
)

func ReadDir(ctx context.Context, root, path string, showHidden, showIgnored bool) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if !NameVisible(item.Name(), showHidden, showIgnored) {
			continue
		}
		paths = append(paths, filepath.Join(path, item.Name()))
	}
	ignored := map[string]bool{}
	if !showIgnored {
		ignored = GitIgnored(ctx, root, paths)
	}
	entries := make([]Entry, 0, len(paths))
	for _, item := range items {
		p := filepath.Join(path, item.Name())
		if !NameVisible(item.Name(), showHidden, showIgnored) || ignored[p] {
			continue
		}
		entries = append(entries, Entry{Path: p, Dir: item.IsDir()})
	}
	slices.SortFunc(entries, func(x, y Entry) int {
		if x.Dir != y.Dir {
			if x.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(filepath.Base(x.Path)), strings.ToLower(filepath.Base(y.Path)))
	})
	return entries, ctx.Err()
}

func NameVisible(name string, showHidden, showIgnored bool) bool {
	if name == ".git" {
		return showIgnored
	}
	return showHidden || !strings.HasPrefix(name, ".")
}

func GitIgnored(ctx context.Context, root string, paths []string) map[string]bool {
	ignored := map[string]bool{}
	if len(paths) == 0 {
		return ignored
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "--stdin", "-z")
	var input bytes.Buffer
	for _, p := range paths {
		input.WriteString(p)
		input.WriteByte(0)
	}
	cmd.Stdin = &input
	out, err := cmd.Output()
	if err != nil {
		return ignored // exit 1 means nothing was ignored; 128 means no Git repository
	}
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			ignored[string(p)] = true
		}
	}
	return ignored
}

func Find(ctx context.Context, root, query string, showHidden, showIgnored bool) Search {
	r := Search{Tree: map[string][]Entry{}}
	query = strings.ToLower(query)
	var walk func(string)
	walk = func(dir string) {
		if ctx.Err() != nil || r.More {
			return
		}
		entries, err := ReadDir(ctx, root, dir, showHidden, showIgnored)
		if err != nil {
			if dir == root && !errors.Is(err, context.Canceled) {
				r.Err = err.Error()
			}
			return
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(strings.ToLower(filepath.Base(e.Path)), query) {
				if r.Matches >= MaxFileMatches {
					r.More = true
					return
				}
				r.Matches++
				for p := e.Path; p != root; p = filepath.Dir(p) {
					parent := filepath.Dir(p)
					child := Entry{Path: p, Dir: p != e.Path || e.Dir}
					found := false
					for _, old := range r.Tree[parent] {
						if old.Path == p {
							found = true
							break
						}
					}
					if !found {
						r.Tree[parent] = append(r.Tree[parent], child)
					}
				}
			}
			if e.Dir {
				walk(e.Path)
			}
		}
	}
	walk(root)
	for _, entries := range r.Tree {
		slices.SortFunc(entries, func(x, y Entry) int {
			if x.Dir != y.Dir {
				if x.Dir {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(filepath.Base(x.Path)), strings.ToLower(filepath.Base(y.Path)))
		})
	}
	return r
}

func ReadPreview(path string) Preview {
	r := Preview{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		r.Note = fmt.Sprintf("Can't read this file: %v", err)
		return r
	}
	r.Size = info.Size()
	if !info.Mode().IsRegular() {
		r.Note = "This item cannot be previewed."
		return r
	}
	if runtime.GOOS == "darwin" && strings.EqualFold(filepath.Ext(path), ".pdf") {
		file, err := os.Open(path)
		if err != nil {
			r.Note = fmt.Sprintf("Can't read this file: %v", err)
			return r
		}
		defer file.Close()
		var header [5]byte
		if _, err := file.Read(header[:]); err != nil || string(header[:]) != "%PDF-" {
			r.Note = "This PDF could not be opened. Open it externally to view it."
			return r
		}
		r.Pdf = true
		return r
	}
	image := IsPreviewImage(path)
	limit := int64(MaxFilePreview)
	if image {
		limit = MaxImagePreview
	}
	if info.Size() > limit {
		r.Note = fmt.Sprintf("This file is too large to preview (%.1f MB). Open it externally to view it.", float64(info.Size())/(1<<20))
		return r
	}
	data, err := os.ReadFile(path)
	if err != nil {
		r.Note = fmt.Sprintf("Can't read this file: %v", err)
		return r
	}
	if image {
		r.Image, err = ui.DecodeBitmap(data)
		if err != nil {
			r.Note = "This image could not be decoded. Open it externally to view it."
		}
		return r
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		r.Note = "This binary file cannot be previewed. Open it externally to view it."
		return r
	}
	r.Text = string(data)
	if r.Text == "" {
		r.Note = "This file is empty."
	}
	return r
}

func IsPreviewImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	}
	return false
}
