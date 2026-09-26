package source

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
)

func TestExportTwoTablesSameTransaction(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("snapshot_isolation_state").WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(1))
	m.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(1))
	m.ExpectBegin()
	m.ExpectQuery("SERVERPROPERTY").WillReturnRows(sqlmock.NewRows([]string{"server", "database"}).AddRow("test-server", "test-db"))
	m.ExpectQuery("CURRENT_TRANSACTION_ID").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(47))
	for _, name := range []string{"dbo.A", "dbo.B"} {
		expectMeta(m, name)
	}
	m.ExpectQuery("sys.objects o").WillReturnRows(sqlmock.NewRows([]string{"type", "schema", "name", "parent_schema", "parent_name"}).AddRow("U", "dbo", "A", "", "").AddRow("U", "dbo", "B", "", "").AddRow("V", "dbo", "view1", "", ""))
	for _, name := range []string{"A", "B"} {
		m.ExpectQuery("SELECT COUNT_BIG").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		m.ExpectQuery("SELECT ").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		_ = name
	}
	for _, name := range []string{"dbo.A", "dbo.B"} {
		expectMeta(m, name)
	}
	m.ExpectQuery("CURRENT_TRANSACTION_ID").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(47))
	m.ExpectCommit()
	dir := filepath.Join(t.TempDir(), "out")
	if e = Export(context.Background(), db, []string{"dbo.A", "dbo.B"}, dir); e != nil {
		t.Fatal(e)
	}
	got, e := bundle.Verify(dir)
	if e != nil {
		t.Fatal(e)
	}
	if got.TransactionID != 47 || len(got.Tables) != 2 || got.Tables[0].Data.Count != 1 || got.Tables[1].Data.Count != 1 {
		t.Fatal(got)
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestCanceledExportNeverSeals(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("snapshot_isolation_state").WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow(1))
	m.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(1))
	m.ExpectBegin()
	m.ExpectQuery("SERVERPROPERTY").WillReturnRows(sqlmock.NewRows([]string{"server", "db"}).AddRow("s", "d"))
	m.ExpectQuery("CURRENT_TRANSACTION_ID").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	expectMeta(m, "dbo.T")
	m.ExpectQuery("sys.objects o").WillReturnRows(sqlmock.NewRows([]string{"type", "schema", "name", "parent_schema", "parent_name"}))
	m.ExpectQuery("SELECT COUNT_BIG").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	m.ExpectQuery("SELECT ").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	ctx, cancel := context.WithCancel(context.Background())
	dir := filepath.Join(t.TempDir(), "canceled")
	e = exportWithObserver(ctx, db, []string{"dbo.T"}, dir, 1024, func(int) error { cancel(); return ctx.Err() })
	if e == nil {
		t.Fatal("accepted canceled export")
	}
	if _, e = bundle.Verify(dir); e == nil {
		t.Fatal("sealed canceled export")
	}
}
func TestObserverRunsBetweenTablesInsideSameSnapshot(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("snapshot_isolation_state").WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow(1))
	m.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(1))
	m.ExpectBegin()
	m.ExpectQuery("SERVERPROPERTY").WillReturnRows(sqlmock.NewRows([]string{"server", "db"}).AddRow("s", "d"))
	m.ExpectQuery("CURRENT_TRANSACTION_ID").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	expectMeta(m, "dbo.A")
	expectMeta(m, "dbo.B")
	m.ExpectQuery("sys.objects o").WillReturnRows(sqlmock.NewRows([]string{"type", "schema", "name", "parent_schema", "parent_name"}))
	for range 2 {
		m.ExpectQuery("SELECT COUNT_BIG").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
		m.ExpectQuery("SELECT ").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	}
	expectMeta(m, "dbo.A")
	expectMeta(m, "dbo.B")
	m.ExpectQuery("CURRENT_TRANSACTION_ID").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	m.ExpectCommit()
	called := 0
	dir := filepath.Join(t.TempDir(), "observer")
	if e = exportWithObserver(context.Background(), db, []string{"dbo.A", "dbo.B"}, dir, 1024, func(i int) error {
		called++
		if i != called-1 {
			t.Fatal("table order")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if called != 2 {
		t.Fatal("observer never called")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestSchemaDriftNeverSeals(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("snapshot_isolation_state").WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow(1))
	m.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"p"}).AddRow(1))
	m.ExpectBegin()
	m.ExpectQuery("SERVERPROPERTY").WillReturnRows(sqlmock.NewRows([]string{"server", "db"}).AddRow("s", "d"))
	m.ExpectQuery("CURRENT_TRANSACTION_ID").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	expectMeta(m, "dbo.T")
	m.ExpectQuery("sys.objects o").WillReturnRows(sqlmock.NewRows([]string{"type", "schema", "name", "parent_schema", "parent_name"}))
	m.ExpectQuery("SELECT COUNT_BIG").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	m.ExpectQuery("SELECT ").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	m.ExpectQuery("sys.foreign_keys").WithArgs("dbo.T").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	m.ExpectQuery("sys.columns c").WithArgs("dbo.T").WillReturnRows(sqlmock.NewRows([]string{"name", "type", "length", "precision", "scale", "nullable", "identity", "computed", "default", "alias", "collation"}).AddRow("other", "int", 4, 10, 0, false, false, false, false, false, nil))
	m.ExpectQuery("sys.indexes i").WithArgs("dbo.T").WillReturnRows(sqlmock.NewRows([]string{"name", "unique", "primary", "filter", "kind", "included", "column", "ordinal", "descending", "disabled"}))
	m.ExpectQuery("COUNT\\(\\*\\) FROM sys.indexes").WithArgs("dbo.T").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	m.ExpectRollback()
	dir := filepath.Join(t.TempDir(), "drift")
	if e = Export(context.Background(), db, []string{"dbo.T"}, dir); e == nil {
		t.Fatal("drift accepted")
	}
	if _, e = bundle.Verify(dir); e == nil {
		t.Fatal("drift sealed")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestExportReadErrorNeverSeals(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("snapshot_isolation_state").WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(1))
	m.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(1))
	m.ExpectBegin()
	m.ExpectQuery("SERVERPROPERTY").WillReturnRows(sqlmock.NewRows([]string{"server", "db"}).AddRow("s", "d"))
	m.ExpectQuery("CURRENT_TRANSACTION_ID").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	expectMeta(m, "dbo.T")
	m.ExpectQuery("sys.objects o").WillReturnRows(sqlmock.NewRows([]string{"type", "schema", "name", "parent_schema", "parent_name"}))
	m.ExpectQuery("SELECT COUNT_BIG").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))
	m.ExpectQuery("SELECT ").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	m.ExpectRollback()
	dir := filepath.Join(t.TempDir(), "out")
	if e = Export(context.Background(), db, []string{"dbo.T"}, dir); e == nil {
		t.Fatal("expected count mismatch")
	}
	if _, e = bundle.Verify(dir); e == nil {
		t.Fatal("sealed failed export")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func expectMeta(m sqlmock.Sqlmock, name string) {
	m.ExpectQuery("sys.foreign_keys").WithArgs(name).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	m.ExpectQuery("sys.columns c").WithArgs(name).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "length", "precision", "scale", "nullable", "identity", "computed", "default", "alias", "collation"}).AddRow("id", "int", 4, 10, 0, false, false, false, false, false, nil))
	m.ExpectQuery("sys.indexes i").WithArgs(name).WillReturnRows(sqlmock.NewRows([]string{"name", "unique", "primary", "filter", "kind", "included", "column", "ordinal", "descending", "disabled"}))
	m.ExpectQuery("COUNT\\(\\*\\) FROM sys.indexes").WithArgs(name).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
}
