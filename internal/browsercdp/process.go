package browsercdp

import (
	"os"
	"syscall"
	"time"
)

// processAlive reports whether pid is a running process. Where signal 0
// isn't supported (Windows) it reports false, and a locked profile is
// caught when Chrome fails to start instead.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// stopProcess ends a Chrome that is not our child, waiting for it to go and
// killing it if it won't. Browser.close asks it to quit, but a Chrome started
// through Launch Services is an application: it keeps running, and keeps its
// DevTools port, with no windows left.
func stopProcess(pid int, timeout time.Duration) {
	if pid <= 0 || !processAlive(pid) {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			_ = p.Kill()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
