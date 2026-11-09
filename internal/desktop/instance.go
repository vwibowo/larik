package desktop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// One window runs per data directory: two would show the same sessions,
// each with tabs of its own, and save over each other's. The window
// listens on a socket named after the data directory; a later larik
// desktop hands it the directory it ran in and exits, and the window
// comes forward on that directory's project. Commands such as larik
// desktop open and larik desktop wait send it requests on the same
// socket, as JSON (see control.go).

// socketPath is the window's socket for dataDir: in a short temp directory, as
// Unix socket paths are limited to about 100 bytes.
func socketPath(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	name := "larik-desktop-" + hex.EncodeToString(sum[:6]) + ".sock"
	return filepath.Join(shortSocketDir(name), name)
}

func shortSocketDir(name string) string {
	dir := os.TempDir()
	if len(filepath.Join(dir, name)) > 100 && runtime.GOOS != "windows" {
		dir = "/tmp"
	}
	return dir
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
// a later larik desktop hands over, and control with each request a
// larik desktop command sends (see Call), until the listener closes. A
// socket left by a window that crashed is taken over.
func listen(dataDir string, open func(cwd string), control func(Request) Response) (net.Listener, error) {
	path := socketPath(dataDir)
	unlock, err := lockSocket(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if Handover(dataDir, "") {
		return nil, errRunning
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	ln, err = ownSocket(ln, path)
	if err != nil {
		return nil, err
	}
	ready := make(chan struct{})
	go func() {
		close(ready)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// One at a time would hold every other command up behind a wait.
			go serve(conn, open, control)
		}
	}()
	<-ready
	return ln, nil
}

// serve answers one connection: a request, as JSON, gets its response
// back; anything else is a directory handed over.
func serve(conn net.Conn, open func(cwd string), control func(Request) Response) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	data, _ := io.ReadAll(io.LimitReader(conn, maxRequest))
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		conn.Close() // the handover waits for this
		open(string(data))
		return
	}
	var req Request
	res := Response{Error: "not a request larik desktop knows"}
	if err := json.Unmarshal(data, &req); err == nil {
		_ = conn.SetDeadline(time.Time{}) // a wait takes as long as it takes
		res = control(req)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = json.NewEncoder(conn).Encode(res)
}

// maxRequest is the most a request may say: text sent to a tab included.
const maxRequest = 1 << 20

// errNotOpen is Call's answer when no window is open for the data
// directory.
var errNotOpen = errors.New("larik desktop isn't open: start it with larik desktop")

// Call sends req to the window open for dataDir and returns its answer,
// or with no window open, has the host answer it (see callHost).
// A response with an error is returned as an error, with the response.
func Call(dataDir string, req Request) (Response, error) {
	conn, err := net.DialTimeout("unix", socketPath(dataDir), time.Second)
	if err != nil {
		if pingHost(dataDir) == nil {
			// No window, but the tabs it left running.
			return callHost(dataDir, req)
		}
		return Response{}, errNotOpen
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	var res Response
	if err := json.NewDecoder(conn).Decode(&res); errors.Is(err, io.EOF) {
		// A window from before commands takes a request for a directory.
		return Response{}, errors.New("the open larik desktop is older than this larik and takes no commands: quit it and start larik desktop again")
	} else if err != nil {
		return Response{}, fmt.Errorf("larik desktop didn't answer: %w", err)
	}
	if res.Error != "" {
		return res, errors.New(res.Error)
	}
	return res, nil
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
