//go:build !windows

package status

import (
	"errors"
	"syscall"
)

// alive reports whether pid is a running process. EPERM means it runs as
// someone else, which still counts.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
