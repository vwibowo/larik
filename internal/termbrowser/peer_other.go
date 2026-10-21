//go:build !darwin && !linux

package termbrowser

import "net"

// sameUser trusts the socket's file permissions where peer credentials
// can't be read.
func sameUser(net.Conn) bool { return true }
