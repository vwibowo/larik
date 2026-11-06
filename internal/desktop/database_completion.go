package desktop

import (
	"sort"
	"strings"

	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

var databaseKeywords = []string{
	"SELECT", "WITH", "RECURSIVE", "VALUES", "FROM", "WHERE", "JOIN", "LEFT", "RIGHT", "INNER", "OUTER", "CROSS",
	"ON", "AS", "AND", "OR", "NOT", "IN", "IS", "NULL", "LIKE", "BETWEEN", "GROUP", "BY", "HAVING",
	"ORDER", "LIMIT", "OFFSET", "UNION", "ALL", "DISTINCT", "CASE", "WHEN", "THEN", "ELSE", "END",
	"EXISTS", "ASC", "DESC",
}

var databaseFunctions = []string{"COUNT", "SUM", "AVG", "MIN", "MAX", "COALESCE", "NULLIF", "CAST", "LOWER", "UPPER", "LENGTH"}

var databaseReservedIdentifiers = []string{"INSERT", "UPDATE", "DELETE", "CREATE", "TABLE", "DROP", "ALTER", "INDEX", "VIEW", "TRIGGER", "PRAGMA", "VACUUM", "RETURNING"}

// databaseCompletions uses only the schema already in the pane. The editor
// filters and presents the returned items; typing never opens a connection.
func databaseCompletions(d *databaseState, req editor.CompletionRequest) []editor.Completion {
	if d == nil || d.sql == nil || d.sql.Buffer == nil || req.Version != d.sql.Buffer.Version() {
		return nil
	}
	text := d.sql.Buffer.String()
	start := req.Offset - len(req.Prefix)
	if start < 0 || start > len(text) || req.Offset > len(text) {
		return nil
	}
	tokens, blocked := databaseSQLTokens(text[:start])
	if blocked {
		return nil
	}
	allTokens, _ := databaseSQLTokens(text)
	afterDot := len(tokens) > 0 && tokens[len(tokens)-1] == "."
	if !req.Explicit && !afterDot && len(req.Prefix) < 2 {
		return nil
	}
	var items []editor.Completion
	add := func(label, detail, insert string, kind editor.CompletionKind) {
		items = append(items, editor.Completion{Label: label, Detail: detail, Insert: insert, Kind: kind})
	}
	addColumns := func(table database.Table) {
		for _, column := range table.Columns {
			add(column.Name, table.Name+" · "+column.Type, databaseSQLIdentifierFor(d.driver, column.Name), editor.CompletionField)
		}
	}
	addTables := func(schema string, afterQualifier bool) {
		for _, table := range d.tables {
			if schema != "" && !strings.EqualFold(table.Schema, schema) {
				continue
			}
			insert := databaseSQLIdentifierFor(d.driver, table.Name)
			if !afterQualifier && table.Schema != "" && !strings.EqualFold(table.Schema, "main") && !strings.EqualFold(table.Schema, "public") {
				insert = databaseSQLIdentifierFor(d.driver, table.Schema) + "." + insert
			}
			add(table.Name, table.Schema+" · "+table.Type, insert, editor.CompletionModule)
		}
	}
	addSchemas := func() {
		seen := map[string]bool{}
		for _, table := range d.tables {
			key := strings.ToLower(table.Schema)
			if key != "" && !seen[key] {
				add(table.Schema, "Schema", databaseSQLIdentifierFor(d.driver, table.Schema), editor.CompletionModule)
				seen[key] = true
			}
		}
	}
	if afterDot {
		if len(tokens) < 2 {
			return nil
		}
		qualifier := tokens[len(tokens)-2]
		for _, table := range d.tables {
			if strings.EqualFold(table.Schema, qualifier) {
				addTables(table.Schema, true)
				return databaseFilterCompletions(items, req.Prefix)
			}
		}
		if table, ok := databaseReferencedTables(allTokens, d.tables, d.schema)[strings.ToLower(qualifier)]; ok {
			addColumns(table)
			return databaseFilterCompletions(items, req.Prefix)
		}
		if table, ok := databaseFindTable(d.tables, "", qualifier, d.schema); ok {
			addColumns(table)
		}
		return databaseFilterCompletions(items, req.Prefix)
	}
	if n := len(tokens); n > 0 && (strings.EqualFold(tokens[n-1], "FROM") || strings.EqualFold(tokens[n-1], "JOIN")) {
		addTables("", false)
		addSchemas()
		return databaseFilterCompletions(items, req.Prefix)
	}
	for _, keyword := range databaseKeywords {
		if d.driver == "sqlserver" && (keyword == "LIMIT" || keyword == "RECURSIVE" || keyword == "VALUES") {
			continue
		}
		add(keyword, "SQL keyword", keyword, editor.CompletionKeyword)
	}
	if d.driver == "sqlserver" {
		for _, keyword := range []string{"TOP", "FETCH", "NEXT", "ROWS", "ONLY"} {
			add(keyword, "SQL keyword", keyword, editor.CompletionKeyword)
		}
	}
	for _, function := range databaseFunctions {
		add(function, "SQL function", function, editor.CompletionFunction)
	}
	references := databaseReferencedTables(allTokens, d.tables, d.schema)
	if len(references) == 0 {
		if table, ok := databaseFindTable(d.tables, d.schema, d.table, d.schema); ok {
			addColumns(table)
		}
	} else {
		seen := map[string]bool{}
		for _, table := range references {
			key := table.Schema + "." + table.Name
			if !seen[key] {
				addColumns(table)
				seen[key] = true
			}
		}
	}
	addTables("", false)
	addSchemas()
	return databaseFilterCompletions(items, req.Prefix)
}

func databaseFilterCompletions(items []editor.Completion, prefix string) []editor.Completion {
	prefix = strings.ToLower(prefix)
	filtered := items[:0]
	seen := map[string]bool{}
	for _, item := range items {
		if prefix != "" && !strings.Contains(strings.ToLower(item.Label), prefix) {
			continue
		}
		key := item.Label + "\x00" + item.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		iPrefix := strings.HasPrefix(strings.ToLower(filtered[i].Label), prefix)
		jPrefix := strings.HasPrefix(strings.ToLower(filtered[j].Label), prefix)
		if iPrefix != jPrefix {
			return iPrefix
		}
		return strings.ToLower(filtered[i].Label) < strings.ToLower(filtered[j].Label)
	})
	if len(filtered) > 50 {
		filtered = filtered[:50]
	}
	return filtered
}

