package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// remoteFileRequest is deliberately an operation, not a shell command.
// Paths are checked again on the host, where they have meaning.
type remoteFileRequest struct {
	Op      string `json:"op"`
	Root    string `json:"root"`
	Path    string `json:"path,omitempty"`
	Name    string `json:"name,omitempty"`
	Query   string `json:"query,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
	Ignored bool   `json:"ignored,omitempty"`
}

type remoteFileEntry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
}

type remoteFileReply struct {
	Entries []remoteFileEntry            `json:"entries,omitempty"`
	Tree    map[string][]remoteFileEntry `json:"tree,omitempty"`
	Matches int                          `json:"matches,omitempty"`
	More    bool                         `json:"more,omitempty"`
	Text    string                       `json:"text,omitempty"`
	Image   []byte                       `json:"image,omitempty"`
	Note    string                       `json:"note,omitempty"`
	Size    int64                        `json:"size,omitempty"`
	Dest    string                       `json:"dest,omitempty"`
	Dir     bool                         `json:"dir,omitempty"`
}

func wireEntries(entries []fileEntry) []remoteFileEntry {
	out := make([]remoteFileEntry, len(entries))
	for i, e := range entries {
		out[i] = remoteFileEntry{Path: e.path, Dir: e.dir}
	}
	return out
}

func localEntries(entries []remoteFileEntry, host string) []fileEntry {
	out := make([]fileEntry, len(entries))
	for i, e := range entries {
		out[i] = fileEntry{path: remotePath(host, e.Path), dir: e.Dir}
	}
	return out
}

func validFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`) && strings.IndexByte(name, 0) < 0
}

func filePathStaysInRoot(root, path string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if !filesWithinRoot(realRoot, realPath) {
		return errors.New("file path resolves outside the explorer folder")
	}
	return nil
}

func (h *host) fileRequest(req remoteFileRequest) (remoteFileReply, error) {
	var out remoteFileReply
	if !filepath.IsAbs(req.Root) || !filepath.IsAbs(req.Path) || !filesWithinRoot(req.Root, req.Path) {
		return out, errors.New("file path is outside the explorer folder")
	}
	if err := filePathStaysInRoot(req.Root, req.Path); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	switch req.Op {
	case "list":
		entries, err := readFilesDir(ctx, req.Root, req.Path, req.Hidden, req.Ignored)
		out.Entries = wireEntries(entries)
		return out, err
	case "search":
		if req.Path != req.Root {
			return out, errors.New("search must start at the explorer folder")
		}
		found := findFiles(ctx, req.Root, req.Query, req.Hidden, req.Ignored)
		out.Tree = map[string][]remoteFileEntry{}
		for path, entries := range found.tree {
			out.Tree[path] = wireEntries(entries)
		}
		out.Matches, out.More = found.matches, found.more
		if found.err != "" {
			return out, errors.New(found.err)
		}
		return out, ctx.Err()
	case "preview":
		return filePreviewWire(req.Path), nil
	case "stat":
		info, err := os.Lstat(req.Path)
		if err == nil {
			out.Dir = info.IsDir()
		}
		return out, err
	case "create-file", "create-folder", "rename":
		if !validFileName(req.Name) {
			return out, errors.New("invalid file name")
		}
		parent := req.Path
		if req.Op == "rename" {
			if req.Path == req.Root {
				return out, errors.New("cannot rename the explorer folder")
			}
			parent = filepath.Dir(req.Path)
			if info, err := os.Lstat(req.Path); err == nil {
				out.Dir = info.IsDir()
			}
		}
		out.Dest = filepath.Join(parent, req.Name)
		if !filesWithinRoot(req.Root, out.Dest) {
			return out, errors.New("destination is outside the explorer folder")
		}
		if _, err := os.Lstat(out.Dest); err == nil {
			return out, os.ErrExist
		} else if !os.IsNotExist(err) {
			return out, err
		}
		switch req.Op {
		case "create-file":
			file, err := os.OpenFile(out.Dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err == nil {
				err = file.Close()
			}
			return out, err
		case "create-folder":
			out.Dir = true
			return out, os.Mkdir(out.Dest, 0o755)
		default:
			return out, os.Rename(req.Path, out.Dest)
		}
	case "duplicate":
		if req.Path == req.Root {
			return out, errors.New("cannot duplicate the explorer folder")
		}
		var err error
		if info, statErr := os.Lstat(req.Path); statErr == nil {
			out.Dir = info.IsDir()
		}
		out.Dest, err = duplicateFilesPath(req.Path)
		return out, err
	case "trash":
		if req.Path == req.Root {
			return out, errors.New("cannot trash the explorer folder")
		}
		return out, moveRemoteTrash(h.dataDir, req.Path)
	default:
		return out, fmt.Errorf("unknown file operation %q", req.Op)
	}
}

func filePreviewWire(path string) remoteFileReply {
	out := remoteFileReply{}
	info, err := os.Stat(path)
	if err != nil {
		out.Note = "Can't read this file: " + err.Error()
		return out
	}
	out.Size = info.Size()
	if !info.Mode().IsRegular() {
		out.Note = "This item cannot be previewed."
		return out
	}
	image := isPreviewImage(path)
	limit := int64(maxFilePreview)
	if image {
		limit = maxImagePreview
	}
	if info.Size() > limit {
		out.Note = fmt.Sprintf("This file is too large to preview (%.1f MB).", float64(info.Size())/(1<<20))
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		out.Note = "Can't read this file: " + err.Error()
	} else if image {
		out.Image = data
	} else if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		out.Note = "This binary file cannot be previewed."
	} else {
		out.Text = string(data)
		if out.Text == "" {
			out.Note = "This file is empty."
		}
	}
	return out
}

