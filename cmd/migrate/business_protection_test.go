package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/tablescope"
)

func TestMissingCSVStillTruncatesExistingTableAndSucceeds(t *testing.T) {
	for _, adaptive := range []bool{true, false} {
		t.Run(adaptiveLabel(adaptive), func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			csvDir := filepath.Join(dir, "csv")
			if err := os.MkdirAll(csvDir, 0755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			ddlPath := writeTempDDL(t, dir, "T")

			conn, mock := newMockConnection(t)
			expectShowTables(mock, "T")
			expectTruncate(mock, "T")

			cfg := testMigrationConfig(ddlPath, csvDir, adaptive)
			tracker := newTracker(t)
			err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{})
			if err != nil {
				t.Fatalf("runMigration() error = %v, want nil", err)
			}

			state := tracker.GetTableState("T")
			if state == nil {
				t.Fatal("table T state is nil")
			}
			if state.Status != progress.StatusSkipped {
				t.Fatalf("table T status = %s, want %s", state.Status, progress.StatusSkipped)
			}
			assertTableNotInFile(t, failedTablesFile, "T")
			assertTableNotInFile(t, completedTablesFile, "T")
			assertTableNotInFile(t, rowCountMismatchFile, "T")

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

func TestMissingCSVSkipTablesDoesNotTruncate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ddlPath := writeTempDDL(t, dir, "T")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	cfg.Migration.SkipTables = []string{"T"}
	tracker := newTracker(t)
	if err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{}); err != nil {
		t.Fatalf("runMigration() error = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMissingCSVCompletedListDoesNotTruncate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ddlPath := writeTempDDL(t, dir, "T")
	if err := os.WriteFile(completedTablesFile, []byte("T\n"), 0644); err != nil {
		t.Fatalf("WriteFile completed list: %v", err)
	}

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	if err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{}); err != nil {
		t.Fatalf("runMigration() error = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMissingCSVDryRunDoesNotTruncate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ddlPath := writeTempDDL(t, dir, "T")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	if err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{DryRun: true}); err != nil {
		t.Fatalf("runMigration() error = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMissingCSVCreateOnlyDoesNotTruncate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ddlPath := writeTempDDL(t, dir, "T")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	if err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{CreateOnly: true}); err != nil {
		t.Fatalf("runMigration() error = %v, want nil", err)
	}
	state := tracker.GetTableState("T")
	if state == nil || state.Status != progress.StatusSkipped {
		t.Fatalf("create-only existing table status = %v, want skipped", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMissingCSVTableScopeStillTruncatesSelectedTable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ddlPath := writeTempDDL(t, dir, "T", "OTHER")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T", "OTHER")
	expectTruncate(mock, "T")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{
		TableScope: tablescope.Scope{Enabled: true, Tables: []string{"T"}, Source: "--tables"},
	})
	if err != nil {
		t.Fatalf("runMigration() error = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func adaptiveLabel(adaptive bool) string {
	if adaptive {
		return "adaptive_on"
	}
	return "adaptive_off"
}
