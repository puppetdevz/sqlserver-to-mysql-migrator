package importer

import (
	"context"
	"errors"
	"fmt"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func legacyQuery(bi *BatchInserter, numRows int) string {
	var query strings.Builder
	if bi.onDuplicate == "replace" {
		query.WriteString("REPLACE INTO ")
	} else {
		query.WriteString("INSERT IGNORE INTO ")
	}
	query.WriteString(bi.quotedTable + " ")
	query.WriteString(fmt.Sprintf("(%s) VALUES ", strings.Join(bi.quotedColumns, ", ")))
	values := make([]string, numRows)
	for i := range values {
		p := make([]string, len(bi.columns))
		for j := range p {
			p[j] = "?"
		}
		values[i] = fmt.Sprintf("(%s)", strings.Join(p, ", "))
	}
	query.WriteString(strings.Join(values, ", "))
	return query.String()
}
func legacyRanges(rows [][]any, maxRows int, maxBytes int64) []insertRange {
	if len(rows) == 0 {
		return nil
	}
	if maxRows < 1 {
		maxRows = 1
	}
	ranges := make([]insertRange, 0, (len(rows)+maxRows-1)/maxRows)
	for i := 0; i < len(rows); {
		end := i + maxRows
		if end > len(rows) {
			end = len(rows)
		}
		if maxBytes > 0 {
			for end > i+1 && estimateBatchBytes(rows[i:end]) > maxBytes {
				end = (i + end) / 2
			}
		}
		ranges = append(ranges, insertRange{i, end})
		i = end
	}
	return ranges
}
func TestQueryEquivalentAcrossShapes(t *testing.T) {
	for _, table := range []string{"T", "with`quote", "with?mark"} {
		for _, columns := range [][]string{{"a"}, {"a", "b?", "q`"}, make([]string, 201)} {
			for _, mode := range []string{"replace", "ignore"} {
				for _, n := range []int{0, 1, 2, 309, 1000} {
					bi := NewBatchInserter(nil, table, columns, 1000, mode)
					if got, want := bi.buildInsertQuery(n), legacyQuery(bi, n); got != want {
						t.Fatalf("query mismatch %s %d columns %d rows", table, len(columns), n)
					}
				}
			}
		}
	}
}
func TestRangesEquivalentAcrossBoundaries(t *testing.T) {
	rows := make([][]any, 1000)
	for i := range rows {
		rows[i] = []any{strings.Repeat("x", i%200), int64(i), nil, []byte{1, 2}}
	}
	for _, n := range []int{0, 1, 2, 31, 500, 1000} {
		for _, bytes := range []int64{-1, 0, 1, 40, 100, 1024, 1 << 20} {
			if got, want := planInsertRanges(rows, n, bytes), legacyRanges(rows, n, bytes); !reflect.DeepEqual(got, want) {
				t.Fatalf("n=%d bytes=%d ranges differ", n, bytes)
			}
		}
	}
}
func TestPlanRangesExtremeRowLimitDoesNotOverflow(t *testing.T) {
	got := planInsertRanges([][]any{{"x"}, {"y"}}, math.MaxInt, 0)
	if !reflect.DeepEqual(got, []insertRange{{0, 2}}) {
		t.Fatalf("got %v", got)
	}
	if maxRowsPerBatchForColumns(math.MaxInt) != 1 {
		t.Fatal("column bound overflow")
	}
}
func TestStatementCacheUsesExactSQLIncludingQuestionMarkIdentifiers(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "?T", []string{"a"}, 2, "ignore")
	defer bi.Close()
	query := bi.buildInsertQuery(1)
	mock.ExpectPrepare(query).WillBeClosed().ExpectExec().WithArgs("first").WillReturnResult(sqlmock.NewResult(0, 1))
	if _, err := bi.InsertBatch([][]any{{"first"}}); err != nil {
		t.Fatal(err)
	}
	// Same SQL must hit despite '?' in the quoted identifier.
	mock.ExpectExec(query).WithArgs("second").WillReturnResult(sqlmock.NewResult(0, 1))
	if _, err := bi.InsertBatch([][]any{{"second"}}); err != nil {
		t.Fatal(err)
	}
	if err := bi.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSingleRowMemoryAndPlaceholderOversizeFailsBeforePrepare(t *testing.T) {
	for _, wide := range []bool{false, true} {
		db, mock := newMockDB(t)
		columns := []string{"a"}
		row := []any{strings.Repeat("x", 1024)}
		if wide {
			columns = make([]string, maxPreparedPlaceholders+1)
			row = make([]any, len(columns))
		}
		bi := NewBatchInserter(db, "T", columns, 1, "ignore")
		bi.SetContext(context.Background())
		bi.rowMemoryLimit = 512
		if _, err := bi.InsertBatch([][]any{row}); !errors.Is(err, migration.ErrResourceLimit) {
			t.Fatalf("want resource rejection before Prepare, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}
func BenchmarkQueryComparison(b *testing.B) {
	bi := NewBatchInserter(nil, "T", make([]string, 32), 1000, "replace")
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprintf("legacy=%t", legacy), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if legacy {
					benchmarkSQL = legacyQuery(bi, 1000)
				} else {
					benchmarkSQL = bi.buildInsertQuery(1000)
				}
			}
		})
	}
}
func BenchmarkRangeComparison(b *testing.B) {
	rows := benchmarkRows(1000, 32)
	for _, limit := range []int64{65536, 32 << 20} {
		for _, legacy := range []bool{true, false} {
			b.Run(fmt.Sprintf("limit=%d/legacy=%t", limit, legacy), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if legacy {
						benchmarkRanges = legacyRanges(rows, 500, limit)
					} else {
						benchmarkRanges = planInsertRanges(rows, 500, limit)
					}
				}
			})
		}
	}
}
