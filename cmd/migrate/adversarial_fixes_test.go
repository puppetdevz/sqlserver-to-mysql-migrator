package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/converter"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/tablescope"
)

func expectCreateTable(t *testing.T, mock sqlmock.Sqlmock, table string, err error) {
	t.Helper()
	sql := convertSimpleCreateSQL(t, table)
	stmt := strings.TrimSpace(strings.Split(sql, ";")[0]) + ";"
	exec := mock.ExpectExec(stmt)
	if err != nil {
		exec.WillReturnError(err)
		return
	}
	exec.WillReturnResult(sqlmock.NewResult(0, 0))
}

func convertSimpleCreateSQL(t *testing.T, table string) string {
	t.Helper()
	tc := converter.NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  999,
		MaxNvarcharToTextColumns: 999,
		MaxVarcharToTextSize:     9999,
		MaxNvarcharToTextSize:    9999,
	})
	sql, err := tc.ConvertToMySQL(&parser.TableDDL{
		TableName: table,
		Columns:   []parser.ColumnDef{{Name: "id", Type: "int NULL", Nullable: true}},
	})
	if err != nil {
		t.Fatalf("ConvertToMySQL: %v", err)
	}
	return sql
}

func expectDuplicateIgnoreImport(mock sqlmock.Sqlmock, table string, affected, count int64, countErr error) {
	expectDescribe(mock, table, []string{"id", "int", "YES", "PRI"})
	query := fmt.Sprintf("INSERT IGNORE INTO `%s` (`id`) VALUES (?), (?)", table)
	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().WithArgs("1", "1").WillReturnResult(sqlmock.NewResult(0, affected))
	if countErr != nil {
		mock.ExpectQuery(fmt.Sprintf("SELECT COUNT(*) FROM `%s`", table)).WillReturnError(countErr)
		return
	}
	if count >= 0 {
		expectCount(mock, table, count)
	}
}

func TestRowCountValidationFourCombinations(t *testing.T) {
	type tc struct {
		adaptive   bool
		validate   bool
		wantErr    bool
		wantCount  bool
		wantFailed bool
	}
	cases := []tc{
		{adaptive: true, validate: true, wantErr: true, wantCount: true, wantFailed: true},
		{adaptive: false, validate: true, wantErr: true, wantCount: true, wantFailed: true},
		{adaptive: true, validate: false, wantErr: false, wantCount: false, wantFailed: false},
		{adaptive: false, validate: false, wantErr: false, wantCount: false, wantFailed: false},
	}
	for _, c := range cases {
		name := fmt.Sprintf("adaptive=%t,validate=%t", c.adaptive, c.validate)
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			csvDir := filepath.Join(dir, "csv")
			ddlPath := writeTempDDL(t, dir, "T")
			writeCSV(t, csvDir, "T", "id\n1\n1\n")

			conn, mock := newMockConnection(t)
			expectShowTables(mock, "T")
			expectTruncate(mock, "T")
			affected := int64(1)
			count := int64(-1)
			if c.wantCount {
				count = 1
			}
			expectDuplicateIgnoreImport(mock, "T", affected, count, nil)

			cfg := testMigrationConfig(ddlPath, csvDir, c.adaptive)
			cfg.Migration.RowCountValidation = boolPtr(c.validate)
			tracker := newTracker(t)
			err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{})
			if c.wantErr && err == nil {
				t.Fatal("runMigration() error = nil, want mismatch error")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("runMigration() error = %v, want nil", err)
			}
			if c.wantFailed {
				assertFileContainsTable(t, failedTablesFile, "T")
				assertFileContainsTable(t, rowCountMismatchFile, "T")
				assertTableNotInFile(t, completedTablesFile, "T")
			} else {
				assertTableNotInFile(t, failedTablesFile, "T")
				assertTableNotInFile(t, rowCountMismatchFile, "T")
				if tableFileOccurrences(t, completedTablesFile, "T") != 1 {
					t.Fatalf("completed occurrences = %d, want 1", tableFileOccurrences(t, completedTablesFile, "T"))
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("SQL expectations: %v", err)
			}
		})
	}
}