func databaseFindTable(tables []database.Table, schema, name, preferredSchema string) (database.Table, bool) {
	if name == "" {
		return database.Table{}, false
	}
	var first database.Table
	found := false
	for _, table := range tables {
		if !strings.EqualFold(table.Name, name) || schema != "" && !strings.EqualFold(table.Schema, schema) {
			continue
		}
		if strings.EqualFold(table.Schema, preferredSchema) || schema != "" {
			return table, true
		}
		if !found {
			first, found = table, true
		}
	}
	return first, found
}

func databaseReferencedTables(tokens []string, tables []database.Table, preferredSchema string) map[string]database.Table {
	out := map[string]database.Table{}
	for i, token := range tokens {
		if !strings.EqualFold(token, "FROM") && !strings.EqualFold(token, "JOIN") {
			continue
		}
		j := i + 1
		if j >= len(tokens) {
			continue
		}
		schema, name := "", tokens[j]
		j++
		if j+1 < len(tokens) && tokens[j] == "." {
			schema, name = name, tokens[j+1]
			j += 2
		}
		table, ok := databaseFindTable(tables, schema, name, preferredSchema)
		if !ok {
			continue
		}
		out[strings.ToLower(table.Name)] = table
		if j < len(tokens) && strings.EqualFold(tokens[j], "AS") {
			j++
		}
		if j < len(tokens) && databaseAlias(tokens[j]) {
			out[strings.ToLower(tokens[j])] = table
		}
	}
	return out
}

func databaseAlias(token string) bool {
	if token == "." || token == "," || token == "(" || token == ")" {
		return false
	}
	for _, keyword := range []string{"WHERE", "JOIN", "LEFT", "RIGHT", "INNER", "OUTER", "CROSS", "ON", "GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET", "UNION"} {
		if strings.EqualFold(token, keyword) {
			return false
		}
	}
	return true
}

func databaseSQLIdentifier(name string) string {
	return databaseSQLIdentifierFor("", name)
}

func databaseSQLIdentifierFor(driver, name string) string {
	plain := name != "" && (name[0] >= 'a' && name[0] <= 'z' || name[0] == '_')
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			plain = false
			break
		}
	}
	if plain {
		for _, keyword := range databaseKeywords {
			if strings.EqualFold(name, keyword) {
				plain = false
				break
			}
		}
	}
	if plain {
		for _, keyword := range databaseReservedIdentifiers {
			if strings.EqualFold(name, keyword) {
				plain = false
				break
			}
		}
	}
	if plain {
		return name
	}
	switch driver {
	case "sqlserver":
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	case "mysql":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// databaseSQLTokens returns complete SQL identifiers and punctuation before
// the caret, and whether the caret is inside a string, quoted name or comment.
func databaseSQLTokens(text string) ([]string, bool) {
	const (
		normal = iota
		stringLiteral
		quotedIdentifier
		lineComment
		blockComment
	)
	state := normal
	quoteEnd := byte('"')
	var tokens []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch state {
		case stringLiteral:
			if ch == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i++
				} else {
					state = normal
				}
			}
		case quotedIdentifier:
			if ch == quoteEnd {
				if i+1 < len(text) && text[i+1] == quoteEnd {
					word.WriteByte(quoteEnd)
					i++
				} else {
					flush()
					state = normal
				}
			} else {
				word.WriteByte(ch)
			}
		case lineComment:
			if ch == '\n' {
				state = normal
			}
		case blockComment:
			if ch == '*' && i+1 < len(text) && text[i+1] == '/' {
				i++
				state = normal
			}
		default:
			switch {
			case ch == '-' && i+1 < len(text) && text[i+1] == '-':
				flush()
				i++
				state = lineComment
			case ch == '#':
				flush()
				state = lineComment
			case ch == '/' && i+1 < len(text) && text[i+1] == '*':
				flush()
				i++
				state = blockComment
			case ch == '\'':
				flush()
				state = stringLiteral
			case ch == '"' || ch == '`' || ch == '[':
				flush()
				quoteEnd = ch
				if ch == '[' {
					quoteEnd = ']'
				}
				state = quotedIdentifier
			case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_':
				word.WriteByte(ch)
			default:
				flush()
				if ch == '.' || ch == ',' || ch == '(' || ch == ')' {
					tokens = append(tokens, string(ch))
				}
			}
		}
	}
	flush()
	return tokens, state != normal
}
