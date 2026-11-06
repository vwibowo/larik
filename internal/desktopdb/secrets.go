package desktopdb

import (
	"encoding/json"
	"os"

	"larik/internal/config"
	"larik/internal/database"
)

// SecretEnv names Desktop connection URL variables so an agent's bash tool
// cannot inherit them from the Desktop process.
func SecretEnv() []string {
	data, err := os.ReadFile(config.DesktopDatabasePath())
	if err != nil {
		return nil
	}
	var file struct {
		Connections map[string]database.ConnectionConfig `json:"database_connections"`
	}
	if json.Unmarshal(data, &file) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range file.Connections {
		for _, name := range []string{p.URLEnv, p.DSNEnv, p.PasswordEnv} {
			if name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}
