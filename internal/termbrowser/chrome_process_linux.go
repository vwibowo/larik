//go:build linux

package termbrowser

import (
	"os/exec"
	"syscall"
	"time"

	"larik/internal/procgroup"
)

// ModifyCmdFunc overrides chromedp's Linux defaults, so preserve the
// parent-death signal that kills Chrome if Larik itself crashes.
func configureChromeCmd(cmd *exec.Cmd) {
	procgroup.Configure(cmd)
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func chromeProcessGroupAlive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	err := syscall.Kill(-cmd.Process.Pid, 0)
	return err == nil || err == syscall.EPERM
}

func waitForChromeProcessGroupExit(cmd *exec.Cmd, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for chromeProcessGroupAlive(cmd) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
}
