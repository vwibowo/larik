package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"larik/internal/worktree"
)

type remoteProjectRequest struct {
	Op       string `json:"op"`
	Dir      string `json:"dir"`
	Path     string `json:"path,omitempty"`
	From     string `json:"from,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Was      string `json:"was,omitempty"`
	Text     string `json:"text,omitempty"`
	Against  int    `json:"against,omitempty"`
	All      bool   `json:"all,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Name     string `json:"name,omitempty"`
}

type remoteFileRead struct {
	Path, From, Code string
	Adds, Dels       int
	Note, Old, New   string
	Index            string
	Hunks            bool
}

type remoteChangesRead struct {
	Root, Branch, Base string
	Remote, Gh         bool
	Files              []remoteFileRead
	More               int
}

type remoteProjectReply struct {
	Changes  *remoteChangesRead `json:"changes,omitempty"`
	Text     string             `json:"text,omitempty"`
	Worktree *worktree.Worktree `json:"worktree,omitempty"`
	Info     *worktree.Info     `json:"info,omitempty"`
	Merged   *worktree.Merged   `json:"merged,omitempty"`
}

func changesWire(r changesRead) *remoteChangesRead {
	w := &remoteChangesRead{Root: r.root, Branch: r.branch, Base: r.base, Remote: r.remote, Gh: r.gh, More: r.more}
	for _, f := range r.snaps {
		w.Files = append(w.Files, remoteFileRead{Path: f.path, From: f.from, Code: f.code, Adds: f.adds, Dels: f.dels, Note: f.note, Old: f.old, New: f.new, Index: f.index, Hunks: f.hunks})
	}
	return w
}

func changesLocal(w *remoteChangesRead, name string, against int) changesRead {
	r := changesRead{against: against, root: remotePath(name, w.Root), branch: w.Branch, base: w.Base, remote: w.Remote, gh: w.Gh, more: w.More}
	for _, f := range w.Files {
		r.snaps = append(r.snaps, fileRead{path: f.Path, from: f.From, code: f.Code, adds: f.Adds, dels: f.Dels, note: f.Note, old: f.Old, new: f.New, index: f.Index, hunks: f.Hunks})
	}
	return r
}

func safeGitPath(root, path string) bool {
	return path != "" && !filepath.IsAbs(path) && !strings.ContainsRune(path, 0) && filesWithinRoot(root, filepath.Join(root, path))
}

