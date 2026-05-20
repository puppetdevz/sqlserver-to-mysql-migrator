package main

import (
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
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

func TestExcludeTablesCaseSensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(true)

	got := excludeTables([]string{"SAMPLE_MAIN_102"}, []string{"sample_main_102"}, tableMatcher)

	if len(got) != 1 || got[0] != "SAMPLE_MAIN_102" {
		t.Fatalf("excludeTables() = %v, want [SAMPLE_MAIN_102]", got)
	}
}

func TestExcludeTablesCaseInsensitive(t *testing.T) {
	tableMatcher := matcher.NewTableNameMatcher(false)

	got := excludeTables([]string{"SAMPLE_MAIN_102"}, []string{"sample_main_102"}, tableMatcher)

	if len(got) != 0 {
		t.Fatalf("excludeTables() = %v, want empty", got)
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
