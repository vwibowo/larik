package desktop

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"larik/internal/session"
	"larik/internal/status"
)

// With no window open, the commands that need none go to the host: list
// what it runs, read what a tab shows, type into one, close one, and wait
// for a tab's session. A tab is found by its id, or its session's, as
// the window finds them, the session by the tab id its status file
// names. HostTabs and StopHost are larik desktop host's.

// noWindowCommands are the commands the host answers with no window open.
var noWindowCommands = []string{"list", "read", "send", "close", "wait", "status"}

// callHost answers req from the host, as the window would.
func callHost(dataDir string, req Request) (Response, error) {
	if !slices.Contains(noWindowCommands, req.Cmd) {
		return Response{}, fmt.Errorf("larik desktop's window isn't open, and %s needs it; what it left running is in larik desktop list", req.Cmd)
	}
	if req.Cmd == "wait" {
		return hostWait(dataDir, req)
	}
	tabs, err := hostTabList(dataDir)
	if err != nil {
		return Response{}, err
	}
	if req.Cmd == "list" {
		return Response{Tabs: tabs}, nil
	}
	t, msg := hostTabRef(tabs, req.Tab)
	if msg != "" {
		return Response{Error: msg}, errors.New(msg)
	}
	fail := func(err error) (Response, error) { return Response{Tab: &t, Error: err.Error()}, err }
	switch req.Cmd {
	case "read":
		conn, _, rep, err := askHost(dataDir, hostRequest{Op: "read", ID: t.ID})
		if err != nil {
			return fail(err)
		}
		conn.Close()
		return Response{Tab: &t, Text: lastLines(rep.Text, req.Lines)}, nil
	case "send":
		conn, _, _, err := askHost(dataDir, hostRequest{Op: "send", ID: t.ID, Text: req.Text, Enter: req.Enter})
		if err != nil {
			return fail(err)
		}
		conn.Close()
	case "status":
		if t.Kind != "terminal" || t.Exited {
			return fail(errors.New("status needs a running terminal tab"))
		}
		payload, err := statusPayload(req)
		if err != nil {
			return fail(err)
		}
		conn, _, _, err := askHost(dataDir, hostRequest{Op: "status", ID: t.ID, Status: payload})
		if err != nil {
			return fail(err)
		}
		conn.Close()
		return Response{State: req.State}, nil
	case "close":
		if !req.Force && t.State == string(status.Working) {
			return fail(errors.New("the tab is working: close it with --force to stop it mid-turn"))
		}
		if err := closeHostTab(dataDir, t.ID); err != nil {
			return fail(err)
		}
	}
	return Response{Tab: &t}, nil
}

// HostTab is a tab the host runs, as commands see tabs, with when it
// started and how many windows show it.
type HostTab struct {
	TabInfo
	Started time.Time `json:"started"`
	Windows int       `json:"windows"`
}

// hostTabList is HostTabs as commands see tabs.
func hostTabList(dataDir string) ([]TabInfo, error) {
	tabs, err := HostTabs(dataDir)
	out := make([]TabInfo, len(tabs))
	for i, t := range tabs {
		out[i] = t.TabInfo
	}
	return out, err
}

// HostTabs are the tabs the host for dataDir runs, in the order they
// started, each with the session its larik has open.
func HostTabs(dataDir string) ([]HostTab, error) {
	tabs, err := hostTabs(dataDir)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(tabs, func(x, y hostTabInfo) int { return x.Started.Compare(y.Started) })
	// Each tab's session: the newest naming it, as a window's tab follows
	// its larik.
	sessions := map[string]session.Indexed{}
	projects, _ := session.Index(dataDir, session.IndexOptions{})
	for _, p := range projects {
		for _, s := range p.Sessions {
			if have, ok := sessions[s.Tab]; s.Tab != "" && (!ok || s.Modified.After(have.Modified)) {
				sessions[s.Tab] = s
			}
		}
	}
	out := make([]HostTab, 0, len(tabs))
	for _, h := range tabs {
		info := TabInfo{ID: h.ID, Kind: "larik", Run: h.Run, Dir: h.Dir, Title: h.Title, State: string(status.Idle), Exited: h.Exited, Code: h.Code}
		if h.Shell {
			info.Kind, info.Title = "terminal", cmp.Or(h.Title, h.Run, "shell")
			if h.Status.State != "" {
				info.State, info.Wants, info.Msg = string(h.Status.State), string(h.Status.Kind), h.Status.Msg
				info.Title = cmp.Or(h.Status.Title, info.Title)
			}
		}
		if s, ok := sessions[h.ID]; ok && !h.Shell {
			info.Session, info.Path = s.ID, s.Path
			info.State, info.Wants, info.Msg = string(s.Status.State), string(s.Status.Kind), s.Status.Msg
			info.Model, info.Mode, info.CostUSD, info.Tokens = cmp.Or(s.Usage.Model, s.Model), s.Usage.Mode, s.Usage.CostUSD, s.Usage.Tokens
			info.Title = cmp.Or(s.Title, info.Title)
		}
		if h.Exited && h.Code != 0 {
			info.State = string(status.Error)
		}
		out = append(out, HostTab{TabInfo: info, Started: h.Started, Windows: h.Windows})
	}
	return out, nil
}

