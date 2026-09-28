//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func Lock(f *os.File, nonBlocking bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonBlocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlapped)
}

func Unlock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}

func Busy(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
