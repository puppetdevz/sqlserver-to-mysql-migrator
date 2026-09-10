package importer

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/go-sql-driver/mysql"
)

// ErrUnknownCommit is returned when a statement may already have been committed
// but the client cannot prove the outcome. Callers must not replay that range.
var ErrUnknownCommit = errors.New("commit result unknown; verify table contents before re-importing")

// maxPreparedPlaceholders MySQL prepared statement 占位符上限（留有余量）
const maxPreparedPlaceholders = 65535

// maxBatchBytes 单批次数据量上限（估算值，留一半余量避免超过 max_allowed_packet 64MB）
const maxBatchBytes = 32 * 1024 * 1024

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
	_dataTooLongExtractor       = &errorColumnExtractor{pattern: reDataTooLongColumn}
	_incorrectTemporalExtractor = &errorColumnExtractor{pattern: reIncorrectTemporalColumn}
	_incorrectNumericExtractor  = &errorColumnExtractor{pattern: reIncorrectNumericColumn}
	_outOfRangeExtractor        = &errorColumnExtractor{pattern: reOutOfRangeColumn}
)

// BatchInserter 批量插入器
type BatchInserter struct {
	db            *sql.DB
	tableName     string
	quotedTable   string   // 预计算的引用表名
	columns       []string // 只包含数据库中存在的列
	quotedColumns []string // 预计算的引用列名
	batchSize     int
	onDuplicate   string // "replace" or "ignore"
	stmt          *sql.Stmt
	cachedRows    int // 缓存语句的行数（用于判断后续批次是否能复用）
	stmtMu        sync.RWMutex
	buildQueryMu  sync.Mutex // 保护 buildInsertQuery 多次调用时的竞态
	skippedCols   []string   // 跳过的列
	maxBatchBytes int64      // 单批次最大字节数，默认 32MB
	ctx           context.Context
}

// NewBatchInserter 创建批量插入器
func NewBatchInserter(db *sql.DB, tableName string, columns []string, batchSize int, onDuplicate string) *BatchInserter {
	quotedCols := make([]string, len(columns))
	for i, col := range columns {
		quotedCols[i] = quoteIdentifier(col)
	}
	return &BatchInserter{
		db:            db,
		tableName:     tableName,
		quotedTable:   quoteIdentifier(tableName),
		columns:       columns,
		quotedColumns: quotedCols,
		batchSize:     batchSize,
		onDuplicate:   onDuplicate,
		maxBatchBytes: maxBatchBytes,
		ctx:           context.Background(),
	}
}

// GetSkippedColumns 获取跳过的列列表
func (bi *BatchInserter) GetSkippedColumns() []string {
	return bi.skippedCols
}

// SetMaxBatchBytes 设置单批次最大字节数。正数覆盖默认 32MB 限制，0 保持默认值，负数表示不限制
func (bi *BatchInserter) SetMaxBatchBytes(bytes int) {
	if bytes > 0 {
		bi.maxBatchBytes = int64(bytes)
	} else if bytes < 0 {
		bi.maxBatchBytes = 0 // 负数表示不限制
	}
	// bytes == 0: 保持构造函数设置的默认值（32MB）
}

// SetContext sets the context for SQL execution cancellation.
func (bi *BatchInserter) SetContext(ctx context.Context) {
	bi.ctx = ctx
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

	quotedCols := make([]string, len(validColumns))
	for i, col := range validColumns {
		quotedCols[i] = quoteIdentifier(col)
	}

	return &BatchInserter{
		db:            db,
		tableName:     tableName,
		quotedTable:   quoteIdentifier(tableName),
		columns:       validColumns,
		quotedColumns: quotedCols,
		batchSize:     batchSize,
		onDuplicate:   onDuplicate,
		skippedCols:   skippedColumns,
		maxBatchBytes: maxBatchBytes,
		ctx:           context.Background(),
	}, skippedColumns
}

