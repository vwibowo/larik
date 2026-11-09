package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectStatementQuotesQualifiedObjectName(t *testing.T) {
	tests := []struct {
		driver, schema, object, want string
	}{
		{"postgres", "order", `weird"name`, `SELECT * FROM "order"."weird""name";`},
		{"mysql", "order", "weird`name", "SELECT * FROM `order`.`weird``name`;"},
		{"sqlserver", "order", "weird]name", "SELECT * FROM [order].[weird]]name];"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			if got := SelectStatement(tt.driver, tt.schema, tt.object); got != tt.want {
				t.Fatalf("SelectStatement() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTableCreationStatementIncludesStructureAndQuotes(t *testing.T) {
	object := Table{Schema: "app", Name: "order]items", Columns: []Column{
		{Name: "order", Type: "integer", PrimaryKey: true},
		{Name: "display name", Type: "varchar(80)", Nullable: true},
	}}
	tests := []struct {
		driver, want string
	}{
		{"postgres", "CREATE TABLE \"app\".\"order]items\" (\n  \"order\" integer,\n  \"display name\" varchar(80),\n  PRIMARY KEY (\"order\")\n);"},
		{"sqlserver", "CREATE TABLE [app].[order]]items] (\n  [order] integer,\n  [display name] varchar(80),\n  PRIMARY KEY ([order])\n);"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			got, err := tableCreationStatement(tt.driver, object.Schema, object)
			if err != nil || got != tt.want {
				t.Fatalf("tableCreationStatement() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCreationStatementUsesNativeSQLiteDDL(t *testing.T) {
	m, path := fixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE VIEW note_view AS SELECT id, body FROM notes`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, want string }{
		{"notes", `CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)`},
		{"note_view", `CREATE VIEW note_view AS SELECT id, body FROM notes`},
	} {
		got, err := m.CreationStatement(context.Background(), "local", "main", tt.name, true)
		if err != nil || got != tt.want {
			t.Errorf("CreationStatement(%s) = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

func fixture(t *testing.T) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes(body) VALUES ('hello'),('world')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	m, err := New(map[string]ConnectionConfig{"local": {Driver: "sqlite", Path: path, AgentAccess: true}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return m, path
}

func TestSQLiteInspectionAndReadOnly(t *testing.T) {
	m, path := fixture(t)
	ctx := context.Background()
	tables, err := m.Schema(ctx, "local", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Name != "notes" || len(tables[0].Columns) != 2 {
		t.Fatalf("schema: %#v", tables)
	}
	page, err := m.Rows(ctx, "local", "main", "notes", 0, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || !page.Truncated || page.NextOffset != 1 {
		t.Fatalf("first page: %#v", page)
	}
	page, err = m.Rows(ctx, "local", "main", "notes", 1, 1, true)
	if err != nil || len(page.Rows) != 1 || page.Truncated {
		t.Fatalf("second page: %#v %v", page, err)
	}
	result, err := m.Query(ctx, "local", "SELECT body FROM notes ORDER BY id", 10, true)
	if err != nil || len(result.Rows) != 2 || result.Rows[0][0] != "hello" {
		t.Fatalf("query: %#v %v", result, err)
	}
	for _, statement := range []string{
		`UPDATE notes SET body='changed'`, `WITH x AS (SELECT 1) UPDATE notes SET body='changed'`,
		`SELECT * FROM notes; DELETE FROM notes`, `ATTACH DATABASE '/tmp/new.sqlite' AS other`,
		`PRAGMA writable_schema=ON`,
	} {
		if _, err := m.Query(ctx, "local", statement, 10, true); err == nil {
			t.Errorf("accepted %q", statement)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	result, err = m.Query(ctx, "local", "SELECT count(*) FROM notes", 1, true)
	if err != nil || result.Rows[0][0] != int64(2) {
		t.Fatalf("database changed: %#v %v", result, err)
	}
}

func TestAgentOptInAndMissingFile(t *testing.T) {
	m, path := fixture(t)
	if err := m.Replace(map[string]ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	if len(m.List(true)) != 0 {
		t.Fatal("agent sees private profile")
	}
	if _, err := m.Query(context.Background(), "local", "SELECT 1", 1, true); err == nil {
		t.Fatal("agent queried private profile")
	}
	if _, err := m.Query(context.Background(), "local", "SELECT 1", 1, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Replace(map[string]ConnectionConfig{"local": {Driver: "sqlite", Path: path + ".missing"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Query(context.Background(), "local", "SELECT 1", 1, false); err == nil {
		t.Fatal("created missing file")
	}
	if _, err := os.Stat(path + ".missing"); !os.IsNotExist(err) {
		t.Fatalf("missing file created: %v", err)
	}
}

func TestQueryLimitsAndCancellation(t *testing.T) {
	m, path := fixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<400) INSERT INTO notes(body) SELECT printf('%04096d',x) FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	r, err := m.Query(context.Background(), "local", "SELECT body FROM notes", 999, true)
	if err != nil || !r.Truncated || len(r.Rows) > MaxRows || len(r.Rows) == 0 {
		t.Fatalf("limits: %d %+v %v", len(r.Rows), r, err)
	}
	long, err := m.Query(context.Background(), "local", "SELECT printf('%05000d',1)", 1, true)
	if err != nil || !long.ValuesTruncated || len(long.Rows[0][0].(string)) <= 4096 {
		t.Fatalf("shortened value was not marked: %+v %v", long, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Query(ctx, "local", "SELECT * FROM notes", 1, true); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestPostgresCredentialRedaction(t *testing.T) {
	m, err := New(map[string]ConnectionConfig{"remote": {Driver: "postgres", URLEnv: "LARIK_DB_TEST_URL"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARIK_DB_TEST_URL", "postgres://user:very_secret@127.0.0.1:1/db?connect_timeout=1")
	_, err = m.Query(context.Background(), "remote", "SELECT 1", 1, false)
	if err == nil || strings.Contains(err.Error(), "very_secret") {
		t.Fatalf("credential in error: %v", err)
	}
}

type fakePGDriver struct{ readOnly *bool }

func (d fakePGDriver) Open(string) (driver.Conn, error) {
	return &fakePGConn{readOnly: d.readOnly}, nil
}

type fakePGConn struct{ readOnly *bool }

func (*fakePGConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (*fakePGConn) Close() error                        { return nil }
func (*fakePGConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }
func (c *fakePGConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	*c.readOnly = opts.ReadOnly
	return fakePGTx{}, nil
}
func (*fakePGConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return &fakePGRows{}, nil
}

type fakePGTx struct{}

func (fakePGTx) Commit() error   { return errors.New("read-only query committed") }
func (fakePGTx) Rollback() error { return nil }

type fakePGRows struct{ done bool }

func (*fakePGRows) Columns() []string { return []string{"answer"} }
func (*fakePGRows) Close() error      { return nil }
func (r *fakePGRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	dest[0] = int64(42)
	r.done = true
	return nil
}

func TestPostgresUsesReadOnlyTransaction(t *testing.T) {
	readOnly := false
	sql.Register("larik-test-pg-readonly", fakePGDriver{readOnly: &readOnly})
	m, err := New(map[string]ConnectionConfig{"remote": {Driver: "postgres", URLEnv: "LARIK_DB_TEST_URL", AgentAccess: true}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.openPostgres = func(string) (*sql.DB, error) { return sql.Open("larik-test-pg-readonly", "") }
	t.Setenv("LARIK_DB_TEST_URL", "postgres://test@localhost/example")
	r, err := m.Query(context.Background(), "remote", "SELECT 42", 1, true)
	if err != nil || !readOnly || len(r.Rows) != 1 || r.Rows[0][0] != int64(42) {
		t.Fatalf("read-only transaction: %#v, %v, readOnly=%v", r, err, readOnly)
	}
}
