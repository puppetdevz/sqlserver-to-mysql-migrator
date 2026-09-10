package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestAdaptiveRetryDoesNotReplaySuccessfulByteSplitSubBatchAfter1205(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"v"}, 100, "ignore")
	bi.SetMaxBatchBytes(8)

	rowA := strings.Repeat("a", 16)
	rowB := strings.Repeat("b", 16)
	rows := [][]interface{}{{rowA}, {rowB}}

	query := bi.buildInsertQuery(1)
	prep := mock.ExpectPrepare(query)
	prep.WillBeClosed()
	prep.ExpectExec().WithArgs(rowA).WillReturnResult(sqlmock.NewResult(0, 1))
	prep.ExpectExec().WithArgs(rowB).WillReturnError(&mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"})
	prep.ExpectExec().WithArgs(rowB).WillReturnResult(sqlmock.NewResult(0, 1))

	result := insertBatchWithAdaptiveRetry(context.Background(), bi, "t", 1, rows, noRetryWait)
	if result.err != nil {
		t.Fatalf("insertBatchWithAdaptiveRetry() error = %v", result.err)
	}
	if result.consumedRows != 2 {
		t.Fatalf("consumedRows = %d, want 2", result.consumedRows)
	}
	if result.affectedRows != 2 {
		t.Fatalf("affectedRows = %d, want 2", result.affectedRows)
	}
	if err := bi.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL order/replay mismatch: %v", err)
	}
}

func TestAdaptiveRetryDoesNotReplaySuccessfulPlaceholderSplitSubBatchAfter1205(t *testing.T) {
	db, mock := newMockDB(t)

	columns := make([]string, 201)
	for i := range columns {
		columns[i] = fmt.Sprintf("col%d", i)
	}
	bi := NewBatchInserter(db, "t", columns, 5000, "ignore")

	rows := make([][]interface{}, 500)
	for i := range rows {
		row := make([]interface{}, 201)
		for j := range row {
			row[j] = i
		}
		rows[i] = row
	}

	query1 := bi.buildInsertQuery(309)
	prep1 := mock.ExpectPrepare(query1)
	prep1.WillBeClosed()
	prep1.ExpectExec().WithArgs(anyArgs(309 * 201)...).WillReturnResult(sqlmock.NewResult(0, 309))

	query2 := bi.buildInsertQuery(191)
	prep2 := mock.ExpectPrepare(query2)
	prep2.WillBeClosed()
	prep2.ExpectExec().WithArgs(anyArgs(191 * 201)...).WillReturnError(&mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"})

	prep3 := mock.ExpectPrepare(query2)
	prep3.ExpectExec().WithArgs(anyArgs(191 * 201)...).WillReturnResult(sqlmock.NewResult(0, 191))

	result := insertBatchWithAdaptiveRetry(context.Background(), bi, "t", 1, rows, noRetryWait)
	if result.err != nil {
		t.Fatalf("insertBatchWithAdaptiveRetry() error = %v", result.err)
	}
	if result.consumedRows != 500 {
		t.Fatalf("consumedRows = %d, want 500", result.consumedRows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL order/replay mismatch: %v", err)
	}
}

func TestAdaptiveRetryAdvancesAfterIgnoreZeroAffectedRows(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"v"}, 100, "ignore")
	bi.SetMaxBatchBytes(8)

	rowA := strings.Repeat("a", 16)
	rowB := strings.Repeat("b", 16)
	rows := [][]interface{}{{rowA}, {rowB}}

	query := bi.buildInsertQuery(1)
	prep := mock.ExpectPrepare(query)
	prep.WillBeClosed()
	prep.ExpectExec().WithArgs(rowA).WillReturnResult(sqlmock.NewResult(0, 0))
	prep.ExpectExec().WithArgs(rowB).WillReturnResult(sqlmock.NewResult(0, 1))

	result := insertBatchWithAdaptiveRetry(context.Background(), bi, "t", 1, rows, noRetryWait)
	if result.err != nil {
		t.Fatalf("error = %v", result.err)
	}
	if result.consumedRows != 2 {
		t.Fatalf("consumedRows = %d, want 2 (zero affected must not block cursor)", result.consumedRows)
	}
	if result.affectedRows != 1 {
		t.Fatalf("affectedRows = %d, want 1", result.affectedRows)
	}
	if err := bi.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("first sub-batch was replayed or skipped: %v", err)
	}
}

func TestAdaptiveRetryAdvancesAfterReplaceOverAffectedRows(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"v"}, 100, "replace")
	bi.SetMaxBatchBytes(8)

	rowA := strings.Repeat("a", 16)
	rowB := strings.Repeat("b", 16)
	rows := [][]interface{}{{rowA}, {rowB}}

	query := bi.buildInsertQuery(1)
	prep := mock.ExpectPrepare(query)
	prep.WillBeClosed()
	prep.ExpectExec().WithArgs(rowA).WillReturnResult(sqlmock.NewResult(0, 2))
	prep.ExpectExec().WithArgs(rowB).WillReturnResult(sqlmock.NewResult(0, 1))

	result := insertBatchWithAdaptiveRetry(context.Background(), bi, "t", 1, rows, noRetryWait)
	if result.err != nil {
		t.Fatalf("error = %v", result.err)
	}
	if result.consumedRows != 2 {
		t.Fatalf("consumedRows = %d, want 2 (REPLACE affected=2 must not skip remaining input)", result.consumedRows)
	}
	if result.affectedRows != 3 {
		t.Fatalf("affectedRows = %d, want 3", result.affectedRows)
	}
	if err := bi.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("cursor used affected rows: %v", err)
	}
}

func TestAdaptiveRetryUnknownCommitDoesNotReplayExecRange(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"v"}, 100, "ignore")
	rows := [][]interface{}{{"a"}, {"b"}}

	query := bi.buildInsertQuery(2)
	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().WithArgs("a", "b").WillReturnError(mysql.ErrInvalidConn)

	result := insertBatchWithAdaptiveRetry(context.Background(), bi, "t", 1, rows, noRetryWait)
	if result.err == nil {
		t.Fatal("expected unknown-commit error")
	}
	if !errors.Is(result.err, ErrUnknownCommit) {
		t.Fatalf("err = %v, want errors.Is(..., ErrUnknownCommit)", result.err)
	}
	if result.consumedRows != 0 {
		t.Fatalf("consumedRows = %d, want 0 for unknown commit", result.consumedRows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unknown-commit range was replayed: %v", err)
	}
}

func TestAdaptiveRetryPrepareStageConnectionErrorIsRetried(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"v"}, 100, "ignore")
	rows := [][]interface{}{{"a"}}

	query := bi.buildInsertQuery(1)
	mock.ExpectPrepare(query).WillReturnError(fmt.Errorf("prepare failed: %w", mysql.ErrInvalidConn))
	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().WithArgs("a").WillReturnResult(sqlmock.NewResult(0, 1))

	result := insertBatchWithAdaptiveRetry(context.Background(), bi, "t", 1, rows, noRetryWait)
	if result.err != nil {
		t.Fatalf("error = %v", result.err)
	}
	if result.consumedRows != 1 {
		t.Fatalf("consumedRows = %d, want 1", result.consumedRows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("prepare-stage retry mismatch: %v", err)
	}
}
