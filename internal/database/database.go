// Package database provides bounded, read-only inspection of personal database profiles.
package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	"modernc.org/sqlite"
)

const (
	DefaultRows = 100
	MaxRows     = 500
	MaxBytes    = 1 << 20
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*$`)

type Profile struct {
	Name        string `json:"name"`
	Driver      string `json:"driver"`
	AgentAccess bool   `json:"agent_access"`
}

// ConnectionConfig contains no database credentials. Secrets are supplied
// by the Desktop process environment when a connection opens.
type ConnectionConfig struct {
	Driver                 string `json:"driver"`
	Path                   string `json:"path,omitempty"`
	URLEnv                 string `json:"url_env,omitempty"`
	DSNEnv                 string `json:"dsn_env,omitempty"`
	Host                   string `json:"host,omitempty"`
	Port                   int    `json:"port,omitempty"`
	Database               string `json:"database,omitempty"`
	User                   string `json:"user,omitempty"`
	PasswordEnv            string `json:"password_env,omitempty"`
	TrustServerCertificate bool   `json:"trust_server_certificate,omitempty"`
	AgentAccess            bool   `json:"agent_access,omitempty"`
}
type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	PrimaryKey bool   `json:"primary_key"`
}
type Table struct {
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Columns []Column `json:"columns,omitempty"`
}
type Result struct {
	Columns         []string `json:"columns"`
	Rows            [][]any  `json:"rows"`
	Truncated       bool     `json:"truncated"`
	NextOffset      int      `json:"next_offset,omitempty"`
	ValuesTruncated bool     `json:"-"`
}

type Manager struct {
	mu            sync.RWMutex
	profiles      map[string]ConnectionConfig
	cwd           string
	openPostgres  func(string) (*sql.DB, error)
	openMySQL     func(string) (*sql.DB, error)
	openSQLServer func(string) (*sql.DB, error)
}

func New(profiles map[string]ConnectionConfig, cwd string) (*Manager, error) {
	m := &Manager{profiles: make(map[string]ConnectionConfig, len(profiles)), cwd: cwd,
		openPostgres:  func(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) },
		openMySQL:     func(dsn string) (*sql.DB, error) { return sql.Open("mysql", dsn) },
		openSQLServer: func(dsn string) (*sql.DB, error) { return sql.Open("sqlserver", dsn) },
	}
	for name, p := range profiles {
		if strings.TrimSpace(name) != name || name == "" || len(name) > 80 {
			return nil, fmt.Errorf("invalid database profile name")
		}
		switch p.Driver {
		case "sqlite":
			if p.Path == "" || p.URLEnv != "" || p.DSNEnv != "" || p.Host != "" || p.PasswordEnv != "" {
				return nil, fmt.Errorf("database profile %q needs only path", name)
			}
		case "postgres":
			if p.Path != "" || p.DSNEnv != "" || p.Host != "" || !envName.MatchString(p.URLEnv) {
				return nil, fmt.Errorf("database profile %q needs only url_env", name)
			}
		case "mysql":
			if p.Path != "" || p.URLEnv != "" || p.Host != "" || !envName.MatchString(p.DSNEnv) {
				return nil, fmt.Errorf("database profile %q needs only dsn_env", name)
			}
		case "sqlserver":
			if p.Path != "" || p.URLEnv != "" || p.DSNEnv != "" || strings.TrimSpace(p.Host) == "" || p.Host != strings.TrimSpace(p.Host) || strings.ContainsAny(p.Host, "/@?#\\") || strings.Contains(p.Host, ":") && net.ParseIP(p.Host) == nil || p.Port < 1 || p.Port > 65535 || strings.TrimSpace(p.Database) == "" || strings.TrimSpace(p.User) == "" || !envName.MatchString(p.PasswordEnv) {
				return nil, fmt.Errorf("database profile %q needs SQL Server host, port, database, user, and password_env", name)
			}
		default:
			return nil, fmt.Errorf("database profile %q: unsupported driver", name)
		}
		m.profiles[name] = p
	}
	return m, nil
}

func (m *Manager) List(agent bool) []Profile {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Profile
	for name, p := range m.profiles {
		if !agent || p.AgentAccess {
			out = append(out, Profile{name, p.Driver, p.AgentAccess})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) profile(name string, agent bool) (ConnectionConfig, error) {
	if m == nil {
		return ConnectionConfig{}, errors.New("database service unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.profiles[name]
	if !ok || agent && !p.AgentAccess {
		return p, errors.New("database profile not available")
	}
	return p, nil
}

// Replace publishes validated profile edits without changing the current
// model context's tool definitions.
func (m *Manager) Replace(profiles map[string]ConnectionConfig) error {
	next, err := New(profiles, m.cwd)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.profiles = next.profiles
	m.mu.Unlock()
	return nil
}

// connection owns one physical connection for one operation. SQLite's
// authorizer is installed before any user or metadata SQL is compiled.
func (m *Manager) connection(ctx context.Context, p ConnectionConfig) (*sql.DB, *sql.Conn, *sql.Tx, func(error) error, error) {
	var db *sql.DB
	var secrets []string
	if p.Driver == "sqlite" {
		path := p.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.cwd, path)
		}
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, nil, nil, nil, errors.New("SQLite file does not exist or is not regular")
		}
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
		connector, err := sqlite.NewConnector(u.String())
		if err != nil {
			return nil, nil, nil, nil, errors.New("could not open SQLite profile")
		}
		db = sql.OpenDB(connector)
	} else {
		var err error
		switch p.Driver {
		case "postgres":
			secret := os.Getenv(p.URLEnv)
			if secret == "" {
				return nil, nil, nil, nil, fmt.Errorf("environment variable %s is not set", p.URLEnv)
			}
			secrets = append(secrets, secret)
			db, err = m.openPostgres(secret)
		case "mysql":
			secret := os.Getenv(p.DSNEnv)
			if secret == "" {
				return nil, nil, nil, nil, fmt.Errorf("environment variable %s is not set", p.DSNEnv)
			}
			secrets = append(secrets, secret)
			if cfg, e := mysql.ParseDSN(secret); e == nil && cfg.Passwd != "" {
				secrets = append(secrets, cfg.Passwd)
			}
			db, err = m.openMySQL(secret)
		case "sqlserver":
			password := os.Getenv(p.PasswordEnv)
			if password == "" {
				return nil, nil, nil, nil, fmt.Errorf("environment variable %s is not set", p.PasswordEnv)
			}
			secrets = append(secrets, password, url.QueryEscape(password), url.PathEscape(password))
			u := url.URL{Scheme: "sqlserver", Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), User: url.UserPassword(p.User, password)}
			q := url.Values{"database": {p.Database}, "encrypt": {"true"}, "TrustServerCertificate": {strconv.FormatBool(p.TrustServerCertificate)}}
			u.RawQuery = q.Encode()
			secrets = append(secrets, u.String())
			db, err = m.openSQLServer(u.String())
		}
		if err != nil {
			return nil, nil, nil, nil, safeErrors(err, secrets)
		}
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, nil, nil, safeErrors(err, secrets)
	}
	finish := func(runErr error) error { conn.Close(); db.Close(); return safeErrors(runErr, secrets) }
	if p.Driver == "sqlite" {
		err = conn.Raw(func(raw any) error {
			a, ok := raw.(sqlite.AuthorizerRegisterer)
			if !ok {
				return errors.New("SQLite read-only authorizer unavailable")
			}
			return a.RegisterAuthorizer(func(action sqlite.AuthorizerActionCode, arg1, arg2, _, _ string) sqlite.AuthorizerReturnCode {
				switch action {
				case sqlite.AuthSelect, sqlite.AuthRead, sqlite.AuthRecursive:
					return sqlite.AuthorizerOK
				case sqlite.AuthFunction:
					if strings.EqualFold(arg2, "load_extension") {
						return sqlite.AuthorizerDeny
					}
					return sqlite.AuthorizerOK
				case sqlite.AuthPragma:
					if strings.EqualFold(arg1, "table_info") {
						return sqlite.AuthorizerOK
					}
					return sqlite.AuthorizerDeny
				default:
					return sqlite.AuthorizerDeny
				}
			})
		})
		if err != nil {
			finish(err)
			return nil, nil, nil, nil, errors.New("could not enforce SQLite read-only access")
		}
		return db, conn, nil, finish, nil
	}
	options := &sql.TxOptions{ReadOnly: p.Driver != "sqlserver"}
	tx, err := conn.BeginTx(ctx, options)
	if err != nil {
		finish(err)
		return nil, nil, nil, nil, errors.New("could not start database inspection transaction")
	}
	return db, conn, tx, func(runErr error) error { _ = tx.Rollback(); return finish(runErr) }, nil
}

func safeError(err error, secret string) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
		if u, e := url.Parse(secret); e == nil && u.User != nil {
			if password, ok := u.User.Password(); ok && password != "" {
				s = strings.ReplaceAll(s, password, "[redacted]")
				s = strings.ReplaceAll(s, url.QueryEscape(password), "[redacted]")
				s = strings.ReplaceAll(s, url.PathEscape(password), "[redacted]")
			}
		}
	}
	return errors.New(s)
}

func safeErrors(err error, secrets []string) error {
	for _, secret := range secrets {
		err = safeError(err, secret)
	}
	return err
}

func queryContext(ctx context.Context, conn *sql.Conn, tx *sql.Tx, statement string, args ...any) (*sql.Rows, error) {
	if tx != nil {
		return tx.QueryContext(ctx, statement, args...)
	}
	return conn.QueryContext(ctx, statement, args...)
}

func bounded(ctx context.Context, conn *sql.Conn, tx *sql.Tx, statement string, limit int, args ...any) (Result, error) {
	if limit <= 0 {
		limit = DefaultRows
	}
	if limit > MaxRows {
		limit = MaxRows
	}
	rows, err := queryContext(ctx, conn, tx, statement, args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Result{}, err
	}
	result := Result{Columns: cols, Rows: make([][]any, 0, min(limit, 32))}
	columnTypes, _ := rows.ColumnTypes()
	header, _ := json.Marshal(cols)
	size := len(header) + 128
	for rows.Next() {
		if len(result.Rows) == limit {
			result.Truncated = true
			break
		}
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return Result{}, err
		}
		for i, v := range values {
			switch x := v.(type) {
			case []byte:
				if i < len(columnTypes) && isTextColumn(columnTypes[i].DatabaseTypeName()) {
					text := string(x)
					if len(text) > 4096 {
						text = text[:4096] + "…"
						result.ValuesTruncated = true
					}
					values[i] = text
				} else if len(x) > 256 {
					values[i] = "<blob " + fmt.Sprint(len(x)) + " bytes>"
					result.ValuesTruncated = true
				} else {
					values[i] = "0x" + hex.EncodeToString(x)
				}
			case string:
				if len(x) > 4096 {
					values[i] = x[:4096] + "…"
					result.ValuesTruncated = true
				}
			case time.Time:
				values[i] = x.Format(time.RFC3339Nano)
			}
		}
		encoded, marshalErr := json.Marshal(values)
		if marshalErr != nil {
			return Result{}, marshalErr
		}
		if size+len(encoded)+1 > MaxBytes {
			result.Truncated = true
			break
		}
		size += len(encoded) + 1
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func isTextColumn(kind string) bool {
	kind = strings.ToUpper(kind)
	for _, marker := range []string{"CHAR", "TEXT", "JSON", "ENUM", "SET", "XML"} {
		if strings.Contains(kind, marker) {
			return true
		}
	}
	return false
}

func validSQL(sqlText string) error {
	s := strings.TrimSpace(sqlText)
	if strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	if s == "" || strings.Contains(s, ";") {
		return errors.New("enter one read-only SQL statement")
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return errors.New("enter a query")
	}
	switch strings.ToUpper(parts[0]) {
	case "SELECT", "WITH", "VALUES":
		return nil
	}
	return errors.New("only SELECT, WITH, and VALUES queries are supported")
}

// validRemoteSQL rejects obvious write and command forms even when they
// begin with SELECT or WITH. Database privileges remain the safety boundary.
func validRemoteSQL(statement, driver string) error {
	if driver == "mysql" && strings.Contains(statement, "/*!") {
		return errors.New("MySQL executable comments are not supported")
	}
	clean := sqlCodeOnly(statement)
	words := strings.FieldsFunc(strings.ToUpper(clean), func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') })
	if len(words) == 0 || words[0] != "SELECT" && words[0] != "WITH" {
		return errors.New("only result-producing SELECT queries are supported for this database")
	}
	seenSelect := false
	for _, word := range words {
		if word == "SELECT" {
			seenSelect = true
		}
		switch word {
		case "INSERT", "UPDATE", "DELETE", "MERGE", "CREATE", "ALTER", "DROP", "TRUNCATE", "EXEC", "EXECUTE", "CALL", "GRANT", "REVOKE", "INTO", "OUTFILE", "DUMPFILE", "LOAD", "OPENROWSET", "OPENQUERY":
			return errors.New("only read-only SELECT queries are supported")
		}
	}
	if !seenSelect {
		return errors.New("query must produce a SELECT result")
	}
	return nil
}

func sqlCodeOnly(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "--") || s[i] == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case strings.HasPrefix(s[i:], "/*"):
			i += 2
			for i < len(s) && !strings.HasPrefix(s[i:], "*/") {
				i++
			}
			if i < len(s) {
				i += 2
			}
			b.WriteByte(' ')
		case s[i] == '\'' || s[i] == '"' || s[i] == '`' || s[i] == '[':
			end := s[i]
			if end == '[' {
				end = ']'
			}
			i++
			for i < len(s) {
				if s[i] == end {
					i++
					if i < len(s) && s[i] == end {
						i++
						continue
					}
					break
				}
				i++
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func (m *Manager) Query(ctx context.Context, name, statement string, limit int, agent bool) (Result, error) {
	p, err := m.profile(name, agent)
	if err != nil {
		return Result{}, err
	}
	if err := validSQL(statement); err != nil {
		return Result{}, err
	}
	if p.Driver == "sqlserver" || p.Driver == "mysql" {
		if err := validRemoteSQL(statement, p.Driver); err != nil {
			return Result{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, conn, tx, finish, err := m.connection(ctx, p)
	if err != nil {
		return Result{}, err
	}
	result, err := bounded(ctx, conn, tx, statement, limit)
	return result, finish(err)
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteFor(driver, s string) string {
	switch driver {
	case "sqlserver":
		return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
	case "mysql":
		return "`" + strings.ReplaceAll(s, "`", "``") + "`"
	default:
		return quoteIdent(s)
	}
}

func (m *Manager) Schema(ctx context.Context, name string, agent bool) ([]Table, error) {
	p, err := m.profile(name, agent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, conn, tx, finish, err := m.connection(ctx, p)
	if err != nil {
		return nil, err
	}
	var statement string
	switch p.Driver {
	case "sqlite":
		statement = `SELECT name,type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`
	case "postgres":
		statement = `SELECT table_schema,table_name,table_type FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_schema,table_name`
	case "mysql":
		statement = `SELECT table_schema,table_name,table_type FROM information_schema.tables WHERE table_schema=DATABASE() AND table_type IN ('BASE TABLE','VIEW') ORDER BY table_schema,table_name`
	case "sqlserver":
		statement = `SELECT table_schema,table_name,table_type FROM information_schema.tables WHERE table_schema NOT IN ('sys','INFORMATION_SCHEMA') AND table_type IN ('BASE TABLE','VIEW') ORDER BY table_schema,table_name`
	}
	rows, err := queryContext(ctx, conn, tx, statement)
	if err != nil {
		return nil, finish(err)
	}
	var out []Table
	for rows.Next() {
		var t Table
		if p.Driver == "sqlite" {
			t.Schema = "main"
			err = rows.Scan(&t.Name, &t.Type)
		} else {
			err = rows.Scan(&t.Schema, &t.Name, &t.Type)
		}
		if err != nil {
			break
		}
		out = append(out, t)
		if len(out) >= 500 {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, finish(err)
	}
	for i := range out {
		var cr *sql.Rows
		switch p.Driver {
		case "sqlite":
			cr, err = queryContext(ctx, conn, tx, `SELECT name,type,"notnull",pk FROM pragma_table_info(?)`, out[i].Name)
		case "postgres":
			cr, err = queryContext(ctx, conn, tx, `SELECT column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, out[i].Schema, out[i].Name)
		case "mysql":
			cr, err = queryContext(ctx, conn, tx, `SELECT column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`, out[i].Schema, out[i].Name)
		case "sqlserver":
			cr, err = queryContext(ctx, conn, tx, `SELECT column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema=@p1 AND table_name=@p2 ORDER BY ordinal_position`, out[i].Schema, out[i].Name)
		}
		if err != nil {
			return nil, finish(err)
		}
		for cr.Next() {
			var c Column
			if p.Driver == "sqlite" {
				var notnull, pk int
				err = cr.Scan(&c.Name, &c.Type, &notnull, &pk)
				c.Nullable = notnull == 0
				c.PrimaryKey = pk > 0
			} else {
				var nullable string
				err = cr.Scan(&c.Name, &c.Type, &nullable)
				c.Nullable = nullable == "YES"
			}
			if err != nil {
				break
			}
			out[i].Columns = append(out[i].Columns, c)
		}
		if err == nil {
			err = cr.Err()
		}
		cr.Close()
		if err != nil {
			return nil, finish(err)
		}
	}
	return out, finish(nil)
}

func (m *Manager) Rows(ctx context.Context, name, schema, table string, offset, limit int, agent bool) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := m.profile(name, agent)
	if err != nil {
		return Result{}, err
	}
	if offset < 0 || offset > 1000000 {
		return Result{}, errors.New("invalid row offset")
	}
	tables, err := m.Schema(ctx, name, agent)
	if err != nil {
		return Result{}, err
	}
	found := false
	for _, t := range tables {
		if t.Schema == schema && t.Name == table {
			found = true
			break
		}
	}
	if !found {
		return Result{}, errors.New("table not found")
	}
	if limit <= 0 {
		limit = DefaultRows
	}
	if limit > MaxRows {
		limit = MaxRows
	}
	_, conn, tx, finish, err := m.connection(ctx, p)
	if err != nil {
		return Result{}, err
	}
	base := `SELECT * FROM ` + quoteFor(p.Driver, schema) + `.` + quoteFor(p.Driver, table)
	statement := base + ` LIMIT ` + fmt.Sprint(limit+1) + ` OFFSET ` + fmt.Sprint(offset)
	var args []any
	switch p.Driver {
	case "mysql":
		statement = base + ` LIMIT ? OFFSET ?`
		args = []any{limit + 1, offset}
	case "sqlserver":
		statement = base + ` ORDER BY (SELECT NULL) OFFSET @p1 ROWS FETCH NEXT @p2 ROWS ONLY`
		args = []any{offset, limit + 1}
	}
	result, err := bounded(ctx, conn, tx, statement, limit, args...)
	if result.Truncated {
		result.NextOffset = offset + len(result.Rows)
	}
	return result, finish(err)
}
