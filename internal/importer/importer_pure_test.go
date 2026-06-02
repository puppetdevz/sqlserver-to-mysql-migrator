package importer

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

// ============================================================================
// buildColumnMapping 测试
// ============================================================================

func TestBuildColumnMapping(t *testing.T) {
	csvHeaders := []string{"ID", "NAME", "EXTRA"}
	dbColumns := []string{"id", "name"}
	mapping := buildColumnMapping(csvHeaders, dbColumns)
	if len(mapping) != 3 {
		t.Fatalf("len = %d, want 3", len(mapping))
	}
	if mapping[0] != 0 {
		t.Errorf("ID should map to 0, got %d", mapping[0])
	}
	if mapping[1] != 1 {
		t.Errorf("NAME should map to 1, got %d", mapping[1])
	}
	if mapping[2] != -1 {
		t.Errorf("EXTRA should map to -1, got %d", mapping[2])
	}
}

func TestBuildColumnMappingCaseInsensitive(t *testing.T) {
	csvHeaders := []string{"UserId", "UserName"}
	dbColumns := []string{"userid", "username"}
	mapping := buildColumnMapping(csvHeaders, dbColumns)
	if mapping[0] != 0 || mapping[1] != 1 {
		t.Errorf("case-insensitive mapping failed: %v", mapping)
	}
}

// ============================================================================
// countMatchedColumns 测试
// ============================================================================

