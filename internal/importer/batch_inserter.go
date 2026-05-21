package importer

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// maxPreparedPlaceholders MySQL prepared statement 占位符上限（留有余量）
const maxPreparedPlaceholders = 60000

var reDataTooLongColumn = regexp.MustCompile(`Data too long for column '([^']+)'`)
var reIncorrectTemporalColumn = regexp.MustCompile(`Incorrect (?:date|datetime|time|timestamp) value: .* for column '([^']+)'`)
var reIncorrectNumericColumn = regexp.MustCompile(`Incorrect (?:integer|decimal|double|float) value: .* for column '([^']+)'`)
var reOutOfRangeColumn = regexp.MustCompile(`Out of range value for column '([^']+)'`)

// errorColumnExtractor 提取错误消息中的列名
type errorColumnExtractor struct {
	pattern *regexp.Regexp
}

func (e *errorColumnExtractor) extract(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	matches := e.pattern.FindStringSubmatch(err.Error())
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}

var (
	_dataTooLongExtractor    = &errorColumnExtractor{pattern: reDataTooLongColumn}
	_incorrectTemporalExtractor = &errorColumnExtractor{pattern: reIncorrectTemporalColumn}
	_incorrectNumericExtractor = &errorColumnExtractor{pattern: reIncorrectNumericColumn}
	_outOfRangeExtractor      = &errorColumnExtractor{pattern: reOutOfRangeColumn}
)

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
		db:          db,
		tableName:   tableName,
		columns:     validColumns,
		batchSize:   batchSize,
		onDuplicate: onDuplicate,
		skippedCols: skippedColumns,
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
			bi.resetStmt()
			return bi.insertBatchSingleWithAutoWiden(rows, false)
		}
		return bi.insertBatchSingleWithAutoWiden(rows, true) // 单批次，可缓存
	}

	// 拆分为多个小批次（各批次行数可能不同，不缓存以避免占位符数量不匹配）
	var totalAffected int64
	for i := 0; i < len(rows); i += maxRowsPerBatch {
		end := i + maxRowsPerBatch
		if end > len(rows) {
			end = len(rows)
		}
		affected, err := bi.insertBatchSingleWithAutoWiden(rows[i:end], false) // 拆分的批次，不缓存
		if err != nil {
			return totalAffected, err
		}
		totalAffected += affected
	}
	return totalAffected, nil
}

func (bi *BatchInserter) insertBatchSingleWithAutoWiden(rows [][]interface{}, canCache bool) (int64, error) {
	affected, err := bi.insertBatchSingle(rows, canCache)
	if err == nil {
		return affected, nil
	}

	column, ok := autoTextColumn(err)
	if !ok {
		return 0, err
	}

	if widenErr := bi.widenColumnToText(column); widenErr != nil {
		return 0, fmt.Errorf("%w; failed to widen column %s: %v", err, column, widenErr)
	}
	bi.resetStmt()

	return bi.insertBatchSingle(rows, canCache)
}

func autoTextColumn(err error) (string, bool) {
	if column, ok := _dataTooLongExtractor.extract(err); ok {
		return column, true
	}
	if column, ok := _incorrectTemporalExtractor.extract(err); ok {
		return column, true
	}
	if column, ok := _incorrectNumericExtractor.extract(err); ok {
		return column, true
	}
	return _outOfRangeExtractor.extract(err)
}

func (bi *BatchInserter) widenColumnToText(column string) error {
	var dataType, isNullable, columnKey string
	err := bi.db.QueryRow(`
SELECT DATA_TYPE, IS_NULLABLE, COLUMN_KEY
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?
`, bi.tableName, column).Scan(&dataType, &isNullable, &columnKey)
	if err != nil {
		return fmt.Errorf("failed to inspect column metadata: %w", err)
	}
	if columnKey != "" {
		return fmt.Errorf("column is indexed (%s)", columnKey)
	}

	nextType, ok := widenedTextType(dataType)
	if !ok {
		return fmt.Errorf("column type %s is not auto-widenable", dataType)
	}

	nullability := "NULL"
	if strings.EqualFold(isNullable, "NO") {
		nullability = "NOT NULL"
	}

	query := fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s %s",
		quoteIdentifier(bi.tableName),
		quoteIdentifier(column),
		nextType,
		nullability,
	)
	if _, err := bi.db.Exec(query); err != nil {
		return fmt.Errorf("failed to alter column: %w", err)
	}
	return nil
}

func widenedTextType(dataType string) (string, bool) {
	switch strings.ToLower(dataType) {
	case "char", "varchar", "tinytext", "date", "datetime", "timestamp", "time", "year":
		return "TEXT", true
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real":
		return "TEXT", true
	case "text":
		return "MEDIUMTEXT", true
	case "mediumtext":
		return "LONGTEXT", true
	default:
		return "", false
	}
}

func quoteIdentifier(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

func (bi *BatchInserter) resetStmt() {
	bi.stmtMu.Lock()
	defer bi.stmtMu.Unlock()
	if bi.stmt != nil {
		bi.stmt.Close()
		bi.stmt = nil
	}
	bi.cachedRows = 0
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
