//go:build !darwin && !linux

package claudeagent

import "os/exec"

func prepareCommand(*exec.Cmd) {}
