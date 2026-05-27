package importer

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, mock
}

// anyArgs 为 n 个占位符创建 sqlmock.AnyArg() 切片
func anyArgs(n int) []driver.Value {
	args := make([]driver.Value, n)
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	return args
}

// ============================================================================
// getStmt 测试
// ============================================================================

func TestGetStmtNonCache(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	query := bi.buildInsertQuery(2)

	mock.ExpectPrepare(query).WillBeClosed()

	stmt, needsClose, err := bi.getStmt(query, false)
	if err != nil {
		t.Fatalf("getStmt error: %v", err)
	}
	if !needsClose {
		t.Fatal("non-cache path should return needsClose=true")
	}
	if stmt == nil {
		t.Fatal("stmt should not be nil")
	}
	stmt.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetStmtCacheFirstTime(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	query := bi.buildInsertQuery(3)

	mock.ExpectPrepare(query).WillBeClosed()

	stmt, needsClose, err := bi.getStmt(query, true)
	if err != nil {
		t.Fatalf("getStmt error: %v", err)
	}
	if needsClose {
		t.Fatal("cache path should return needsClose=false")
	}
	if stmt == nil {
		t.Fatal("stmt should not be nil")
	}
	stmt.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
	// Verify stmt is cached
	bi.stmtMu.Lock()
	if bi.stmt == nil {
		t.Fatal("stmt should be cached after getStmt with canCache=true")
	}
	bi.stmtMu.Unlock()
}

func TestGetStmtCacheHit(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	query := bi.buildInsertQuery(2)
	prep := mock.ExpectPrepare(query)
	prep.WillBeClosed()

	stmt1, _, err := bi.getStmt(query, true)
	if err != nil {
		t.Fatalf("first getStmt: %v", err)
	}

	bi.stmtMu.Lock()
	bi.cachedRows = 2
	bi.stmtMu.Unlock()

	stmt2, needsClose, err := bi.getStmt(query, true)
	if err != nil {
		t.Fatalf("second getStmt: %v", err)
	}
	if needsClose {
		t.Fatal("cache hit should return needsClose=false")
	}
	if stmt2 != stmt1 {
		t.Fatal("cache hit should return same stmt instance")
	}

	stmt1.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetStmtCacheMissDifferentQuery(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	query1 := bi.buildInsertQuery(2)
	prep1 := mock.ExpectPrepare(query1)
	prep1.WillBeClosed()

	stmt1, _, err := bi.getStmt(query1, true)
	if err != nil {
		t.Fatalf("first getStmt: %v", err)
	}

	bi.stmtMu.Lock()
	bi.cachedRows = 2
	bi.stmtMu.Unlock()

	query2 := bi.buildInsertQuery(3)
	prep2 := mock.ExpectPrepare(query2)
	prep2.WillBeClosed()

	stmt1.Close()

	stmt2, needsClose, err := bi.getStmt(query2, true)
	if err != nil {
		t.Fatalf("second getStmt: %v", err)
	}
	if needsClose {
		t.Fatal("cache path should return needsClose=false")
	}
	if stmt2 == nil {
		t.Fatal("stmt2 should not be nil")
	}
	stmt2.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetStmtPrepareError(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	query := bi.buildInsertQuery(2)

	mock.ExpectPrepare(query).WillReturnError(fmt.Errorf("prepare failed"))

	_, _, err := bi.getStmt(query, false)
	if err == nil {
		t.Fatal("expected error from Prepare")
	}
	if !strings.Contains(err.Error(), "failed to prepare statement") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// insertBatchSingle 测试
// ============================================================================

func TestInsertBatchSingleSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}, {2, "Bob"}}
	query := bi.buildInsertQuery(2)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "Alice", 2, "Bob").
		WillReturnResult(sqlmock.NewResult(0, 2))

	affected, err := bi.insertBatchSingle(rows, false)
	if err != nil {
		t.Fatalf("insertBatchSingle error: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInsertBatchSingleWithCache(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}}
	query := bi.buildInsertQuery(1)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "Alice").
		WillReturnResult(sqlmock.NewResult(0, 1))

	affected, err := bi.insertBatchSingle(rows, true)
	if err != nil {
		t.Fatalf("insertBatchSingle error: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	bi.stmtMu.RLock()
	if bi.cachedRows != 1 {
		t.Errorf("cachedRows = %d, want 1", bi.cachedRows)
	}
	bi.stmtMu.RUnlock()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInsertBatchSingleExecError(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}}
	query := bi.buildInsertQuery(1)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "Alice").
		WillReturnError(fmt.Errorf("Error 1406 (22001): Data too long for column 'name' at row 1"))

	_, err := bi.insertBatchSingle(rows, false)
	if err == nil {
		t.Fatal("expected error from Exec")
	}
	if !strings.Contains(err.Error(), "failed to execute batch insert") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// InsertBatch 测试
// ============================================================================

func TestInsertBatchSingleBatchCached(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}, {2, "Bob"}}
	query := bi.buildInsertQuery(2)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "Alice", 2, "Bob").
		WillReturnResult(sqlmock.NewResult(0, 2))

	affected, err := bi.InsertBatch(rows)
	if err != nil {
		t.Fatalf("InsertBatch error: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInsertBatchSingleBatchSmallerThanCache(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	query3 := bi.buildInsertQuery(3)
	prep3 := mock.ExpectPrepare(query3)
	prep3.WillBeClosed()

	firstRows := [][]interface{}{{1, "A"}, {2, "B"}, {3, "C"}}
	prep3.ExpectExec().
		WithArgs(1, "A", 2, "B", 3, "C").
		WillReturnResult(sqlmock.NewResult(0, 3))

	affected, err := bi.InsertBatch(firstRows)
	if err != nil {
		t.Fatalf("first InsertBatch: %v", err)
	}
	if affected != 3 {
		t.Fatalf("first affected = %d, want 3", affected)
	}

	query2 := bi.buildInsertQuery(2)
	prep2 := mock.ExpectPrepare(query2)
	prep2.WillBeClosed()

	secondRows := [][]interface{}{{4, "D"}, {5, "E"}}
	prep2.ExpectExec().
		WithArgs(4, "D", 5, "E").
		WillReturnResult(sqlmock.NewResult(0, 2))

	affected, err = bi.InsertBatch(secondRows)
	if err != nil {
		t.Fatalf("second InsertBatch: %v", err)
	}
	if affected != 2 {
		t.Fatalf("second affected = %d, want 2", affected)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInsertBatchSplitBatch(t *testing.T) {
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
			row[j] = i*100 + j
		}
		rows[i] = row
	}

	// maxRowsPerBatch = (65535 * 95) / (100 * 201) = 309
	// 500 rows split into 309 + 191
	query1 := bi.buildInsertQuery(309)
	prep1 := mock.ExpectPrepare(query1)
	prep1.WillBeClosed()
	prep1.ExpectExec().
		WithArgs(anyArgs(309 * 201)...).
		WillReturnResult(sqlmock.NewResult(0, 309))

	query2 := bi.buildInsertQuery(191)
	prep2 := mock.ExpectPrepare(query2)
	prep2.WillBeClosed()
	prep2.ExpectExec().
		WithArgs(anyArgs(191 * 201)...).
		WillReturnResult(sqlmock.NewResult(0, 191))

	affected, err := bi.InsertBatch(rows)
	if err != nil {
		t.Fatalf("InsertBatch error: %v", err)
	}
	if affected != 500 {
		t.Fatalf("affected = %d, want 500", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// widenQuery 是 widenColumnToText 中查询 information_schema 的完整 SQL
var widenQuery = `
	SELECT DATA_TYPE, IS_NULLABLE, COLUMN_KEY
	FROM information_schema.COLUMNS
	WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?
	`

// ============================================================================
// insertBatchSingleWithAutoWiden 测试
// ============================================================================

func TestInsertBatchSingleWithAutoWidenSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}}
	query := bi.buildInsertQuery(1)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "Alice").
		WillReturnResult(sqlmock.NewResult(0, 1))

	affected, err := bi.insertBatchSingleWithAutoWiden(rows, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInsertBatchSingleWithAutoWidenRetry(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}}
	query := bi.buildInsertQuery(1)

	prep1 := mock.ExpectPrepare(query)
	prep1.ExpectExec().
		WithArgs(1, "Alice").
		WillReturnError(fmt.Errorf("Error 1406 (22001): Data too long for column 'name' at row 1"))

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("varchar", "YES", ""))

	mock.ExpectExec("ALTER TABLE `users` MODIFY COLUMN `name` TEXT NULL").
		WillReturnResult(sqlmock.NewResult(0, 0))

	prep2 := mock.ExpectPrepare(query)
	prep2.ExpectExec().
		WithArgs(1, "Alice").
		WillReturnResult(sqlmock.NewResult(0, 1))

	affected, err := bi.insertBatchSingleWithAutoWiden(rows, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInsertBatchSingleWithAutoWidenNonWidenableError(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}}
	query := bi.buildInsertQuery(1)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "Alice").
		WillReturnError(fmt.Errorf("some other error"))

	_, err := bi.insertBatchSingleWithAutoWiden(rows, false)
	if err == nil {
		t.Fatal("expected error for non-widenable error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInsertBatchSingleWithAutoWidenIndexedColumn(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	rows := [][]interface{}{{1, "Alice"}}
	query := bi.buildInsertQuery(1)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "Alice").
		WillReturnError(fmt.Errorf("Error 1406 (22001): Data too long for column 'name' at row 1"))

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("varchar", "YES", "PRI"))

	_, err := bi.insertBatchSingleWithAutoWiden(rows, false)
	if err == nil {
		t.Fatal("expected error for indexed column")
	}
	if !strings.Contains(err.Error(), "column is indexed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// widenColumnToText 测试
// ============================================================================

func TestWidenColumnToTextVarcharToText(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("varchar", "YES", ""))

	mock.ExpectExec("ALTER TABLE `users` MODIFY COLUMN `name` TEXT NULL").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := bi.widenColumnToText("name"); err != nil {
		t.Fatalf("widenColumnToText error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWidenColumnToTextNotNull(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("varchar", "NO", ""))

	mock.ExpectExec("ALTER TABLE `users` MODIFY COLUMN `name` TEXT NOT NULL").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := bi.widenColumnToText("name"); err != nil {
		t.Fatalf("widenColumnToText error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWidenColumnToTextTextToMediumtext(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("text", "YES", ""))

	mock.ExpectExec("ALTER TABLE `users` MODIFY COLUMN `name` MEDIUMTEXT NULL").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := bi.widenColumnToText("name"); err != nil {
		t.Fatalf("widenColumnToText error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWidenColumnToTextMediumtextToLongtext(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("mediumtext", "YES", ""))

	mock.ExpectExec("ALTER TABLE `users` MODIFY COLUMN `name` LONGTEXT NULL").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := bi.widenColumnToText("name"); err != nil {
		t.Fatalf("widenColumnToText error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWidenColumnToTextQueryError(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnError(fmt.Errorf("table not found"))

	err := bi.widenColumnToText("name")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to inspect column metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWidenColumnToTextIndexed(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("varchar", "YES", "UNI"))

	err := bi.widenColumnToText("name")
	if err == nil {
		t.Fatal("expected error for indexed column")
	}
	if !strings.Contains(err.Error(), "column is indexed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWidenColumnToTextNotWidenable(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")

	mock.ExpectQuery(widenQuery).
		WithArgs("users", "name").
		WillReturnRows(sqlmock.NewRows([]string{"DATA_TYPE", "IS_NULLABLE", "COLUMN_KEY"}).
			AddRow("longtext", "YES", ""))

	err := bi.widenColumnToText("name")
	if err == nil {
		t.Fatal("expected error for non-widenable type")
	}
	if !strings.Contains(err.Error(), "not auto-widenable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// resetStmt 测试
// ============================================================================

func TestResetStmtWithStmt(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id"}, 100, "ignore")

	query := bi.buildInsertQuery(1)
	prep := mock.ExpectPrepare(query)
	prep.WillBeClosed()

	stmt, _, _ := bi.getStmt(query, true)
	if stmt == nil {
		t.Fatal("stmt is nil")
	}
	stmt.Close()
	bi.resetStmt()

	bi.stmtMu.RLock()
	if bi.stmt != nil {
		t.Error("stmt should be nil after reset")
	}
	if bi.cachedRows != 0 {
		t.Errorf("cachedRows = %d, want 0", bi.cachedRows)
	}
	bi.stmtMu.RUnlock()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestResetStmtWithoutStmt(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id"}, 100, "ignore")
	bi.resetStmt()
	if bi.stmt != nil {
		t.Error("stmt should still be nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// Close 测试
// ============================================================================

func TestCloseWithStmt(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id"}, 100, "ignore")
	query := bi.buildInsertQuery(1)
	prep := mock.ExpectPrepare(query)
	prep.WillBeClosed()

	_, _, err := bi.getStmt(query, true)
	if err != nil {
		t.Fatalf("getStmt: %v", err)
	}

	if err := bi.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	bi.stmtMu.RLock()
	if bi.stmt != nil {
		t.Error("stmt should be nil after Close")
	}
	bi.stmtMu.RUnlock()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCloseWithoutStmt(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id"}, 100, "ignore")
	if err := bi.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// InsertBatch 缓存复用
// ============================================================================

func TestInsertBatchCachedReuse(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	query := bi.buildInsertQuery(2)

	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(1, "A", 2, "B").
		WillReturnResult(sqlmock.NewResult(0, 2))

	_, err := bi.InsertBatch([][]interface{}{{1, "A"}, {2, "B"}})
	if err != nil {
		t.Fatalf("first InsertBatch: %v", err)
	}

	prep.ExpectExec().
		WithArgs(3, "C", 4, "D").
		WillReturnResult(sqlmock.NewResult(0, 2))

	_, err = bi.InsertBatch([][]interface{}{{3, "C"}, {4, "D"}})
	if err != nil {
		t.Fatalf("second InsertBatch: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// 并发安全测试
// ============================================================================

func TestGetStmtConcurrent(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	query := bi.buildInsertQuery(2)

	mock.ExpectPrepare(query).WillBeClosed()
	mock.ExpectPrepare(query).WillBeClosed()
	mock.ExpectPrepare(query).WillBeClosed()

	done := make(chan struct{})
	for i := 0; i < 3; i++ {
		go func() {
			stmt, _, err := bi.getStmt(query, true)
			if err == nil && stmt != nil {
				stmt.Close()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 3; i++ {
		<-done
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// InsertBatch 拆分批次错误传播
// ============================================================================

func TestInsertBatchSplitBatchErrorPropagation(t *testing.T) {
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

	// maxRowsPerBatch = (65535 * 95) / (100 * 201) = 309
	// 500 rows split into 309 + 191, error on second sub-batch
	query1 := bi.buildInsertQuery(309)
	prep1 := mock.ExpectPrepare(query1)
	prep1.WillBeClosed()
	prep1.ExpectExec().
		WithArgs(anyArgs(309 * 201)...).
		WillReturnResult(sqlmock.NewResult(0, 309))

	query2 := bi.buildInsertQuery(191)
	prep2 := mock.ExpectPrepare(query2)
	prep2.WillBeClosed()
	prep2.ExpectExec().
		WithArgs(anyArgs(191 * 201)...).
		WillReturnError(fmt.Errorf("some error"))

	affected, err := bi.InsertBatch(rows)
	if err == nil {
		t.Fatal("expected error from split batch")
	}
	if affected != 309 {
		t.Fatalf("affected = %d, want 309", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// maxRowsPerBatch 计算验证
// ============================================================================

func TestInsertBatchMaxRowsPerBatchCalculation(t *testing.T) {
	db, mock := newMockDB(t)

	columns := make([]string, 1000)
	for i := range columns {
		columns[i] = fmt.Sprintf("col%d", i)
	}
	bi := NewBatchInserter(db, "t", columns, 5000, "ignore")

	row := make([]interface{}, 1000)
	for j := range row {
		row[j] = j
	}
	expectedMax := (maxPreparedPlaceholders * 95) / (100 * len(columns))
	if expectedMax != 62 {
		t.Fatalf("maxRowsPerBatch = %d, want 62", expectedMax)
	}

	query := bi.buildInsertQuery(1)
	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(anyArgs(1000)...).
		WillReturnResult(sqlmock.NewResult(0, 1))

	affected, err := bi.InsertBatch([][]interface{}{row})
	if err != nil {
		t.Fatalf("InsertBatch error: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// Fix #2: PrepareContext 验证
// ============================================================================

func TestGetStmtUsesPrepareContext(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	query := bi.buildInsertQuery(2)

	mock.ExpectPrepare(query).WillBeClosed()

	stmt, needsClose, err := bi.getStmt(query, false)
	if err != nil {
		t.Fatalf("getStmt error: %v", err)
	}
	if !needsClose {
		t.Fatal("non-cache path should return needsClose=true")
	}
	stmt.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetStmtCacheHitPrepareContext(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "users", []string{"id", "name"}, 100, "ignore")
	query := bi.buildInsertQuery(2)

	prep := mock.ExpectPrepare(query)
	prep.WillBeClosed()

	stmt1, _, err := bi.getStmt(query, true)
	if err != nil {
		t.Fatalf("first getStmt: %v", err)
	}

	bi.stmtMu.Lock()
	bi.cachedRows = 2
	bi.stmtMu.Unlock()

	stmt2, needsClose, err := bi.getStmt(query, true)
	if err != nil {
		t.Fatalf("second getStmt: %v", err)
	}
	if needsClose {
		t.Fatal("cache hit should return needsClose=false")
	}
	if stmt2 != stmt1 {
		t.Fatal("cache hit should return same stmt instance")
	}

	stmt1.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// Fix #6: MaxBatchBytes=0 保持默认值，负数表示不限制
// ============================================================================

func TestSetMaxBatchBytesZeroKeepsDefault(t *testing.T) {
	db, _ := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"col1"}, 5000, "ignore")

	defaultVal := bi.maxBatchBytes // 构造函数设置的默认值（32MB）
	bi.SetMaxBatchBytes(0)

	if bi.maxBatchBytes != defaultVal {
		t.Fatalf("maxBatchBytes = %d, want %d (default preserved)", bi.maxBatchBytes, defaultVal)
	}
}

func TestSetMaxBatchBytesNegativeDisablesLimit(t *testing.T) {
	db, _ := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"col1"}, 5000, "ignore")
	bi.SetMaxBatchBytes(-1)

	if bi.maxBatchBytes != 0 {
		t.Fatalf("maxBatchBytes = %d, want 0 (negative = no limit)", bi.maxBatchBytes)
	}
}

func TestInsertBatchWithNegativeMaxBatchBytesSkipsByteCheck(t *testing.T) {
	db, mock := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{"col1"}, 5000, "ignore")
	bi.SetMaxBatchBytes(-1) // 负数表示不限制

	bigRow := make([]interface{}, 1)
	bigRow[0] = strings.Repeat("x", 10*1024*1024)

	rows := make([][]interface{}, 5)
	for i := range rows {
		rows[i] = bigRow
	}

	// maxBatchBytes=0（无限制），不触发字节拆分，所有行应在一个批次
	query := bi.buildInsertQuery(5)
	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().
		WithArgs(anyArgs(5)...).
		WillReturnResult(sqlmock.NewResult(0, 5))

	affected, err := bi.InsertBatch(rows)
	if err != nil {
		t.Fatalf("InsertBatch error: %v", err)
	}
	if affected != 5 {
		t.Fatalf("affected = %d, want 5", affected)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ============================================================================
// Fix #9: InsertBatch 空列除零防护
// ============================================================================

func TestInsertBatchEmptyColumnsReturnsError(t *testing.T) {
	db, _ := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{}, 100, "ignore")

	rows := [][]interface{}{{"val1"}}
	_, err := bi.InsertBatch(rows)
	if err == nil {
		t.Fatal("expected error for empty columns")
	}
	if !strings.Contains(err.Error(), "no columns defined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInsertBatchEmptyRowsWithEmptyColumns(t *testing.T) {
	db, _ := newMockDB(t)
	bi := NewBatchInserter(db, "t", []string{}, 100, "ignore")

	affected, err := bi.InsertBatch([][]interface{}{})
	if err != nil {
		t.Fatalf("expected no error for empty rows, got: %v", err)
	}
	if affected != 0 {
		t.Fatalf("affected = %d, want 0", affected)
	}
}
