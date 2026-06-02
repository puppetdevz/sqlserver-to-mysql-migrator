package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/converter"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/tablescope"
)

func TestCSVNotFoundShouldAdvanceOverallSkip(t *testing.T) {
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(1)

	if err := tracker.SkipTable("missing_csv_table", "CSV file not found"); err != nil {
		t.Fatalf("SkipTable() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.SkippedCount != 1 {
		t.Fatalf("SkippedCount = %d, want 1", info.SkippedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
	if !info.IsCompleted {
		t.Fatal("IsCompleted = false, want true")
	}
}

type fakeDDLExecutor struct {
	errs []error
	ddls []string
}

func (f *fakeDDLExecutor) ExecuteDDL(ddl string) error {
	f.ddls = append(f.ddls, ddl)
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func TestCreateTableDDLDoesNotRetryOnMySQL1118(t *testing.T) {
	tc := converter.NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  999,
		MaxNvarcharToTextColumns: 999,
		MaxNvarcharToTextSize:    9999,
		MaxVarcharToTextSize:     9999,
	})

	cols := []parser.ColumnDef{
		{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
	}
	for i := 0; i < 220; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     fmt.Sprintf("field%04d", i),
			Type:     "nvarchar(100) NULL",
			Nullable: true,
		})
	}
	tableDDL := &parser.TableDDL{
		TableName:  "retry_wide",
		Columns:    cols,
		PrimaryKey: &parser.PrimaryKeyDef{Name: "PK_retry_wide", Columns: []string{"ID"}},
	}
	rowSizeErr := &mysql.MySQLError{Number: 1118, Message: "Row size too large"}
	executor := &fakeDDLExecutor{
		errs: []error{
			fmt.Errorf("failed to execute DDL: %w", rowSizeErr),
			nil,
		},
	}

	result, err := createTableDDL(executor, tc, tableDDL)
	if err == nil {
		t.Fatal("createTableDDL returned nil error, want first 1118 error")
	}
	if !errors.Is(err, rowSizeErr) {
		t.Fatalf("errors.Is(err, rowSizeErr) = false, err = %v", err)
	}
	if len(executor.ddls) != 1 {
		t.Fatalf("ExecuteDDL calls = %d, want 1", len(executor.ddls))
	}
	if result.Mode != converter.ConvertModeNormal {
		t.Fatalf("Mode = %q, want %q", result.Mode, converter.ConvertModeNormal)
	}
}

func TestWriteCreateFailedTablesWritesOneTablePerLine(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := writeCreateFailedTables([]string{"FORM_A", "FORM_B"}); err != nil {
		t.Fatalf("writeCreateFailedTables() error = %v", err)
	}

	data, err := os.ReadFile(createFailedTablesFile)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", createFailedTablesFile, err)
	}
	if got, want := string(data), "FORM_A\nFORM_B\n"; got != want {
		t.Fatalf("create failed table content = %q, want %q", got, want)
	}
}

type fakeRowCounter struct {
	count int64
	err   error
}

func (f fakeRowCounter) GetRowCount(string) (int64, error) {
	return f.count, f.err
}

func TestValidateImportedRowCountMatches(t *testing.T) {
	result := validateImportedRowCount(fakeRowCounter{count: 42}, "FORM_A", 42)
	if !result.Valid {
		t.Fatalf("Valid = false, want true: %#v", result)
	}
	if result.ExpectedRows != 42 || result.ActualRows != 42 {
		t.Fatalf("result rows = (%d,%d), want (42,42)", result.ExpectedRows, result.ActualRows)
	}
}

func TestValidateImportedRowCountMismatch(t *testing.T) {
	result := validateImportedRowCount(fakeRowCounter{count: 40}, "FORM_A", 42)
	if result.Valid {
		t.Fatalf("Valid = true, want false: %#v", result)
	}
	if result.ExpectedRows != 42 {
		t.Fatalf("ExpectedRows = %d, want 42", result.ExpectedRows)
	}
	if result.ActualRows != 40 {
		t.Fatalf("ActualRows = %d, want 40", result.ActualRows)
	}
	if result.ErrorMessage == "" {
		t.Fatal("ErrorMessage is empty, want mismatch detail")
	}
}

func TestValidateImportedRowCountError(t *testing.T) {
	result := validateImportedRowCount(fakeRowCounter{err: errors.New("db down")}, "FORM_A", 42)
	if result.Valid {
		t.Fatalf("Valid = true, want false: %#v", result)
	}
	if result.ErrorMessage == "" {
		t.Fatal("ErrorMessage is empty, want error detail")
	}
	if result.TableName != "FORM_A" {
		t.Fatalf("TableName = %q, want FORM_A", result.TableName)
	}
	if result.ExpectedRows != 42 {
		t.Fatalf("ExpectedRows = %d, want 42", result.ExpectedRows)
	}
}

func TestWriteTableListWritesOneTablePerLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tables.txt")
	if err := writeTableList(path, []string{"FORM_A", "FORM_B"}); err != nil {
		t.Fatalf("writeTableList() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "FORM_A\nFORM_B\n" {
		t.Fatalf("content = %q, want one table per line", string(data))
	}
}

func TestWriteTableListEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := writeTableList(path, []string{}); err != nil {
		t.Fatalf("writeTableList(empty) error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("content = %q, want empty file", string(data))
	}
}

func TestWriteTableListNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nil.txt")
	if err := writeTableList(path, nil); err != nil {
		t.Fatalf("writeTableList(nil) error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("content = %q, want empty file", string(data))
	}
}

func TestBuildImportCandidatesUsesCSVFileSize(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "FORM_A.csv")
	if err := os.WriteFile(csvPath, make([]byte, 129*1024*1024), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg := &config.Config{
		Migration: config.MigrationConfig{
			AdaptiveImport: config.AdaptiveImportConfig{
				ImportTokens: 10,
				LargeTableMB: 1024,
				HugeTableMB:  5120,
			},
		},
	}
	tableMatcher := matcher.DefaultTableNameMatcher()
	csvTableMap := map[string]string{
		tableMatcher.Key("FORM_A"): csvPath,
	}

	candidates, skipped := buildImportCandidates([]string{"FORM_A"}, csvTableMap, tableMatcher, cfg)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	if candidates[0].Class != importer.ImportTableMedium {
		t.Fatalf("Class = %s, want %s", candidates[0].Class, importer.ImportTableMedium)
	}
}