// hostTabRef finds the tab ref names among tabs, as tabRef among the
// window's, or says why not.
func hostTabRef(tabs []TabInfo, ref string) (TabInfo, string) {
	switch ref {
	case "":
		return TabInfo{}, "which tab? Give its id, or its session's, with --tab"
	case "focused":
		return TabInfo{}, "no tab is focused: larik desktop's window isn't open"
	}
	var found []TabInfo
	for _, t := range tabs {
		if t.ID == ref {
			return t, ""
		}
		if t.Session != "" && strings.HasPrefix(t.Session, ref) {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 0:
		return TabInfo{}, fmt.Sprintf("no tab %q is running", ref)
	case 1:
		return found[0], ""
	}
	return TabInfo{}, fmt.Sprintf("%q is the start of %d tabs' sessions: give more of it", ref, len(found))
}

// hostWait waits as the window's wait does, for a tab the host runs, or
// a session by id.
func hostWait(dataDir string, req Request) (Response, error) {
	want, msg := wantStates(req.State)
	if msg != "" {
		return Response{Error: msg}, errors.New(msg)
	}
	start := time.Now()
	var deadline <-chan time.Time
	if req.Timeout > 0 {
		timer := time.NewTimer(req.Timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	failed := func(msg string) (Response, error) { return Response{Error: msg}, errors.New(msg) }
	path := ""
	if req.Tab == "" {
		projects, _ := session.Index(dataDir, session.IndexOptions{})
		var found []session.Indexed
		for _, p := range projects {
			for _, s := range p.Sessions {
				if strings.HasPrefix(s.ID, req.Session) {
					found = append(found, s)
				}
			}
		}
		if len(found) != 1 {
			return failed(fmt.Sprintf("no one session %q", req.Session))
		}
		path = found[0].Path
	}
	poll := time.NewTicker(400 * time.Millisecond)
	defer poll.Stop()
	for {
		var info TabInfo
		if req.Tab != "" {
			tabs, err := hostTabList(dataDir)
			if err != nil {
				return failed(err.Error())
			}
			t, msg := hostTabRef(tabs, req.Tab)
			switch {
			case msg != "" && path == "":
				return failed(msg)
			case msg == "":
				info = t
				path = cmp.Or(t.Path, path)
			}
			if info.Exited && info.Code != 0 {
				if want[status.Error] {
					return Response{Tab: &info, State: string(status.Error)}, nil
				}
				return failed(fmt.Sprintf("larik exited with status %d", info.Code))
			}
		}
		if path != "" {
			if cur, ok := reached(path, want, start); ok {
				if info.ID == "" {
					return Response{State: string(cur.State)}, nil
				}
				info.State, info.Wants, info.Msg = string(cur.State), string(cur.Kind), cur.Msg
				return Response{Tab: &info, State: string(cur.State)}, nil
			}
		}
		select {
		case <-poll.C:
		case <-deadline:
			return failed(fmt.Sprintf("timed out after %s waiting for %s", req.Timeout, cmp.Or(req.State, string(status.Done))))
		}
	}
}

// ErrNoHost says no host is running for the data directory.
var ErrNoHost = errors.New("nothing is running in the background: larik desktop's host isn't running")

// HostPID is the host for dataDir's process, or ErrNoHost.
func HostPID(dataDir string) (int, error) {
	conn, _, rep, err := askHost(dataDir, hostRequest{Op: "ping"})
	if err != nil {
		return 0, ErrNoHost
	}
	conn.Close()
	return rep.PID, nil
}

// StopHost ends the host for dataDir and every program it runs: by
// asking it, or, when it doesn't answer, by its pid, with SIGTERM.
func StopHost(dataDir string) error {
	conn, _, _, err := askHost(dataDir, hostRequest{Op: "stop"})
	if err == nil {
		conn.Close()
	} else {
		data, rerr := os.ReadFile(hostSocketPath(dataDir) + ".pid")
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if rerr != nil || pid <= 0 {
			return ErrNoHost
		}
		p, err := os.FindProcess(pid)
		if err == nil {
			err = p.Signal(syscall.SIGTERM)
		}
		if err != nil {
			return fmt.Errorf("the host (pid %d) doesn't answer, and can't be told to stop: %v", pid, err)
		}
	}
	// It's gone once its socket is.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(hostSocketPath(dataDir)); err != nil {
			return nil
		}
	}
	return errors.New("the host didn't stop within 5 seconds: " + filepath.Base(hostSocketPath(dataDir)) + " is still there")
}
