package desktop

import (
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"larik/internal/database"
)

var databaseEncodeCSV = func(database.Result) ([]byte, error) { return nil, fmt.Errorf("Database panes are unavailable") }
var databaseEncodeTSV = func(database.Result, []string, func(int) bool, bool) string { return "" }

func databaseTSV(v *databaseResultView, headers bool) string {
	if v.Selection.Len() == 0 {
		return ""
	}
	return databaseEncodeTSV(v.Result, v.Grid.SelectedColumns(), v.Selection.Has, headers)
}
func databaseCSV(r database.Result) ([]byte, error) { return databaseEncodeCSV(r) }

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
	d := tb.databaseState()
	v := d.CurrentResult()
	if len(v.Result.Columns) == 0 || a.saveDatabaseCSV == nil {
		return
	}
	data, err := databaseCSV(v.Result)
	if err != nil {
		v.Status = err.Error()
		return
	}
	name := "database-data.csv"
	if d.ActiveResult == databaseData && d.Table != "" {
		name = d.Table + ".csv"
	} else if d.ActiveResult == databaseQuery {
		name = "database-query.csv"
	} else if d.ActiveResult == databaseAgent {
		name = "database-agent.csv"
	}
	result := d.ActiveResult
	a.saveDatabaseCSV(name, data, func(path string, err error) {
		if err != nil {
			d.Results[result].Status = "Export failed: " + err.Error()
		} else if path != "" {
			d.Results[result].Status = "Exported loaded results to CSV"
		}
	})
}

func (a *App) exportDatabaseObject(tb *tab, object database.Table) {
	d := tb.databaseState()
	if d == nil || d.Profile == "" || a.databaseStore.Manager() == nil {
		return
	}
	d.ExportSeq++
	seq, profile := d.ExportSeq, d.Profile
	source := object.Schema + "." + object.Name
	loaded := &d.Results[databaseData]
	if loaded.Source == source && len(loaded.Result.Columns) > 0 {
		a.saveDatabaseObjectCSV(tb, object.Name, loaded.Result)
		return
	}
	limit := d.PageSize
	if limit <= 0 {
		limit = database.DefaultRows
	}
	d.Status = "Loading rows for CSV export…"
	a.databaseWork(func() {
		result, err := a.databaseStore.Manager().Rows(d.Ctx, profile, object.Schema, object.Name, 0, limit, false)
		a.update(func() {
			if tb.databaseState() != d || d.Ctx.Err() != nil || d.ExportSeq != seq {
				return
			}
			if err != nil {
				d.Status = "Export failed: " + err.Error()
				return
			}
			d.Status = ""
			a.saveDatabaseObjectCSV(tb, object.Name, result)
		})
	})
}

func (a *App) saveDatabaseObjectCSV(tb *tab, objectName string, result database.Result) {
	d := tb.databaseState()
	if a.saveDatabaseCSV == nil {
		d.Status = "CSV export is unavailable"
		return
	}
	data, err := databaseCSV(result)
	if err != nil {
		d.Status = "Export failed: " + err.Error()
		return
	}
	name := strings.NewReplacer("/", "_", "\\", "_").Replace(objectName)
	if name == "" {
		name = "database-object"
	}
	a.saveDatabaseCSV(name+".csv", data, func(path string, err error) {
		if tb.databaseState() != d || d.Ctx.Err() != nil {
			return
		}
		if err != nil {
			d.Status = "Export failed: " + err.Error()
		} else if path != "" {
			d.Status = "Exported loaded rows as CSV"
		}
	})
}