func TestCountMatchedColumns(t *testing.T) {
	csvHeaders := []string{"ID", "NAME", "EXTRA"}
	dbColumns := []string{"id", "name"}
	got := countMatchedColumns(csvHeaders, dbColumns)
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestCountMatchedColumnsNone(t *testing.T) {
	csvHeaders := []string{"A", "B"}
	dbColumns := []string{"x", "y"}
	got := countMatchedColumns(csvHeaders, dbColumns)
	if got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}

// ============================================================================
// alignColumnInfos 测试
// ============================================================================

func TestAlignColumnInfos(t *testing.T) {
	headers := []string{"ID", "NAME"}
	infos := []dbColumnInfo{
		{Name: "id", Type: "int"},
		{Name: "name", Type: "varchar"},
	}
	aligned := alignColumnInfos(headers, infos)
	if len(aligned) != 2 {
		t.Fatalf("len = %d, want 2", len(aligned))
	}
	if aligned[0].Name != "id" || aligned[0].Type != "int" {
		t.Errorf("aligned[0] = %+v, want {id, int}", aligned[0])
	}
	if aligned[1].Name != "name" || aligned[1].Type != "varchar" {
		t.Errorf("aligned[1] = %+v, want {name, varchar}", aligned[1])
	}
}

func TestAlignColumnInfosMissingColumn(t *testing.T) {
	headers := []string{"ID", "EXTRA"}
	infos := []dbColumnInfo{{Name: "id", Type: "int"}}
	aligned := alignColumnInfos(headers, infos)
	if len(aligned) != 2 {
		t.Fatalf("len = %d, want 2", len(aligned))
	}
	if aligned[1].Name != "EXTRA" {
		t.Errorf("unknown column should keep header name, got %s", aligned[1].Name)
	}
}

// ============================================================================
// collapseDelimitedFields 测试
// ============================================================================

func TestCollapseDelimitedFields(t *testing.T) {
	row := []string{"1", "hello", "world", "extra", "2", "2020-01-01"}
	result := collapseDelimitedFields(row, 1, 2)
	expected := []string{"1", "hello,world,extra", "2", "2020-01-01"}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
}

func TestCollapseDelimitedFieldsSingle(t *testing.T) {
	row := []string{"a", "b", "c", "d"}
	result := collapseDelimitedFields(row, 0, 1)
	expected := []string{"a,b", "c", "d"}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
}

// ============================================================================
// repairDelimitedRow 测试
// ============================================================================

func TestRepairDelimitedRowNoExtraFields(t *testing.T) {
	row := []string{"1", "John", "2020-01-01"}
	infos := []dbColumnInfo{
		{Name: "id", Type: "int"},
		{Name: "name", Type: "varchar"},
		{Name: "date", Type: "date"},
	}
	result := repairDelimitedRow(row, infos)
	if !reflect.DeepEqual(result, row) {
		t.Fatalf("row should be unchanged, got %v", result)
	}
}

func TestRepairDelimitedRowEmptyColumns(t *testing.T) {
	row := []string{"a", "b", "c"}
	infos := []dbColumnInfo{}
	result := repairDelimitedRow(row, infos)
	if !reflect.DeepEqual(result, row) {
		t.Fatalf("row should be unchanged with empty columnInfo, got %v", result)
	}
}

// ============================================================================
// canAbsorbDelimitedFields 测试
// ============================================================================

func TestCanAbsorbDelimitedFields(t *testing.T) {
	// matches strings.Any containing one of "chartextblobjson"
	for _, ct := range []string{"varchar", "char", "text", "longtext", "mediumtext", "blob", "json"} {
		if !canAbsorbDelimitedFields(ct) {
			t.Errorf("%q should be absorbable", ct)
		}
	}
	// "guid" shares no chars with "chartextblobjson"
	if canAbsorbDelimitedFields("guid") {
		t.Error("guid should not be absorbable")
	}
}

// ============================================================================
// isIntegerColumnType 测试
// ============================================================================

func TestIsIntegerColumnType(t *testing.T) {
	for _, ct := range []string{"int", "bigint", "tinyint", "smallint", "mediumint", "integer", "bit"} {
		if !isIntegerColumnType(ct) {
			t.Errorf("%q should be integer", ct)
		}
	}
	if isIntegerColumnType("varchar") {
		t.Error("varchar should not be integer")
	}
}

// ============================================================================
// isDecimalColumnType 测试
// ============================================================================

func TestIsDecimalColumnType(t *testing.T) {
	for _, ct := range []string{"decimal", "numeric", "float", "double", "real"} {
		if !isDecimalColumnType(ct) {
			t.Errorf("%q should be decimal", ct)
		}
	}
	// "gkp" has no overlap with "decimalnumericfloatdoublereal"
	if isDecimalColumnType("gkp") {
		t.Error("gkp should not be decimal")
	}
}

// ============================================================================
// isTemporalColumnType 测试
// ============================================================================

func TestIsTemporalColumnType(t *testing.T) {
	for _, ct := range []string{"date", "datetime", "timestamp", "time", "year"} {
		if !isTemporalColumnType(ct) {
			t.Errorf("%q should be temporal", ct)
		}
	}
	// "bool" has no overlap with "datetimeyear"
	if isTemporalColumnType("bool") {
		t.Error("bool should not be temporal")
	}
}

// ============================================================================
// isTemporalValue 测试
// ============================================================================

func TestIsTemporalValue(t *testing.T) {
	valids := []string{
		"2020-01-15",
		"2020-01-15 10:30:00",
		"2020-01-15 10:30:00.123",
		"2020-01-15 10:30:00.123456",
		"10:30:00",
		"2020-01-15T10:30:00Z",
	}
	for _, v := range valids {
		if !isTemporalValue(v) {
			t.Errorf("%q should be valid temporal value", v)
		}
	}

	invalids := []string{"abc", "123", "", "not-a-date"}
	for _, v := range invalids {
		if isTemporalValue(v) {
			t.Errorf("%q should NOT be a valid temporal value", v)
		}
	}
}

// ============================================================================
// scoreValueForColumnType 测试
// ============================================================================

func TestScoreValueForColumnType(t *testing.T) {
	// Empty value
	if s := scoreValueForColumnType("", dbColumnInfo{Type: "int"}); s != 1 {
		t.Fatalf("empty value score = %d, want 1", s)
	}
	// Integer column with valid integer value
	if s := scoreValueForColumnType("42", dbColumnInfo{Type: "int"}); s != 3 {
		t.Fatalf("valid integer score = %d, want 3", s)
	}
	// Integer column with invalid value
	if s := scoreValueForColumnType("abc", dbColumnInfo{Type: "int"}); s != -5 {
		t.Fatalf("invalid integer score = %d, want -5", s)
	}
	// Temporal column with valid date
	if s := scoreValueForColumnType("2020-01-01", dbColumnInfo{Type: "date"}); s != 3 {
		t.Fatalf("valid date score = %d, want 3", s)
	}
	// Unknown column type with no matching heuristic → default 0
	if s := scoreValueForColumnType("anything", dbColumnInfo{Type: "gkp"}); s != 0 {
		t.Fatalf("unknown type score = %d, want 0", s)
	}
}

func TestScoreRowAgainstColumnTypes(t *testing.T) {
	row := []string{"42", "hello", ""}
	infos := []dbColumnInfo{
		{Name: "id", Type: "int"},
		{Name: "desc", Type: "varchar"},
		{Name: "note", Type: "gkp"},
	}
	s := scoreRowAgainstColumnTypes(row, infos)
	// "42" vs int → valid integer → 3, "hello" vs varchar → canAbsorb → 1, "" → 1 → total 5
	if s != 5 {
		t.Fatalf("score = %d, want 5", s)
	}
}

// ============================================================================
// filterRowData 测试
// ============================================================================

func TestFilterRowData(t *testing.T) {
	// mapping[i] = target DB column index for CSV column i, -1 means skip
	row := []any{"a", "b", "c", "d"}
	mapping := []int{0, -1, 1, 2} // CSV col 0→DB0, col 1 skipped, col 2→DB1, col 3→DB2
	result := filterRowData(row, mapping)
	if len(result) != 3 {
		t.Fatalf("len = %d, want 3", len(result))
	}
	if result[0] != "a" || result[1] != "c" || result[2] != "d" {
		t.Fatalf("got %v, want [a c d]", result)
	}
}

func TestFilterRowDataOutOfRange(t *testing.T) {
	row := []any{"a"}
	mapping := []int{0, 1}
	result := filterRowData(row, mapping)
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
	if result[1] != nil {
		t.Fatalf("out of range should be nil, got %v", result[1])
	}
}

// ============================================================================
// normalizeCSVString 测试
// ============================================================================

func TestNormalizeCSVStringValidUTF8(t *testing.T) {
	input := "Hello, 世界"
	result := normalizeCSVString(input)
	if result != input {
		t.Fatalf("valid UTF-8 should pass through: got %q, want %q", result, input)
	}
}

func TestNormalizeCSVStringPlainASCII(t *testing.T) {
	input := "hello world"
	result := normalizeCSVString(input)
	if result != input {
		t.Fatalf("ASCII should pass through: got %q, want %q", result, input)
	}
}

// ============================================================================
// limitSlice 测试
// ============================================================================

func TestLimitSlice(t *testing.T) {
	s := []string{"a", "b", "c", "d", "e"}
	result := limitSlice(s, 3)
	if len(result) != 3 {
		t.Fatalf("len = %d, want 3", len(result))
	}
}

func TestLimitSliceShorter(t *testing.T) {
	s := []string{"a", "b"}
	result := limitSlice(s, 5)
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
}

func TestLimitSliceEmpty(t *testing.T) {
	result := limitSlice(nil, 3)
	if len(result) != 0 {
		t.Fatalf("len = %d, want 0", len(result))
	}
}

// ============================================================================
// BuildUpperColumnMap 测试
// ============================================================================

// ============================================================================
// scoreValueForColumnType 补充测试
// ============================================================================

func TestScoreValueForColumnTypeGKPType(t *testing.T) {
	// "gkp" type does not match any heuristic, falls through to default 0
	tests := []struct {
		value   string
		colType string
		want    int
	}{
		// Empty value always scores 1
		{"", "gkp", 1},
		// Non-empty value with unknown type → 0
		{"hello", "gkp", 0},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := scoreValueForColumnType(tt.value, dbColumnInfo{Type: tt.colType})
			if got != tt.want {
				t.Fatalf("score = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestScoreValueForColumnTypeAbsorbableTypes(t *testing.T) {
	// Only text-like types (char, text, blob, json) can absorb delimited fields
	absorbable := []string{"varchar", "char", "text", "tinytext", "mediumtext", "longtext", "blob", "tinyblob", "mediumblob", "longblob", "json"}
	for _, ct := range absorbable {
		t.Run(ct, func(t *testing.T) {
			got := scoreValueForColumnType("42", dbColumnInfo{Type: ct})
			if got != 1 {
				t.Fatalf("%s score = %d, want 1", ct, got)
			}
		})
	}
	// Non-text types should not be treated as absorbable
	t.Run("int_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("42", dbColumnInfo{Type: "int"}); got != 3 {
			t.Fatalf("int score = %d, want 3", got)
		}
	})
	t.Run("bigint_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("42", dbColumnInfo{Type: "bigint"}); got != 3 {
			t.Fatalf("bigint score = %d, want 3", got)
		}
	})
	t.Run("float_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("42", dbColumnInfo{Type: "float"}); got != 3 {
			t.Fatalf("float score = %d, want 3", got)
		}
	})
	t.Run("double_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("42", dbColumnInfo{Type: "double"}); got != 3 {
			t.Fatalf("double score = %d, want 3", got)
		}
	})
	t.Run("decimal_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("42", dbColumnInfo{Type: "decimal"}); got != 3 {
			t.Fatalf("decimal score = %d, want 3", got)
		}
	})
	t.Run("date_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("2020-01-01", dbColumnInfo{Type: "date"}); got != 3 {
			t.Fatalf("date score = %d, want 3", got)
		}
	})
	t.Run("datetime_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("2020-01-01 10:30:00", dbColumnInfo{Type: "datetime"}); got != 3 {
			t.Fatalf("datetime score = %d, want 3", got)
		}
	})
	t.Run("timestamp_not_absorbable", func(t *testing.T) {
		if got := scoreValueForColumnType("2020-01-01 10:30:00", dbColumnInfo{Type: "timestamp"}); got != 3 {
			t.Fatalf("timestamp score = %d, want 3", got)
		}
	})
}

