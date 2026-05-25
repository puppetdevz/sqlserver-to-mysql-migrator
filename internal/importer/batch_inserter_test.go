package importer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ============================================================================
// errorColumnExtractor + autoTextColumn 测试
// ============================================================================

func TestErrorColumnExtractorDataTooLong(t *testing.T) {
	col, ok := _dataTooLongExtractor.extract(fmt.Errorf("Error 1406 (22001): Data too long for column 'name' at row 1"))
	if !ok {
		t.Fatal("expected true for Data too long error")
	}
	if col != "name" {
		t.Fatalf("column = %q, want %q", col, "name")
	}
}

func TestErrorColumnExtractorIncorrectDatetime(t *testing.T) {
	col, ok := _incorrectTemporalExtractor.extract(fmt.Errorf("Error 1292 (22007): Incorrect datetime value: 'abc' for column 'created_at' at row 1"))
	if !ok {
		t.Fatal("expected true for incorrect datetime error")
	}
	if col != "created_at" {
		t.Fatalf("column = %q, want %q", col, "created_at")
	}
}

func TestErrorColumnExtractorIncorrectDate(t *testing.T) {
	col, ok := _incorrectTemporalExtractor.extract(fmt.Errorf("Error 1292 (22007): Incorrect date value: 'abc' for column 'birthday' at row 1"))
	if !ok {
		t.Fatal("expected true for incorrect date error")
	}
	if col != "birthday" {
		t.Fatalf("column = %q, want %q", col, "birthday")
	}
}

func TestErrorColumnExtractorIncorrectInteger(t *testing.T) {
	col, ok := _incorrectNumericExtractor.extract(fmt.Errorf("Error 1366 (22007): Incorrect integer value: 'abc' for column 'age' at row 1"))
	if !ok {
		t.Fatal("expected true for incorrect integer error")
	}
	if col != "age" {
		t.Fatalf("column = %q, want %q", col, "age")
	}
}

func TestErrorColumnExtractorOutOfRange(t *testing.T) {
	col, ok := _outOfRangeExtractor.extract(fmt.Errorf("Error 1264 (22003): Out of range value for column 'score' at row 1"))
	if !ok {
		t.Fatal("expected true for out of range error")
	}
	if col != "score" {
		t.Fatalf("column = %q, want %q", col, "score")
	}
}

func TestErrorColumnExtractorNilError(t *testing.T) {
	col, ok := _dataTooLongExtractor.extract(nil)
	if ok {
		t.Fatal("expected false for nil error")
	}
	if col != "" {
		t.Fatalf("column = %q, want empty", col)
	}
}

func TestErrorColumnExtractorNoMatch(t *testing.T) {
	col, ok := _dataTooLongExtractor.extract(errors.New("some random error"))
	if ok {
		t.Fatal("expected false for non-matching error")
	}
	if col != "" {
		t.Fatalf("column = %q, want empty", col)
	}
}

func TestAutoTextColumnPriority(t *testing.T) {
	// Data too long takes priority
	col, ok := autoTextColumn(fmt.Errorf("Error 1406 (22001): Data too long for column 'col_a' at row 1"))
	if !ok {
		t.Fatal("expected true")
	}
	if col != "col_a" {
		t.Fatalf("column = %q, want %q", col, "col_a")
	}
}

func TestAutoTextColumnFallsThrough(t *testing.T) {
	// Incorrect datetime falls through after data too long fails
	col, ok := autoTextColumn(fmt.Errorf("Error 1292 (22007): Incorrect datetime value: 'x' for column 'ts' at row 1"))
	if !ok {
		t.Fatal("expected true for temporal error")
	}
	if col != "ts" {
		t.Fatalf("column = %q, want %q", col, "ts")
	}
}

func TestAutoTextColumnNoMatch(t *testing.T) {
	col, ok := autoTextColumn(errors.New("unrelated error"))
	if ok {
		t.Fatal("expected false for non-matching error")
	}
	if col != "" {
		t.Fatalf("column = %q, want empty", col)
	}
}

