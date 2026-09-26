package target

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	w, e := bundle.Begin(dir, bundle.Manifest{Version: 2, RunID: "run-mock", Source: "src/db", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []bundle.Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []bundle.Column{{Name: "id", Type: "int"}, {Name: "value", Type: "nvarchar(10)", Nullable: true}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Row(0, bundle.Row{{Text: "1"}, {Text: ""}}); e != nil {
		t.Fatal(e)
	}
	if e = w.Seal(); e != nil {
		t.Fatal(e)
	}
	return dir
}
func TestSameCountDifferentValueLeavesUnpublished(t *testing.T) {
	dir := fixture(t)
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"table_name"}))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"database", "mode", "strict"}).AddRow("db", "STRICT_ALL_TABLES", 1))
	m.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SHOW WARNINGS").WillReturnRows(sqlmock.NewRows([]string{"Level", "Code", "Message"}))
	m.ExpectQuery("information_schema.columns").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "nullable", "ordinal", "length", "precision", "scale", "dt", "column_type"}).AddRow("id", "int", "NO", 1, nil, nil, nil, nil, "int(11)").AddRow("value", "varchar", "YES", 2, 10, nil, nil, nil, "varchar(10)"))
	m.ExpectQuery("information_schema.statistics").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "unique", "column", "seq"}))
	m.ExpectExec("INSERT INTO").WithArgs("1", "").WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectQuery("SHOW WARNINGS").WillReturnRows(sqlmock.NewRows([]string{"Level", "Code", "Message"}))
	m.ExpectQuery("SELECT .* FROM `_migration_").WillReturnRows(sqlmock.NewRows([]string{"id", "value"}).AddRow("1", "changed"))
	e = Import(context.Background(), db, dir, "db", false)
	if e == nil || !strings.Contains(e.Error(), "content mismatch") {
		t.Fatalf("expected content mismatch, got %v", e)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestWriteErrorNeverRetries(t *testing.T) {
	dir := fixture(t)
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"name"}))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "mode", "strict"}).AddRow("db", "STRICT_ALL_TABLES", 1))
	m.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SHOW WARNINGS").WillReturnRows(sqlmock.NewRows([]string{"l", "c", "m"}))
	m.ExpectQuery("information_schema.columns").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "nullable", "ordinal", "length", "precision", "scale", "dt", "column_type"}).AddRow("id", "int", "NO", 1, nil, nil, nil, nil, "int(11)").AddRow("value", "varchar", "YES", 2, 10, nil, nil, nil, "varchar(10)"))
	m.ExpectQuery("information_schema.statistics").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "unique", "column", "seq"}))
	m.ExpectExec("INSERT INTO").WithArgs("1", "").WillReturnError(errors.New("connection lost after Exec"))
	report := filepath.Join(t.TempDir(), "report.json")
	manifest, _ := bundle.Verify(dir)
	identity := "tcp/host/db/user"
	if e = ImportReported(context.Background(), db, dir, "db", identity, PlanHash(manifest, identity), report); e == nil {
		t.Fatal("uncertain write accepted")
	}
	b, e := os.ReadFile(report)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "staged_verified_not_published") || strings.Contains(string(b), `"verified_stages": [`) {
		t.Fatal("false completion")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestStagingSuccessStillNotPublished(t *testing.T) {
	dir := fixture(t)
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"name"}))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "mode", "strict"}).AddRow("db", "STRICT_ALL_TABLES", 1))
	m.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SHOW WARNINGS").WillReturnRows(sqlmock.NewRows([]string{"level", "code", "message"}))
	m.ExpectQuery("information_schema.columns").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "nullable", "ordinal", "length", "precision", "scale", "dt", "column_type"}).AddRow("id", "int", "NO", 1, nil, nil, nil, nil, "int(11)").AddRow("value", "varchar", "YES", 2, 10, nil, nil, nil, "varchar(10)"))
	m.ExpectQuery("information_schema.statistics").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "unique", "column", "seq"}))
	m.ExpectExec("INSERT INTO").WithArgs("1", "").WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectQuery("SHOW WARNINGS").WillReturnRows(sqlmock.NewRows([]string{"level", "code", "message"}))
	m.ExpectQuery("SELECT .* FROM `_migration_").WillReturnRows(sqlmock.NewRows([]string{"id", "value"}).AddRow("1", ""))
	report := filepath.Join(t.TempDir(), "report.json")
	manifest, _ := bundle.Verify(dir)
	identity := "tcp/host/db/user"
	if e = ImportReported(context.Background(), db, dir, "db", identity, PlanHash(manifest, identity), report); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(report)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), `"published": false`) || !strings.Contains(string(b), "staged_verified_not_published") {
		t.Fatalf("bad status: %s", b)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestDDLWarningStopsBeforeInsert(t *testing.T) {
	dir := fixture(t)
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"name"}))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "mode", "strict"}).AddRow("db", "STRICT_ALL_TABLES", 1))
	m.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SHOW WARNINGS").WillReturnRows(sqlmock.NewRows([]string{"level", "code", "message"}).AddRow("Warning", 1265, "truncated"))
	report := filepath.Join(t.TempDir(), "report.json")
	manifest, _ := bundle.Verify(dir)
	identity := "tcp/host/db/user"
	e = ImportReported(context.Background(), db, dir, "db", identity, PlanHash(manifest, identity), report)
	if e == nil {
		t.Fatal("warning accepted")
	}
	b, e := os.ReadFile(report)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), `"published": false`) || strings.Contains(string(b), "staged_verified_not_published") {
		t.Fatalf("false completion: %s", b)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestDuplicateKeyIsNotSuccessful(t *testing.T) {
	dir := fixture(t)
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"name"}))
	m.ExpectQuery("SELECT DATABASE").WillReturnRows(sqlmock.NewRows([]string{"db", "mode", "strict"}).AddRow("db", "STRICT_ALL_TABLES", 1))
	m.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectQuery("SHOW WARNINGS").WillReturnRows(sqlmock.NewRows([]string{"l", "c", "m"}))
	m.ExpectQuery("information_schema.columns").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "nullable", "ordinal", "length", "precision", "scale", "dt", "column_type"}).AddRow("id", "int", "NO", 1, nil, nil, nil, nil, "int(11)").AddRow("value", "varchar", "YES", 2, 10, nil, nil, nil, "varchar(10)"))
	m.ExpectQuery("information_schema.statistics").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"name", "unique", "column", "seq"}))
	m.ExpectExec("INSERT INTO").WithArgs("1", "").WillReturnError(errors.New("Duplicate entry"))
	report := filepath.Join(t.TempDir(), "report.json")
	manifest, _ := bundle.Verify(dir)
	identity := "tcp/host/db/user"
	if e = ImportReported(context.Background(), db, dir, "db", identity, PlanHash(manifest, identity), report); e == nil {
		t.Fatal("duplicate key accepted")
	}
	b, e := os.ReadFile(report)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "staged_verified_not_published") || strings.Contains(string(b), `"published": true`) {
		t.Fatal(string(b))
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestPlanBindsManifestAndDestination(t *testing.T) {
	m, e := bundle.Verify(fixture(t))
	if e != nil {
		t.Fatal(e)
	}
	p := PlanHash(m, "tcp/host/db/user")
	if p == PlanHash(m, "tcp/other/db/user") {
		t.Fatal("target identity lost")
	}
	m.RunID = "new-run"
	if p == PlanHash(m, "tcp/host/db/user") {
		t.Fatal("different snapshot/run reused")
	}
	m.RunID = "run-mock"
	m.Tables[0].Data.Count++
	if p == PlanHash(m, "tcp/host/db/user") {
		t.Fatal("manifest content lost")
	}
}