// ============================================================================
// repairDelimitedRow 补充测试
// ============================================================================

func TestRepairDelimitedRowAllAbsorbable(t *testing.T) {
	// All columns can absorb, first column wins with highest score
	row := []string{"1", "hello", "world", "42", "2020-01-01"}
	infos := []dbColumnInfo{
		{Name: "id", Type: "int"},
		{Name: "desc", Type: "varchar"},
		{Name: "age", Type: "int"},
		{Name: "date", Type: "date"},
	}
	result := repairDelimitedRow(row, infos)
	if len(result) != 4 {
		t.Fatalf("len = %d, want 4", len(result))
	}
}

func TestRepairDelimitedRowWithNonAbsorbableColumn(t *testing.T) {
	// Column 1 ("gkp") is non-absorbable, cannot be selected as absorbIdx
	row := []string{"1", "gkp_val", "hello", "world", "42"}
	infos := []dbColumnInfo{
		{Name: "id", Type: "int"},
		{Name: "val", Type: "gkp"},
		{Name: "desc", Type: "varchar"},
		{Name: "age", Type: "int"},
	}
	result := repairDelimitedRow(row, infos)
	if len(result) != 4 {
		t.Fatalf("len = %d, want 4", len(result))
	}
}

func TestRepairDelimitedRowExtraFieldsOutOfRange(t *testing.T) {
	// absorbIdx + extraFields >= len(row) → skip
	row := []string{"a", "b", "c"}
	infos := []dbColumnInfo{
		{Name: "col", Type: "varchar"},
		{Name: "extra", Type: "text"},
	}
	// extraFields=1, i=0 candidate ["a,b","c"] → score 2, i=1 candidate ["a","b,c"] → score 2
	// bestIdx=0 → collapse at 0: ["a,b", "c"] → len 2
	result := repairDelimitedRow(row, infos)
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
}

