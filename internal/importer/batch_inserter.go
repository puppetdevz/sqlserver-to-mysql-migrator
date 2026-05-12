package importer

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
)

// maxPreparedPlaceholders MySQL prepared statement 占位符上限（留有余量）
const maxPreparedPlaceholders = 60000

// BatchInserter 批量插入器
type BatchInserter struct {
	db           *sql.DB
	tableName    string
	columns      []string // 只包含数据库中存在的列
	batchSize    int
	onDuplicate  string // "replace" or "ignore"
	stmt         *sql.Stmt
	cachedRows   int // 缓存语句的行数（用于判断后续批次是否能复用）
	stmtMu       sync.RWMutex
	buildQueryMu sync.Mutex // 保护 buildInsertQuery 多次调用时的竞态
	skippedCols  []string   // 跳过的列
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

// GetSkippedColumns 获取跳过的列列表
func (bi *BatchInserter) GetSkippedColumns() []string {
	return bi.skippedCols
}

// NewBatchInserterWithDBColumns 创建批量插入器（使用数据库列过滤 CSV 列）
// csvColumns: CSV 文件中的列
// dbColumns: 数据库中实际存在的列
// tableImporter: 用于记录跳过的列
func NewBatchInserterWithDBColumns(db *sql.DB, tableName string, csvColumns []string, dbColumns []string, batchSize int, onDuplicate string) (*BatchInserter, []string) {
	dbColMap := BuildUpperColumnMap(dbColumns)

	// 只保留数据库中存在的列
	var validColumns []string
	var skippedColumns []string
	for _, csvCol := range csvColumns {
		upperCol := strings.ToUpper(csvCol)
		if dbOrig, ok := dbColMap[upperCol]; ok {
			validColumns = append(validColumns, dbOrig) // 使用数据库中的原始列名
		} else {
			skippedColumns = append(skippedColumns, csvCol)
		}
	}

	if len(validColumns) == 0 {
		return nil, skippedColumns
	}

	return &BatchInserter{
		db:           db,
		tableName:    tableName,
		columns:     validColumns,
		batchSize:   batchSize,
		onDuplicate:  onDuplicate,
		skippedCols:  skippedColumns,
	}, skippedColumns
}

// getStmt 获取预编译语句
// canCache: 是否使用缓存（拆分批次不缓存，避免占位符数量不匹配）
func (bi *BatchInserter) getStmt(query string, canCache bool) (*sql.Stmt, bool, error) {
	if canCache {
		bi.stmtMu.Lock()
		defer bi.stmtMu.Unlock()

		// 如果有缓存的 statement，检查 query 是否匹配
		if bi.stmt != nil {
			// 通过占位符数量判断 query 是否相同
			cachedPlaceholders := bi.cachedRows * len(bi.columns)
			newPlaceholders := strings.Count(query, "?")
			if cachedPlaceholders == newPlaceholders {
				// query 相同，可以复用
				return bi.stmt, false, nil
			}
			// query 不同，关闭旧 statement，使用新 query
			bi.stmt.Close()
			bi.stmt = nil
		}

		stmt, err := bi.db.Prepare(query)
		if err != nil {
			return nil, false, fmt.Errorf("failed to prepare statement: %w", err)
		}
		bi.stmt = stmt
		bi.cachedRows = 0 // 重置，等待 insertBatchSingle 更新
		return stmt, false, nil
	}

	// 不使用缓存，每次重新 Prepare
	stmt, err := bi.db.Prepare(query)
	if err != nil {
		return nil, false, fmt.Errorf("failed to prepare statement: %w", err)
	}
	bi.cachedRows = 0 // 不使用缓存，重置容量标记
	return stmt, true, nil
}

// InsertBatch 批量插入数据（超宽表自动拆分）
func (bi *BatchInserter) InsertBatch(rows [][]interface{}) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	// 根据列数计算每批最大行数，避免超出 MySQL prepared statement 占位符限制
	maxRowsPerBatch := (maxPreparedPlaceholders * 85) / (100 * len(bi.columns))
	if maxRowsPerBatch < 1 {
		maxRowsPerBatch = 1
	}

	if len(rows) <= maxRowsPerBatch {
		bi.stmtMu.RLock()
		cachedCapacity := bi.cachedRows
		bi.stmtMu.RUnlock()

		if cachedCapacity > 0 && len(rows) < cachedCapacity {
			// cached stmt expects more placeholders than we have rows — close old stmt and create new one
			bi.stmtMu.Lock()
			if bi.stmt != nil {
				bi.stmt.Close()
				bi.stmt = nil
			}
			bi.stmtMu.Unlock()
			return bi.insertBatchSingle(rows, false)
		}
		return bi.insertBatchSingle(rows, true) // 单批次，可缓存
	}

	// 拆分为多个小批次（各批次行数可能不同，不缓存以避免占位符数量不匹配）
	var totalAffected int64
	for i := 0; i < len(rows); i += maxRowsPerBatch {
		end := i + maxRowsPerBatch
		if end > len(rows) {
			end = len(rows)
		}
		affected, err := bi.insertBatchSingle(rows[i:end], false) // 拆分的批次，不缓存
		if err != nil {
			return totalAffected, err
		}
		totalAffected += affected
	}
	return totalAffected, nil
}

// insertBatchSingle 执行单次插入
// canCache: 是否允许缓存预编译语句（拆分批次不允许，避免占位符数量不一致）
func (bi *BatchInserter) insertBatchSingle(rows [][]interface{}, canCache bool) (int64, error) {
	query := bi.buildInsertQuery(len(rows))
	stmt, needsClose, err := bi.getStmt(query, canCache)
	if err != nil {
		return 0, err
	}
	if needsClose {
		defer stmt.Close()
	}

	var args []interface{}
	for _, row := range rows {
		args = append(args, row...)
	}

	result, err := stmt.Exec(args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute batch insert: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	if canCache {
		bi.stmtMu.Lock()
		bi.cachedRows = len(rows)
		bi.stmtMu.Unlock()
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
