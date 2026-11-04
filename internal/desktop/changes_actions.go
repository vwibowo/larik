package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// What the changes pane does with what it shows, as you would in a
// terminal: stage a file or unstage it, discard its change, commit, and
// push the branch to open a pull request with gh. The git runs off the
// main thread, as you: your hooks run, and your identity commits. Taking
// work away (discarding, pushing) asks first.

// actionTimeout bounds an action's git: a hook, or a push, can take a
// while.
const actionTimeout = 2 * time.Minute

// gitDo runs git in dir for an action, failing with what git said.
func gitDo(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	said := strings.TrimSpace(string(out))
	if err != nil {
		if said == "" {
			said = err.Error()
		}
		return said, fmt.Errorf("git %s: %s", args[0], said)
	}
	return said, nil
}

// changesDo runs an action on the pane's repository off the main thread,
// saying what's under way as busy, then says how it went in the notice
// bar, or done's words, and reads the repository again.
func (a *App) changesDo(ch *changes, busy string, do func(ctx context.Context, root string) (string, error)) {
	if ch.busy != "" || ch.root == "" {
		return
	}
	ch.busy = busy
	root := ch.root
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		said, err := do(ctx, root)
		a.update(func() {
			ch.busy = ""
			switch {
			case err != nil:
				a.notice = err.Error()
			case said != "":
				a.notice = said
			}
			ch.reread()
		})
	}()
}

func (a *App) changesRemoteDo(ch *changes, busy string, req remoteProjectRequest) {
	a.changesDo(ch, busy, func(_ context.Context, root string) (string, error) {
		req.Dir = root
		reply, err := a.remoteProjectCall(req)
		return reply.Text, err
	})
}

// stage stages the file's change, all of it, or unstages it.
func (a *App) stage(ch *changes, f *fileChange, on bool) {
	if isRemote(ch.root) {
		op := "unstage"
		if on {
			op = "stage"
		}
		a.changesRemoteDo(ch, "Staging…", remoteProjectRequest{Op: op, Path: f.path, From: f.from})
		return
	}
	paths := []string{f.path}
	if f.from != "" {
		paths = append(paths, f.from)
	}
	a.changesDo(ch, "Staging…", func(ctx context.Context, root string) (string, error) {
		if on {
			_, err := gitDo(ctx, root, append([]string{"add", "-A", "--"}, paths...)...)
			return "", err
		}
		return "", unstage(ctx, root, paths...)
	})
}

// unstage takes paths out of the index, or everything with none.
func unstage(ctx context.Context, root string, paths ...string) error {
	if _, err := gitDo(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		// Before the first commit there's no HEAD to reset to.
		args := []string{"rm", "-r", "-q", "--cached", "--"}
		if len(paths) == 0 {
			paths = []string{"."}
		}
		_, err := gitDo(ctx, root, append(args, paths...)...)
		return err
	}
	_, err := gitDo(ctx, root, append([]string{"reset", "-q", "--"}, paths...)...)
	return err
}

// stageAll stages every change, or unstages every one.
func (a *App) stageAll(ch *changes, on bool) {
	if isRemote(ch.root) {
		op := "unstage-all"
		if on {
			op = "stage-all"
		}
		a.changesRemoteDo(ch, "Staging…", remoteProjectRequest{Op: op})
		return
	}
	a.changesDo(ch, "Staging…", func(ctx context.Context, root string) (string, error) {
		if on {
			_, err := gitDo(ctx, root, "add", "-A")
			return "", err
		}
		return "", unstage(ctx, root)
	})
}

// askDiscard asks whether to throw the file's change away.
func (a *App) askDiscard(ch *changes, f *fileChange) {
	title, msg := fmt.Sprintf("Discard the changes to “%s”?", filepath.Base(f.path)), "It goes back to how it was at the last commit, staged changes and all. This can't be undone."
	switch {
	case f.code == "??":
		title, msg = fmt.Sprintf("Delete “%s”?", filepath.Base(f.path)), "It isn't in git, so discarding its change deletes it. This can't be undone."
	case f.code[0] == 'A':
		msg = "It's new since the last commit, so discarding its change deletes it. This can't be undone."
	}
	a.confirmAsk(title, msg, "Discard", func() { a.discard(ch, f) })
}

// discard throws the file's change away: back to the last commit, or
// gone for a file that's new since.
func (a *App) discard(ch *changes, f *fileChange) {
	if isRemote(ch.root) {
		a.changesRemoteDo(ch, "Discarding…", remoteProjectRequest{Op: "discard", Path: f.path, From: f.from, Code: f.code})
		return
	}
	path, from, code := f.path, f.from, f.code
	a.changesDo(ch, "Discarding…", func(ctx context.Context, root string) (string, error) {
		switch {
		case code == "??":
			return "", os.Remove(filepath.Join(root, path))
		case code[0] == 'A' || from != "":
			if _, err := gitDo(ctx, root, "rm", "-q", "-f", "--", path); err != nil {
				return "", err
			}
			if from == "" {
				return "", nil
			}
			path = from // a rename: the old name comes back
		}
		_, err := gitDo(ctx, root, "restore", "--source=HEAD", "--staged", "--worktree", "--", path)
		return "", err
	})
}

