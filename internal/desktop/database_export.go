package desktop

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"larik/internal/database"
)

func databaseValue(v any) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprint(v)
}

// databaseTSV copies the cells currently chosen in their displayed column order.
func databaseTSV(v *databaseResultView, headers bool) string {
	ids := v.grid.SelectedColumns()
	if len(ids) == 0 || v.selection.Len() == 0 {
		return ""
	}
	var columns []int
	for _, id := range ids {
		i, err := strconv.Atoi(id)
		if err == nil && i >= 0 && i < len(v.result.Columns) {
			columns = append(columns, i)
		}
	}
	if len(columns) == 0 {
		return ""
	}
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	w.Comma = '\t'
	if headers {
		row := make([]string, len(columns))
		for i, col := range columns {
			row[i] = v.result.Columns[col]
		}
		_ = w.Write(row)
	}
	for i, values := range v.result.Rows {
		if !v.selection.Has(i) {
			continue
		}
		row := make([]string, len(columns))
		for j, col := range columns {
			if col < len(values) {
				row[j] = databaseValue(values[col])
			} else {
				row[j] = "NULL"
			}
		}
		_ = w.Write(row)
	}
	w.Flush()
	return strings.TrimSuffix(out.String(), "\n")
}

func databaseCSV(r database.Result) ([]byte, error) {
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	if err := w.Write(r.Columns); err != nil {
		return nil, err
	}
	for _, values := range r.Rows {
		row := make([]string, len(r.Columns))
		for i := range row {
			if i < len(values) {
				row[i] = databaseValue(values[i])
			} else {
				row[i] = "NULL"
			}
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return out.Bytes(), w.Error()
}

func databaseCSVDialog(win *mygo.Window, a *App) func(string, []byte, func(string, error)) {
	return func(name string, data []byte, done func(string, error)) {
		go func() {
			path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
				Parent: win, Title: "Export loaded database results", DefaultPath: name,
				Filters: []mygo.FileFilter{{Name: "CSV", Extensions: []string{"csv"}}},
			})
			if err == nil && path != "" {
				err = os.WriteFile(path, data, 0o644)
			}
			a.update(func() { done(path, err) })
		}()
	}
}

func (a *App) exportDatabaseResult(tb *tab) {
	d := tb.database
	v := d.currentResult()
	if len(v.result.Columns) == 0 || a.saveDatabaseCSV == nil {
		return
	}
	data, err := databaseCSV(v.result)
	if err != nil {
		v.status = err.Error()
		return
	}
	name := "database-data.csv"
	if d.activeResult == databaseData && d.table != "" {
		name = d.table + ".csv"
	} else if d.activeResult == databaseQuery {
		name = "database-query.csv"
	} else if d.activeResult == databaseAgent {
		name = "database-agent.csv"
	}
	result := d.activeResult
	a.saveDatabaseCSV(name, data, func(path string, err error) {
		if err != nil {
			d.results[result].status = "Export failed: " + err.Error()
		} else if path != "" {
			d.results[result].status = "Exported loaded results to CSV"
		}
	})
}