// getStmt 获取预编译语句
// canCache: 是否使用缓存（拆分批次不缓存，避免占位符数量不匹配）
func (bi *BatchInserter) getStmt(query string, canCache bool) (*sql.Stmt, bool, error) {
	if canCache {
		bi.stmtMu.Lock()

		if bi.stmt != nil {
			// 通过占位符数量判断 query 是否相同
			cachedPlaceholders := bi.cachedRows * len(bi.columns)
			newPlaceholders := strings.Count(query, "?")
			if cachedPlaceholders == newPlaceholders {
				// query 相同，可以复用
				stmt := bi.stmt
				bi.stmtMu.Unlock()
				return stmt, false, nil
			}
			// query 不同，关闭旧 statement，使用新 query
			bi.stmt.Close()
			bi.stmt = nil
		}

		// 释放锁后再 Prepare，避免长时间持有写锁阻塞其他 goroutine
		bi.stmtMu.Unlock()
		stmt, err := bi.db.PrepareContext(bi.ctx, query)
		if err != nil {
			bi.resetOnConnErr(err)
			return nil, false, &insertStageError{Stage: insertStagePrepare, Err: fmt.Errorf("failed to prepare statement: %w", err)}
		}

		bi.stmtMu.Lock()
		bi.stmt = stmt
		bi.cachedRows = 0
		bi.stmtMu.Unlock()
		return stmt, false, nil
	}

	// 不使用缓存，每次重新 Prepare
	stmt, err := bi.db.PrepareContext(bi.ctx, query)
	if err != nil {
		bi.resetOnConnErr(err)
		return nil, false, &insertStageError{Stage: insertStagePrepare, Err: fmt.Errorf("failed to prepare statement: %w", err)}
	}
	bi.stmtMu.Lock()
	bi.cachedRows = 0
	bi.stmtMu.Unlock()
	return stmt, true, nil
}

// estimateBatchBytes 估算一批行序列化后的字节大小
func estimateBatchBytes(rows [][]interface{}) int64 {
	var total int64
	for _, row := range rows {
		for _, v := range row {
			switch val := v.(type) {
			case string:
				total += int64(len(val))
			case []byte:
				total += int64(len(val))
			case nil:
				total += 4 // NULL 占位
			default:
				total += 16 // 数值类型默认估算
			}
		}
	}
	return total
}

type insertRange struct {
	Start int
	End   int
}

type insertStage string

const (
	insertStagePrepare insertStage = "prepare"
	insertStageExec    insertStage = "exec"
	insertStageResult  insertStage = "result"
)

type insertStageError struct {
	Stage insertStage
	Err   error
}

func (e *insertStageError) Error() string {
	if e == nil || e.Err == nil {
		return "insert error"
	}
	if e.UnknownCommit() {
		return fmt.Sprintf("failed to execute batch insert: %v: %v", ErrUnknownCommit, e.Err)
	}
	return e.Err.Error()
}

func (e *insertStageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *insertStageError) Is(target error) bool {
	return e != nil && target == ErrUnknownCommit && e.UnknownCommit()
}

func (e *insertStageError) UnknownCommit() bool {
	return e != nil && e.Stage == insertStageExec && isBareConnectionError(e.Err)
}

func maxRowsPerBatchForColumns(nCols int) int {
	if nCols <= 0 {
		return 1
	}
	n := (maxPreparedPlaceholders * 95) / (100 * nCols)
	if n < 1 {
		return 1
	}
	return n
}

func planInsertRanges(rows [][]interface{}, maxRowsPerBatch int, maxBatchBytes int64) []insertRange {
	if len(rows) == 0 {
		return nil
	}
	if maxRowsPerBatch < 1 {
		maxRowsPerBatch = 1
	}
	ranges := make([]insertRange, 0, (len(rows)+maxRowsPerBatch-1)/maxRowsPerBatch)
	for i := 0; i < len(rows); {
		end := i + maxRowsPerBatch
		if end > len(rows) {
			end = len(rows)
		}
		if maxBatchBytes > 0 {
			for end > i+1 && estimateBatchBytes(rows[i:end]) > maxBatchBytes {
				end = (i + end) / 2
			}
		}
		ranges = append(ranges, insertRange{Start: i, End: end})
		i = end
	}
	return ranges
}

// PlanInsertRanges returns deterministic contiguous sub-batch ranges for rows.
func (bi *BatchInserter) PlanInsertRanges(rows [][]interface{}) []insertRange {
	return planInsertRanges(rows, maxRowsPerBatchForColumns(len(bi.columns)), bi.maxBatchBytes)
}