// ============================================================================
// widenedTextType 测试
// ============================================================================

func TestWidenedTextType(t *testing.T) {
	tests := []struct {
		input    string
		wantType string
		wantOK   bool
	}{
		// case-insensitive matching
		{"char", "TEXT", true},
		{"CHAR", "TEXT", true},
		{"varchar", "TEXT", true},
		{"VARCHAR", "TEXT", true},
		{"tinytext", "TEXT", true},
		{"date", "TEXT", true},
		{"datetime", "TEXT", true},
		{"timestamp", "TEXT", true},
		{"time", "TEXT", true},
		{"year", "TEXT", true},
		// numeric types widen to TEXT
		{"tinyint", "TEXT", true},
		{"smallint", "TEXT", true},
		{"mediumint", "TEXT", true},
		{"int", "TEXT", true},
		{"integer", "TEXT", true},
		{"bigint", "TEXT", true},
		{"decimal", "TEXT", true},
		{"numeric", "TEXT", true},
		{"float", "TEXT", true},
		{"double", "TEXT", true},
		{"real", "TEXT", true},
		// TEXT widening chain
		{"text", "MEDIUMTEXT", true},
		{"TEXT", "MEDIUMTEXT", true},
		{"mediumtext", "LONGTEXT", true},
		{"MEDIUMTEXT", "LONGTEXT", true},
		// unsupported types
		{"longtext", "", false},
		{"blob", "", false},
		{"enum", "", false},
		{"json", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := widenedTextType(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("widenedTextType(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if got != tt.wantType {
				t.Fatalf("widenedTextType(%q) = %q, want %q", tt.input, got, tt.wantType)
			}
		})
	}
}

