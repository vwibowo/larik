package desktop

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

// Remote windows: the window shows the sessions and tabs of larik on
// other machines, servers, beside its own, over SSH. Connecting runs
// `larik desktop host ensure` there, which starts the server's host and
// prints its socket, and keeps an SSH connection forwarding that socket
// to one here; the host's protocol, a connection per tab, then goes over
// it as it goes over the local socket. The server's index comes from
// the host (op index), and its tabs run in its host, never here.
//
// In the window a server's paths are marked, ssh://name/path, so they
// never meet this machine's: the window looks nothing up on this disk
// for them, and takes the mark off for the server.

// remoteScheme marks a server's paths.
const remoteScheme = "ssh://"

// remotePath is path on the server name, as the window knows it.
func remotePath(name, path string) string { return remoteScheme + name + path }

// splitRemote returns the server a path is on, and the path there; ok
// is false for this machine's.
func splitRemote(p string) (name, path string, ok bool) {
	s, ok := strings.CutPrefix(p, remoteScheme)
	if !ok {
		return "", p, false
	}
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return s, "", true // the host's own folder: the home folder there
	}
	return s[:i], s[i:], true
}

// isRemote reports whether path is on a server.
func isRemote(path string) bool { return strings.HasPrefix(path, remoteScheme) }

// server is a machine whose larik the window shows.
type server struct {
	name string // the SSH destination, as ~/.ssh/config names it
	// dial reaches its host, bin is larik there, and stop ends the
	// connection: all set once connected; dial is read off the main
	// thread, under App.serversMu.
	dial dialer
	bin  string
	stop func()
	// projects are its sessions, their paths marked; err is why it isn't
	// connected, and connecting that it's on the way.
	projects     []session.Project
	err          string
	connecting   bool
	features     []string
	worktreeRoot string
	// epoch invalidates monitors from earlier connections.
	epoch, retryAttempt uint64
	retryPending        bool
}

// connector connects to the server name: over SSH, or in tests to a host
// here standing in for one. It returns how to reach the server's host,
// larik's path there, and how to end the connection.
type connector func(name string) (dial dialer, bin string, stop func(), health <-chan error, err error)

// remoteSetupError needs a change to SSH or larik on the server before
// another attempt can succeed. Connection loss and transport errors are
// left as ordinary errors and retried.
type remoteSetupError struct{ message string }

func (e *remoteSetupError) Error() string { return e.message }

func retryRemoteError(err error) bool {
	var setup *remoteSetupError
	return !errors.As(err, &setup)
}

func retryDelay(attempt uint64) time.Duration {
	shift := min(attempt, 5)
	return min(3*time.Second<<shift, time.Minute)
}

func (a *App) retryServer(srv *server) {
	if srv.retryPending || !slices.Contains(a.servers, srv) {
		return
	}
	delay := retryDelay(srv.retryAttempt)
	srv.retryAttempt++
	srv.retryPending = true
	epoch := srv.epoch
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-a.done:
			return
		case <-timer.C:
			a.update(func() {
				if srv.epoch != epoch || !srv.retryPending {
					return // a manual connection replaced this retry
				}
				srv.retryPending = false
				if slices.Contains(a.servers, srv) && !srv.connecting && a.dialOf(srv) == nil {
					a.connectServerAttempt(srv.name, true)
				}
			})
		}
	}()
}

// serverNamed is the server called name, or nil.
func (a *App) serverNamed(name string) *server {
	for _, s := range a.servers {
		if s.name == name {
			return s
		}
	}
	return nil
}

// connectServer connects to the server name, adding it, or again when
// it isn't connected; its sessions show once its index comes.
func (a *App) connectServer(name string) {
	a.connectServerAttempt(name, false)
}