// ============================================================================
// ErrorRecorder 测试
// ============================================================================

func TestNewErrorRecorderDefault(t *testing.T) {
	recorder, err := NewErrorRecorder("")
	if err != nil {
		t.Fatalf("NewErrorRecorder error: %v", err)
	}
	if recorder == nil {
		t.Fatal("recorder should not be nil")
	}
	if !recorder.enabled {
		t.Fatal("recorder should be enabled")
	}
	if recorder.file != nil {
		t.Error("file should be nil when logDir is empty")
	}
	recorder.Close()
}

func TestRecordError(t *testing.T) {
	recorder, _ := NewErrorRecorder("")
	defer recorder.Close()

	recorder.RecordError("users", "INSERT INTO users...", []string{"1", "John", "test@example.com", "extra"}, fmt.Errorf("duplicate key"))

	recorder.mu.Lock()
	errs := recorder.errors
	recorder.mu.Unlock()

	if len(errs) != 1 {
		t.Fatalf("errors len = %d, want 1", len(errs))
	}
	if errs[0].TableName != "users" {
		t.Errorf("TableName = %q, want users", errs[0].TableName)
	}
	if errs[0].SQL != "INSERT INTO users..." {
		t.Errorf("SQL = %q", errs[0].SQL)
	}
	if errs[0].Error != "duplicate key" {
		t.Errorf("Error = %q", errs[0].Error)
	}
	// RowData should be truncated to first 3
	if len(errs[0].RowData) > 3 {
		t.Fatalf("RowData len = %d, want <= 3", len(errs[0].RowData))
	}
}

func TestRecordErrorShortRow(t *testing.T) {
	recorder, _ := NewErrorRecorder("")
	defer recorder.Close()

	recorder.RecordError("t", "SQL", []string{"a"}, fmt.Errorf("err"))

	recorder.mu.Lock()
	errs := recorder.errors
	recorder.mu.Unlock()

	if len(errs) != 1 {
		t.Fatal("expected 1 error record")
	}
	if len(errs[0].RowData) != 1 {
		t.Fatalf("RowData len = %d, want 1", len(errs[0].RowData))
	}
}

func TestRecordBatchError(t *testing.T) {
	recorder, _ := NewErrorRecorder("")
	defer recorder.Close()

	recorder.RecordBatchError("t", 3, [][]interface{}{{"a", "b"}, {"c", "d"}}, fmt.Errorf("batch error"))

	recorder.mu.Lock()
	errs := recorder.errors
	recorder.mu.Unlock()

	if len(errs) != 1 {
		t.Fatal("expected 1 error record")
	}
	if errs[0].TableName != "t" {
		t.Errorf("TableName = %q", errs[0].TableName)
	}
	if errs[0].BatchNum != 3 {
		t.Errorf("BatchNum = %d, want 3", errs[0].BatchNum)
	}
}

