package desktop

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type remotePortKey struct {
	name string
	port int
}

type remoteForward struct {
	local   int
	stop    func()
	pending []*tab
}

func (a *App) openRemoteBookmark(b browserBookmark) {
	key := remotePortKey{b.RemoteName, b.RemotePort}
	srv := a.serverNamed(key.name)
	if srv == nil || a.dialOf(srv) == nil {
		a.notice = "Not connected to " + key.name
		return
	}
	if !slices.Contains(srv.features, "remote-meta") {
		a.notice = "Update larik on " + key.name + " to open its ports"
		return
	}
	t := a.startBrowser("")
	if t == nil {
		return
	}
	t.dir = remotePath(key.name, "")
	t.remotePort, t.remotePortLost = key, true
	t.remoteBookmarkSuffix = b.RemotePath
	a.activate(t)
	a.startRemoteForward(key, false, t, "")
}

func sshPortForwardArgs(name string, local, remote int) []string {
	return []string{"-N", "-T", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", local, remote), name}
}

// forwardRemotePort binds only to loopback on both ends. A failed SSH
// process is reported through health after the forward is ready.
func forwardRemotePort(name string, remote int) (int, func(), <-chan error, error) {
	if remote < 1 || remote > 65535 {
		return 0, nil, nil, errors.New("invalid remote port")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, nil, err
	}
	local := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cmd := exec.Command("ssh", sshPortForwardArgs(name, local, remote)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, nil, nil, err
	}
	health := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if said := strings.TrimSpace(stderr.String()); said != "" {
			err = fmt.Errorf("ssh %s: %s", name, said)
		}
		health <- err
	}()
	stop := func() { _ = cmd.Process.Kill() }
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-health:
			if err == nil {
				err = errors.New("SSH port forward ended")
			}
			return 0, nil, nil, err
		default:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return local, stop, health, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return 0, nil, nil, fmt.Errorf("ssh %s: port %d did not become ready", name, remote)
}

func (a *App) openRemotePort(path string, port int, external bool) {
	name, _, ok := splitRemote(path)
	if !ok || port < 1 || port > 65535 {
		return
	}
	key := remotePortKey{name, port}
	if external {
		a.startRemoteForward(key, true, nil, path)
		return
	}
	for _, t := range a.tabs {
		if t.browser && t.remotePort == key {
			a.activate(t)
			if t.remotePortLost {
				a.startRemoteForward(key, false, t, path)
			}
			return
		}
	}
	a.startRemoteForward(key, external, nil, path)
}

func (a *App) startRemoteForward(key remotePortKey, external bool, target *tab, path string) {
	srv := a.serverNamed(key.name)
	if srv == nil || a.dialOf(srv) == nil {
		a.notice = "Not connected to " + key.name
		return
	}
	if !slices.Contains(srv.features, "remote-meta") {
		a.notice = "Update larik on " + key.name + " to open its ports"
		return
	}
	if a.forwards == nil {
		a.forwards = map[remotePortKey]*remoteForward{}
	}
	if f := a.forwards[key]; f != nil {
		if f.local > 0 {
			a.showRemotePort(key, f.local, external, target, path)
		} else if target != nil {
			f.pending = append(f.pending, target)
		}
		return
	}
	f := &remoteForward{}
	a.forwards[key] = f
	epoch := srv.epoch
	go func() {
		local, stop, health, err := forwardRemotePort(key.name, key.port)
		a.update(func() {
			if a.forwards[key] != f || srv.epoch != epoch || a.dialOf(srv) == nil {
				if stop != nil {
					stop()
				}
				return
			}
			if err != nil {
				delete(a.forwards, key)
				for _, pending := range f.pending {
					pending.remotePortLost = true
				}
				a.notice = fmt.Sprintf("Can't open %s:%d: %v", key.name, key.port, err)
				return
			}
			f.local, f.stop = local, stop
			a.showRemotePort(key, local, external, target, path)
			for _, pending := range f.pending {
				a.showRemotePort(key, local, false, pending, "")
			}
			f.pending = nil
			go func() {
				select {
				case <-a.done:
					return
				case failure := <-health:
					a.update(func() {
						if a.forwards[key] == f {
							delete(a.forwards, key)
							for _, t := range a.tabs {
								if t.remotePort == key {
									t.remotePortLost = true
								}
							}
							a.notice = fmt.Sprintf("Forward to %s:%d ended: %v", key.name, key.port, failure)
						}
					})
				}
			}()
		})
	}()
}

func (a *App) showRemotePort(key remotePortKey, local int, external bool, target *tab, path string) {
	u := fmt.Sprintf("http://127.0.0.1:%d", local)
	if external {
		a.openURL(u)
		return
	}
	if target != nil {
		if !slices.Contains(a.tabs, target) {
			return
		}
		u += target.remoteBookmarkSuffix
		target.remotePortLost = false
		target.url, target.addr = u, u
		if target.web != nil {
			if err := target.web.Page().LoadURL(u); err != nil {
				a.notice = err.Error()
			}
		}
		return
	}
	t := a.startBrowser(u)
	if t == nil {
		return
	}
	t.dir, t.remotePort = remotePath(key.name, ""), key
	if source := a.tabOf(path); source != nil {
		t.dir = source.dir
	}
	if p := a.paneOf(a.tabOf(path)); p != nil {
		a.focusPane(p)
		a.splitPane(p, t, false)
	} else {
		a.activate(t)
	}
}

func (a *App) releaseRemoteForward(key remotePortKey) {
	if key.port == 0 {
		return
	}
	for _, t := range a.tabs {
		if t.browser && t.remotePort == key {
			return
		}
	}
	if f := a.forwards[key]; f != nil {
		delete(a.forwards, key)
		if f.stop != nil {
			f.stop()
		}
	}
}

func (a *App) dropRemoteForwards(name string) {
	for key, f := range a.forwards {
		if key.name == name {
			delete(a.forwards, key)
			if f.stop != nil {
				f.stop()
			}
		}
	}
	for _, t := range a.tabs {
		if t.browser && t.remotePort.name == name {
			t.remotePortLost = true
		}
	}
}

func (a *App) reconnectRemotePorts(srv *server) {
	for _, t := range a.tabs {
		if t.browser && t.remotePort.name == srv.name && t.remotePortLost {
			a.startRemoteForward(t.remotePort, false, t, "")
		}
	}
}

func (a *App) expireRemoteForwards(ports map[string][]int, seen map[string]bool) {
	for key, f := range a.forwards {
		if !seen[key.name] || f.local == 0 {
			continue
		}
		live := false
		for path, list := range ports {
			name, _, remote := splitRemote(path)
			if remote && name == key.name && slices.Contains(list, key.port) {
				live = true
				break
			}
		}
		if live {
			continue
		}
		delete(a.forwards, key)
		if f.stop != nil {
			f.stop()
		}
		for _, t := range a.tabs {
			if t.remotePort == key {
				t.remotePortLost = true
			}
		}
	}
}
