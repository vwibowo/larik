package termbrowser

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// sameUser reports whether the other end of a Unix socket runs as us.
func sameUser(conn net.Conn) bool {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	same := false
	_ = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		same = err == nil && int(cred.Uid) == os.Getuid()
	})
	return same
}
