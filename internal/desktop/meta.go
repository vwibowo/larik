package desktop

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What the sidebar says about a project and a session beyond their
// status: the branch checked out and how many files changed, and the
// ports the session's programs listen on, as a dev server it started.
// It is looked up off the main thread, every few seconds.

// gitMeta is a checkout's branch and its changed files.
type gitMeta struct {
	Branch string // the branch, or the commit when detached
	Dirty  int    // files changed, staged or not, untracked included
}

// metaEvery is how often the sidebar's metadata is looked up again.
const metaEvery = 3 * time.Second

// metaLoop keeps a.git and a.ports current until the window closes.
func (a *App) metaLoop() {
	tick := time.NewTicker(metaEvery)
	defer tick.Stop()
	for {
		dirs, pids := a.metaWanted()
		gits := map[string]gitMeta{}
		for _, dir := range dirs {
			if m, ok := readGit(dir); ok {
				gits[dir] = m
			}
		}
		ports := listening(pids)
		a.update(func() { a.git, a.ports = gits, ports })
		select {
		case <-a.done:
			return
		case <-tick.C:
		}
	}
}

// metaWanted is what to look up, asked of the main thread: the roots of
// the projects whose sections are open, and the directories of sessions
// running in one of them, or in one of larik's worktrees, which a
// worktree has a branch of its own in;
// and the processes of sessions running in this window's tabs, by
// transcript path.
func (a *App) metaWanted() (dirs []string, pids map[string]int) {
	got := make(chan struct{})
	a.update(func() {
		defer close(got)
		pids = map[string]int{}
		for _, p := range a.projects {
			if p.Missing || a.open[p.Root] == nil || !*a.open[p.Root] {
				continue
			}
			dirs = append(dirs, p.Root)
			for _, s := range p.Sessions {
				if (s.Running || a.inWorktree(s.Cwd)) && s.Cwd != p.Root && !slices.Contains(dirs, s.Cwd) {
					dirs = append(dirs, s.Cwd)
				}
			}
		}
		for _, t := range a.tabs {
			if s, _, ok := a.find(t.path); ok && s.PID > 0 {
				pids[s.Path] = s.PID
			}
		}
	})
	select {
	case <-got:
	case <-a.done:
	}
	return dirs, pids
}

// readGit reads dir's branch and changes, or reports it isn't a checkout.
func readGit(dir string) (gitMeta, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := git(ctx, dir, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return gitMeta{}, false
	}
	return parseGitStatus(out), true
}

// parseGitStatus reads git status --porcelain=v2 --branch.
func parseGitStatus(out []byte) gitMeta {
	var m gitMeta
	var oid string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "# branch.head "):
			m.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.oid "):
			oid = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "#"):
		default:
			m.Dirty++
		}
	}
	if m.Branch == "(detached)" && len(oid) >= 7 {
		m.Branch = oid[:7]
	}
	return m
}

// listening finds the TCP ports the processes in pids, and those they
// started, listen on, by transcript path. Only macOS and Linux have lsof
// to ask; elsewhere, and without lsof, it finds none.
func listening(pids map[string]int) map[string][]int {
	if len(pids) == 0 || runtime.GOOS == "windows" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ps, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,tty=").Output()
	if err != nil {
		return nil
	}
	owner := descendants(parsePS(ps), pids)
	if len(owner) == 0 {
		return nil
	}
	var list []string
	for pid := range owner {
		list = append(list, strconv.Itoa(pid))
	}
	slices.Sort(list)
	// lsof exits 1 when it finds nothing, with nothing to read.
	out, _ := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-iTCP", "-sTCP:LISTEN", "-p", strings.Join(list, ","), "-F", "pn").Output()
	return parseLsof(out, owner)
}

// proc is a process as ps lists it: its parent, and its terminal, or ""
// for none.
type proc struct {
	ppid int
	tty  string
}

// parsePS reads ps's pid, ppid and tty columns.
func parsePS(out []byte) map[int]proc {
	procs := map[int]proc{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		p := proc{ppid: ppid}
		if len(f) > 2 && f[2] != "??" && f[2] != "?" && f[2] != "-" {
			p.tty = f[2]
		}
		procs[pid] = p
	}
	return procs
}

// descendants maps each of pids, and every process that belongs to one,
// to the transcript path of its session: those under it, and those on
// its terminal, as a server started with & whose shell has exited, which
// leaves it to launchd or init.
func descendants(procs map[int]proc, pids map[string]int) map[int]string {
	owner := map[int]string{}
	ttys := map[string]string{}
	for path, pid := range pids {
		owner[pid] = path
		if tty := procs[pid].tty; tty != "" {
			ttys[tty] = path
		}
	}
	for pid, p := range procs {
		if path, ok := ttys[p.tty]; ok && p.tty != "" {
			owner[pid] = path
			continue
		}
		// Up the tree to a session's process, if there is one.
		for up, n := p.ppid, 0; up > 1 && n < 64; up, n = procs[up].ppid, n+1 {
			if path, ok := owner[up]; ok && pids[path] == up {
				owner[pid] = path
				break
			}
		}
	}
	return owner
}

// parseLsof reads lsof -F pn: a "p<pid>" line, then an "n<address>" line
// per socket, as "*:8000", "127.0.0.1:8000" or "[::1]:8000".
func parseLsof(out []byte, owner map[int]string) map[string][]int {
	ports := map[string][]int{}
	var path string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ := strconv.Atoi(line[1:])
			path = owner[pid]
		case strings.HasPrefix(line, "n") && path != "":
			i := strings.LastIndexByte(line, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(line[i+1:])
			if err == nil && !slices.Contains(ports[path], port) {
				ports[path] = append(ports[path], port)
			}
		}
	}
	for _, list := range ports {
		slices.Sort(list)
	}
	return ports
}
