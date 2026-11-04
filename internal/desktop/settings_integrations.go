package desktop

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"larik/internal/desktop/integration"
)

func integrationHome() string {
	home, _ := os.UserHomeDir()
	return home
}

func (a *App) integrationNote(id string) string {
	home := integrationHome()
	if id == "integration-skills" {
		state := "Not installed"
		if integration.SkillsInstalled(home) {
			state = "Installed; restart agents or reload their skills"
		}
		return state + " · " + integration.SkillsPath(home) + " · Claude links: " + filepath.Join(home, ".claude", "skills")
	}
	agent := strings.TrimPrefix(id, "integration-")
	installed, err := integration.HookInstalled(home, agent)
	if err != nil {
		return "Cannot read hooks: " + err.Error()
	}
	state := "Not installed"
	if installed {
		state = "Installed; start a new agent session"
		if agent == "codex" {
			state += "; review and trust it in Codex /hooks"
		}
	}
	return state + " · " + integration.HookPath(home, agent)
}

func (a *App) integrationControl(c *ui.Context, id string) {
	home := integrationHome()
	installed := false
	if id == "integration-skills" {
		installed = integration.SkillsInstalled(home)
	} else {
		installed, _ = integration.HookInstalled(home, strings.TrimPrefix(id, "integration-"))
	}
	label := "Install"
	if installed {
		label = "Remove"
	}
	if !ui.Button(c.Key("install"), label).Label(label + " " + strings.TrimPrefix(id, "integration-")).Clicked() {
		return
	}
	var err error
	if id == "integration-skills" {
		if installed {
			err = integration.RemoveSkills(home)
		} else {
			err = integration.InstallSkills(home)
		}
	} else {
		agent := strings.TrimPrefix(id, "integration-")
		if installed {
			err = integration.RemoveHook(home, agent)
		} else {
			bin := a.opts.Bin
			if bin == "" {
				bin, err = os.Executable()
			}
			if err == nil {
				err = integration.InstallHook(home, bin, agent)
			}
		}
	}
	if err != nil {
		a.settings.errors[id] = err.Error()
	} else {
		delete(a.settings.errors, id)
	}
}
