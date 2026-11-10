package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

type dialectCall struct {
	query string
	args  []driver.NamedValue
}
type dialectRecorder struct {
	dsn       string
	readOnly  []bool
	calls     []dialectCall
	fail      string
	textMode  bool
	functions bool
}
type dialectDriver struct{ r *dialectRecorder }

func (d dialectDriver) Open(dsn string) (driver.Conn, error) {
	d.r.dsn = dsn
	return &dialectConn{d.r}, nil
}

type dialectConn struct{ r *dialectRecorder }

func (*dialectConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (*dialectConn) Close() error              { return nil }
func (*dialectConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected Begin") }
func (c *dialectConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	c.r.readOnly = append(c.r.readOnly, o.ReadOnly)
	return fakePGTx{}, nil
}
func (c *dialectConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.r.calls = append(c.r.calls, dialectCall{q, args})
	if c.r.fail != "" {
		return nil, errors.New(c.r.fail)
	}
	switch {
	case strings.Contains(q, "pg_get_functiondef"):
		return &dialectRows{cols: []string{"pg_get_functiondef"}, values: [][]driver.Value{{[]byte("CREATE FUNCTION app.calculate() RETURNS integer")}}}, nil
	case strings.Contains(q, "information_schema.routines"):
		if c.r.functions {
			return &dialectRows{cols: []string{"routine_schema", "routine_name", "routine_type"}, values: [][]driver.Value{{"app", "calculate", "FUNCTION"}}}, nil
		}
		return &dialectRows{cols: []string{"routine_schema", "routine_name", "routine_type"}}, nil
	case strings.Contains(q, "information_schema.tables"):
		return &dialectRows{cols: []string{"table_schema", "table_name", "table_type"}, values: [][]driver.Value{{"app", "order]items", "BASE TABLE"}}}, nil
	case strings.Contains(q, "information_schema.columns") || strings.Contains(q, "pg_attribute"):
		return &dialectRows{cols: []string{"column_name", "data_type", "is_nullable", "is_primary_key"}, values: [][]driver.Value{{"id", "int", "NO", true}}}, nil
	default:
		if c.r.textMode {
			return &dialectRows{cols: []string{"name"}, kinds: []string{"VARCHAR"}, values: [][]driver.Value{{[]byte("hello")}}}, nil
		}
		return &dialectRows{cols: []string{"id"}, values: [][]driver.Value{{int64(1)}, {int64(2)}}}, nil
	}
}

func TestFunctionDefinitionReturnsText(t *testing.T) {
	r := &dialectRecorder{functions: true}
	sql.Register("larik-test-postgres-function-definition", dialectDriver{r})
	t.Setenv("LARIK_TEST_POSTGRES_URL", "postgres://reader:secret@localhost:5432/app")
	m, err := New(map[string]ConnectionConfig{"pg": {Driver: "postgres", URLEnv: "LARIK_TEST_POSTGRES_URL"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.openPostgres = func(dsn string) (*sql.DB, error) { return sql.Open("larik-test-postgres-function-definition", dsn) }
	got, err := m.FunctionDefinition(context.Background(), "pg", "app", "calculate", false)
	if err != nil || got != "CREATE FUNCTION app.calculate() RETURNS integer" {
		t.Fatalf("FunctionDefinition() = %q, %v", got, err)
	}
}

type dialectRows struct {
	cols   []string
	kinds  []string
	values [][]driver.Value
	pos    int
}

func (r *dialectRows) Columns() []string { return r.cols }
func (r *dialectRows) ColumnTypeDatabaseTypeName(i int) string {
	if i < len(r.kinds) {
		return r.kinds[i]
	}
	return ""
}
func (*dialectRows) Close() error { return nil }
func (r *dialectRows) Next(dest []driver.Value) error {
	if r.pos == len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

func TestMySQLDialectAndReadOnly(t *testing.T) {
	r := &dialectRecorder{}
	sql.Register("larik-test-mysql-dialect", dialectDriver{r})
	t.Setenv("LARIK_TEST_MYSQL_DSN", "reader:very_secret@tcp(localhost:3306)/app")
	m, err := New(map[string]ConnectionConfig{"my": {Driver: "mysql", DSNEnv: "LARIK_TEST_MYSQL_DSN", AgentAccess: true}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.openMySQL = func(dsn string) (*sql.DB, error) { return sql.Open("larik-test-mysql-dialect", dsn) }
	tables, err := m.Schema(context.Background(), "my", true)
	if err != nil || len(tables) != 1 || tables[0].Columns[0].Name != "id" || !tables[0].Columns[0].PrimaryKey {
		t.Fatalf("schema: %+v %v", tables, err)
	}
	result, err := m.Rows(context.Background(), "my", "app", "order]items", 3, 1, true)
	if err != nil || !result.Truncated || result.NextOffset != 4 {
		t.Fatalf("rows: %+v %v", result, err)
	}
	if r.dsn != "reader:very_secret@tcp(localhost:3306)/app" {
		t.Fatalf("DSN not passed to driver")
	}
	for _, readonly := range r.readOnly {
		if !readonly {
			t.Fatal("MySQL transaction was writable")
		}
	}
	last := r.calls[len(r.calls)-1]
	if !strings.Contains(last.query, "`app`.`order]items` LIMIT ? OFFSET ?") || len(last.args) != 2 || last.args[0].Value != int64(2) || last.args[1].Value != int64(3) {
		t.Fatalf("MySQL paging: %+v", last)
	}
	r.textMode = true
	text, err := m.Query(context.Background(), "my", "SELECT name FROM items", 1, true)
	if err != nil || text.Rows[0][0] != "hello" {
		t.Fatalf("MySQL text result: %+v %v", text, err)
	}
	r.textMode = false
	r.fail = "driver failure: very_secret"
	if _, err := m.Query(context.Background(), "my", "SELECT 1", 1, true); err == nil || strings.Contains(err.Error(), "very_secret") {
		t.Fatalf("password in error: %v", err)
	}
}

func TestPostgresDirectPasswordAndSSLMode(t *testing.T) {
	r := &dialectRecorder{}
	sql.Register("larik-test-postgres-direct-password", dialectDriver{r})
	m, err := New(map[string]ConnectionConfig{"pg": {
		Driver: "postgres", Host: "localhost", Port: 5432, Database: "app", User: "reader",
		Password: "p@ss word", SSLMode: "verify-full",
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.openPostgres = func(dsn string) (*sql.DB, error) { return sql.Open("larik-test-postgres-direct-password", dsn) }
	if _, err := m.Schema(context.Background(), "pg", false); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(r.dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != "p@ss word" || u.Query().Get("sslmode") != "verify-full" {
		t.Fatalf("PostgreSQL DSN: %q", r.dsn)
	}
}

func TestMySQLDirectPasswordAndTLSMode(t *testing.T) {
	r := &dialectRecorder{}
	sql.Register("larik-test-mysql-direct-password", dialectDriver{r})
	m, err := New(map[string]ConnectionConfig{"my": {
		Driver: "mysql", Host: "localhost", Port: 3306, Database: "app", User: "reader",
		Password: "very_secret", SSLMode: "required",
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.openMySQL = func(dsn string) (*sql.DB, error) { return sql.Open("larik-test-mysql-direct-password", dsn) }
	if _, err := m.Schema(context.Background(), "my", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(r.dsn)
	if err != nil || cfg.Passwd != "very_secret" || cfg.TLSConfig != "true" {
		t.Fatalf("MySQL DSN: %q %v", r.dsn, err)
	}
}

func TestSQLServerDialectAndCredentials(t *testing.T) {
	r := &dialectRecorder{}
	sql.Register("larik-test-sqlserver-dialect", dialectDriver{r})
	t.Setenv("LARIK_TEST_MSSQL_PASSWORD", "very_secret")
	p := ConnectionConfig{Driver: "sqlserver", Host: "localhost", Port: 1433, Database: "app", User: "reader", PasswordEnv: "LARIK_TEST_MSSQL_PASSWORD", AgentAccess: true}
	m, err := New(map[string]ConnectionConfig{"ms": p}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.openSQLServer = func(dsn string) (*sql.DB, error) { return sql.Open("larik-test-sqlserver-dialect", dsn) }
	result, err := m.Rows(context.Background(), "ms", "app", "order]items", 3, 1, true)
	if err != nil || result.NextOffset != 4 {
		t.Fatalf("rows: %+v %v", result, err)
	}
	if !strings.Contains(r.dsn, "encrypt=true") || !strings.Contains(r.dsn, "TrustServerCertificate=false") || !strings.Contains(r.dsn, "reader:very_secret@localhost:1433") {
		t.Fatalf("SQL Server DSN: %q", r.dsn)
	}
	for _, readonly := range r.readOnly {
		if readonly {
			t.Fatal("SQL Server driver received unsupported ReadOnly option")
		}
	}
	last := r.calls[len(r.calls)-1]
	if !strings.Contains(last.query, "[app].[order]]items] ORDER BY (SELECT NULL) OFFSET @p1 ROWS FETCH NEXT @p2 ROWS ONLY") || len(last.args) != 2 || last.args[0].Value != int64(3) || last.args[1].Value != int64(2) {
		t.Fatalf("SQL Server paging: %+v", last)
	}
	r.fail = "driver failure: very_secret"
	if _, err := m.Query(context.Background(), "ms", "SELECT 1", 1, true); err == nil || strings.Contains(err.Error(), "very_secret") {
		t.Fatalf("password in error: %v", err)
	}
	t.Setenv("LARIK_TEST_MSSQL_PASSWORD", "p@ssword")
	r.fail = "driver failure: p%40ssword"
	if _, err := m.Query(context.Background(), "ms", "SELECT 1", 1, true); err == nil || strings.Contains(err.Error(), "p%40ssword") {
		t.Fatalf("encoded password in error: %v", err)
	}
	p.TrustServerCertificate = true
	if err := m.Replace(map[string]ConnectionConfig{"ms": p}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Query(context.Background(), "ms", "SELECT 1", 1, true); err == nil || !strings.Contains(r.dsn, "TrustServerCertificate=true") {
		t.Fatalf("trust option: %q %v", r.dsn, err)
	}
}

func TestRemoteQueryGuard(t *testing.T) {
	for _, driver := range []string{"mysql", "sqlserver"} {
		for _, q := range []string{"SELECT * INTO copied FROM notes", "WITH x AS (SELECT 1) DELETE FROM notes", "SELECT 1; DROP TABLE notes", "SELECT * FROM OPENROWSET(foo)"} {
			if err := validSQL(q); err == nil {
				if err = validRemoteSQL(q, driver); err == nil {
					t.Errorf("%s accepted %q", driver, q)
				}
			}
		}
		if err := validRemoteSQL("SELECT 'INTO', [DELETE] FROM notes", driver); err != nil {
			t.Errorf("%s rejected quoted text: %v", driver, err)
		}
	}
	if err := validRemoteSQL("SELECT 1 /*! INTO OUTFILE '/tmp/x' */", "mysql"); err == nil {
		t.Fatal("MySQL executable comment accepted")
	}
}
