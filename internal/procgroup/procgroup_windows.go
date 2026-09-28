//go:build windows

package procgroup

import "os/exec"

// Windows does not expose Unix process groups through os/exec.
func Configure(cmd *exec.Cmd) {}

func Kill(cmd *exec.Cmd) error { return cmd.Process.Kill() }
