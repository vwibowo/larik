package databasepane

import (
	"strings"
	"time"

	"larik/internal/database"
)

func Timing(elapsed time.Duration) string {
	if elapsed == 0 {
		return "—"
	}
	if elapsed < time.Millisecond {
		return "<1 ms"
	}
	return elapsed.Round(time.Millisecond).String()
}

func FindObjectType(objects []database.Table, schema, name string) string {
	for _, object := range objects {
		if object.Schema == schema && object.Name == name {
			return object.Type
		}
	}
	return ""
}

func FindObject(objects []database.Table, schema, name string) (database.Table, bool) {
	for _, object := range objects {
		if object.Schema == schema && object.Name == name {
			return object, true
		}
	}
	return database.Table{}, false
}

func ObjectMarker(kind string) string {
	switch {
	case database.IsFunction(kind):
		return "ƒ"
	case strings.EqualFold(kind, "VIEW"):
		return "◫"
	default:
		return "▦"
	}
}

func DriverLabel(driver string) string {
	switch driver {
	case "postgres":
		return "PostgreSQL"
	case "sqlserver":
		return "SQL Server"
	case "mysql":
		return "MySQL / MariaDB"
	default:
		return "SQLite"
	}
}

func ContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
