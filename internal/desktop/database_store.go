package desktop

import "larik/internal/database"

// databaseStore is the narrow service the window needs from the Database domain.
type databaseStore interface {
	Names() []string
	Get(string) (database.ConnectionConfig, bool)
	All() map[string]database.ConnectionConfig
	MaskedEnv() []string
	Save(map[string]database.ConnectionConfig) error
	Manager() *database.Manager
	Err() error
	Path() string
}

type databaseSecrets interface {
	Set(string, []byte) error
	Get(string) ([]byte, error)
	Delete(string) error
}

var loadDatabaseStore = func() databaseStore { return nil }
var loadDatabaseStoreWithSecrets = func(databaseSecrets) databaseStore { return nil }
var databaseSecretKey = func(name string) string { return "connection:" + name }

func (a *App) paneEnvironment() []string {
	var out []string
	for _, variant := range paneVariants {
		if variant.env != nil {
			out = append(out, variant.env(a)...)
		}
	}
	return out
}
