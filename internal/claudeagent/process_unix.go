//go:build darwin || linux

package claudeagent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// prepareCommand gives the CLI its own process group so cancellation also
// stops any descendants it started. This is important when a malformed stream
// or timeout closes the bridge while the CLI is still waiting on it.
func prepareCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
}
