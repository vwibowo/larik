package databasepane

import (
	"github.com/egoist/mygo/ui"
	"larik/internal/database"
)

type ObjectMenuActions struct {
	Open, OpenStructure, CopyFunction, CopyCreation, ExportCSV func()
	CopyText                                                   func(string)
}

func ObjectMenu(m *ui.Menu, object database.Table, driver string, actions ObjectMenuActions) {
	isFunction := database.IsFunction(object.Type)
	if isFunction {
		if m.Item("Open Definition").Chosen() {
			actions.Open()
		}
	} else {
		if m.Item("Open").Chosen() {
			actions.Open()
		}
		if m.Item("Open Structure").Chosen() {
			actions.OpenStructure()
		}
	}
	m.Separator()
	if m.Item("Copy Name").Chosen() && actions.CopyText != nil {
		actions.CopyText(object.Name)
	}
	m.Submenu("Copy Script As", func(sub *ui.Menu) {
		if isFunction {
			if sub.Item("Function Definition").Chosen() {
				actions.CopyFunction()
			}
			return
		}
		if sub.Item("SELECT statement").Chosen() && actions.CopyText != nil {
			actions.CopyText(database.SelectStatement(driver, object.Schema, object.Name))
		}
		if sub.Item("Creation").Chosen() {
			actions.CopyCreation()
		}
	})
	if !isFunction {
		m.Submenu("Export", func(sub *ui.Menu) {
			if sub.Item("CSV").Chosen() {
				actions.ExportCSV()
			}
		})
	}
}
