package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// One window runs per data directory: two would show the same sessions,
// each with tabs of its own, and save over each other's. The window
// listens on a socket named after the data directory; a later larik
// desktop hands it the directory it ran in and exits, and the window
// comes forward on that directory's project.

// socketPath is the window's socket for dataDir: in the temp folder, as
// Unix socket paths are limited to about 100 bytes.
func socketPath(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(os.TempDir(), "larik-desktop-"+hex.EncodeToString(sum[:6])+".sock")
}

// Handover gives cwd to the window open for dataDir, and reports whether
// there is one.
func Handover(dataDir, cwd string) bool {
	conn, err := net.DialTimeout("unix", socketPath(dataDir), time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, cwd); err != nil {
		return false
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	_, _ = io.Copy(io.Discard, conn) // until the window has read it
	return true
}

// errRunning is listen's answer when another window has the socket.
var errRunning = errors.New("larik desktop is already open")

// listen takes the socket for dataDir, calling open with each directory
// a later larik desktop hands over, until the listener closes. A socket
// left by a window that crashed is taken over.
func listen(dataDir string, open func(cwd string)) (net.Listener, error) {
	path := socketPath(dataDir)
	if Handover(dataDir, "") {
		return nil, errRunning
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			data, _ := io.ReadAll(io.LimitReader(conn, 4096))
			conn.Close()
			open(string(data))
		}
	}()
	return ln, nil
}

// openDir shows dir's project, as a larik desktop run there asks for: its
// newest session, in a tab, or a new session when it has none. The home
// directory, where launchers start, asks for nothing but the window.
func (a *App) openDir(dir string) {
	if dir == "" {
		return
	}
	if home, err := os.UserHomeDir(); err == nil && dir == home {
		return
	}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if s.Cwd == dir || p.Root == dir {
				*a.open[p.Root] = true
				a.selectSession(s.Path)
				return
			}
		}
	}
	a.newTab(dir, "", "")
}
