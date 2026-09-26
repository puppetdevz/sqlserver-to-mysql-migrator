package target

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
	"path/filepath"
	"testing"
)

func TestMySQLFixtureRejectsUnrelatedLoopbackPort(t *testing.T) {
	if fixtureMatchesPort("127.0.0.1:3307", "127.0.0.1:3306") {
		t.Fatal("accepted unrelated local server")
	}
}
func TestPreflightRefusesExistingWithoutWrites(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	dir := filepath.Join(t.TempDir(), "b")
	w, e := bundle.Begin(dir, bundle.Manifest{Version: 2, RunID: "run1", Source: "src/db", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []bundle.Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []bundle.Column{{Name: "id", Type: "int"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Row(0, bundle.Row{{Text: "1"}}); e != nil {
		t.Fatal(e)
	}
	if e = w.Seal(); e != nil {
		t.Fatal(e)
	}
	m.ExpectQuery("information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("T"))
	if e = Import(context.Background(), db, dir, "db", false); e == nil {
		t.Fatal("overwrote existing table")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
