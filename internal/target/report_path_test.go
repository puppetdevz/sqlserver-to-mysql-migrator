package target

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
)

func TestImportReportedRejectsReportInsideBundleViaSymlink(t *testing.T) {
	dir := fixture(t)
	alias := filepath.Join(t.TempDir(), "bundle-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manifest, err := bundle.Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity := "tcp/host/db/user"
	// This must fail before accessing the DB or writing a report.
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("information_schema.tables").WillReturnError(os.ErrPermission)
	report := filepath.Join(alias, "report.json")
	if err := ImportReported(context.Background(), db, dir, "db", identity, PlanHash(manifest, identity), report); err == nil || !strings.Contains(err.Error(), "report must be outside sealed bundle") {
		t.Fatalf("expected bundle boundary rejection, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "report.json")); !os.IsNotExist(err) {
		t.Fatalf("bundle was modified, report stat: %v", err)
	}
	if _, err := bundle.Verify(dir); err != nil {
		t.Fatalf("sealed bundle was corrupted: %v", err)
	}
}

func TestImportReportedRejectsBundleAliasViaSymlink(t *testing.T) {
	dir := fixture(t)
	alias := filepath.Join(t.TempDir(), "bundle-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manifest, err := bundle.Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity := "tcp/host/db/user"
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("information_schema.tables").WillReturnError(os.ErrPermission)
	report := filepath.Join(dir, "report.json")
	if err := ImportReported(context.Background(), db, alias, "db", identity, PlanHash(manifest, identity), report); err == nil || !strings.Contains(err.Error(), "report must be outside sealed bundle") {
		t.Fatalf("expected bundle boundary rejection, got %v", err)
	}
	if _, err := os.Lstat(report); !os.IsNotExist(err) {
		t.Fatalf("bundle was modified, report stat: %v", err)
	}
}