func (a *App) connectServerAttempt(name string, automatic bool) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/ \t") {
		a.notice = fmt.Sprintf("%q isn't a server: give a host from ~/.ssh/config, or user@host", name)
		return
	}
	srv := a.serverNamed(name)
	if srv == nil {
		srv = &server{name: name}
		a.servers = append(a.servers, srv)
		a.settingsChanged()
	}
	if srv.connecting || a.dialOf(srv) != nil {
		return
	}
	if !automatic {
		srv.retryAttempt = 0
	}
	srv.retryPending = false
	srv.connecting, srv.err = true, ""
	srv.epoch++
	epoch := srv.epoch
	connect := a.connect
	if connect == nil {
		connect = sshConnect
	}
	go func() {
		dial, bin, stop, health, err := connect(name)
		var features []string
		var worktreeRoot string
		if err == nil {
			features, worktreeRoot, err = probeHost(dial, name)
			if err != nil {
				stop()
			}
		}
		a.update(func() {
			if srv.epoch != epoch {
				if err == nil {
					go stop()
				}
				return
			}
			srv.connecting = false
			if err != nil {
				srv.err = err.Error()
				if retryRemoteError(err) {
					a.retryServer(srv)
				}
				return
			}
			if !slices.Contains(a.servers, srv) { // disconnected meanwhile
				stop()
				return
			}
			a.serversMu.Lock()
			srv.dial, srv.bin, srv.stop = dial, bin, stop
			a.serversMu.Unlock()
			srv.features, srv.worktreeRoot, srv.retryAttempt = features, worktreeRoot, 0
			a.reconnectRemoteTabs(srv)
			if a.dialOf(srv) == nil {
				return
			}
			a.adoptRemote(srv)
			a.reconnectRemotePorts(srv)
			a.refreshIndex()
			if health != nil {
				go func() {
					select {
					case <-a.done:
						return
					case failure, ok := <-health:
						if !ok || failure == nil {
							failure = errors.New("SSH forward ended")
						}
						a.update(func() {
							if srv.epoch == epoch && a.dialOf(srv) != nil {
								a.lostServer(srv, failure)
							}
						})
					}
				}()
			}
		})
	}()
}

// checkProto asks the server's host what it speaks, saying when it's
// older than this window.
func checkProto(dial dialer, name string) error {
	_, _, err := probeHost(dial, name)
	return err
}

func probeHost(dial dialer, name string) ([]string, string, error) {
	conn, _, rep, err := askHostVia(dial, hostRequest{Op: "ping"})
	if err != nil {
		return nil, "", fmt.Errorf("can't reach larik on %s: %w", name, err)
	}
	conn.Close()
	if rep.Proto < hostProto {
		return nil, "", &remoteSetupError{fmt.Sprintf("larik on %s is older than this one: update it there (larik update)", name)}
	}
	return rep.Features, rep.WorktreeRoot, nil
}

// dialOf is how to reach srv's host, nil while it isn't connected.
func (a *App) dialOf(srv *server) dialer {
	a.serversMu.Lock()
	defer a.serversMu.Unlock()
	return srv.dial
}

// disconnectServer ends the connection to the server name and forgets
// it. Its tabs close here and their programs go on there, for the next
// connection to show again.
func (a *App) disconnectServer(name string) {
	srv := a.serverNamed(name)
	if srv == nil {
		return
	}
	srv.epoch++
	a.dropRemoteForwards(name)
	for _, t := range slices.Clone(a.tabs) {
		if n, _, ok := splitRemote(t.dir); ok && n == name {
			a.forgetTab(t)
		}
	}
	a.serversMu.Lock()
	stop := srv.stop
	srv.dial, srv.stop = nil, nil
	a.serversMu.Unlock()
	if stop != nil {
		stop()
	}
	a.servers = slices.DeleteFunc(a.servers, func(s *server) bool { return s == srv })
	a.setProjects(a.withRemote(a.localProjects()))
	a.settingsChanged()
}

