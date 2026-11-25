package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	changespane "larik/internal/desktop/panes/changes"
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
// changesDo runs an action on the pane's repository off the main thread,
// saying what's under way as busy, then says how it went in the notice
// bar, or done's words, and reads the repository again.
func (a *App) changesDo(ch *changes, busy string, do func(ctx context.Context, root string) (string, error)) {
	if ch.Busy != "" || ch.Root == "" {
		return
	}
	ch.Busy = busy
	root := ch.Root
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		said, err := do(ctx, root)
		a.update(func() {
			ch.Busy = ""
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
	if isRemote(ch.Root) {
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
		return "", changespane.Stage(ctx, root, paths, on)
	})
}

// unstage takes paths out of the index, or everything with none.
func unstage(ctx context.Context, root string, paths ...string) error {
	return changespane.Unstage(ctx, root, paths...)
}

// stageAll stages every change, or unstages every one.
func (a *App) stageAll(ch *changes, on bool) {
	if isRemote(ch.Root) {
		op := "unstage-all"
		if on {
			op = "stage-all"
		}
		a.changesRemoteDo(ch, "Staging…", remoteProjectRequest{Op: op})
		return
	}
	a.changesDo(ch, "Staging…", func(ctx context.Context, root string) (string, error) {
		return "", changespane.StageAll(ctx, root, on)
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
	if isRemote(ch.Root) {
		a.changesRemoteDo(ch, "Discarding…", remoteProjectRequest{Op: "discard", Path: f.path, From: f.from, Code: f.code})
		return
	}
	path, from, code := f.path, f.from, f.code
	a.changesDo(ch, "Discarding…", func(ctx context.Context, root string) (string, error) {
		return "", changespane.Discard(ctx, root, path, from, code)
	})
}

// commit commits what's staged, or with nothing staged every change,
// with the pane's message.
func (a *App) commit(ch *changes) {
	msg := strings.TrimSpace(ch.Message)
	if msg == "" {
		return
	}
	all := ch.stagedCount() == 0
	if isRemote(ch.Root) {
		a.changesDo(ch, "Committing…", func(_ context.Context, root string) (string, error) {
			reply, err := a.remoteProjectCall(remoteProjectRequest{Op: "commit", Dir: root, Message: msg, All: all})
			if err == nil {
				a.update(func() {
					if strings.TrimSpace(ch.Message) == msg {
						ch.Message = ""
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
			if strings.TrimSpace(ch.Message) == msg {
				ch.Message = ""
			}
		})
		short, _ := gitDo(ctx, root, "log", "-1", "--format=%h %s")
		return "Committed " + short + ".", nil
	})
}

// stagedCount counts the files with some of their change staged.
func (ch *changes) stagedCount() int {
	n := 0
	for _, f := range ch.Files {
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
	return ch.GH && ch.Remote && ch.Branch != "" && ch.Base != ""
}

// askPullRequest opens the branch's pull request, or, asking first,
// pushes the branch and opens a new one in the browser, for gh to fill
// in from its commits.
func (a *App) askPullRequest(ch *changes) {
	branch, base := ch.Branch, strings.TrimPrefix(ch.Base, "origin/")
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

func gitDo(ctx context.Context, dir string, args ...string) (string, error) {
	return changespane.GitDo(ctx, dir, args...)
}
func ghDo(ctx context.Context, dir string, args ...string) (string, error) {
	return changespane.GhDo(ctx, dir, args...)
}