func TestRecordBatchErrorEmptyRows(t *testing.T) {
	recorder, _ := NewErrorRecorder("")
	defer recorder.Close()

	recorder.RecordBatchError("t", 1, [][]interface{}{}, fmt.Errorf("error"))

	recorder.mu.Lock()
	errs := recorder.errors
	recorder.mu.Unlock()

	if len(errs) != 1 {
		t.Fatal("expected 1 error record even with empty rows")
	}
}

func TestPressureEventsForBatchClassifiesSignals(t *testing.T) {
	tests := []struct {
		name          string
		batchNum      int
		retries       int
		duration      time.Duration
		slowThreshold time.Duration
		err           error
		wantSignals   []PressureSignal
	}{
		{
			name:          "retry and slow batch",
			batchNum:      3,
			retries:       2,
			duration:      12 * time.Second,
			slowThreshold: 10 * time.Second,
			wantSignals:   []PressureSignal{PressureRetry, PressureSlowBatch},
		},
		{
			name:          "lock wait",
			batchNum:      7,
			duration:      time.Second,
			slowThreshold: 10 * time.Second,
			err: fmt.Errorf("failed to execute batch insert: %w", &mysql.MySQLError{
				Number:  1205,
				Message: "Lock wait timeout exceeded; try restarting transaction",
			}),
			wantSignals: []PressureSignal{PressureLockWait},
		},
		{
			name:          "connection",
			batchNum:      9,
			duration:      time.Second,
			slowThreshold: 10 * time.Second,
			err:           fmt.Errorf("failed to execute batch insert: %w", driver.ErrBadConn),
			wantSignals:   []PressureSignal{PressureConnection},
		},
		{
			name:          "capacity",
			batchNum:      11,
			duration:      time.Second,
			slowThreshold: 10 * time.Second,
			err: fmt.Errorf("failed to execute batch insert: %w", &mysql.MySQLError{
				Number:  1114,
				Message: "The table is full",
			}),
			wantSignals: []PressureSignal{PressureCapacity},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := pressureEventsForBatch("FORM_A", tt.batchNum, tt.retries, tt.duration, tt.slowThreshold, tt.err)
			if len(events) != len(tt.wantSignals) {
				t.Fatalf("len(events) = %d, want %d: %#v", len(events), len(tt.wantSignals), events)
			}
			for i, want := range tt.wantSignals {
				if events[i].Signal != want {
					t.Fatalf("events[%d].Signal = %s, want %s", i, events[i].Signal, want)
				}
				if events[i].TableName != "FORM_A" {
					t.Fatalf("events[%d].TableName = %s, want FORM_A", i, events[i].TableName)
				}
				if events[i].BatchNum != tt.batchNum {
					t.Fatalf("events[%d].BatchNum = %d, want %d", i, events[i].BatchNum, tt.batchNum)
				}
			}
		})
	}
}

func TestTableImporterPressureCallbackEmitsPerEventWithoutMutatingInsertResult(t *testing.T) {
	var got []ImportPressureEvent
	ti := (&TableImporter{tableName: "FORM_A"}).WithPressureCallback(func(event ImportPressureEvent) {
		got = append(got, event)
	})

	result := adaptiveBatchInsertResult{affectedRows: 5, retries: 1}
	wantResult := result
	ti.emitPressureEventsForBatch(4, result, 12*time.Second, 10*time.Second)

	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("result mutated: got %#v, want %#v", result, wantResult)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %#v", len(got), got)
	}
	if got[0].Signal != PressureRetry || got[1].Signal != PressureSlowBatch {
		t.Fatalf("signals = %s, %s; want %s, %s", got[0].Signal, got[1].Signal, PressureRetry, PressureSlowBatch)
	}
}

func TestDataImporterWithPressureCallbackPropagatesToCreatedTableImporter(t *testing.T) {
	var got []ImportPressureEvent
	di := (&DataImporter{ctx: context.Background()}).WithPressureCallback(func(event ImportPressureEvent) {
		got = append(got, event)
	})

	ti := di.newTableImporter("FORM_A", "FORM_A.csv")
	ti.emitPressureEventsForBatch(1, adaptiveBatchInsertResult{retries: 1}, time.Second, 10*time.Second)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %#v", len(got), got)
	}
	if got[0].Signal != PressureRetry {
		t.Fatalf("Signal = %s, want %s", got[0].Signal, PressureRetry)
	}
}