// forgetTab takes t out of the window without ending its program, which
// a host runs.
func (a *App) forgetTab(t *tab) {
	a.unshow(t, 0)
	a.tabs = slices.DeleteFunc(a.tabs, func(x *tab) bool { return x == t })
	t.close()
	a.releaseRemoteForward(t.remotePort)
}

// adoptRemote shows the tabs srv's host runs that the window has none
// for, as the tabs a window there left, or this one did when it closed.
func (a *App) adoptRemote(srv *server) {
	conn, _, rep, err := askHostVia(srv.dial, hostRequest{Op: "list"})
	if err != nil {
		srv.err = err.Error()
		return
	}
	conn.Close()
	slices.SortFunc(rep.Tabs, func(x, y hostTabInfo) int { return x.Started.Compare(y.Started) })
	for _, h := range rep.Tabs {
		if h.Exited || h.Detached || a.tabByID(h.ID) != nil {
			continue
		}
		h.Dir = remotePath(srv.name, h.Dir)
		if t := a.attachTab(h, ""); t != nil {
			a.activate(t)
		}
	}
	a.followSessions()
}

// reconnectRemoteTabs reuses the visible tab and pane when its host still
// has the program. A missing program stays visible as failed.
func (a *App) reconnectRemoteTabs(srv *server) {
	conn, _, rep, err := askHostVia(srv.dial, hostRequest{Op: "list"})
	if err != nil {
		a.lostServer(srv, err)
		return
	}
	conn.Close()
	live := map[string]hostTabInfo{}
	for _, h := range rep.Tabs {
		live[h.ID] = h
	}
	for _, t := range a.tabs {
		name, _, ok := splitRemote(t.dir)
		if !ok || name != srv.name || !t.disconnected {
			continue
		}
		h, found := live[t.id]
		if !found || h.Exited {
			t.disconnected, t.exited = false, true
			if found {
				t.code = h.Code
				t.agent = agentForTab(t, h.Agent)
			}
			t.agentOffered = t.agent.valid()
			continue
		}
		t.agent = agentForTab(t, h.Agent)
		t.reattach = true
		old := t.term
		if a.run(t, "") {
			t.disconnected, t.exited, t.code = false, false, 0
			if old != nil {
				old.Close()
			}
		}
	}
}

// openRemote connects t to its program in the host of the server name,
// in dir there: larik there rather than here, or the shell it has.
func (a *App) openRemote(t *tab, name, dir string, args []string) (*hostConn, error) {
	srv := a.serverNamed(name)
	dial := dialer(nil)
	if srv != nil {
		dial = a.dialOf(srv)
	}
	if dial == nil {
		return nil, fmt.Errorf("not connected to %s: File › Connect to Server… connects", name)
	}
	if t.shell() {
		args = remoteShellCommand(t.run)
	} else {
		args = append([]string{srv.bin}, args[1:]...)
	}
	req := hostRequest{Op: "open", ID: t.id, Command: args, Dir: dir, Env: []string{status.TabEnv + "=" + t.id}, Shell: t.shell(), Run: t.run}
	if t.reattach {
		req.Op = "attach"
	}
	conn, r, _, err := askHostVia(dial, req)
	if err != nil {
		return nil, fmt.Errorf("can't open the tab on %s: %w", name, err)
	}
	return newHostConn(conn, r), nil
}

// lostServer marks srv as no longer connected: its host didn't answer.
func (a *App) lostServer(srv *server, err error) {
	if a.dialOf(srv) == nil {
		return
	}
	srv.epoch++
	a.serversMu.Lock()
	stop := srv.stop
	srv.dial, srv.stop = nil, nil
	a.serversMu.Unlock()
	if stop != nil {
		go stop()
	}
	srv.err = "lost the connection: " + err.Error()
	a.dropRemoteForwards(srv.name)
	for _, t := range a.tabs {
		if name, _, ok := splitRemote(t.dir); ok && name == srv.name && (!t.exited || t.code == -1) {
			t.disconnected, t.exited, t.code = true, true, -1
		}
	}
	a.retryServer(srv)
}

