package desktopdb

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"larik/internal/config"
)

func TestSecretEnvIncludesNewDatabaseDrivers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	path := config.DesktopDatabasePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"database_connections":{"my":{"driver":"mysql","dsn_env":"MYSQL_SECRET"},"ms":{"driver":"sqlserver","password_env":"MSSQL_SECRET"},"pg":{"driver":"postgres","url_env":"PG_SECRET"}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got := SecretEnv()
	for _, name := range []string{"MYSQL_SECRET", "MSSQL_SECRET", "PG_SECRET"} {
		if !slices.Contains(got, name) {
			t.Fatalf("missing %s in %v", name, got)
		}
	}
	if strings.Contains(strings.Join(got, " "), "driver") {
		t.Fatalf("unexpected secret list: %v", got)
	}
}
