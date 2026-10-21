package termbrowser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
)

// legacyBrokerLabel names the macOS LaunchAgent that Larik 0.15 could
// install to start Chrome. Chrome now runs headless as Larik's own child,
// so the agent has no job left.
const legacyBrokerLabel = "dev.larik.browser-launcher"

// RemoveLegacyBroker uninstalls that LaunchAgent if it is there. The agent
// was kept alive by launchd and runs "larik browser-broker run", so the
// first run of a newer larik through it is what removes it: the plist goes
// first, then the job (which ends this process when launchd started it).
func RemoveLegacyBroker() (removed bool, err error) {
	if goruntime.GOOS != "darwin" {
		return false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", legacyBrokerLabel+".plist")
	if err := os.Remove(plist); err == nil {
		removed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	_ = os.RemoveAll(filepath.Join(home, "Library", "Application Support", "Larik", "BrowserLauncher"))
	out, err := exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+legacyBrokerLabel).CombinedOutput()
	if err != nil {
		msg := string(out)
		if strings.Contains(msg, "No such process") || strings.Contains(msg, "Could not find") || strings.Contains(msg, "not find") {
			return removed, nil
		}
		return removed, fmt.Errorf("unloading %s: %w: %s", legacyBrokerLabel, err, strings.TrimSpace(msg))
	}
	return true, nil
}