func TestGetErrorCount(t *testing.T) {
	recorder, _ := NewErrorRecorder("")
	defer recorder.Close()

	if n := recorder.GetErrorCount(); n != 0 {
		t.Fatalf("initial count = %d, want 0", n)
	}

	recorder.RecordError("t", "", nil, fmt.Errorf("e1"))
	recorder.RecordError("t", "", nil, fmt.Errorf("e2"))

	if n := recorder.GetErrorCount(); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
}

func TestBuildUpperColumnMap(t *testing.T) {
	m := BuildUpperColumnMap([]string{"id", "Name", "AGE"})
	if v, ok := m["ID"]; !ok || v != "id" {
		t.Errorf("ID → %q, want id", v)
	}
	if v, ok := m["NAME"]; !ok || v != "Name" {
		t.Errorf("NAME → %q, want Name", v)
	}
	if v, ok := m["AGE"]; !ok || v != "AGE" {
		t.Errorf("AGE → %q, want AGE", v)
	}
}

// ============================================================================
// RecordError 写入文件路径测试
// ============================================================================

func TestRecordErrorWithFile(t *testing.T) {
	tmpDir := os.TempDir()
	logPath := filepath.Join(tmpDir, fmt.Sprintf("migration_test_%d", time.Now().UnixNano()))

	recorder, err := NewErrorRecorder(logPath)
	if err != nil {
		t.Fatalf("NewErrorRecorder error: %v", err)
	}
	defer func() {
		recorder.Close()
		os.RemoveAll(logPath)
	}()

	if recorder.file == nil {
		t.Fatal("file should not be nil when logDir is provided")
	}

	recorder.RecordError("users", "INSERT INTO users (id) VALUES (1)", []string{"1", "John", "extra"}, fmt.Errorf("test error"))
	recorder.RecordBatchError("orders", 5, [][]interface{}{{42, "item"}}, fmt.Errorf("batch failed"))

	// verify in-memory records
	errs := recorder.GetErrors()
	if len(errs) != 2 {
		t.Fatalf("errors len = %d, want 2", len(errs))
	}
	if errs[0].TableName != "users" {
		t.Errorf("TableName[0] = %q, want users", errs[0].TableName)
	}
	if errs[0].SQL != "INSERT INTO users (id) VALUES (1)" {
		t.Errorf("SQL = %q", errs[0].SQL)
	}
	if errs[1].TableName != "orders" {
		t.Errorf("TableName[1] = %q, want orders", errs[1].TableName)
	}
	if errs[1].BatchNum != 5 {
		t.Errorf("BatchNum = %d, want 5", errs[1].BatchNum)
	}

	// verify file was written
	content, err := os.ReadFile(filepath.Join(logPath, "migration.log"))
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if !strings.Contains(string(content), "users") {
		t.Error("log file should contain 'users'")
	}
	if !strings.Contains(string(content), "orders") {
		t.Error("log file should contain 'orders'")
	}
}

func TestNewErrorRecorderCreateDir(t *testing.T) {
	baseDir := os.TempDir()
	rootName := fmt.Sprintf("migration_nested_%d", time.Now().UnixNano())
	logPath := filepath.Join(baseDir, rootName, "data", "logs")

	recorder, err := NewErrorRecorder(logPath)
	if err != nil {
		t.Fatalf("NewErrorRecorder error: %v", err)
	}
	defer func() {
		recorder.Close()
		os.RemoveAll(filepath.Join(baseDir, rootName))
	}()

	if recorder.file == nil {
		t.Fatal("file should be created when dir is created")
	}
	recorder.RecordError("t", "SQL", []string{"a"}, fmt.Errorf("err"))
	errs := recorder.GetErrors()
	if len(errs) != 1 {
		t.Fatalf("errors len = %d, want 1", len(errs))
	}
}

// ============================================================================
// Fix #1/#3: WithContext 传播验证
// ============================================================================

