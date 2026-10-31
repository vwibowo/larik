//go:build windows

package termbrowser

import (
	"os/exec"
	"time"
)

func configureChromeCmd(*exec.Cmd) {}

// Windows has no process-group support in the existing procgroup helper;
// chromedp's allocator cancellation owns and reaps its browser process.
func chromeProcessGroupAlive(cmd *exec.Cmd) bool {
	return cmd != nil && cmd.Process != nil && cmd.ProcessState == nil
}

func waitForChromeProcessGroupExit(*exec.Cmd, time.Duration) {}