// askServer shows the sidebar's field for a server to connect to.
func (a *App) askServer() {
	a.noSidebar, a.askingServer, a.focusServer, a.serverName = false, true, true, ""
}

// serverField is where Connect to Server… takes a server: a host from
// ~/.ssh/config, which it offers, or user@host. Enter connects, and
// Escape puts it away.
func (a *App) serverField(c *ui.Context, t *ui.Theme) {
	if !a.askingServer {
		return
	}
	done := func(connect bool) {
		if connect {
			a.connectServer(a.serverName)
		}
		a.askingServer, a.serverName = false, ""
	}
	ui.Row(c.Key("server-field")).Gap(6).Padding(4, 4).AlignItems(ui.Center).Children(func() {
		field := ui.Autocomplete(c.Key("server"), &a.serverName, sshHosts()).Placeholder("Server, as ~/.ssh/config names it").
			Label("Server to connect to").Grow(1).
			OnSubmit(func() { done(true) }).
			OnShortcut(0, ui.KeyEscape, func() { done(false) })
		if a.focusServer {
			field.Focus()
			a.focusServer = false
		}
		if ui.Button(c.Key("connect"), "Connect").Disabled(strings.TrimSpace(a.serverName) == "").Clicked() {
			done(true)
		}
	})
}

// sshHosts are the hosts ~/.ssh/config names, but patterns.
func sshHosts() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	return parseSSHHosts(string(data))
}

func parseSSHHosts(config string) []string {
	var out []string
	for _, line := range strings.Split(config, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.EqualFold(f[0], "Host") {
			continue
		}
		for _, h := range f[1:] {
			if !strings.ContainsAny(h, "*?!") && !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

// serverSection is a server's part of the sidebar: its name and how
// the connection is, and its projects; its menu starts a session or a
// terminal there, or disconnects.
func (a *App) serverSection(c *ui.Context, t *ui.Theme, srv *server) {
	connected := a.dialOf(srv) != nil
	head := machineHeading(c, t, "server:"+srv.name, iconServer, srv.name, func() {
		switch {
		case srv.connecting:
			ui.Spinner(c).Size(10, 10).Label("Connecting")
		case !connected:
			if ui.Button(c.Key("reconnect"), "Reconnect").Clicked() {
				a.connectServer(srv.name)
			}
		}
	})
	if srv.err != "" {
		head.Tooltip(srv.err)
	}
	head.ContextMenu(func(m *ui.Menu) {
		home := remotePath(srv.name, "")
		if m.Item("New Session on " + srv.name).Chosen() {
			a.newTab(home, "", "")
		}
		if m.Item("New Terminal on " + srv.name).Chosen() {
			if tb := a.startShell(home, ""); tb != nil {
				a.activate(tb)
			}
		}
		m.Separator()
		if !connected && !srv.connecting && m.Item("Reconnect").Chosen() {
			a.connectServer(srv.name)
		}
		if m.Item("Disconnect").Chosen() {
			a.disconnectServer(srv.name)
		}
	})
	if srv.err != "" && !srv.connecting {
		message := srv.err
		if srv.retryPending {
			message += " · retrying"
		}
		ui.Text(c.Key("server-err:"+srv.name), message).FontSize(11).TextColor(t.Danger).Padding(0, 8, 4, 8)
	}
	if connected && len(srv.projects) == 0 {
		ui.Text(c.Key("server-empty:"+srv.name), "No sessions there yet: right-click "+srv.name+" to start one.").FontSize(11).TextColor(t.TextMuted).Padding(0, 8, 4, 8)
	}
	for _, p := range srv.projects {
		a.project(c, t, p)
	}
}

// setIndex shows this machine's projects, local, and the servers' as
// remoteIndexes read them.
func (a *App) setIndex(local []session.Project, remote map[*server][]session.Project, failed map[*server]error) {
	for srv, ps := range remote {
		srv.projects, srv.err = ps, ""
	}
	for srv, err := range failed {
		a.lostServer(srv, err)
	}
	a.setProjects(a.withRemote(local))
}

// machineHeading names a machine above its projects in the sidebar,
// with what more follows it on the line.
func machineHeading(c *ui.Context, t *ui.Theme, key string, icon *ui.SVG, name string, more ...func()) ui.Element {
	return ui.Row(c.Key(key)).Gap(6).Padding(10, 8, 2, 8).AlignItems(ui.Center).Label(name).Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, name).FontSize(11).Bold().TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		for _, fn := range more {
			fn()
		}
	})
}

