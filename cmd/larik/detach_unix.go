//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, so closing the terminal
// doesn't close the window.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