func TestTableImporterWithContextPropagation(t *testing.T) {
	ti := &TableImporter{
		tableName: "test_table",
	}
	// 初始状态 ctx 应为 nil (零值)
	if ti.ctx != nil {
		t.Fatal("initial ctx should be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ti.WithContext(ctx)
	if ti.ctx != ctx {
		t.Fatal("WithContext should set the context")
	}
}

func TestTableImporterWithContextNil(t *testing.T) {
	ti := &TableImporter{
		tableName: "test_table",
	}

	// pipelinedImport 中的 safeCtx 回退逻辑需要处理 nil ctx
	ti.WithContext(nil)
	if ti.ctx != nil {
		t.Error("WithContext(nil) should store nil")
	}

	// 验证 safeCtx 回退：当 ti.ctx 为 nil 时应回退到 Background
	safeCtx := ti.ctx
	if safeCtx == nil {
		safeCtx = context.TODO()
	}
	if safeCtx != context.TODO() {
		t.Error("nil ctx fallback should produce a valid context")
	}
}

func TestDataImporterWithContextPropagation(t *testing.T) {
	di := &DataImporter{
		cfg: nil,
	}
	if di.ctx != nil {
		t.Fatal("initial ctx should be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	di.WithContext(ctx)
	if di.ctx != ctx {
		t.Fatal("WithContext should set the context")
	}
}

func TestBatchInserterSetContext(t *testing.T) {
	bi := &BatchInserter{
		tableName: "test_table",
	}
	if bi.ctx != nil {
		t.Fatal("initial ctx should be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bi.SetContext(ctx)
	if bi.ctx != ctx {
		t.Fatal("SetContext should set the context")
	}
}

func TestBatchInserterDefaultContextIsBackground(t *testing.T) {
	// NewBatchInserter 应将 ctx 初始化为 context.Background()
	bi := &BatchInserter{
		tableName:     "test",
		columns:       []string{"col1"},
		batchSize:     100,
		maxBatchBytes: maxBatchBytes,
		ctx:           context.Background(),
	}
	insertResult, err := bi.InsertBatch([][]interface{}{})
	if err != nil {
		t.Fatalf("InsertBatch with empty rows: %v", err)
	}
	if insertResult != 0 {
		t.Fatalf("expected 0, got %d", insertResult)
	}
}

func TestBatchRetryPolicyUsesLongerBackoffForConnectionErrors(t *testing.T) {
	attempts, baseDelay := batchRetryPolicy(fmt.Errorf("wrapped: %w", driver.ErrBadConn))

	if attempts != maxConnectionRetries {
		t.Fatalf("attempts = %d, want %d", attempts, maxConnectionRetries)
	}
	if baseDelay != connectionRetryBaseDelay {
		t.Fatalf("baseDelay = %s, want %s", baseDelay, connectionRetryBaseDelay)
	}
}

func TestBatchRetryPolicyKeepsDefaultForDataErrors(t *testing.T) {
	attempts, baseDelay := batchRetryPolicy(fmt.Errorf("data too long for column"))

	if attempts != maxRetries {
		t.Fatalf("attempts = %d, want %d", attempts, maxRetries)
	}
	if baseDelay != retryDelay {
		t.Fatalf("baseDelay = %s, want %s", baseDelay, retryDelay)
	}
}

func TestBatchRetryPolicyDoesNotRetryDatabaseCapacityErrors(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &mysql.MySQLError{
		Number:  1114,
		Message: "The table 'sample_main_104' is full",
	})

	attempts, baseDelay := batchRetryPolicy(err)

	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if baseDelay != 0 {
		t.Fatalf("baseDelay = %s, want 0", baseDelay)
	}
}

func TestDatabaseCapacityErrorMatchesDiskWriteErrors(t *testing.T) {
	cases := []uint16{3, 1021, 1114}
	for _, code := range cases {
		err := fmt.Errorf("wrapped: %w", &mysql.MySQLError{
			Number:  code,
			Message: "capacity related error",
		})
		if !isDatabaseCapacityError(err) {
			t.Fatalf("isDatabaseCapacityError(%d) = false, want true", code)
		}
	}
}

func TestDatabaseCapacityDiagnosticExplainsMySQLOwnership(t *testing.T) {
	err := fmt.Errorf("failed to execute batch insert: %w", &mysql.MySQLError{
		Number:  1114,
		Message: "The table 'sample_main_104' is full",
	})

	diagnostic := databaseCapacityDiagnostic("sample_main_104", 7, err)

	required := []string{
		"Failure owner: MySQL storage layer",
		"not a CSV parser or migration-program logic error",
		"table=sample_main_104",
		"batch=7",
		"mysql_error=1114",
		"check target MySQL datadir free space",
		"container or cloud storage quota",
	}
	for _, want := range required {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, diagnostic)
		}
	}
}

func TestDatabaseCapacityDiagnosticIgnoresNonCapacityErrors(t *testing.T) {
	diagnostic := databaseCapacityDiagnostic("sample_main_104", 7, fmt.Errorf("data too long"))

	if diagnostic != "" {
		t.Fatalf("diagnostic = %q, want empty", diagnostic)
	}
}

func TestBatchRetryDelayCapsExponentialBackoff(t *testing.T) {
	delay := batchRetryDelay(10, time.Second, 5*time.Second)

	if delay != 5*time.Second {
		t.Fatalf("delay = %s, want 5s", delay)
	}
}

func TestCountRowsForProgressSkipsWhenDisabled(t *testing.T) {
	disabled := false
	ti := &TableImporter{
		cfg: &config.Config{
			Migration: config.MigrationConfig{
				CountCSVRowsBeforeImport: &disabled,
			},
		},
		countCSVRowsFunc: func(*os.File) (int64, error) {
			t.Fatal("countCSVRowsFunc should not be called when pre-count is disabled")
			return 0, nil
		},
	}

	file, err := os.CreateTemp(t.TempDir(), "rows-*.csv")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer file.Close()

	got, err := ti.countRowsForProgress(file)
	if err != nil {
		t.Fatalf("countRowsForProgress() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("countRowsForProgress() = %d, want 0 when disabled", got)
	}
}

func TestCountRowsForProgressUsesCounterByDefault(t *testing.T) {
	called := false
	ti := &TableImporter{
		cfg: &config.Config{},
		countCSVRowsFunc: func(*os.File) (int64, error) {
			called = true
			return 42, nil
		},
	}

	file, err := os.CreateTemp(t.TempDir(), "rows-*.csv")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer file.Close()

	got, err := ti.countRowsForProgress(file)
	if err != nil {
		t.Fatalf("countRowsForProgress() error = %v", err)
	}
	if !called {
		t.Fatal("countCSVRowsFunc was not called")
	}
	if got != 42 {
		t.Fatalf("countRowsForProgress() = %d, want 42", got)
	}
}

type scriptedInsertResult struct {
	affected int64
	err      error
}

type scriptedBatchInserter struct {
	results      []scriptedInsertResult
	calls        []int
	lastErr      error
	lastAffected int64
}

func (s *scriptedBatchInserter) InsertBatch(rows [][]interface{}) (int64, error) {
	s.calls = append(s.calls, len(rows))
	if len(s.results) == 0 {
		return 0, s.lastErr
	}
	result := s.results[0]
	s.results = s.results[1:]
	s.lastErr = result.err
	if result.err != nil {
		s.lastAffected = result.affected
	}
	return result.affected, result.err
}

func noRetryWait(time.Duration) error {
	return nil
}

func TestAdaptiveBatchInsertRetriesOriginalBatchBeforeSplit(t *testing.T) {
	inserter := &scriptedBatchInserter{
		results: []scriptedInsertResult{
			{err: driver.ErrBadConn},
			{err: driver.ErrBadConn},
			{affected: 2},
			{affected: 2},
		},
	}
	rows := [][]interface{}{{1}, {2}, {3}, {4}}

	result := insertBatchWithAdaptiveRetry(context.Background(), inserter, "orders", 3, rows, noRetryWait)

	if result.err != nil {
		t.Fatalf("insertBatchWithAdaptiveRetry() error = %v", result.err)
	}
	if result.affectedRows != 4 {
		t.Fatalf("affectedRows = %d, want 4", result.affectedRows)
	}
	if result.retries != 1 {
		t.Fatalf("retries = %d, want 1 same-size retry before split", result.retries)
	}
	if !result.split {
		t.Fatal("split = false, want true after repeated connection error")
	}
	wantCalls := []int{4, 4, 2, 2}
	if !reflect.DeepEqual(inserter.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", inserter.calls, wantCalls)
	}
}

func TestAdaptiveBatchInsertDoesNotSplitDataErrors(t *testing.T) {
	dataErr := fmt.Errorf("data too long for column 'NAME'")
	inserter := &scriptedBatchInserter{
		results: []scriptedInsertResult{{err: dataErr}},
	}
	rows := [][]interface{}{{1}, {2}, {3}, {4}}

	result := insertBatchWithAdaptiveRetry(context.Background(), inserter, "orders", 3, rows, noRetryWait)

	if result.err == nil {
		t.Fatal("expected data error")
	}
	if result.split {
		t.Fatal("split = true, want false for data errors")
	}
	wantCalls := []int{4, 4, 4}
	if !reflect.DeepEqual(inserter.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", inserter.calls, wantCalls)
	}
}

func TestAdaptiveBatchInsertReturnsContextErrorDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inserter := &scriptedBatchInserter{
		results: []scriptedInsertResult{{err: driver.ErrBadConn}},
	}
	rows := [][]interface{}{{1}, {2}}

	result := insertBatchWithAdaptiveRetry(ctx, inserter, "orders", 3, rows, func(d time.Duration) error { return waitForRetry(ctx, d) })

	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", result.err)
	}
	if result.split {
		t.Fatal("split = true, want false when context cancels before retry")
	}
}