// thisMachine is what the sidebar calls this machine beside servers.
func thisMachine() string {
	if runtime.GOOS == "darwin" {
		return "This Mac"
	}
	return "This Computer"
}

// localProjects are this machine's projects, of those the window shows.
func (a *App) localProjects() []session.Project {
	return slices.DeleteFunc(slices.Clone(a.projects), func(p session.Project) bool { return isRemote(p.Root) })
}

// withRemote is local with every server's projects after.
func (a *App) withRemote(local []session.Project) []session.Project {
	out := local
	for _, srv := range a.servers {
		out = append(out, srv.projects...)
	}
	return out
}

// remoteIndexes reads each connected server's index, off the main
// thread, and returns them, and why each it couldn't read failed.
func (a *App) remoteIndexes() (map[*server][]session.Project, map[*server]error) {
	a.serversMu.Lock()
	dials := map[*server]dialer{}
	for _, srv := range a.servers {
		if srv.dial != nil {
			dials[srv] = srv.dial
		}
	}
	a.serversMu.Unlock()
	got, failed := map[*server][]session.Project{}, map[*server]error{}
	for srv, dial := range dials {
		conn, _, rep, err := askHostVia(dial, hostRequest{Op: "index"})
		if err != nil {
			failed[srv] = err
			continue
		}
		conn.Close()
		got[srv] = markRemote(srv.name, rep.Projects)
	}
	return got, failed
}

// markRemote marks the paths of a server's projects as the window knows
// them.
func markRemote(name string, ps []session.Project) []session.Project {
	mark := func(ss []session.Indexed) {
		for i := range ss {
			ss[i].Path, ss[i].Cwd = remotePath(name, ss[i].Path), remotePath(name, ss[i].Cwd)
		}
	}
	for i := range ps {
		ps[i].Root = remotePath(name, ps[i].Root)
		ps[i].Missing = false // as the server sees it, not this disk
		mark(ps[i].Sessions)
		mark(ps[i].Archived)
	}
	return ps
}

// remoteShellCommand is a terminal tab's program on a server: the shell
// it has there, which this machine can't know, or run through it.
func remoteShellCommand(run string) []string {
	if run == "" {
		return []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -l`}
	}
	return []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -ilc "$1"`, "sh", run}
}

// sshEnsureArgs are the arguments of the ssh that starts the server's
// host and asks where it is.
func sshEnsureArgs(name string) []string {
	return []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", name, remoteLarik() + " desktop host ensure"}
}

// remoteLarik is the command that runs larik on a server: larik, from
// the PATH of a non-interactive shell there, or LARIK_DESKTOP_REMOTE, a
// path or a command line, as "~/bin/larik" or "XDG_DATA_HOME=/tmp/l larik".
func remoteLarik() string {
	if c := strings.TrimSpace(os.Getenv("LARIK_DESKTOP_REMOTE")); c != "" {
		return c
	}
	return "larik"
}

// sshForwardArgs are the arguments of the ssh that forwards the server's
// host's socket, remote, to local, for as long as it runs.
func sshForwardArgs(name, local, remote string) []string {
	return []string{
		"-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes", "-o", "ServerAliveInterval=15",
		"-L", local + ":" + remote, name,
	}
}

