package browsercdp

import (
	"os"
	"syscall"
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