func TestPartialImportShouldNotBeMarkedCompleted(t *testing.T) {
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(1)

	if err := tracker.StartTable("partial_table", "/tmp/partial.csv", false); err != nil {
		t.Fatalf("StartTable() error = %v", err)
	}
	if err := tracker.FailTable("partial_table", "partial import: 3 row errors"); err != nil {
		t.Fatalf("FailTable() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 0 {
		t.Fatalf("CompletedCount = %d, want 0", info.CompletedCount)
	}
	if info.FailedCount != 1 {
		t.Fatalf("FailedCount = %d, want 1", info.FailedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
}

func TestCreateOnlyExpectedOverallClosure(t *testing.T) {
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(2)

	if err := tracker.StartTable("new_table", "", false); err != nil {
		t.Fatalf("StartTable(new_table) error = %v", err)
	}
	if err := tracker.CompleteTable("new_table", 0, 0, 0); err != nil {
		t.Fatalf("CompleteTable(new_table) error = %v", err)
	}
	if err := tracker.SkipTable("existing_table", "table already exists"); err != nil {
		t.Fatalf("SkipTable(existing_table) error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 1 {
		t.Fatalf("CompletedCount = %d, want 1", info.CompletedCount)
	}
	if info.SkippedCount != 1 {
		t.Fatalf("SkippedCount = %d, want 1", info.SkippedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
	if !info.IsCompleted {
		t.Fatal("IsCompleted = false, want true")
	}
}

func TestFinalizeCreateOnlyProgressMarksExistingTablesAsSkipped(t *testing.T) {
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(1)

	if err := finalizeCreateOnlyProgress(tracker, []string{"existing_table"}); err != nil {
		t.Fatalf("finalizeCreateOnlyProgress() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 0 {
		t.Fatalf("CompletedCount = %d, want 0", info.CompletedCount)
	}
	if info.SkippedCount != 1 {
		t.Fatalf("SkippedCount = %d, want 1 (only existing tables are skipped)", info.SkippedCount)
	}
}

func TestCollectDDLTableNamesUsesOriginalTableNameForCaseSensitiveMatching(t *testing.T) {
	allDDLs := map[string]*parser.TableDDL{
		"SAMPLE_MAIN_101$": {TableName: "sample_main_101$"},
		"NIL_TABLE":      nil,
		"ADDRESSBOOK":    {TableName: "ADDRESSBOOK"},
		"AGENT":          {TableName: "agent"},
	}

	tableNames := collectDDLTableNames(allDDLs)
	want := []string{"ADDRESSBOOK", "agent", "sample_main_101$"}
	if len(tableNames) != len(want) {
		t.Fatalf("collectDDLTableNames() = %v, want %v", tableNames, want)
	}
	for i := range want {
		if tableNames[i] != want[i] {
			t.Fatalf("collectDDLTableNames() = %v, want %v", tableNames, want)
		}
	}
}

func TestBuildDDLLookupKeepsLexicographicallyFirstConflict(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)
	allDDLs := map[string]*parser.TableDDL{
		"A": {TableName: "A"},
		"a": {TableName: "a"},
	}

	for i := 0; i < 100; i++ {
		lookup := buildDDLLookup(allDDLs, tableMatcher)

		got := lookup[tableMatcher.Key("a")]
		if got == nil || got.TableName != "A" {
			t.Fatalf("buildDDLLookup() kept %v, want A", got)
		}
	}
}

func TestBuildCSVTableMapCaseSensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(true)
	csvPath := "/tmp/sample_main_102_20000101000000.csv"

	tableMap := buildCSVTableMap([]string{csvPath}, "20000101000000", tableMatcher)

	if _, ok := tableMap[tableMatcher.Key("SAMPLE_MAIN_102")]; ok {
		t.Fatal("buildCSVTableMap matched different case in case-sensitive mode")
	}
	if got := tableMap[tableMatcher.Key("sample_main_102")]; got != csvPath {
		t.Fatalf("buildCSVTableMap actual path = %q, want %q", got, csvPath)
	}
}

func TestBuildCSVTableMapCaseInsensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)
	csvPath := "/tmp/sample_main_102_20000101000000.csv"

	tableMap := buildCSVTableMap([]string{csvPath}, "20000101000000", tableMatcher)

	if got := tableMap[tableMatcher.Key("SAMPLE_MAIN_102")]; got != csvPath {
		t.Fatalf("buildCSVTableMap actual path = %q, want %q", got, csvPath)
	}
}

func TestBuildCSVTableMapDollarTableWithTimestamp(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(true)
	csvPath := "/tmp/TABLE__20000101000000.csv"

	tableMap := buildCSVTableMap([]string{csvPath}, "20000101000000", tableMatcher)

	if got := tableMap[tableMatcher.Key("TABLE$")]; got != csvPath {
		t.Fatalf("buildCSVTableMap actual path = %q, want %q", got, csvPath)
	}
}

func TestBuildCSVTableMapDollarTableWithTimestampCaseInsensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)
	csvPath := "/tmp/table__20000101000000.csv"

	tableMap := buildCSVTableMap([]string{csvPath}, "20000101000000", tableMatcher)

	if got := tableMap[tableMatcher.Key("TABLE$")]; got != csvPath {
		t.Fatalf("buildCSVTableMap actual path = %q, want %q", got, csvPath)
	}
}

func TestImportDataWithCSVMappingReturnsContextErrorBeforeDispatch(t *testing.T) {
	cfg := &config.Config{
		Source: config.SourceConfig{
			CSVDirectory: t.TempDir(),
		},
		Migration: config.MigrationConfig{
			MaxWorkers: 1,
			BatchSize:  1,
		},
		Logging: config.LoggingConfig{
			File: "",
		},
	}
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	migrationCtx := migration.NewMigrationContext()
	wantErr := errors.New("create table failed")
	migrationCtx.Stop(wantErr)

	err = importDataWithCSVMapping(cfg, nil, nil, []string{"ADDRESSBOOK"}, tracker, migrationCtx, matcher.NewTableNameMatcher(true))
	if !errors.Is(err, wantErr) {
		t.Fatalf("importDataWithCSVMapping() error = %v, want %v", err, wantErr)
	}
}

func TestFinalErrorPrefersMigrationContextStopCause(t *testing.T) {
	migrationCtx := migration.NewMigrationContext()
	wantErr := errors.New("failed to import table WF_CASE_RUN: failed to execute batch insert: invalid connection")
	migrationCtx.Stop(wantErr)

	err := finalError(20, "tables failed to import", migrationCtx)
	if !errors.Is(err, wantErr) {
		t.Fatalf("finalError() error = %v, want %v", err, wantErr)
	}
	if strings.Contains(err.Error(), "20 tables failed") {
		t.Fatalf("finalError() returned aggregate fallout instead of root stop cause: %v", err)
	}
}

func TestFinalErrorReportsAggregateWithoutStopCause(t *testing.T) {
	err := finalError(3, "tables failed to import", migration.NewMigrationContext())
	if err == nil {
		t.Fatal("finalError() error = nil, want aggregate failure")
	}
	if !strings.Contains(err.Error(), "3 tables failed to import") {
		t.Fatalf("finalError() error = %v, want aggregate failure", err)
	}
}

// ============================================================================
// Fix #8: createTableDDL 直接返回首次建表错误，保留 %w 错误链
// ============================================================================

func TestCreateTableDDLReturnsFirstErrorWrapping(t *testing.T) {
	tc := converter.NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  999,
		MaxNvarcharToTextColumns: 999,
		MaxNvarcharToTextSize:    9999,
		MaxVarcharToTextSize:     9999,
	})

	cols := []parser.ColumnDef{
		{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
	}
	for i := 0; i < 220; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     fmt.Sprintf("field%04d", i),
			Type:     "nvarchar(100) NULL",
			Nullable: true,
		})
	}
	tableDDL := &parser.TableDDL{
		TableName:  "retry_wide",
		Columns:    cols,
		PrimaryKey: &parser.PrimaryKeyDef{Name: "PK_retry_wide", Columns: []string{"ID"}},
	}

	rowSizeErr := &mysql.MySQLError{Number: 1118, Message: "Row size too large"}

	executor := &fakeDDLExecutor{
		errs: []error{
			fmt.Errorf("wrapped: %w", rowSizeErr),
		},
	}

	_, err := createTableDDL(executor, tc, tableDDL)
	if err == nil {
		t.Fatal("expected error from createTableDDL")
	}

	if !errors.Is(err, rowSizeErr) {
		t.Fatalf("errors.Is(err, rowSizeErr) = false, %%w wrapping should preserve error chain. err = %v", err)
	}
	if len(executor.ddls) != 1 {
		t.Fatalf("ExecuteDDL calls = %d, want 1", len(executor.ddls))
	}
}

