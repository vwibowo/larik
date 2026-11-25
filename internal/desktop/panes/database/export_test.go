package databasepane

import (
	"encoding/csv"
	"strings"
	"testing"

	"larik/internal/database"
)

func TestDatabaseCSVLoadedRows(t *testing.T) {
	r := database.Result{Columns: []string{"name", "note", "missing"}, Rows: [][]any{{"a,b", "line\n\"two\"", nil}, {"NULL", "", "ok"}}, Truncated: true}
	data, err := CSV(r)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(rows) != 3 || rows[1][0] != "a,b" || rows[1][1] != "line\n\"two\"" || rows[1][2] != "NULL" || rows[2][0] != "NULL" || rows[2][1] != "" {
		t.Fatalf("CSV rows %q, error %v", rows, err)
	}
}
