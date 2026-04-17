package importer

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
)

// BatchInserter 批量插入器
type BatchInserter struct {
	db           *sql.DB
	tableName    string
	columns      []string
	batchSize    int
	onDuplicate  string // "replace" or "ignore"
	stmt         *sql.Stmt
	stmtMu       sync.RWMutex
	buildQueryMu sync.Mutex // 保护 buildInsertQuery 多次调用时的竞态
}

// NewBatchInserter 创建批量插入器
func NewBatchInserter(db *sql.DB, tableName string, columns []string, batchSize int, onDuplicate string) *BatchInserter {
	return &BatchInserter{
		db:          db,
		tableName:   tableName,
		columns:     columns,
		batchSize:   batchSize,
		onDuplicate: onDuplicate,
	}
}

// getStmt 获取预编译语句（延迟初始化）
func (bi *BatchInserter) getStmt(query string) (*sql.Stmt, error) {
	bi.stmtMu.RLock()
	if bi.stmt != nil {
		bi.stmtMu.RUnlock()
		return bi.stmt, nil
	}
	bi.stmtMu.RUnlock()

	bi.stmtMu.Lock()
	defer bi.stmtMu.Unlock()
	if bi.stmt != nil {
		return bi.stmt, nil
	}

	stmt, err := bi.db.Prepare(query)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}
	bi.stmt = stmt
	return stmt, nil
}

// InsertBatch 批量插入数据
func (bi *BatchInserter) InsertBatch(rows [][]interface{}) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	// 构建 SQL 语句
	query := bi.buildInsertQuery(len(rows))

	// 使用预编译语句
	stmt, err := bi.getStmt(query)
	if err != nil {
		return 0, err
	}

	// 展平数据
	var args []interface{}
	for _, row := range rows {
		args = append(args, row...)
	}

	// 执行插入
	result, err := stmt.Exec(args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute batch insert: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return rowsAffected, nil
}

// buildInsertQuery 构建批量插入 SQL 语句（线程安全）
func (bi *BatchInserter) buildInsertQuery(numRows int) string {
	bi.buildQueryMu.Lock()
	defer bi.buildQueryMu.Unlock()

	var query strings.Builder

	// 选择插入类型
	if bi.onDuplicate == "replace" {
		query.WriteString("REPLACE INTO ")
	} else {
		query.WriteString("INSERT IGNORE INTO ")
	}

	// 表名
	query.WriteString(fmt.Sprintf("`%s` ", bi.tableName))

	// 列名
	columnNames := make([]string, len(bi.columns))
	for i, col := range bi.columns {
		columnNames[i] = fmt.Sprintf("`%s`", col)
	}
	query.WriteString(fmt.Sprintf("(%s) VALUES ", strings.Join(columnNames, ", ")))

	// 值占位符
	valuePlaceholders := make([]string, numRows)
	placeholderCount := len(bi.columns)
	for i := 0; i < numRows; i++ {
		placeholders := make([]string, placeholderCount)
		for j := 0; j < placeholderCount; j++ {
			placeholders[j] = "?"
		}
		valuePlaceholders[i] = fmt.Sprintf("(%s)", strings.Join(placeholders, ", "))
	}
	query.WriteString(strings.Join(valuePlaceholders, ", "))

	return query.String()
}

// Close 关闭预编译语句
func (bi *BatchInserter) Close() error {
	bi.stmtMu.Lock()
	defer bi.stmtMu.Unlock()
	if bi.stmt != nil {
		return bi.stmt.Close()
	}
	return nil
}
