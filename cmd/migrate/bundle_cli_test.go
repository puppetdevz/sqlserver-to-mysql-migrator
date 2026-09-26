package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/bundle"
)

func TestBundleCLIDryRunNoConfigOrTargetConnection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bundle")
	w, e := bundle.Begin(dir, bundle.Manifest{Version: 2, RunID: "dry-run", Source: "fixture/db", Snapshot: "SNAPSHOT", TransactionID: 1, Started: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:00:01Z", Tables: []bundle.Table{{Schema: "dbo", Name: "T", File: "000001.rows", Columns: []bundle.Column{{Name: "id", Type: "int"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Row(0, bundle.Row{{Text: "1"}}); e != nil {
		t.Fatal(e)
	}
	if e = w.Seal(); e != nil {
		t.Fatal(e)
	}
	t.Setenv("GOLDENDB_TARGET_DSN", "user:secret@tcp(203.0.113.42:3306)/fixture?tls=true")
	if code := runBundleCLI([]string{"import-bundle", "--bundle", dir, "--dry-run"}); code != 0 {
		t.Fatal("dry-run must be offline")
	}
	if code := runBundleCLI([]string{"import-bundle", "--bundle", dir}); code == 0 {
		t.Fatal("import without confirmation/report accepted")
	}
	if code := runBundleCLI([]string{"import-bundle", "--bundle", dir, "--config", "config.local.yaml"}); code == 0 {
		t.Fatal("legacy config accepted")
	}
}
func TestOldCSVDirectoryIsNotABundle(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "T.csv"), []byte("1,2\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("GOLDENDB_TARGET_DSN", "user:secret@tcp(203.0.113.42:3306)/fixture?tls=true")
	if code := runBundleCLI([]string{"import-bundle", "--bundle", dir, "--dry-run"}); code == 0 {
		t.Fatal("old CSV directory accepted as bundle")
	}
}
func TestExportRequiresExplicitSourceWithoutTarget(t *testing.T) {
	t.Setenv("SQLSERVER_SOURCE_URL", "")
	if code := runBundleCLI([]string{"export-sqlserver", "--bundle", filepath.Join(t.TempDir(), "new"), "--tables", "dbo.A"}); code == 0 {
		t.Fatal("missing source accepted")
	}
}