// sshSocketPath is where this machine reaches the server name's host.
func sshSocketPath(name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(os.TempDir(), "larik-desktop-ssh-"+hex.EncodeToString(sum[:6])+".sock")
}

// parseEnsure reads what larik desktop host ensure prints: the host's
// socket, then larik's path, there.
func parseEnsure(out string) (socket, bin string, err error) {
	sc := bufio.NewScanner(strings.NewReader(out))
	var lines []string
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return "", "", errors.New("larik desktop host ensure said " + strings.TrimSpace(out))
	}
	bin = "larik"
	if len(lines) > 1 {
		bin = lines[1]
	}
	return lines[0], bin, nil
}

// sshConnect connects to the server name over SSH, with the keys and
// agent ~/.ssh/config gives it: it can't ask for a password.
func sshConnect(name string) (dialer, string, func(), <-chan error, error) {
	out, err := exec.Command("ssh", sshEnsureArgs(name)...).CombinedOutput()
	remote, bin, perr := parseEnsure(string(out))
	if err != nil || perr != nil {
		return nil, "", nil, nil, ensureError(name, string(out), err)
	}
	local := sshSocketPath(name)
	_ = os.Remove(local)
	cmd := exec.Command("ssh", sshForwardArgs(name, local, remote)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, "", nil, nil, err
	}
	exited := make(chan struct{})
	var runErr error
	go func() { runErr = cmd.Wait(); close(exited) }()
	health := make(chan error, 1)
	go func() {
		<-exited
		health <- runErr
		close(health)
	}()
	stop := func() {
		_ = cmd.Process.Kill()
		<-exited
		_ = os.Remove(local)
	}
	dial := dialer(func() (net.Conn, error) { return net.DialTimeout("unix", local, 5*time.Second) })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		select {
		case <-exited:
			_ = os.Remove(local)
			return nil, "", nil, nil, fmt.Errorf("ssh %s ended: %s", name, firstNonEmpty(strings.TrimSpace(stderr.String()), "the forward failed"))
		default:
		}
		if conn, err := dial(); err == nil {
			conn.Close()
			return dial, bin, stop, health, nil
		}
		if time.Now().After(deadline) {
			stop()
			return nil, "", nil, nil, fmt.Errorf("ssh %s: the host's socket didn't come through", name)
		}
	}
}

// ensureError says why larik desktop host ensure, run on the server
// name, didn't say where its host is: SSH failed, larik isn't on the
// PATH there, or it's too old to know the command, and takes the words
// for a prompt.
func ensureError(name, out string, err error) error {
	msg := strings.TrimSpace(out)
	switch {
	case strings.Contains(msg, "command not found") || strings.Contains(msg, ": not found"):
		return &remoteSetupError{fmt.Sprintf("larik isn't on the PATH of a non-interactive shell on %s: install it, or add its folder to PATH in ~/.zshenv or ~/.bashrc there", name)}
	case strings.Contains(msg, "Permission denied") || strings.Contains(msg, "publickey"):
		return &remoteSetupError{fmt.Sprintf("SSH authentication failed for %s: check its key, agent and ~/.ssh/config entry", name)}
	case strings.Contains(msg, "Could not resolve hostname"):
		return &remoteSetupError{fmt.Sprintf("SSH can't find %s: check the server name in ~/.ssh/config", name)}
	case err != nil && isSSHFailure(err):
		return fmt.Errorf("ssh %s: %s", name, firstNonEmpty(msg, err.Error()))
	}
	return &remoteSetupError{fmt.Sprintf("larik on %s couldn't start its host: it needs larik 0.28.0 or newer there (larik update)", name)}
}

// isSSHFailure reports whether ssh itself failed, which it says with
// status 255, rather than the command it ran.
func isSSHFailure(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 255
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