// InsertBatch 批量插入数据（按占位符和字节数自动拆分）
func (bi *BatchInserter) InsertBatch(rows [][]interface{}) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	if len(bi.columns) == 0 {
		return 0, fmt.Errorf("no columns defined for table %s", bi.tableName)
	}

	ranges := bi.PlanInsertRanges(rows)
	if len(ranges) == 1 {
		bi.stmtMu.RLock()
		cachedCapacity := bi.cachedRows
		bi.stmtMu.RUnlock()

		if cachedCapacity > 0 && len(rows) < cachedCapacity {
			bi.resetStmt()
			return bi.insertBatchSingleWithAutoWiden(rows, false)
		}
		return bi.insertBatchSingleWithAutoWiden(rows, true) // 单批次，可缓存
	}

	// 拆分为多个小批次（按占位符和字节数双重限制，不缓存）
	var totalAffected int64
	for _, r := range ranges {
		affected, err := bi.insertBatchSingleWithAutoWiden(rows[r.Start:r.End], false)
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
	err := bi.db.QueryRowContext(bi.ctx, `
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
	if _, err := bi.db.ExecContext(bi.ctx, query); err != nil {
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

	result, err := stmt.ExecContext(bi.ctx, args...)
	if err != nil {
		bi.resetOnConnErr(err)
		return 0, &insertStageError{Stage: insertStageExec, Err: fmt.Errorf("failed to execute batch insert: %w", err)}
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, &insertStageError{Stage: insertStageResult, Err: fmt.Errorf("failed to get rows affected: %w", err)}
	}

	if canCache {
		bi.stmtMu.Lock()
		bi.cachedRows = len(rows)
		bi.stmtMu.Unlock()
	}
	return rowsAffected, nil
}

func isBareConnectionError(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) &&
		(errors.Is(err, mysql.ErrInvalidConn) || errors.Is(err, driver.ErrBadConn))
}

func isRetryableConnectionError(err error) bool {
	return isBareConnectionError(err)
}

func isUnknownCommitError(err error) bool {
	if isRetryablePrepareConnectionError(err) {
		return false
	}
	if errors.Is(err, ErrUnknownCommit) {
		return true
	}
	// Connection errors without a proven pre-exec stage cannot be retried safely.
	return isBareConnectionError(err)
}

func isRetryablePrepareConnectionError(err error) bool {
	var staged *insertStageError
	if errors.As(err, &staged) {
		return staged.Stage == insertStagePrepare && isBareConnectionError(staged.Err)
	}
	return false
}

func isDatabaseCapacityError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}

	switch mysqlErr.Number {
	case 3, 1021, 1114:
		return true
	default:
		return false
	}
}

func databaseCapacityDiagnostic(tableName string, batchNum int, err error) string {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || !isDatabaseCapacityError(err) {
		return ""
	}

	cause := "MySQL cannot allocate more storage for this write"
	switch mysqlErr.Number {
	case 3:
		cause = "MySQL failed while writing a file"
	case 1021:
		cause = "MySQL reports disk full while writing a table or temporary file"
	case 1114:
		cause = "MySQL reports the target table or its tablespace is full"
	}

	return fmt.Sprintf(
		"Failure owner: MySQL storage layer; this is not a CSV parser or migration-program logic error. table=%s batch=%d mysql_error=%d mysql_message=%q. Cause: %s. Next checks: check target MySQL datadir free space, container or cloud storage quota, InnoDB tablespace limits, MySQL tmpdir free space, and MySQL error log.",
		tableName,
		batchNum,
		mysqlErr.Number,
		mysqlErr.Message,
		cause,
	)
}

func (bi *BatchInserter) resetOnConnErr(err error) {
	if isRetryableConnectionError(err) {
		bi.resetStmt()
	}
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
	query.WriteString(bi.quotedTable + " ")

	// 列名
	query.WriteString(fmt.Sprintf("(%s) VALUES ", strings.Join(bi.quotedColumns, ", ")))

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
		err := bi.stmt.Close()
		bi.stmt = nil
		return err
	}
	return nil
}
