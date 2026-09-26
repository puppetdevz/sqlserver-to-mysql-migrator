package source

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestFixturePortMustMatchContainer(t *testing.T) {
	if fixturePortMatches("sqlserver://u:p@127.0.0.1:1434?database=pi_migration_fixture", "127.0.0.1:1433") {
		t.Fatal("accepted unrelated local port")
	}
}
func TestFixtureDSNRejectsNonIsolatedSource(t *testing.T) {
	for _, dsn := range []string{"sqlserver://sa:secret@db.example:1433?database=pi_migration_fixture", "sqlserver://sa:secret@127.0.0.1:1433?database=migration_example"} {
		if err := validateSQLServerFixtureDSN(dsn); err == nil {
			t.Errorf("accepted non-fixture DSN %q", dsn)
		}
	}
}
func TestSourceRejectsNumericTrustBypass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := Open(ctx, "sqlserver://user:secret@203.0.113.42:1433?database=fixture&encrypt=true&TrustServerCertificate=1")
	if err == nil || !strings.Contains(err.Error(), "verified certificate") {
		t.Fatalf("unsafe TLS setting not rejected: %v", err)
	}
}
func TestSnapshotDisabledFailsBeforeExport(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.ExpectQuery("snapshot_isolation_state").WillReturnRows(sqlmock.NewRows([]string{"snapshot_isolation_state"}).AddRow(0))
	if e = Export(context.Background(), db, []string{"dbo.T"}, t.TempDir()); e == nil {
		t.Fatal("accepted non-snapshot source")
	}
	if e = m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestUnsupportedType(t *testing.T) {
	for _, s := range []string{"text", "xml", "datetime", "money", "nvarchar(4001)", "datetime2(7)", "varchar"} {
		if Type(s, 0, 0, 0) != "" {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestIdentityColumnRejected(t *testing.T) {
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
	m.ExpectQuery("sys.foreign_keys").WithArgs("dbo.T").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	m.ExpectQuery("sys.columns c").WithArgs("dbo.T").WillReturnRows(sqlmock.NewRows([]string{"name", "type", "length", "precision", "scale", "nullable", "identity", "computed", "default", "alias", "collation"}).AddRow("id", "int", 4, 10, 0, false, true, false, false, false, nil))
	m.ExpectRollback()
	dir := t.TempDir() + "/out"
	if e = Export(context.Background(), db, []string{"dbo.T"}, dir); e == nil {
		t.Fatal("identity accepted")
	}
	if _, e = os.Stat(dir + "/manifest.json"); !os.IsNotExist(e) && e == nil {
		t.Fatal("sealed identity export")
	}
}
