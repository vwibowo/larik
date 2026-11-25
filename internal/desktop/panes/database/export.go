package databasepane

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"larik/internal/database"
	"strconv"
	"strings"
)

func databaseValue(v any) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprint(v)
}

// databaseTSV copies the cells currently chosen in their displayed column order.
func TSV(result database.Result, ids []string, has func(int) bool, headers bool) string {
	if len(ids) == 0 || has == nil {
		return ""
	}
	var columns []int
	for _, id := range ids {
		i, err := strconv.Atoi(id)
		if err == nil && i >= 0 && i < len(result.Columns) {
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
			row[i] = result.Columns[col]
		}
		_ = w.Write(row)
	}
	for i, values := range result.Rows {
		if !has(i) {
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

func CSV(r database.Result) ([]byte, error) {
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