func TestRowCountValidationCountQueryErrorFails(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "T")
	writeCSV(t, csvDir, "T", "id\n1\n1\n")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")
	expectTruncate(mock, "T")
	expectDuplicateIgnoreImport(mock, "T", 2, -1, errors.New("count failed"))

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{})
	if err == nil {
		t.Fatal("expected COUNT failure to fail migration")
	}
	assertFileContainsTable(t, failedTablesFile, "T")
	assertFileContainsTable(t, rowCountMismatchFile, "T")
	assertTableNotInFile(t, completedTablesFile, "T")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestRowCountValidationMatchCompletesOnce(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "T")
	writeCSV(t, csvDir, "T", "id\n1\n2\n")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")
	expectTruncate(mock, "T")
	expectDescribe(mock, "T", []string{"id", "int", "YES", "PRI"})
	query := "INSERT IGNORE INTO `T` (`id`) VALUES (?), (?)"
	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().WithArgs("1", "2").WillReturnResult(sqlmock.NewResult(0, 2))
	expectCount(mock, "T", 2)

	cfg := testMigrationConfig(ddlPath, csvDir, true)
	tracker := newTracker(t)
	if err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{}); err != nil {
		t.Fatalf("runMigration() error = %v", err)
	}
	if tableFileOccurrences(t, completedTablesFile, "T") != 1 {
		t.Fatalf("completed file should contain T once")
	}
	assertTableNotInFile(t, failedTablesFile, "T")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestRowCountValidationIgnoresDisabledPreCount(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "T")
	writeCSV(t, csvDir, "T", "id\n1\n1\n")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")
	expectTruncate(mock, "T")
	expectDuplicateIgnoreImport(mock, "T", 1, 1, nil)

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	cfg.Migration.CountCSVRowsBeforeImport = boolPtr(false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{})
	if err == nil {
		t.Fatal("expected mismatch even when pre-count is disabled")
	}
	assertFileContainsTable(t, rowCountMismatchFile, "T")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestFinalizeImportedTableFastFailStopsContext(t *testing.T) {
	for _, fastFail := range []bool{true, false} {
		t.Run(fmt.Sprintf("fast_fail=%t", fastFail), func(t *testing.T) {
			cfg := testMigrationConfig("ddl.sql", "csv", false)
			cfg.Migration.FastFail = boolPtr(fastFail)
			cfg.Migration.RowCountValidation = boolPtr(true)
			tracker := newTracker(t)
			tracker.SetPlannedTotalTables(1)
			if err := tracker.StartTable("T", "T.csv", false); err != nil {
				t.Fatal(err)
			}
			mCtx := migration.NewMigrationContext()
			sink := newImportCompletionSink(nil, nil, 0)
			result := &importer.ImportResult{TableName: "T", Success: true, ProcessedRows: 2, InsertedRows: 1}
			finalizeImportedTable(cfg, fakeRowCounter{count: 1}, tracker, mCtx, sink, result, 0)
			if result.Success {
				t.Fatal("validation should mark result failed")
			}
			if fastFail && mCtx.Err() == nil {
				t.Fatal("fast_fail=true should stop migration context")
			}
			if !fastFail && mCtx.Err() != nil {
				t.Fatalf("fast_fail=false should not stop context, got %v", mCtx.Err())
			}
		})
	}
}

func TestEmptyFailedListOverwritesOldFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "T")
	writeCSV(t, csvDir, "T", "id\n1\n")
	if err := os.WriteFile(failedTablesFile, []byte("OLD\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "T")
	expectTruncate(mock, "T")
	expectDescribe(mock, "T", []string{"id", "int", "YES", ""})
	prep := mock.ExpectPrepare("INSERT IGNORE INTO `T` (`id`) VALUES (?)")
	prep.ExpectExec().WithArgs("1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCount(mock, "T", 1)

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	if err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{}); err != nil {
		t.Fatalf("runMigration() error = %v", err)
	}
	data, err := os.ReadFile(failedTablesFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "OLD") {
		t.Fatalf("old failed list survived: %q", data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestWriteCreateFailedTablesFailureIsReturned(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "T")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(createFailedTablesFile, 0755); err != nil {
		t.Fatal(err)
	}

	conn, mock := newMockConnection(t)
	expectShowTables(mock)
	expectCreateTable(t, mock, "T", errors.New("create denied"))
	expectShowTables(mock)

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{CreateOnly: true})
	if err == nil {
		t.Fatal("expected write-list failure to surface")
	}
	if !strings.Contains(err.Error(), createFailedTablesFile) && !strings.Contains(err.Error(), "writeTableList") && !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("error = %v, want create-failed list write error", err)
	}
}

func TestCreateOnlyAllFailReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "T")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatal(err)
	}

	conn, mock := newMockConnection(t)
	expectShowTables(mock)
	expectCreateTable(t, mock, "T", errors.New("create denied"))
	expectShowTables(mock)

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{CreateOnly: true})
	if err == nil {
		t.Fatal("create-only all-fail should return error")
	}
	assertFileContainsTable(t, createFailedTablesFile, "T")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestCreateOnlyAllSuccessReturnsNil(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "T")
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatal(err)
	}

	conn, mock := newMockConnection(t)
	expectShowTables(mock)
	expectCreateTable(t, mock, "T", nil)
	expectShowTables(mock, "T")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	if err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{CreateOnly: true}); err != nil {
		t.Fatalf("create-only success error = %v", err)
	}
	state := tracker.GetTableState("T")
	if state == nil || state.Status != progress.StatusCompleted {
		t.Fatalf("create-only progress = %v, want completed via opts.CreateOnly", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestCreateFailureThenSuccessfulImportStillReturnsCreateError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "NEW", "OLD")
	writeCSV(t, csvDir, "OLD", "id\n1\n")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "OLD")
	expectCreateTable(t, mock, "NEW", errors.New("create denied"))
	expectShowTables(mock, "OLD")
	expectTruncate(mock, "OLD")
	expectDescribe(mock, "OLD", []string{"id", "int", "YES", ""})
	prep := mock.ExpectPrepare("INSERT IGNORE INTO `OLD` (`id`) VALUES (?)")
	prep.ExpectExec().WithArgs("1").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCount(mock, "OLD", 1)

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{})
	if err == nil {
		t.Fatal("expected create error to survive successful import")
	}
	if !strings.Contains(err.Error(), "failed to create") && !strings.Contains(err.Error(), "1 tables failed to create") {
		t.Fatalf("error = %v, want create failure", err)
	}
	assertFileContainsTable(t, createFailedTablesFile, "NEW")
	assertTableNotInFile(t, completedTablesFile, "NEW")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestCreateAndImportErrorsAreJoined(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDL(t, dir, "NEW", "OLD")
	writeCSV(t, csvDir, "OLD", "id\n1\n1\n")

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "OLD")
	expectCreateTable(t, mock, "NEW", errors.New("create denied"))
	expectShowTables(mock, "OLD")
	expectTruncate(mock, "OLD")
	expectDuplicateIgnoreImport(mock, "OLD", 1, 1, nil)

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, defaultMatcher(), migrationRunOptions{})
	if err == nil {
		t.Fatal("expected joined errors")
	}
	if !strings.Contains(err.Error(), "failed to create") && !strings.Contains(err.Error(), "tables failed to create") {
		t.Fatalf("missing create error: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to import") && !strings.Contains(err.Error(), "row count") && !strings.Contains(err.Error(), "tables failed to import") {
		t.Fatalf("missing import error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestSensitiveTableScopeSelectsExactCaseOnly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDLRaw(t, dir, `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
-- V80.dbo.foo definition
CREATE TABLE V80.dbo.foo (
id int NULL
);
`)
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatal(err)
	}

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "Foo", "foo")
	expectTruncate(mock, "Foo")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, matcher.NewTableNameMatcher(true), migrationRunOptions{
		TableScope: tablescope.Scope{Enabled: true, Tables: []string{"Foo"}, Source: "--tables"},
	})
	if err != nil {
		t.Fatalf("runMigration() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestInsensitiveLookupConflictErrorsBeforeWrites(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDLRaw(t, dir, `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
-- V80.dbo.foo definition
CREATE TABLE V80.dbo.foo (
id int NULL
);
`)
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatal(err)
	}

	conn, mock := newMockConnection(t)
	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, matcher.NewTableNameMatcher(false), migrationRunOptions{})
	if err == nil {
		t.Fatal("expected case-insensitive DDL conflict")
	}
	if !strings.Contains(err.Error(), "Foo") || !strings.Contains(err.Error(), "foo") {
		t.Fatalf("error = %v, want both original names", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected SQL: %v", err)
	}
}

func TestInsensitiveLookupMatchesSingleFoo(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	csvDir := filepath.Join(dir, "csv")
	ddlPath := writeTempDDLRaw(t, dir, `-- V80.dbo.Foo definition
CREATE TABLE V80.dbo.Foo (
id int NULL
);
`)
	if err := os.MkdirAll(csvDir, 0755); err != nil {
		t.Fatal(err)
	}

	conn, mock := newMockConnection(t)
	expectShowTables(mock, "Foo")
	expectTruncate(mock, "Foo")

	cfg := testMigrationConfig(ddlPath, csvDir, false)
	tracker := newTracker(t)
	err := runMigration(cfg, conn, tracker, matcher.NewTableNameMatcher(false), migrationRunOptions{
		TableScope: tablescope.Scope{Enabled: true, Tables: []string{"FOO"}, Source: "--tables"},
	})
	if err != nil {
		t.Fatalf("runMigration() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}
