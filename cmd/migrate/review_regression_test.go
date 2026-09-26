package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
)

func TestUnreadableCompletedListStopsBeforeAnyDatabaseWrite(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir(completedTablesFile, 0755); err != nil {
		t.Fatal(err)
	}
	csvDir := filepath.Join(t.TempDir(), "csv")
	writeCSV(t, csvDir, "T", "id\n1\n")
	ddlPath := writeTempDDL(t, t.TempDir(), "T")
	conn, mock := newMockConnection(t)
	cfg := testMigrationConfig(ddlPath, csvDir, false)
	err := runMigration(cfg, conn, newTracker(t), defaultMatcher(), migrationRunOptions{})
	if err == nil || !strings.Contains(err.Error(), completedTablesFile) {
		t.Fatalf("unreadable completion list must stop before writes, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedListOpenFailureStopsImport(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir(completedTablesFile, 0755); err != nil {
		t.Fatal(err)
	}
	csvDir := filepath.Join(t.TempDir(), "csv")
	csv := writeCSV(t, csvDir, "T", "id\n1\n")
	cfg := testMigrationConfig("unused.sql", csvDir, false)
	conn, mock := newMockConnection(t)
	err := importDataWithCSVMapping(cfg, conn, []string{csv}, []string{"T"}, newTracker(t), migration.NewMigrationContext(), defaultMatcher(), nil)
	if err == nil || !strings.Contains(err.Error(), completedTablesFile) {
		t.Fatalf("completion list open failure must fail import, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSlowListFailureDoesNotRegisterCompletedTable(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "completed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	slow, err := os.Create(filepath.Join(t.TempDir(), "slow.txt"))
	if err != nil {
		t.Fatal(err)
	}
	slow.Close()
	sink := newImportCompletionSink(file, slow, time.Nanosecond)
	if err := sink.appendCompleted("T", time.Second); err == nil {
		t.Fatal("expected slow list failure")
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "T\n") {
		t.Fatalf("failed table was recorded completed: %q", data)
	}
}

func TestAdaptiveCSVStatFailureIsNotMissingCSV(t *testing.T) {
	t.Chdir(t.TempDir())
	csvPath := filepath.Join(t.TempDir(), "T.csv") // mapped file disappeared between scan and scheduling
	cfg := testMigrationConfig("unused.sql", filepath.Dir(csvPath), true)
	conn, mock := newMockConnection(t)
	tracker := newTracker(t)
	ctx := migration.NewMigrationContext()
	dataImporter := importer.NewDataImporter(conn, cfg)
	defer dataImporter.Close()
	_, _, err := importDataAdaptive(conn, cfg, []string{"T"}, map[string]string{"T": csvPath}, defaultMatcher(), tracker, ctx, dataImporter, newImportCompletionSink(nil, nil, 0))
	if err == nil {
		t.Fatal("stat error was silently counted as a successful skip")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIndexCreateFailureLeavesRerunGuard(t *testing.T) {
	t.Chdir(t.TempDir())
	csvDir := filepath.Join(t.TempDir(), "csv")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatal(err)
	}
	ddl := writeTempDDLRaw(t, t.TempDir(), "-- V80.dbo.T definition\nCREATE TABLE V80.dbo.T (\nid int NULL\n);\nCREATE INDEX idx_t ON V80.dbo.T (id ASC);\n")
	conn, mock := newMockConnection(t)
	expectShowTables(mock)
	mock.ExpectExec(strings.TrimSuffix(strings.TrimSpace(convertSimpleCreateSQL(t, "T")), ";")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE INDEX `idx_t` ON `T` (`id`)").WillReturnError(errors.New("index rejected"))
	expectShowTables(mock, "T")
	cfg := testMigrationConfig(ddl, csvDir, false)
	if err := runMigration(cfg, conn, newTracker(t), defaultMatcher(), migrationRunOptions{}); err == nil {
		t.Fatal("index failure must be reported")
	}
	assertFileContainsTable(t, createFailedTablesFile, "T")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	// The next run must refuse to TRUNCATE this partially-created table.
	conn2, mock2 := newMockConnection(t)
	err := runMigration(cfg, conn2, newTracker(t), defaultMatcher(), migrationRunOptions{})
	if err == nil || !strings.Contains(err.Error(), createFailedTablesFile) {
		t.Fatalf("partial table was allowed to rerun: %v", err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedCreateDoesNotClearPendingJournal(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := writeCreateFailedTables([]string{"T"}); err != nil {
		t.Fatal(err)
	}
	if err := finalizeCreateJournal(nil, nil, context.Canceled); err == nil {
		t.Fatal("canceled create should fail")
	}
	assertFileContainsTable(t, createFailedTablesFile, "T")
}

func TestFailedCreateListKeepsFailuresFromOtherScopes(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(createFailedTablesFile, []byte("OLD\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeCreateFailedTables([]string{"NEW"}); err != nil {
		t.Fatal(err)
	}
	assertFileContainsTable(t, createFailedTablesFile, "OLD")
	assertFileContainsTable(t, createFailedTablesFile, "NEW")
}

func TestPreviousPartialCreateCannotBeTruncatedOnRerun(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(createFailedTablesFile, []byte("T\n"), 0644); err != nil {
		t.Fatal(err)
	}
	csvDir := filepath.Join(t.TempDir(), "csv")
	writeCSV(t, csvDir, "T", "id\n1\n")
	ddlPath := writeTempDDL(t, t.TempDir(), "T")
	conn, mock := newMockConnection(t)
	err := runMigration(testMigrationConfig(ddlPath, csvDir, false), conn, newTracker(t), defaultMatcher(), migrationRunOptions{})
	if err == nil || !strings.Contains(err.Error(), "create_failed_tables.txt") || !strings.Contains(err.Error(), "T") {
		t.Fatalf("previous failed create must be resolved before rerun, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