// Remote trash lives beside the remote Larik data, with the original path
// recorded so the user can restore it. A failed rename leaves the file put.
func moveRemoteTrash(dataDir, path string) error {
	base := filepath.Join(dataDir, "desktop", "trash")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	folder, err := os.MkdirTemp(base, "item-")
	if err != nil {
		return err
	}
	to := filepath.Join(folder, filepath.Base(path))
	if err := os.Rename(path, to); err != nil {
		os.Remove(folder)
		return err
	}
	if err := os.WriteFile(filepath.Join(folder, "original-path"), []byte(path+"\n"), 0o600); err != nil {
		_ = os.Rename(to, path)
		_ = os.Remove(folder)
		return err
	}
	return nil
}

type remoteFiles struct {
	name, root string
	dial       dialer
	err        error
}

func (a *App) remoteFiles(root string) remoteFiles {
	name, dir, ok := splitRemote(root)
	if !ok {
		return remoteFiles{err: errors.New("not a remote folder")}
	}
	srv := a.serverNamed(name)
	if srv == nil || a.dialOf(srv) == nil {
		return remoteFiles{err: fmt.Errorf("not connected to %s", name)}
	}
	if !slicesContains(srv.features, "remote-files") {
		return remoteFiles{err: fmt.Errorf("update larik on %s to browse its files", name)}
	}
	return remoteFiles{name: name, root: dir, dial: a.dialOf(srv)}
}

func slicesContains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (r remoteFiles) call(req remoteFileRequest) (remoteFileReply, error) {
	if r.err != nil {
		return remoteFileReply{}, r.err
	}
	name, path, ok := splitRemote(req.Path)
	if !ok || name != r.name {
		return remoteFileReply{}, errors.New("file is on another machine")
	}
	req.Root, req.Path = r.root, path
	conn, _, rep, err := askHostVia(r.dial, hostRequest{Op: "file", File: &req})
	if err != nil {
		return remoteFileReply{}, err
	}
	conn.Close()
	if rep.File == nil {
		return remoteFileReply{}, errors.New("remote host returned no file result")
	}
	return *rep.File, nil
}
