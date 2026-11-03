package desktop

import (
	"net"
	"os"
	"sync"

	"larik/internal/filelock"
)

// lockSocket serializes stale-socket cleanup and binding across processes.
// The lock file stays on disk: removing it would let another process lock
// a different inode while the first still holds this one.
func lockSocket(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(f, false); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
}

// ownedSocket removes only its own socket on Close. A later process may
// replace the pathname after the listener closes but before cleanup runs.
type ownedSocket struct {
	*net.UnixListener
	path string
	info os.FileInfo
	once sync.Once
}

func ownSocket(ln net.Listener, path string) (net.Listener, error) {
	u := ln.(*net.UnixListener)
	info, err := os.Stat(path)
	if err != nil {
		u.Close()
		return nil, err
	}
	u.SetUnlinkOnClose(false)
	return &ownedSocket{UnixListener: u, path: path, info: info}, nil
}

func (s *ownedSocket) Close() error {
	err := s.UnixListener.Close()
	s.once.Do(func() {
		unlock, lockErr := lockSocket(s.path)
		if lockErr != nil {
			return
		}
		defer unlock()
		if current, statErr := os.Stat(s.path); statErr == nil && os.SameFile(current, s.info) {
			_ = os.Remove(s.path)
		}
	})
	return err
}