// ============================================================================
// quoteIdentifier 测试
// ============================================================================

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"name", "`name`"},
		{"a`b", "`a``b`"},
		{"", "``"},
		{"user_id", "`user_id`"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := quoteIdentifier(tt.input)
			if got != tt.want {
				t.Fatalf("quoteIdentifier(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ============================================================================
// NewBatchInserter 测试
// ============================================================================

func TestNewBatchInserter(t *testing.T) {
	bi := NewBatchInserter(nil, "test_table", []string{"a", "b"}, 100, "ignore")
	if bi == nil {
		t.Fatal("NewBatchInserter returned nil")
	}
	if bi.tableName != "test_table" {
		t.Fatalf("tableName = %q, want %q", bi.tableName, "test_table")
	}
	if len(bi.columns) != 2 {
		t.Fatalf("columns len = %d, want 2", len(bi.columns))
	}
	if bi.batchSize != 100 {
		t.Fatalf("batchSize = %d, want 100", bi.batchSize)
	}
	if bi.onDuplicate != "ignore" {
		t.Fatalf("onDuplicate = %q, want %q", bi.onDuplicate, "ignore")
	}
}

// ============================================================================
// NewBatchInserterWithDBColumns 测试
// ============================================================================

func TestNewBatchInserterWithDBColumnsAllMatch(t *testing.T) {
	csvCols := []string{"ID", "NAME", "AGE"}
	dbCols := []string{"id", "name", "age"}
	bi, skipped := NewBatchInserterWithDBColumns(nil, "t", csvCols, dbCols, 100, "ignore")
	if bi == nil {
		t.Fatal("should return non-nil BatchInserter")
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want empty", skipped)
	}
	if len(bi.columns) != 3 {
		t.Fatalf("columns len = %d, want 3", len(bi.columns))
	}
	// Should use original DB column names (lowercase)
	for _, col := range bi.columns {
		if col != strings.ToLower(col) {
			t.Fatalf("column %q should be lowercase (db original)", col)
		}
	}
}

func TestNewBatchInserterWithDBColumnsPartialMatch(t *testing.T) {
	csvCols := []string{"ID", "EXTRA", "NAME"}
	dbCols := []string{"id", "name"}
	bi, skipped := NewBatchInserterWithDBColumns(nil, "t", csvCols, dbCols, 100, "ignore")
	if bi == nil {
		t.Fatal("should return non-nil for partial match")
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped len = %d, want 1", len(skipped))
	}
	if skipped[0] != "EXTRA" {
		t.Fatalf("skipped[0] = %q, want %q", skipped[0], "EXTRA")
	}
	if len(bi.columns) != 2 {
		t.Fatalf("columns len = %d, want 2", len(bi.columns))
	}
}

func TestNewBatchInserterWithDBColumnsNoMatch(t *testing.T) {
	csvCols := []string{"X", "Y"}
	dbCols := []string{"id", "name"}
	bi, skipped := NewBatchInserterWithDBColumns(nil, "t", csvCols, dbCols, 100, "ignore")
	if bi != nil {
		t.Fatal("should return nil when no columns match")
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped len = %d, want 2", len(skipped))
	}
}

func TestGetSkippedColumns(t *testing.T) {
	bi := &BatchInserter{skippedCols: []string{"A", "B"}}
	got := bi.GetSkippedColumns()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != "A" || got[1] != "B" {
		t.Fatalf("got %v, want [A B]", got)
	}
}

// ============================================================================
// buildInsertQuery 测试
// ============================================================================

func TestBuildInsertQueryInsertIgnore(t *testing.T) {
	bi := NewBatchInserter(nil, "users", []string{"id", "name"}, 100, "ignore")
	q := bi.buildInsertQuery(2)
	if !strings.HasPrefix(q, "INSERT IGNORE INTO ") {
		t.Fatalf("query should start with INSERT IGNORE, got: %s", q)
	}
	if !strings.Contains(q, "`users`") {
		t.Fatalf("query should contain table name, got: %s", q)
	}
	if !strings.Contains(q, "`id`") || !strings.Contains(q, "`name`") {
		t.Fatalf("query should contain column names, got: %s", q)
	}
	// 2 rows * 2 columns = 4 placeholders
	got := strings.Count(q, "?")
	want := 4
	if got != want {
		t.Fatalf("placeholder count = %d, want %d, query: %s", got, want, q)
	}
}

func TestBuildInsertQueryReplace(t *testing.T) {
	bi := NewBatchInserter(nil, "users", []string{"id"}, 100, "replace")
	q := bi.buildInsertQuery(1)
	if !strings.HasPrefix(q, "REPLACE INTO ") {
		t.Fatalf("query should start with REPLACE, got: %s", q)
	}
}

func TestBuildInsertQuerySingleRow(t *testing.T) {
	bi := NewBatchInserter(nil, "t", []string{"a", "b", "c"}, 100, "ignore")
	q := bi.buildInsertQuery(1)
	// 1 row * 3 columns = 3 placeholders
	if strings.Count(q, "?") != 3 {
		t.Fatalf("placeholder count should be 3, got: %s", q)
	}
}

func TestBuildInsertQueryEscapedTable(t *testing.T) {
	bi := NewBatchInserter(nil, "table`name", []string{"col"}, 100, "ignore")
	q := bi.buildInsertQuery(1)
	// Backtick in table name should be doubled
	if !strings.Contains(q, "`table``name`") {
		t.Fatalf("query should contain escaped table name, got: %s", q)
	}
}

// ============================================================================
// InsertBatch 空批次测试 (不需要 DB)
// ============================================================================

func TestInsertBatchEmptyPure(t *testing.T) {
	bi := NewBatchInserter(nil, "t", []string{"a"}, 100, "ignore")
	affected, err := bi.InsertBatch(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if affected != 0 {
		t.Fatalf("affected = %d, want 0", affected)
	}

	affected, err = bi.InsertBatch([][]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if affected != 0 {
		t.Fatalf("affected = %d, want 0", affected)
	}
}