// commit commits what's staged, or with nothing staged every change,
// with the pane's message.
func (a *App) commit(ch *changes) {
	msg := strings.TrimSpace(ch.message)
	if msg == "" {
		return
	}
	all := ch.stagedCount() == 0
	if isRemote(ch.root) {
		a.changesDo(ch, "Committing…", func(_ context.Context, root string) (string, error) {
			reply, err := a.remoteProjectCall(remoteProjectRequest{Op: "commit", Dir: root, Message: msg, All: all})
			if err == nil {
				a.update(func() {
					if strings.TrimSpace(ch.message) == msg {
						ch.message = ""
					}
				})
			}
			return reply.Text, err
		})
		return
	}
	a.changesDo(ch, "Committing…", func(ctx context.Context, root string) (string, error) {
		if all {
			if _, err := gitDo(ctx, root, "add", "-A"); err != nil {
				return "", err
			}
		}
		if _, err := gitDo(ctx, root, "commit", "-q", "-m", msg); err != nil {
			return "", err
		}
		a.update(func() {
			if strings.TrimSpace(ch.message) == msg {
				ch.message = ""
			}
		})
		short, _ := gitDo(ctx, root, "log", "-1", "--format=%h %s")
		return "Committed " + short + ".", nil
	})
}

// stagedCount counts the files with some of their change staged.
func (ch *changes) stagedCount() int {
	n := 0
	for _, f := range ch.files {
		if some, _ := f.staged(); some {
			n++
		}
	}
	return n
}

// canPullRequest reports whether the pane can open a pull request for
// its branch: gh is installed, there's a remote, and a base to ask to
// merge into.
func (ch *changes) canPullRequest() bool {
	return ch.gh && ch.remote && ch.branch != "" && ch.base != ""
}

// askPullRequest opens the branch's pull request, or, asking first,
// pushes the branch and opens a new one in the browser, for gh to fill
// in from its commits.
func (a *App) askPullRequest(ch *changes) {
	branch, base := ch.branch, strings.TrimPrefix(ch.base, "origin/")
	a.changesDo(ch, "Looking for a pull request…", func(ctx context.Context, root string) (string, error) {
		var url string
		var err error
		if isRemote(root) {
			var reply remoteProjectReply
			reply, err = a.remoteProjectCall(remoteProjectRequest{Op: "pr-view", Dir: root, Message: branch})
			url = reply.Text
		} else {
			url, err = ghDo(ctx, root, "pr", "view", branch, "--json", "url", "--jq", ".url")
		}
		if err == nil && strings.HasPrefix(url, "http") {
			a.update(func() {
				if a.openExternal != nil {
					a.openExternal(url)
				}
			})
			return "", nil
		}
		a.update(func() {
			a.confirmAsk(fmt.Sprintf("Open a pull request for “%s”?", branch),
				fmt.Sprintf("%s is pushed to its remote, and GitHub opens in the browser to ask to merge it into %s. Only what's committed goes.", branch, base),
				"Push and Open", func() { a.pullRequest(ch, branch, base) })
		})
		return "", nil
	})
}

// pullRequest pushes the branch and opens a pull request for it into
// base in the browser.
func (a *App) pullRequest(ch *changes, branch, base string) {
	a.changesDo(ch, "Pushing…", func(ctx context.Context, root string) (string, error) {
		if isRemote(root) {
			reply, err := a.remoteProjectCall(remoteProjectRequest{Op: "pr-create", Dir: root, Message: branch, From: base})
			if err == nil && strings.HasPrefix(reply.Text, "http") {
				a.update(func() {
					if a.openExternal != nil {
						a.openExternal(reply.Text)
					}
				})
			}
			return "Pushed " + branch + ".", err
		}
		if _, err := gitDo(ctx, root, "push", "--quiet", "-u", "origin", "HEAD"); err != nil {
			return "", err
		}
		if _, err := ghDo(ctx, root, "pr", "create", "--web", "--head", branch, "--base", base); err != nil {
			return "", err
		}
		return "Pushed " + branch + ": finish the pull request in the browser.", nil
	})
}

// ghDo runs gh in dir, failing with what it said.
func ghDo(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	out, err := cmd.CombinedOutput()
	said := strings.TrimSpace(string(out))
	if err != nil {
		if said == "" {
			said = err.Error()
		}
		return said, fmt.Errorf("gh %s: %s", strings.Join(args[:min(2, len(args))], " "), said)
	}
	return said, nil
}