func TestCreateTableDDLDoesNotConsumeSecondExecutorError(t *testing.T) {
	tc := converter.NewTableConverter(config.ConverterConfig{
		MaxVarcharToTextColumns:  999,
		MaxNvarcharToTextColumns: 999,
		MaxNvarcharToTextSize:    9999,
		MaxVarcharToTextSize:     9999,
	})

	cols := []parser.ColumnDef{
		{Name: "ID", Type: "bigint NOT NULL", Nullable: false},
	}
	for i := 0; i < 220; i++ {
		cols = append(cols, parser.ColumnDef{
			Name:     fmt.Sprintf("field%04d", i),
			Type:     "nvarchar(100) NULL",
			Nullable: true,
		})
	}
	tableDDL := &parser.TableDDL{
		TableName:  "retry_wide2",
		Columns:    cols,
		PrimaryKey: &parser.PrimaryKeyDef{Name: "PK_retry_wide2", Columns: []string{"ID"}},
	}

	rowSizeErr := &mysql.MySQLError{Number: 1118, Message: "Row size too large"}
	secondErr := errors.New("would only appear on retry")

	executor := &fakeDDLExecutor{
		errs: []error{
			fmt.Errorf("wrapped: %w", rowSizeErr),
			secondErr,
		},
	}

	_, err := createTableDDL(executor, tc, tableDDL)
	if err == nil {
		t.Fatal("expected error from createTableDDL")
	}

	if !errors.Is(err, rowSizeErr) {
		t.Fatalf("errors.Is(err, rowSizeErr) = false, err = %v", err)
	}
	if errors.Is(err, secondErr) {
		t.Fatalf("errors.Is(err, secondErr) = true, retry error should not be consumed. err = %v", err)
	}
	if len(executor.ddls) != 1 {
		t.Fatalf("ExecuteDDL calls = %d, want 1", len(executor.ddls))
	}
}

// ============================================================================
// Fix #5: excludeTables 配合 createAndTrackTables 新返回值使用
// ============================================================================

func TestExcludeTablesRemovesAllFailedTables(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)

	allTables := []string{"TABLE_A", "TABLE_B", "TABLE_C", "TABLE_D"}
	failedTables := []string{"TABLE_A", "TABLE_C"} // A 和 C 建表失败

	kept := tablescope.ExcludeTables(allTables, failedTables, tableMatcher)

	if len(kept) != 2 {
		t.Fatalf("kept = %d, want 2", len(kept))
	}
	if kept[0] != "TABLE_B" || kept[1] != "TABLE_D" {
		t.Fatalf("kept = %v, want [TABLE_B TABLE_D]", kept)
	}
}

func TestExcludeTablesEmptyFailedList(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)
	allTables := []string{"A", "B", "C"}

	kept := tablescope.ExcludeTables(allTables, nil, tableMatcher)
	if len(kept) != 3 {
		t.Fatalf("kept = %d, want 3", len(kept))
	}
}

func TestCreateAndTrackTablesReturnsFailedTablesAndError(t *testing.T) {
	// 编译时验证：createAndTrackTables 现在返回 ([]string, error)
	var _ = func(cfg *config.Config, conn *database.Connection, missing []string,
		ddl map[string]*parser.TableDDL, tracker *progress.Tracker,
		mCtx *migration.MigrationContext, m matcher.TableNameMatcher) ([]string, error) {
		return createAndTrackTables(cfg, conn, missing, ddl, tracker, mCtx, m)
	}
}