func (h *host) projectRequest(req remoteProjectRequest) (remoteProjectReply, error) {
	var reply remoteProjectReply
	if !filepath.IsAbs(req.Dir) {
		return reply, errors.New("project directory must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if req.Op == "read" {
		r := readChanges(req.Dir, headCache{}, req.Against)
		if r.err != nil {
			return reply, r.err
		}
		reply.Changes = changesWire(r)
		return reply, nil
	}
	rootOut, err := git(ctx, req.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return reply, errors.New("directory is not a git repository")
	}
	root := strings.TrimSpace(string(rootOut))
	if strings.HasPrefix(req.Op, "worktree-") {
		switch req.Op {
		case "worktree-create":
			if req.Name == "" {
				req.Name = "session-" + newTabID()[:6]
			}
			repo, err := worktree.Repo(ctx, req.Dir)
			if err != nil {
				return reply, err
			}
			reply.Worktree, err = worktree.CreateNamed(ctx, filepath.Join(h.dataDir, "worktrees"), repo, req.Name)
			return reply, err
		case "worktree-info", "worktree-merge", "worktree-remove":
			w, err := worktree.Open(ctx, root, req.Worktree)
			if err != nil {
				return reply, err
			}
			reply.Worktree = w
			switch req.Op {
			case "worktree-info":
				list, err := worktree.List(ctx, root)
				if err != nil {
					return reply, err
				}
				for _, in := range list {
					if in.Branch == w.Branch {
						reply.Info = &in
						break
					}
				}
				return reply, nil
			case "worktree-merge":
				if strings.TrimSpace(req.Message) == "" {
					return reply, errors.New("merge needs a commit message")
				}
				merged, err := w.Merge(ctx, req.Message)
				reply.Merged = &merged
				return reply, err
			default:
				return reply, w.Remove(ctx)
			}
		default:
			return reply, fmt.Errorf("unknown project operation %q", req.Op)
		}
	}
	if req.Path != "" && !safeGitPath(root, req.Path) {
		return reply, errors.New("invalid repository path")
	}
	if req.From != "" && !safeGitPath(root, req.From) {
		return reply, errors.New("invalid original repository path")
	}
	switch req.Op {
	case "stage":
		if req.Path == "" {
			return reply, errors.New("stage needs a file")
		}
		paths := []string{req.Path}
		if req.From != "" {
			paths = append(paths, req.From)
		}
		_, err = gitDo(ctx, root, append([]string{"add", "-A", "--"}, paths...)...)
	case "unstage":
		if req.Path == "" {
			return reply, errors.New("unstage needs a file")
		}
		paths := []string{req.Path}
		if req.From != "" {
			paths = append(paths, req.From)
		}
		err = unstage(ctx, root, paths...)
	case "stage-all":
		_, err = gitDo(ctx, root, "add", "-A")
	case "unstage-all":
		err = unstage(ctx, root)
	case "discard":
		if req.Path == "" || len(req.Code) != 2 {
			return reply, errors.New("discard needs a file and status")
		}
		path := req.Path
		switch {
		case req.Code == "??":
			err = os.Remove(filepath.Join(root, path))
			return reply, err
		case req.Code[0] == 'A' || req.From != "":
			_, err = gitDo(ctx, root, "rm", "-q", "-f", "--", path)
			if err != nil || req.From == "" {
				return reply, err
			}
			path = req.From
		}
		_, err = gitDo(ctx, root, "restore", "--source=HEAD", "--staged", "--worktree", "--", path)
	case "commit":
		if strings.TrimSpace(req.Message) == "" {
			return reply, errors.New("commit message is empty")
		}
		if req.All {
			if _, err = gitDo(ctx, root, "add", "-A"); err != nil {
				return reply, err
			}
		}
		if _, err = gitDo(ctx, root, "commit", "-q", "-m", req.Message); err == nil {
			var short string
			short, _ = gitDo(ctx, root, "log", "-1", "--format=%h %s")
			reply.Text = "Committed " + short + "."
		}
	case "set-index":
		if req.Path == "" {
			return reply, errors.New("index update needs a file")
		}
		err = setIndex(ctx, root, req.Path, req.Was, req.Text)
	case "write-kept":
		if req.Path == "" {
			return reply, errors.New("file update needs a file")
		}
		err = writeKept(filepath.Join(root, req.Path), req.Was, req.Text)
	case "pr-view":
		if req.Message == "" {
			return reply, errors.New("pull request needs a branch")
		}
		reply.Text, err = ghDo(ctx, root, "pr", "view", req.Message, "--json", "url", "--jq", ".url")
	case "pr-create":
		if req.Message == "" {
			return reply, errors.New("pull request needs a branch")
		}
		if _, err = gitDo(ctx, root, "push", "--quiet", "-u", "origin", "HEAD"); err == nil {
			reply.Text, err = ghDo(ctx, root, "pr", "create", "--fill", "--head", req.Message, "--base", req.From)
		}
	default:
		return reply, fmt.Errorf("unknown project operation %q", req.Op)
	}
	return reply, err
}

type remoteProject struct {
	name string
	dial dialer
	err  error
}

func (a *App) projectSource(dir string) remoteProject {
	name, _, ok := splitRemote(dir)
	if !ok {
		return remoteProject{err: errors.New("not a remote project")}
	}
	srv := a.serverNamed(name)
	if srv == nil || a.dialOf(srv) == nil {
		return remoteProject{err: fmt.Errorf("not connected to %s", name)}
	}
	if !slicesContains(srv.features, "remote-git") {
		return remoteProject{err: fmt.Errorf("update larik on %s to review its changes", name)}
	}
	return remoteProject{name: name, dial: a.dialOf(srv)}
}

func (r remoteProject) call(req remoteProjectRequest) (remoteProjectReply, error) {
	if r.err != nil {
		return remoteProjectReply{}, r.err
	}
	name, dir, ok := splitRemote(req.Dir)
	if !ok || name != r.name {
		return remoteProjectReply{}, errors.New("project is on another machine")
	}
	req.Dir = dir
	if req.Worktree != "" {
		worktreeHost, path, ok := splitRemote(req.Worktree)
		if !ok || worktreeHost != r.name {
			return remoteProjectReply{}, errors.New("worktree is on another machine")
		}
		req.Worktree = path
	}
	conn, _, rep, err := askHostVia(r.dial, hostRequest{Op: "project", Project: &req})
	if err != nil {
		return remoteProjectReply{}, err
	}
	conn.Close()
	if rep.Project == nil {
		return remoteProjectReply{}, errors.New("remote host returned no project result")
	}
	if rep.Project.Worktree != nil {
		rep.Project.Worktree.Path = remotePath(r.name, rep.Project.Worktree.Path)
		rep.Project.Worktree.Repo = remotePath(r.name, rep.Project.Worktree.Repo)
	}
	if rep.Project.Info != nil {
		rep.Project.Info.Path = remotePath(r.name, rep.Project.Info.Path)
	}
	return *rep.Project, nil
}

func (a *App) remoteProjectCall(req remoteProjectRequest) (remoteProjectReply, error) {
	var source remoteProject
	if !a.onMain(func() { source = a.projectSource(req.Dir) }) {
		return remoteProjectReply{}, errors.New("Desktop is closing")
	}
	return source.call(req)
}

func (a *App) readRemoteChanges(dir string, against int) changesRead {
	reply, err := a.remoteProjectCall(remoteProjectRequest{Op: "read", Dir: dir, Against: against})
	if err != nil {
		return changesRead{against: against, err: err}
	}
	if reply.Changes == nil {
		return changesRead{against: against, err: errors.New("remote host returned no changes")}
	}
	name, _, _ := splitRemote(dir)
	return changesLocal(reply.Changes, name, against)
}
