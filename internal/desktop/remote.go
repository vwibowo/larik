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
	projects   []session.Project
	err        string
	connecting bool
}

// connector connects to the server name: over SSH, or in tests to a host
// here standing in for one. It returns how to reach the server's host,
// larik's path there, and how to end the connection.
type connector func(name string) (dial dialer, bin string, stop func(), err error)

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
	srv.connecting, srv.err = true, ""
	connect := a.connect
	if connect == nil {
		connect = sshConnect
	}
	go func() {
		dial, bin, stop, err := connect(name)
		if err == nil {
			err = checkProto(dial, name)
			if err != nil {
				stop()
			}
		}
		a.update(func() {
			srv.connecting = false
			if err != nil {
				srv.err = err.Error()
				return
			}
			if !slices.Contains(a.servers, srv) { // disconnected meanwhile
				stop()
				return
			}
			a.serversMu.Lock()
			srv.dial, srv.bin, srv.stop = dial, bin, stop
			a.serversMu.Unlock()
			a.adoptRemote(srv)
			a.refreshIndex()
		})
	}()
}

// checkProto asks the server's host what it speaks, saying when it's
// older than this window.
func checkProto(dial dialer, name string) error {
	conn, _, rep, err := askHostVia(dial, hostRequest{Op: "ping"})
	if err != nil {
		return fmt.Errorf("can't reach larik on %s: %w", name, err)
	}
	conn.Close()
	if rep.Proto < hostProto {
		return fmt.Errorf("larik on %s is older than this one: update it there (larik update)", name)
	}
	return nil
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
	if p := a.paneOf(t); p != nil {
		if a.split() {
			a.removePane(p)
		} else {
			a.layout, a.focus = nil, nil
		}
	}
	a.tabs = slices.DeleteFunc(a.tabs, func(x *tab) bool { return x == t })
	t.close()
	if a.layout == nil && len(a.tabs) > 0 {
		a.activate(a.tabs[len(a.tabs)-1])
	}
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
		if h.Exited || a.tabByID(h.ID) != nil {
			continue
		}
		h.Dir = remotePath(srv.name, h.Dir)
		if t := a.attachTab(h, ""); t != nil && a.layout == nil {
			a.activate(t)
		}
	}
	a.followSessions()
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
	a.serversMu.Lock()
	stop := srv.stop
	srv.dial, srv.stop = nil, nil
	a.serversMu.Unlock()
	if stop != nil {
		go stop()
	}
	srv.err = "lost the connection: " + err.Error()
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
		ui.Text(c.Key("server-err:"+srv.name), srv.err).FontSize(11).TextColor(t.Danger).Padding(0, 8, 4, 8)
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
	return []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", name, "larik desktop host ensure"}
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
func sshConnect(name string) (dialer, string, func(), error) {
	out, err := exec.Command("ssh", sshEnsureArgs(name)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "command not found") || strings.Contains(msg, "not found") {
			msg = "larik isn't on the PATH of a non-interactive shell there; install it, or add its folder to PATH in ~/.zshenv or ~/.bashrc"
		}
		return nil, "", nil, fmt.Errorf("ssh %s: %s", name, firstNonEmpty(msg, err.Error()))
	}
	remote, bin, err := parseEnsure(string(out))
	if err != nil {
		return nil, "", nil, err
	}
	local := sshSocketPath(name)
	_ = os.Remove(local)
	cmd := exec.Command("ssh", sshForwardArgs(name, local, remote)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, "", nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
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
			return nil, "", nil, fmt.Errorf("ssh %s ended: %s", name, firstNonEmpty(strings.TrimSpace(stderr.String()), "the forward failed"))
		default:
		}
		if conn, err := dial(); err == nil {
			conn.Close()
			return dial, bin, stop, nil
		}
		if time.Now().After(deadline) {
			stop()
			return nil, "", nil, fmt.Errorf("ssh %s: the host's socket didn't come through", name)
		}
	}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
