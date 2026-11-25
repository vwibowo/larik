package terminalpane

import (
	"os"
	"runtime"
)

// shellCommand is what a terminal tab runs: your login shell, or run
// through it, as an interactive shell reads your profile and its PATH.
func ShellCommand(run string) []string {
	if runtime.GOOS == "windows" {
		sh := os.Getenv("COMSPEC")
		if sh == "" {
			sh = "cmd.exe"
		}
		if run == "" {
			return []string{sh}
		}
		return []string{sh, "/c", run}
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	if run == "" {
		return []string{sh, "-l"}
	}
	return []string{sh, "-ilc", run}
}
