package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/converter"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
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

func TestCreateTableDDLWithRetryRetriesOnceOnMySQL1118(t *testing.T) {
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
	executor := &fakeDDLExecutor{
		errs: []error{
			fmt.Errorf("failed to execute DDL: %w", &mysql.MySQLError{Number: 1118, Message: "Row size too large"}),
			nil,
		},
	}

	result, err := createTableDDLWithRetry(executor, tc, tableDDL)
	if err != nil {
		t.Fatalf("createTableDDLWithRetry returned error: %v", err)
	}
	if len(executor.ddls) != 2 {
		t.Fatalf("ExecuteDDL calls = %d, want 2", len(executor.ddls))
	}
	if result.Mode != converter.ConvertModeAggressive {
		t.Fatalf("Mode = %q, want %q", result.Mode, converter.ConvertModeAggressive)
	}
	if len(result.Degradations) == 0 {
		t.Fatal("retry result should include degradation records")
	}
	for _, degradation := range result.Degradations {
		if degradation.Reason != converter.DegradationReasonError1118 {
			t.Fatalf("degradation reason = %q, want %q", degradation.Reason, converter.DegradationReasonError1118)
		}
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

func TestFinalizeCreateOnlyProgressSkipsUncreatedMissingTablesWhenCreationDisabled(t *testing.T) {
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(2)

	if err := finalizeCreateOnlyProgress(tracker, []string{"existing_table"}, []string{"missing_table"}, false); err != nil {
		t.Fatalf("finalizeCreateOnlyProgress() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 0 {
		t.Fatalf("CompletedCount = %d, want 0", info.CompletedCount)
	}
	if info.SkippedCount != 2 {
		t.Fatalf("SkippedCount = %d, want 2", info.SkippedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
	if !info.IsCompleted {
		t.Fatal("IsCompleted = false, want true")
	}
}

func TestFilterTablesCaseSensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(true)

	got := filterTables([]string{"SAMPLE_MAIN_102"}, []string{"sample_main_102"}, tableMatcher)

	if len(got) != 0 {
		t.Fatalf("filterTables() = %v, want empty", got)
	}
}

func TestFilterTablesCaseInsensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)

	got := filterTables([]string{"SAMPLE_MAIN_102"}, []string{"sample_main_102"}, tableMatcher)

	if len(got) != 1 || got[0] != "SAMPLE_MAIN_102" {
		t.Fatalf("filterTables() = %v, want [SAMPLE_MAIN_102]", got)
	}
}

func TestCollectDDLTableNamesUsesOriginalTableNameForCaseSensitiveMatching(t *testing.T) {
	allDDLs := map[string]*parser.TableDDL{
		"SAMPLE_MAIN_101$": {TableName: "sample_main_101$"},
		"NIL_TABLE":      nil,
		"ADDRESSBOOK":    {TableName: "ADDRESSBOOK"},
		"AGENT":          {TableName: "agent"},
	}
	tableMatcher := matcher.NewTableNameMatcher(true)

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

	got := filterTables(tableNames, []string{"sample_main_101$"}, tableMatcher)
	if len(got) != 1 || got[0] != "sample_main_101$" {
		t.Fatalf("filterTables() = %v, want [sample_main_101$]", got)
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

// ============================================================================
// Fix #8: createTableDDLWithRetry 错误包装使用 %w 使 errors.Is 能遍历整个链
// ============================================================================

func TestCreateTableDDLWithRetryDoubleWErrorWrapping(t *testing.T) {
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
	aggressiveErr := &mysql.MySQLError{Number: 1118, Message: "Row size too large (after aggressive)"}

	// 两次都失败：第一次 Error 1118 触发重试，第二次（aggressive）也失败
	executor := &fakeDDLExecutor{
		errs: []error{
			fmt.Errorf("wrapped: %w", rowSizeErr),
			fmt.Errorf("aggressive also failed: %w", aggressiveErr),
		},
	}

	_, err := createTableDDLWithRetry(executor, tc, tableDDL)
	if err == nil {
		t.Fatal("expected error from createTableDDLWithRetry")
	}

	// errors.Is 应该能遍历到 rowSizeErr（通过第一个 %w 包装）
	if !errors.Is(err, rowSizeErr) {
		t.Fatalf("errors.Is(err, rowSizeErr) = false, double %%w wrapping should preserve error chain. err = %v", err)
	}

	// errors.Is 也应该能遍历到 aggressiveErr（通过第二个 %w 包装）
	if !errors.Is(err, aggressiveErr) {
		t.Fatalf("errors.Is(err, aggressiveErr) = false, double %%w wrapping should preserve retry error. err = %v", err)
	}

	// 包装错误应同时包含两个错误的信息
	errStr := err.Error()
	if !strings.Contains(errStr, "row size error") {
		t.Error("error should mention row size error")
	}
	if !strings.Contains(errStr, "aggressive") {
		t.Error("error should mention aggressive retry failure")
	}
}

func TestCreateTableDDLWithRetryAggressiveRetryErrorWrapping(t *testing.T) {
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
	aggressiveFailErr := &mysql.MySQLError{Number: 1118, Message: "Row size too large (after aggressive)"}

	executor := &fakeDDLExecutor{
		errs: []error{
			fmt.Errorf("wrapped: %w", rowSizeErr),
			fmt.Errorf("aggressive also failed: %w", aggressiveFailErr),
		},
	}

	_, err := createTableDDLWithRetry(executor, tc, tableDDL)
	if err == nil {
		t.Fatal("expected error from createTableDDLWithRetry")
	}

	// errors.Is 应该能遍历到 rowSizeErr
	if !errors.Is(err, rowSizeErr) {
		t.Fatalf("errors.Is(err, rowSizeErr) = false, double %%w should preserve first error. err = %v", err)
	}

	// errors.Is 也应该能遍历到 aggressiveFailErr
	if !errors.Is(err, aggressiveFailErr) {
		t.Fatalf("errors.Is(err, aggressiveFailErr) = false, double %%w should preserve retry error. err = %v", err)
	}
}

// ============================================================================
// Fix #5: excludeTables 配合 createAndTrackTables 新返回值使用
// ============================================================================

func TestExcludeTablesRemovesAllFailedTables(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)

	allTables := []string{"TABLE_A", "TABLE_B", "TABLE_C", "TABLE_D"}
	failedTables := []string{"TABLE_A", "TABLE_C"} // A 和 C 建表失败

	kept := excludeTables(allTables, failedTables, tableMatcher)

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

	kept := excludeTables(allTables, nil, tableMatcher)
	if len(kept) != 3 {
		t.Fatalf("kept = %d, want 3", len(kept))
	}
}

func TestCreateAndTrackTablesReturnsThreeValues(t *testing.T) {
	// 编译时验证：createAndTrackTables 现在返回 ([]string, []string, error)
	// 此测试确保 3 返回值签名在重构中不被意外破坏
	var _ = func(cfg *config.Config, conn *database.Connection, missing []string,
		ddl map[string]*parser.TableDDL, tracker *progress.Tracker,
		mCtx *migration.MigrationContext, m matcher.TableNameMatcher) ([]string, []string, error) {
		return createAndTrackTables(cfg, conn, missing, ddl, tracker, mCtx, m)
	}
}
