package tui

import (
	"os/exec"
	"runtime"
)

// openBrowser opens url in the default browser; tests replace it.
var openBrowser = func(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start() // the URL is also shown, in case this fails
}
