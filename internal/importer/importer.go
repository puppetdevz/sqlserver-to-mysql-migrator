package importer

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/diagnostics"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	maxRetries               = 3
	retryDelay               = 100 * time.Millisecond
	maxConnectionRetries     = 8
	connectionRetryBaseDelay = 1 * time.Second
	maxConnectionRetryDelay  = 30 * time.Second
	maxSplitDepth            = 8        // 递归分批最大深度
	bufferSize               = 10       // 流水线缓冲区大小
	bom                      = "\uFEFF" // UTF-8 BOM 字符
)

// TableImporter 表数据导入器
type TableImporter struct {
	conn             *database.Connection
	cfg              *config.Config
	tableName        string
	csvPath          string
	errorRecorder    *ErrorRecorder
	progressCallback func(tableName string, totalRows, processedRows, insertedRows int64)
	pressureCallback func(ImportPressureEvent)
	ctx              context.Context
	countCSVRowsFunc func(*os.File) (int64, error)
}

type dbColumnInfo struct {
	Name     string
	Type     string
	Nullable bool
}

// NewTableImporter 创建表导入器
func NewTableImporter(conn *database.Connection, cfg *config.Config, tableName string, csvPath string, errorRecorder *ErrorRecorder) *TableImporter {
	return &TableImporter{
		conn:             conn,
		cfg:              cfg,
		tableName:        tableName,
		csvPath:          csvPath,
		errorRecorder:    errorRecorder,
		ctx:              context.Background(),
		countCSVRowsFunc: countCSVRows,
	}
}

// WithProgressCallback sets a per-batch progress callback for long-running imports.
func (ti *TableImporter) WithProgressCallback(callback func(tableName string, totalRows, processedRows, insertedRows int64)) *TableImporter {
	ti.progressCallback = callback
	return ti
}

// WithPressureCallback sets a callback for import pressure signals used by adaptive scheduling.
// The callback runs synchronously on the DB writer path and must stay fast/non-blocking.
func (ti *TableImporter) WithPressureCallback(callback func(ImportPressureEvent)) *TableImporter {
	ti.pressureCallback = callback
	return ti
}

// WithContext sets a context for cancellation support.
func (ti *TableImporter) WithContext(ctx context.Context) *TableImporter {
	ti.ctx = ctx
	return ti
}

// Import 导入表数据（流水线优化：边读边写）
func (ti *TableImporter) Import() (out *ImportResult, outErr error, outDiag *ImportDiagnostic) {
	stats := ti.cfg.Diagnostics.TableStats(ti.tableName)
	ti.cfg.Diagnostics.State(ti.tableName, "active", "not_run", 0)
	defer func() {
		if outErr != nil {
			recordDiagnosticFailure(ti.cfg.Diagnostics, ti.tableName, outErr)
		}
	}()
	logger.Infof("Starting import for table: %s", ti.tableName)

	// 检查表是否存在
	metadataDone := stats.Start(diagnostics.Metadata)
	exists, err := ti.conn.TableExists(ti.tableName)
	metadataDone()
	if err != nil {
		return nil, fmt.Errorf("failed to check table existence: %w", err), nil
	}

	if !exists {
		return nil, fmt.Errorf("table does not exist: %s", ti.tableName), nil
	}

	// 打开 CSV 文件
	file, err := os.Open(ti.csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err), nil
	}
	defer file.Close()

	// 获取正确大小写的表名（解决 MySQL 大小写不敏感问题）
	actualTableName := ti.conn.GetActualTableName(ti.tableName)

	// 统计 CSV 总行数（用于进度显示）
	prescanDone := stats.Start(diagnostics.Prescan)
	csvTotalRows, err := ti.countRowsForProgress(file)
	prescanDone()
	if ti.cfg.Migration.ShouldCountCSVRowsBeforeImport() && err == nil {
		if info, statErr := file.Stat(); statErr == nil {
			stats.Add(func(c *diagnostics.Counters) { c.PrescanBytes += info.Size() })
		}
	}
	if err != nil {
		logger.Warnf("Failed to count CSV rows for %s: %v", ti.tableName, err)
	}

	// 使用流水线导入
	result, err, diag := ti.pipelinedImport(file, actualTableName, csvTotalRows)
	if err != nil {
		ti.errorRecorder.RecordError(ti.tableName, "", nil, err)
		return result, err, diag
	}

	return result, nil, nil
}

// countCSVRows 统计 CSV 文件行数（用于进度显示）
func countCSVRows(f *os.File) (int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	defer f.Seek(0, io.SeekStart)

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	var count int64
	for {
		_, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

func (ti *TableImporter) countRowsForProgress(file *os.File) (int64, error) {
	if ti.cfg != nil && !ti.cfg.Migration.ShouldCountCSVRowsBeforeImport() {
		return 0, nil
	}
	counter := ti.countCSVRowsFunc
	if counter == nil {
		counter = countCSVRows
	}
	return counter(file)
}

func (ti *TableImporter) getDBColumnInfos(ctx context.Context, tableName string) ([]dbColumnInfo, error) {
	// 使用 DESCRIBE 获取列信息
	query := fmt.Sprintf("DESCRIBE %s", matcher.QuoteIdent(tableName))
	rows, err := ti.conn.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []dbColumnInfo
	for rows.Next() {
		var field, colType, null, key, extra string
		var defaultVal *string
		if err := rows.Scan(&field, &colType, &null, &key, &defaultVal, &extra); err != nil {
			return nil, err
		}
		columns = append(columns, dbColumnInfo{
			Name:     field,
			Type:     colType,
			Nullable: strings.EqualFold(null, "YES"),
		})
	}
	return columns, rows.Err()
}

func columnNamesFromInfos(infos []dbColumnInfo) []string {
	columns := make([]string, len(infos))
	for i, info := range infos {
		columns[i] = info.Name
	}
	return columns
}

// BuildColumnIndexMap 构建列名到索引的映射（大写键）
func BuildColumnIndexMap(columns []string) map[string]int {
	m := make(map[string]int, len(columns))
	for i, col := range columns {
		m[strings.ToUpper(col)] = i
	}
	return m
}

// BuildUpperColumnMap 构建大写列名到原始列名的映射
func BuildUpperColumnMap(columns []string) map[string]string {
	m := make(map[string]string, len(columns))
	for _, col := range columns {
		m[strings.ToUpper(col)] = col
	}
	return m
}

// buildColumnMapping 构建 CSV 列索引到有效列索引的映射
// csvColIdx: CSV 列索引 -> -1 表示跳过该列
func buildColumnMapping(csvHeaders []string, dbColumns []string) []int {
	dbColMap := BuildColumnIndexMap(dbColumns)

	// 构建 CSV 列索引映射
	mapping := make([]int, len(csvHeaders))
	for i, csvCol := range csvHeaders {
		if _, ok := dbColMap[strings.ToUpper(csvCol)]; ok {
			mapping[i] = dbColMap[strings.ToUpper(csvCol)]
		} else {
			mapping[i] = -1 // 跳过
		}
	}
	return mapping
}

func countMatchedColumns(csvHeaders []string, dbColumns []string) int {
	dbColMap := BuildUpperColumnMap(dbColumns)
	var matched int
	for _, csvCol := range csvHeaders {
		if _, ok := dbColMap[strings.ToUpper(csvCol)]; ok {
			matched++
		}
	}
	return matched
}

func alignColumnInfos(headers []string, dbColumnInfos []dbColumnInfo) []dbColumnInfo {
	dbInfoMap := make(map[string]dbColumnInfo, len(dbColumnInfos))
	for _, info := range dbColumnInfos {
		dbInfoMap[strings.ToUpper(info.Name)] = info
	}

	aligned := make([]dbColumnInfo, len(headers))
	for i, header := range headers {
		if info, ok := dbInfoMap[strings.ToUpper(header)]; ok {
			aligned[i] = info
		} else {
			aligned[i] = dbColumnInfo{Name: header}
		}
	}
	return aligned
}

type csvRepairError struct {
	Reason   string
	Expected int
	Actual   int
}

func (e *csvRepairError) Error() string {
	if e == nil {
		return "CSV repair error"
	}
	return fmt.Sprintf("%s (expected %d fields, got %d)", e.Reason, e.Expected, e.Actual)
}

// CSVStructureError is a per-record CSV layout error that must not be written.
type CSVStructureError struct {
	TableName    string
	CSVPath      string
	RecordNumber int64
	Expected     int
	Actual       int
	Reason       string
}

func (e *CSVStructureError) Error() string {
	if e == nil {
		return "CSV structure error"
	}
	return fmt.Sprintf("CSV structure error: table=%s file=%s record=%d expected_fields=%d actual_fields=%d reason=%s",
		e.TableName, e.CSVPath, e.RecordNumber, e.Expected, e.Actual, e.Reason)
}

func repairDelimitedRow(row []string, columnInfos []dbColumnInfo) ([]string, error) {
	expected := len(columnInfos)
	actual := len(row)
	if expected == 0 {
		if actual == 0 {
			return row, nil
		}
		return nil, &csvRepairError{Reason: "empty column layout", Expected: 0, Actual: actual}
	}
	if actual == expected {
		return row, nil
	}
	if actual < expected {
		return nil, &csvRepairError{Reason: "short row", Expected: expected, Actual: actual}
	}

	extraFields := actual - expected
	bestScore := -1 << 30
	var best [][]string
	seen := make(map[string]struct{})
	for i, info := range columnInfos {
		if !canAbsorbDelimitedFields(info.Type) || i+extraFields >= len(row) {
			continue
		}
		candidate := collapseDelimitedFields(row, i, extraFields)
		score := scoreRowAgainstColumnTypes(candidate, columnInfos)
		if score < 0 {
			continue
		}
		key := strings.Join(candidate, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		switch {
		case score > bestScore:
			bestScore = score
			best = [][]string{candidate}
		case score == bestScore:
			best = append(best, candidate)
		}
	}
	if len(best) == 0 {
		return nil, &csvRepairError{Reason: "no valid CSV field repair candidate", Expected: expected, Actual: actual}
	}
	if len(best) > 1 {
		return nil, &csvRepairError{Reason: "ambiguous CSV field repair", Expected: expected, Actual: actual}
	}
	return best[0], nil
}

func collapseDelimitedFields(row []string, absorbIdx, extraFields int) []string {
	repaired := make([]string, 0, len(row)-extraFields)
	repaired = append(repaired, row[:absorbIdx]...)
	repaired = append(repaired, strings.Join(row[absorbIdx:absorbIdx+extraFields+1], ","))
	repaired = append(repaired, row[absorbIdx+extraFields+1:]...)
	return repaired
}

func scoreRowAgainstColumnTypes(row []string, columnInfos []dbColumnInfo) int {
	score := 0
	for i, value := range row {
		if i >= len(columnInfos) {
			break
		}
		score += scoreValueForColumnType(value, columnInfos[i])
	}
	return score
}

func scoreValueForColumnType(value string, info dbColumnInfo) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1
	}

	columnType := strings.ToLower(info.Type)
	switch {
	case canAbsorbDelimitedFields(columnType):
		return 1
	case isIntegerColumnType(columnType):
		if _, err := strconv.ParseInt(value, 10, 64); err == nil {
			return 3
		}
		return -5
	case isDecimalColumnType(columnType):
		if _, err := strconv.ParseFloat(value, 64); err == nil {
			return 3
		}
		return -5
	case isTemporalColumnType(columnType):
		if isTemporalValue(value) {
			return 3
		}
		return -5
	default:
		return 0
	}
}

func canAbsorbDelimitedFields(columnType string) bool {
	lower := strings.ToLower(columnType)
	return strings.Contains(lower, "char") || strings.Contains(lower, "text") ||
		strings.Contains(lower, "blob") || strings.Contains(lower, "json")
}

func isIntegerColumnType(columnType string) bool {
	lower := strings.ToLower(columnType)
	return strings.Contains(lower, "int") || lower == "bit"
}

func isDecimalColumnType(columnType string) bool {
	lower := strings.ToLower(columnType)
	return strings.Contains(lower, "decimal") || strings.Contains(lower, "numeric") ||
		strings.Contains(lower, "float") || strings.Contains(lower, "double") ||
		strings.Contains(lower, "real")
}

func isTemporalColumnType(columnType string) bool {
	lower := strings.ToLower(columnType)
	return strings.Contains(lower, "date") || strings.Contains(lower, "time") ||
		strings.Contains(lower, "year")
}

func isTemporalValue(value string) bool {
	layouts := []string{
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05.999",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"15:04:05",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// filterRowData 过滤行数据，只保留有效的列（根据 mapping 映射）
// 支持 []string 和 []any 两种输入类型
func filterRowData(row []any, mapping []int) []any {
	result := make([]any, 0, len(mapping))
	for i, mappedIdx := range mapping {
		if mappedIdx < 0 {
			continue
		}
		if i < len(row) {
			result = append(result, row[i])
		} else {
			result = append(result, nil)
		}
	}
	return result
}

// batchData is a pipeline unit from the CSV reader to the DB writer.
type batchData struct {
	lease      *migration.Lease
	queueLease *migration.Lease
	rows       [][]any
	batchNum   int
	err        error
}

// batchResult 批次处理结果（从 DB writer → 主 goroutine）
type batchResult struct {
	batchNum     int
	rowCount     int
	affectedRows int64
	err          error // nil=成功，非nil=错误
}

type batchInserter interface {
	InsertBatch(rows [][]any) (int64, error)
}

type adaptiveBatchInsertResult struct {
	consumedRows int
	affectedRows int64
	retries      int
	split        bool
	err          error
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func sendPipelineBatch(ctx context.Context, batchChan chan<- batchData, csvDone <-chan struct{}, bd batchData) bool {
	select {
	case batchChan <- bd:
		return true
	case <-csvDone:
		return false
	case <-ctx.Done():
		return false
	}
}

func insertBatchWithAdaptiveRetry(
	ctx context.Context,
	inserter batchInserter,
	tableName string,
	batchNum int,
	rows [][]any,
	wait func(time.Duration) error,
) adaptiveBatchInsertResult {
	if wait == nil {
		wait = func(delay time.Duration) error {
			return waitForRetry(ctx, delay)
		}
	}
	ranges := planRangesForInserter(inserter, rows)
	var total adaptiveBatchInsertResult
	for _, r := range ranges {
		res := insertRangeWithAdaptiveRetryDepth(ctx, inserter, tableName, batchNum, rows[r.Start:r.End], wait, 0)
		total.consumedRows += res.consumedRows
		total.affectedRows += res.affectedRows
		total.retries += res.retries
		total.split = total.split || res.split
		if res.err != nil {
			total.err = res.err
			return total
		}
	}
	return total
}

func planRangesForInserter(inserter batchInserter, rows [][]any) []insertRange {
	if planner, ok := inserter.(interface {
		PlanInsertRanges([][]any) []insertRange
	}); ok {
		return planner.PlanInsertRanges(rows)
	}
	if len(rows) == 0 {
		return nil
	}
	return []insertRange{{Start: 0, End: len(rows)}}
}

func insertRangeWithAdaptiveRetryDepth(
	ctx context.Context,
	inserter batchInserter,
	tableName string,
	batchNum int,
	rows [][]any,
	wait func(time.Duration) error,
	depth int,
) adaptiveBatchInsertResult {
	for retry := 0; ; retry++ {
		affected, err := inserter.InsertBatch(rows)
		if err == nil {
			return adaptiveBatchInsertResult{consumedRows: len(rows), affectedRows: affected, retries: retry}
		}
		if isUnknownCommitError(err) {
			if !errors.Is(err, ErrUnknownCommit) {
				err = fmt.Errorf("%w: %w", ErrUnknownCommit, err)
			}
			return adaptiveBatchInsertResult{retries: retry, err: err}
		}

		attempts, baseDelay := batchRetryPolicy(err)
		canSplit := isRetryablePrepareConnectionError(err) && retry >= 1 && len(rows) > 1 && depth < maxSplitDepth
		if canSplit {
			mid := len(rows) / 2
			logger.Warnf("Connection retry batch split: table=%s batch=%d rows=%d split_rows=%d/%d reason=%v",
				tableName, batchNum, len(rows), mid, len(rows)-mid, err)

			left := insertRangeWithAdaptiveRetryDepth(ctx, inserter, tableName, batchNum, rows[:mid], wait, depth+1)
			if left.err != nil {
				left.retries += retry
				left.split = true
				return left
			}

			right := insertRangeWithAdaptiveRetryDepth(ctx, inserter, tableName, batchNum, rows[mid:], wait, depth+1)
			right.consumedRows += left.consumedRows
			right.affectedRows += left.affectedRows
			right.retries += left.retries + retry
			right.split = true
			return right
		}

		if retry+1 >= attempts {
			return adaptiveBatchInsertResult{retries: retry, err: err}
		}

		delay := batchRetryDelay(retry+1, baseDelay, maxConnectionRetryDelay)
		logger.Warnf("Retry %d/%d for batch %d in table %s after %s: %v",
			retry+1, attempts, batchNum, tableName, delay, err)
		if waitErr := wait(delay); waitErr != nil {
			return adaptiveBatchInsertResult{retries: retry, err: waitErr}
		}
	}
}

func batchRetryPolicy(err error) (int, time.Duration) {
	if isUnknownCommitError(err) {
		return 1, 0
	}
	if isDatabaseCapacityError(err) {
		return 1, 0
	}
	if isRetryablePrepareConnectionError(err) {
		return maxConnectionRetries, connectionRetryBaseDelay
	}
	return maxRetries, retryDelay
}

func batchRetryDelay(attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	return delay
}

func pressureEventsForBatch(tableName string, batchNum int, retries int, duration time.Duration, slowThreshold time.Duration, err error) []ImportPressureEvent {
	var events []ImportPressureEvent
	if retries > 0 {
		events = append(events, ImportPressureEvent{
			TableName: tableName,
			BatchNum:  batchNum,
			Signal:    PressureRetry,
			Detail:    fmt.Sprintf("retries=%d", retries),
		})
	}
	if slowThreshold > 0 && duration > slowThreshold {
		events = append(events, ImportPressureEvent{
			TableName: tableName,
			BatchNum:  batchNum,
			Signal:    PressureSlowBatch,
			Detail:    fmt.Sprintf("duration=%s threshold=%s", duration.Truncate(time.Millisecond), slowThreshold),
		})
	}
	if isRetryableConnectionError(err) {
		events = append(events, ImportPressureEvent{
			TableName: tableName,
			BatchNum:  batchNum,
			Signal:    PressureConnection,
			Detail:    err.Error(),
		})
	}
	if isMySQLLockWaitTimeout(err) {
		events = append(events, ImportPressureEvent{
			TableName: tableName,
			BatchNum:  batchNum,
			Signal:    PressureLockWait,
			Detail:    err.Error(),
		})
	}
	if isDatabaseCapacityError(err) {
		events = append(events, ImportPressureEvent{
			TableName: tableName,
			BatchNum:  batchNum,
			Signal:    PressureCapacity,
			Detail:    err.Error(),
		})
	}
	return events
}

func isMySQLLockWaitTimeout(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1205
}

func (ti *TableImporter) emitPressureEventsForBatch(batchNum int, result adaptiveBatchInsertResult, duration time.Duration, slowThreshold time.Duration) {
	if ti.pressureCallback == nil {
		return
	}
	for _, event := range pressureEventsForBatch(ti.tableName, batchNum, result.retries, duration, slowThreshold, result.err) {
		ti.pressureCallback(event)
	}
}

// pipelinedImport 流水线导入：边读边写
func (ti *TableImporter) pipelinedImport(file *os.File, actualTableName string, csvTotalRows int64) (*ImportResult, error, *ImportDiagnostic) {
	stats := ti.cfg.Diagnostics.TableStats(ti.tableName)
	limits := ti.cfg.Migration.Resources.Effective()
	if BaselineAlgorithms {
		limits.QueueBatches = bufferSize
	}
	budget, budgetErr := ti.cfg.ImportBudget()
	if budgetErr != nil {
		return nil, budgetErr, nil
	}
	queueBudget, budgetErr := migration.NewBudget(limits.QueueBytes, limits.QueueBatches, nil)
	if budgetErr != nil {
		return nil, budgetErr, nil
	}
	defer stats.Start(diagnostics.Pipeline)()
	safeCtx := ti.ctx
	if safeCtx == nil {
		safeCtx = context.Background()
	}

	reader := csv.NewReader(file)
	// LazyQuotes matches dirty source CSV. It must not guess field counts;
	// short/ambiguous rows still fail in repairDelimitedRow.
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	// 获取数据库列（提前获取，用于无表头模式校验）
	metadataDone := stats.Start(diagnostics.Metadata)
	dbColumnInfos, dbErr := ti.getDBColumnInfos(safeCtx, actualTableName)
	metadataDone()
	var dbColumns []string
	if dbErr != nil {
		logger.Warnf("Failed to get DB columns for %s: %v", actualTableName, dbErr)
	} else {
		dbColumns = columnNamesFromInfos(dbColumnInfos)
	}

	// 根据配置决定是否读取表头
	var headers []string
	var firstRow []string // 无表头模式的第一行数据
	hasConfiguredHeader := ti.cfg.Source.CSVHasHeader != nil && *ti.cfg.Source.CSVHasHeader
	useHeaderMapping := hasConfiguredHeader

	if hasConfiguredHeader {
		// 有表头模式：读取第一行作为表头
		var err error
		headers, err = reader.Read()
		if err != nil {
			file.Close()
			if err == io.EOF {
				logger.Infof("Table %s: CSV file is empty, skipping import", ti.tableName)
				return &ImportResult{
					TableName:     ti.tableName,
					Success:       true,
					InsertedRows:  0,
					ProcessedRows: 0,
					ErrorCount:    0,
				}, nil, nil
			}
			diag := &ImportDiagnostic{
				TableName:   ti.tableName,
				ErrorType:   ErrorTypeEOF,
				ErrorDetail: fmt.Sprintf("failed to read CSV header (%v)", err),
				CSVPath:     ti.csvPath,
			}
			return nil, fmt.Errorf("failed to read CSV header: %w", err), diag
		}
		// 清理表头（移除 BOM、空格等）
		for i := range headers {
			headers[i] = strings.TrimSpace(headers[i])
			headers[i] = strings.TrimPrefix(headers[i], bom)
		}

		if dbColumns != nil {
			matchedColumns := countMatchedColumns(headers, dbColumns)
			switch {
			case matchedColumns == 0 && len(headers) == len(dbColumns):
				logger.Warnf("Table %s: configured CSV header but first row does not match DB columns; importing as no-header CSV", ti.tableName)
				firstRow = headers
				headers = dbColumns
				useHeaderMapping = false
			case len(headers) != len(dbColumns):
				peekRow, err := reader.Read()
				if err == io.EOF {
					// Header-only file. Continue with header mapping and no data rows.
					break
				}
				if err != nil {
					file.Close()
					diag := &ImportDiagnostic{
						TableName:   ti.tableName,
						ErrorType:   ErrorTypeEOF,
						ErrorDetail: fmt.Sprintf("failed to read first CSV data row (%v)", err),
						CSVPath:     ti.csvPath,
					}
					return nil, fmt.Errorf("failed to read first CSV data row: %w", err), diag
				}
				firstRow = peekRow
				if len(peekRow) == len(dbColumns) {
					logger.Warnf("Table %s: CSV header has %d columns but data rows and DB have %d columns; falling back to DB column order",
						ti.tableName, len(headers), len(dbColumns))
					headers = dbColumns
					useHeaderMapping = false
				}
			}
		}
	} else {
		// 无表头模式：读取第一行数据，验证列数
		var err error
		firstRow, err = reader.Read()
		if err == io.EOF {
			file.Close()
			return &ImportResult{TableName: ti.tableName, Success: true, ProcessedRows: 0, InsertedRows: 0, ErrorCount: 0}, nil, nil
		}
		if err != nil {
			file.Close()
			diag := &ImportDiagnostic{
				TableName:   ti.tableName,
				ErrorType:   ErrorTypeEOF,
				ErrorDetail: fmt.Sprintf("failed to read first row (%v)", err),
				CSVPath:     ti.csvPath,
			}
			return nil, fmt.Errorf("failed to read first row: %w", err), diag
		}

		// 使用提前获取的 dbColumns
		if dbColumns == nil {
			return nil, fmt.Errorf("failed to get DB columns"), nil
		}

		// 严格校验列数
		if len(firstRow) != len(dbColumns) {
			file.Close()
			return nil, fmt.Errorf("column count mismatch: CSV has %d columns, DB has %d",
				len(firstRow), len(dbColumns)), nil
		}

		// 无表头模式：使用数据库列名作为 headers（用于 BatchInserter），firstRow 作为第一行数据
		headers = dbColumns
	}

	// 如果 dbColumns 未获取（可能是上面的错误分支），使用 headers 作为 fallback
	if dbColumns == nil {
		dbColumns = headers
	}
	rowColumnInfos := alignColumnInfos(headers, dbColumnInfos)

	// 创建批量插入器（使用数据库列过滤 CSV 列）
	inserter, skippedCols := NewBatchInserterWithDBColumns(
		ti.conn.DB,
		actualTableName,
		headers,
		dbColumns,
		ti.cfg.Migration.BatchSize,
		ti.cfg.Migration.OnDuplicate,
	)
	if inserter == nil {
		diag := &ImportDiagnostic{
			TableName:   ti.tableName,
			ErrorType:   ErrorTypeNoMatch,
			ErrorDetail: fmt.Sprintf("no valid columns to insert (0/%d matched)", len(dbColumns)),
			CSVPath:     ti.csvPath,
			CSVColumns:  limitSlice(headers, MaxDiagnosticColumns),
			DBColumns:   limitSlice(dbColumns, MaxDiagnosticColumns),
		}
		return nil, fmt.Errorf("no valid columns to insert for table %s", actualTableName), diag
	}
	inserter.SetMaxBatchBytes(ti.cfg.Migration.MaxBatchBytes)
	inserter.SetContext(safeCtx)
	inserter.stats = stats
	inserter.sqlCacheBytes = limits.SQLCacheBytes
	inserter.rowMemoryLimit = limits.BatchMemoryBytes / 2
	defer inserter.Close()

	// 记录跳过的列
	if len(skippedCols) > 0 {
		logger.Warnf("Table %s: skipped %d columns not in DB (%s)",
			ti.tableName, len(skippedCols), strings.Join(skippedCols, ", "))
	}

	// 构建 CSV 列索引到有效列的映射（用于筛选数据）
	var mapping []int
	if useHeaderMapping {
		// 有表头模式：按列名匹配
		mapping = buildColumnMapping(headers, dbColumns)
	} else {
		// 无表头模式：按位置顺序映射，CSV[i] -> DB[i]
		mapping = make([]int, len(headers))
		for i := range mapping {
			mapping[i] = i
		}
		logger.Infof("Importing CSV without header for table %s: %d columns", ti.tableName, len(headers))
	}

	// 建立流水线：CSV读取 -> 预处理 -> 数据库插入
	var csvDoneOnce sync.Once
	csvDone := make(chan struct{})
	batchChan := make(chan batchData, limits.QueueBatches)
	resultChan := make(chan batchResult, limits.QueueBatches)
	producerCtx, cancelProducer := context.WithCancel(safeCtx)
	defer cancelProducer()
	aggregatorDone := make(chan struct{})

	// fast_fail 配置（闭包捕获，无需锁）
	fastFail := ti.cfg.Migration.IsFastFail()

	var totalRows int64
	var processedRows int64
	var errorCount int64
	var lastErr error
	var wg sync.WaitGroup

	// 启动结果聚合 goroutine
	go func() {
		defer close(aggregatorDone)

		for result := range resultChan {
			stats.Add(func(c *diagnostics.Counters) {
				c.ConsumedRows += int64(result.rowCount)
				c.AffectedRows += result.affectedRows
			})
			processedRows += int64(result.rowCount)
			totalRows += result.affectedRows
			if result.err != nil {
				errorCount++
				if !errors.Is(lastErr, ErrUnknownCommit) {
					lastErr = result.err
				}
				logger.Debugf("[Aggregator] batch %d: error=%v consumed=%d affected=%d", result.batchNum, result.err, result.rowCount, result.affectedRows)
			}
			if ti.progressCallback != nil {
				ti.progressCallback(ti.tableName, csvTotalRows, processedRows, totalRows)
			}

		}
		logger.Debugf("[Aggregator] resultChan closed: processedRows=%d, totalRows=%d, errorCount=%d", processedRows, totalRows, errorCount)
	}()

	// 启动 CSV 读取 goroutine
	wg.Add(1)
	go func(firstData []string) {
		var recordNum int
		var batchNum int
		var totalRead int
		var currentLease, queueLease *migration.Lease
		defer func() { currentLease.Release(); queueLease.Release() }()
		var parsed, repaired, structureErrors, lastOffset int64
		flushReadStats := func() {
			offset := reader.InputOffset()
			stats.Add(func(c *diagnostics.Counters) {
				c.ParsedRows += parsed
				c.Repairs += repaired
				c.StructureErrors += structureErrors
				c.CSVBytes += offset - lastOffset
			})
			parsed, repaired, structureErrors = 0, 0, 0
			lastOffset = offset
		}
		defer flushReadStats()

		defer wg.Done()
		defer func() {
			close(batchChan)
			logger.Debugf("[CSV Reader] batchChan closed after %d batches, totalRead=%d", batchNum, totalRead)
		}()

		send := func(bd batchData) bool {
			defer stats.Start(diagnostics.EnqueueWait)()
			return sendPipelineBatch(safeCtx, batchChan, csvDone, bd)
		}

		nextRow := func() ([]string, error) {
			if firstData != nil {
				row := firstData
				firstData = nil
				return row, nil
			}
			return reader.Read()
		}

		for {
			if safeCtx.Err() != nil {
				return
			}

			var acquireErr error
			budgetWaitDone := stats.Start(diagnostics.EnqueueWait)
			if !BaselineAlgorithms && currentLease == nil {
				queueLease, acquireErr = queueBudget.Acquire(producerCtx, limits.BatchMemoryBytes)
				if acquireErr == nil {
					currentLease, acquireErr = budget.Acquire(producerCtx, limits.BatchMemoryBytes)
				}
			}
			budgetWaitDone()
			if acquireErr != nil {
				return
			}
			batchStart := time.Now()
			var batchMemory int64
			var processedBatch [][]any
			for len(processedBatch) < ti.cfg.Migration.BatchSize {
				row, err := nextRow()
				if err == io.EOF {
					break
				}
				if err != nil {
					logger.Errorf("CSV read error in %s at record %d: %v", ti.tableName, recordNum, err)
					send(batchData{batchNum: batchNum, err: err})
					return
				}
				recordNum++
				parsed++
				var rowMemory int64
				if !BaselineAlgorithms {
					rowMemory = estimateCSVRecordMemory(row)
				}
				if !BaselineAlgorithms && rowMemory > limits.BatchMemoryBytes/2 {
					resourceErr := fmt.Errorf("%w: CSV logical row estimated memory %d exceeds half batch reservation %d", migration.ErrResourceLimit, rowMemory, limits.BatchMemoryBytes/2)
					send(batchData{batchNum: batchNum + 1, err: resourceErr})
					return
				}
				convertStart := time.Now()
				repairedRow, repairErr := repairDelimitedRow(row, rowColumnInfos)
				if repairErr != nil {
					stats.Observe(diagnostics.ConvertRepair, time.Since(convertStart))
					structureErrors++
					csvErr := &CSVStructureError{
						TableName:    ti.tableName,
						CSVPath:      ti.csvPath,
						RecordNumber: int64(recordNum),
						Expected:     len(rowColumnInfos),
						Actual:       len(row),
						Reason:       repairErr.Error(),
					}
					if ti.errorRecorder != nil {
						ti.errorRecorder.RecordError(ti.tableName, "", nil, csvErr)
					}
					logger.Errorf("%s", csvErr.Error())
					batchNum++
					if !send(batchData{batchNum: batchNum, err: csvErr}) {
						return
					}
					if fastFail {
						return
					}
					continue
				}
				if len(repairedRow) != len(row) {
					repaired++
				}
				batchMemory += rowMemory
				processedBatch = append(processedBatch, filterRowData(PreprocessRow(repairedRow), mapping))
				stats.Observe(diagnostics.ConvertRepair, time.Since(convertStart))
				totalRead++
				if (!BaselineAlgorithms && batchMemory >= limits.BatchMemoryBytes/2) || (ti.cfg.Migration.MaxRowsPerTable > 0 && totalRead >= ti.cfg.Migration.MaxRowsPerTable) {
					break
				}
			}

			stats.Observe(diagnostics.ReadParse, time.Since(batchStart))
			flushReadStats()
			if len(processedBatch) == 0 {
				return
			}

			batchNum++
			if !send(batchData{rows: processedBatch, batchNum: batchNum, lease: currentLease, queueLease: queueLease}) {
				return
			}
			currentLease = nil
			queueLease = nil
			logger.Debugf("[CSV Reader] Batch %d: %d rows read, took %.1fs, totalRead=%d",
				batchNum, len(processedBatch), time.Since(batchStart).Seconds(), totalRead)
			if ti.cfg.Migration.MaxRowsPerTable > 0 && totalRead >= ti.cfg.Migration.MaxRowsPerTable {
				logger.Infof("Reached max_rows_per_table limit (%d rows) for %s, stopping import",
					ti.cfg.Migration.MaxRowsPerTable, ti.tableName)
				return
			}
		}
	}(firstRow) // 无表头模式传递 firstRow，有表头模式传递 nil

	// 启动数据库写入 goroutine
	wg.Add(1)
	go func() {
		var totalInserted int64
		stopWriting := false
		defer wg.Done()
		defer func() {
			close(resultChan)
			logger.Debugf("[DB Writer] resultChan closed, totalInserted=%d", totalInserted)
		}()

		for {
			waitDone := stats.Start(diagnostics.DequeueWait)
			bd, ok := <-batchChan
			waitDone()
			if !ok {
				break
			}
			bd.queueLease.Release()
			batchStart := time.Now()
			// CSV 读取错误，跳过插入但传递结果
			if bd.err != nil {
				bd.lease.Release()
				if fastFail {
					stopWriting = true
					cancelProducer()
					csvDoneOnce.Do(func() { close(csvDone) })
				}
				resultChan <- batchResult{
					batchNum:     bd.batchNum,
					rowCount:     len(bd.rows),
					affectedRows: 0,
					err:          bd.err,
				}
				continue
			}

			if stopWriting || safeCtx.Err() != nil {
				bd.lease.Release()
				continue
			}
			wait := func(delay time.Duration) error {
				defer stats.Start(diagnostics.RetryWait)()
				return waitForRetry(safeCtx, delay)
			}
			result := insertBatchWithAdaptiveRetry(safeCtx, inserter, ti.tableName, bd.batchNum, bd.rows, wait)
			stats.Add(func(c *diagnostics.Counters) {
				c.Retries += int64(result.retries)
				c.BatchRows += int64(len(bd.rows))
				c.BatchBytes += estimateBatchBytes(bd.rows)
				if errors.Is(result.err, ErrUnknownCommit) {
					c.UnknownCommits++
				}
			})
			affected := result.affectedRows
			insertErr := result.err
			batchDuration := time.Since(batchStart)
			stats.Observe(diagnostics.Batch, batchDuration)

			slowThreshold := time.Duration(ti.cfg.Migration.EffectiveSlowBatchSeconds()) * time.Second
			ti.emitPressureEventsForBatch(bd.batchNum, result, batchDuration, slowThreshold)

			// 发送结果到 resultChan
			resultChan <- batchResult{
				batchNum:     bd.batchNum,
				rowCount:     result.consumedRows,
				affectedRows: affected,
				err:          insertErr,
			}

			if insertErr != nil {
				if diagnostic := databaseCapacityDiagnostic(ti.tableName, bd.batchNum, insertErr); diagnostic != "" {
					logger.Errorf("%s", diagnostic)
					insertErr = fmt.Errorf("%s: %w", diagnostic, insertErr)
				}
				ti.errorRecorder.RecordBatchError(ti.tableName, bd.batchNum, bd.rows, insertErr)
				logger.Errorf("Failed to insert batch %d for table %s after %d retries: %v", bd.batchNum, ti.tableName, result.retries, insertErr)
				if fastFail {
					stopWriting = true
					cancelProducer()
					csvDoneOnce.Do(func() { close(csvDone) })
				}
			} else {
				totalInserted += affected
				logger.Debugf("[DB Writer] Batch %d: %d rows inserted, estimated_bytes=%d retries=%d split=%t duration=%.1fs totalInserted=%d",
					bd.batchNum, affected, estimateBatchBytes(bd.rows), result.retries, result.split, batchDuration.Seconds(), totalInserted)
			}
			bd.lease.Release()
		}
	}()

	wg.Wait()

	// 等待 aggregator goroutine 完成（resultChan 由 DB writer goroutine 关闭）
	<-aggregatorDone

	if safeCtx.Err() != nil && lastErr == nil {
		lastErr = safeCtx.Err()
		errorCount++
	}
	if (fastFail || safeCtx.Err() != nil) && lastErr != nil {
		return &ImportResult{
			TableName:     ti.tableName,
			ProcessedRows: processedRows,
			InsertedRows:  totalRows,
			TotalRows:     csvTotalRows,
			ErrorCount:    errorCount,
			Success:       false,
			ErrorMessage:  lastErr.Error(),
		}, lastErr, nil
	}

	logger.Infof("Import completed for table %s: %d rows processed, %d rows inserted, %d errors",
		ti.tableName, processedRows, totalRows, errorCount)

	return &ImportResult{
		TableName:     ti.tableName,
		ProcessedRows: processedRows,
		InsertedRows:  totalRows,
		TotalRows:     csvTotalRows,
		ErrorCount:    errorCount,
		Success:       errorCount == 0,
	}, nil, nil
}

// ImportResult 导入结果
type ImportResult struct {
	TableName     string
	ProcessedRows int64
	InsertedRows  int64
	TotalRows     int64 // CSV 文件总行数
	ErrorCount    int64
	Success       bool
	Skipped       bool
	ErrorMessage  string
}

const (
	MaxDiagnosticColumns = 10
	ErrorTypeEOF         = "EOF"
	ErrorTypeNoMatch     = "NO_MATCH"
	ErrorTypeCSVNotFound = "CSV_NOT_FOUND"
)

// ImportDiagnostic carries structured diagnostic info for failed imports.
// Returned alongside error so the caller can log it before continuing.
type ImportDiagnostic struct {
	TableName   string
	ErrorType   string // ErrorTypeEOF, ErrorTypeNoMatch, or ErrorTypeCSVNotFound
	ErrorDetail string // e.g. "failed to read CSV header (EOF)"
	CSVPath     string
	CSVColumns  []string
	DBColumns   []string
	TriedPaths  []string // only populated for CSV_NOT_FOUND
}

func limitSlice(s []string, max int) []string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ErrorRecorder 错误记录器
type ErrorRecorder struct {
	mu      sync.Mutex
	errors  []ErrorRecord
	file    *os.File
	enabled bool
}

type ErrorRecord struct {
	TableName string    `json:"table_name"`
	BatchNum  int       `json:"batch_num,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	SQL       string    `json:"sql,omitempty"`
	Error     string    `json:"error"`
	RowData   []string  `json:"row_data,omitempty"` // 出错的行数据（最多3行）
}

// NewErrorRecorder 创建错误记录器
func NewErrorRecorder(logFile string) (*ErrorRecorder, error) {
	recorder := &ErrorRecorder{enabled: true}

	if logFile != "" {
		logDir := filepath.Dir(logFile)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			logger.Warnf("Failed to create error log dir: %v", err)
		} else {
			f, err := os.OpenFile(
				logFile,
				os.O_APPEND|os.O_CREATE|os.O_WRONLY,
				0644,
			)
			if err != nil {
				logger.Warnf("Failed to open error log file: %v", err)
			} else {
				recorder.file = f
			}
		}
	}

	return recorder, nil
}

// RecordError 记录错误
func (er *ErrorRecorder) RecordError(tableName, sql string, rowData []string, err error) {
	if !er.enabled {
		return
	}

	er.mu.Lock()
	defer er.mu.Unlock()

	record := ErrorRecord{
		TableName: tableName,
		Timestamp: time.Now(),
		SQL:       sql,
		Error:     err.Error(),
	}

	if len(rowData) > 3 {
		record.RowData = rowData[:3]
	} else {
		record.RowData = rowData
	}

	er.errors = append(er.errors, record)

	if er.file != nil {
		fmt.Fprintf(er.file, "[%s] table=%s stage=row err=%s\n", record.Timestamp.Format(time.RFC3339), tableName, err)
	}
}

// RecordBatchError 记录批次错误
func (er *ErrorRecorder) RecordBatchError(tableName string, batchNum int, rows [][]any, err error) {
	if !er.enabled {
		return
	}

	er.mu.Lock()
	defer er.mu.Unlock()

	// 取第一批行数据作为示例
	var sampleRows []string
	if len(rows) > 0 {
		for i := 0; i < len(rows[0]) && len(sampleRows) < 3; i++ {
			sampleRows = append(sampleRows, fmt.Sprintf("%v", rows[0][i]))
		}
	}

	record := ErrorRecord{
		TableName: tableName,
		BatchNum:  batchNum,
		Timestamp: time.Now(),
		Error:     err.Error(),
		RowData:   sampleRows,
	}

	er.errors = append(er.errors, record)

	if er.file != nil {
		fmt.Fprintf(er.file, "[%s] table=%s batch=%d stage=batch err=%s\n", record.Timestamp.Format(time.RFC3339), tableName, batchNum, err)
	}
}

// GetErrors 获取所有错误记录
func (er *ErrorRecorder) GetErrors() []ErrorRecord {
	er.mu.Lock()
	defer er.mu.Unlock()
	return er.errors
}

// GetErrorCount 获取错误数量
func (er *ErrorRecorder) GetErrorCount() int {
	er.mu.Lock()
	defer er.mu.Unlock()
	return len(er.errors)
}

// Close 关闭错误记录器
func (er *ErrorRecorder) Close() error {
	if er.file != nil {
		return er.file.Close()
	}
	return nil
}

// DataImporter 数据导入协调器
type DataImporter struct {
	conn             *database.Connection
	cfg              *config.Config
	errorRecorder    *ErrorRecorder
	progressCallback func(tableName string, totalRows, processedRows, insertedRows int64)
	pressureCallback func(ImportPressureEvent)
	ctx              context.Context
}

// NewDataImporter 创建数据导入协调器
func NewDataImporter(conn *database.Connection, cfg *config.Config) *DataImporter {
	errorRecorder, err := NewErrorRecorder(cfg.Logging.File)
	if err != nil {
		logger.Warnf("Failed to create error recorder: %v", err)
	}

	return &DataImporter{
		conn:          conn,
		cfg:           cfg,
		errorRecorder: errorRecorder,
		ctx:           context.Background(),
	}
}

// WithProgressCallback sets a per-batch progress callback for table imports.
func (di *DataImporter) WithProgressCallback(callback func(tableName string, totalRows, processedRows, insertedRows int64)) *DataImporter {
	di.progressCallback = callback
	return di
}

// WithPressureCallback sets a callback for table-level import pressure signals.
// The callback runs synchronously on each table's DB writer path; scheduler callbacks should only update in-memory limiter state.
func (di *DataImporter) WithPressureCallback(callback func(ImportPressureEvent)) *DataImporter {
	di.pressureCallback = callback
	return di
}

// WithContext sets a context for cancellation support in table imports.
func (di *DataImporter) WithContext(ctx context.Context) *DataImporter {
	di.ctx = ctx
	return di
}

// ImportTable 导入单个表
func (di *DataImporter) ImportTable(tableName string) (*ImportResult, error, *ImportDiagnostic) {
	// 查找 CSV 文件
	csvPath, err, diag := di.FindCSVFile(tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to find CSV file for table %s: %w", tableName, err), diag
	}

	// 创建表导入器
	importer := di.newTableImporter(tableName, csvPath)

	// 执行导入
	return importer.Import()
}

func (di *DataImporter) newTableImporter(tableName string, csvPath string) *TableImporter {
	importer := NewTableImporter(di.conn, di.cfg, tableName, csvPath, di.errorRecorder)
	if di.progressCallback != nil {
		importer.WithProgressCallback(di.progressCallback)
	}
	if di.pressureCallback != nil {
		importer.WithPressureCallback(di.pressureCallback)
	}
	if di.ctx != nil {
		importer.WithContext(di.ctx)
	}
	return importer
}

// Close 关闭导入器
func (di *DataImporter) Close() error {
	if di.errorRecorder != nil {
		return di.errorRecorder.Close()
	}
	return nil
}

// GetErrorRecorder 获取错误记录器
func (di *DataImporter) GetErrorRecorder() *ErrorRecorder {
	return di.errorRecorder
}

func findMatchingCSVPath(csvDir, expectedFileName string, tableMatcher matcher.TableNameMatcher, triedPaths *[]string) string {
	expectedPath := filepath.Join(csvDir, expectedFileName)
	*triedPaths = append(*triedPaths, expectedPath)

	entries, err := os.ReadDir(csvDir)
	if err != nil {
		return ""
	}

	if tableMatcher.CaseSensitive() {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if name != expectedFileName {
				continue
			}
			path := filepath.Join(csvDir, name)
			if _, err := os.Stat(path); err == nil {
				return path
			}
			return ""
		}
		return ""
	}

	expectedKey := tableMatcher.Key(expectedFileName)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".csv") {
			continue
		}
		path := filepath.Join(csvDir, name)
		if tableMatcher.Key(name) == expectedKey {
			return path
		}
	}
	return ""
}

// FindCSVFile 查找表对应的 CSV 文件（精确匹配，无模糊匹配）
func (di *DataImporter) FindCSVFile(tableName string) (string, error, *ImportDiagnostic) {
	var triedPaths []string
	csvDir := di.cfg.Source.CSVDirectory
	tableMatcher := matcher.NewTableNameMatcher(di.cfg.Migration.IsTableNameCaseSensitive())

	var expectedFileNames []string

	if di.cfg.Source.CSVTimestamp != "" {
		ts := di.cfg.Source.CSVTimestamp
		expectedFileNames = append(expectedFileNames, fmt.Sprintf("%s_%s.csv", tableName, ts))
		// $ suffix: TABLE$ → TABLE__.csv
		if strings.HasSuffix(tableName, "$") {
			base := matcher.TableNameToCSVFileName(tableName) + "__"
			expectedFileNames = append(expectedFileNames, fmt.Sprintf("%s%s.csv", base, ts))
		}
	} else {
		expectedFileNames = append(expectedFileNames, tableName+".csv")
		// $ suffix: TABLE$ → TABLE__.csv
		if strings.HasSuffix(tableName, "$") {
			base := matcher.TableNameToCSVFileName(tableName) + "__"
			expectedFileNames = append(expectedFileNames, base+".csv")
		}
	}

	for _, expectedFileName := range expectedFileNames {
		if path := findMatchingCSVPath(csvDir, expectedFileName, tableMatcher, &triedPaths); path != "" {
			return path, nil, nil
		}
	}

	return "", fmt.Errorf("CSV file not found for table: %s", tableName), &ImportDiagnostic{
		TableName:  tableName,
		ErrorType:  ErrorTypeCSVNotFound,
		TriedPaths: triedPaths,
	}
}

// ImportTables 批量导入表
func (di *DataImporter) ImportTables(tableNames []string) ([]*ImportResult, error) {
	results := make([]*ImportResult, 0, len(tableNames))

	for _, tableName := range tableNames {
		result, err, _ := di.ImportTable(tableName)
		if err != nil {
			logger.Errorf("Failed to import table %s: %v", tableName, err)
			results = append(results, &ImportResult{
				TableName:    tableName,
				Success:      false,
				ErrorMessage: err.Error(),
			})
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

// PreprocessRow 预处理行数据
func PreprocessRow(row []string) []any {
	processed := make([]any, len(row))
	for i, value := range row {
		value = normalizeCSVString(value)
		// 空字符串转换为 NULL
		if strings.TrimSpace(value) == "" {
			processed[i] = nil
		} else {
			processed[i] = value
		}
	}
	return processed
}

func normalizeCSVString(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().String(value)
	if err == nil && utf8.ValidString(decoded) {
		return decoded
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

// PipelinedImporter 流水线优化的大规模导入器
type PipelinedImporter struct {
	conn          *database.Connection
	cfg           *config.Config
	errorRecorder *ErrorRecorder
	concurrency   int
}

// NewPipelinedImporter 创建流水线导入器
func NewPipelinedImporter(conn *database.Connection, cfg *config.Config, concurrency int) *PipelinedImporter {
	errorRecorder, _ := NewErrorRecorder(cfg.Logging.File)

	return &PipelinedImporter{
		conn:          conn,
		cfg:           cfg,
		errorRecorder: errorRecorder,
		concurrency:   concurrency,
	}
}

// ImportMultiple 并发导入多张表
func (pi *PipelinedImporter) ImportMultiple(tableNames []string) ([]*ImportResult, []string) {
	results := make([]*ImportResult, 0, len(tableNames))

	// 使用 worker pool 并发导入
	tableChan := make(chan string, len(tableNames))
	resultChan := make(chan *ImportResult, len(tableNames))

	// 启动 workers
	var wg sync.WaitGroup
	for i := 0; i < pi.concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			importer := NewDataImporter(pi.conn, pi.cfg)
			defer importer.Close()

			for tableName := range tableChan {
				logger.Debugf("[Worker %d] Processing table: %s", workerID, tableName)

				result, err, _ := importer.ImportTable(tableName)
				if err != nil {
					logger.Errorf("[Worker %d] Failed to import table %s: %v", workerID, tableName, err)
					result = &ImportResult{
						TableName:    tableName,
						Success:      false,
						ErrorMessage: err.Error(),
					}
				}
				resultChan <- result
			}
		}(i)
	}

	// 分发任务
	for _, tableName := range tableNames {
		tableChan <- tableName
	}
	close(tableChan)

	// 等待完成
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var failedTables []string
	for result := range resultChan {
		results = append(results, result)
		if result != nil && !result.Success {
			failedTables = append(failedTables, result.TableName)
		}
	}

	return results, failedTables
}

// GetErrorRecorder 获取错误记录器
func (pi *PipelinedImporter) GetErrorRecorder() *ErrorRecorder {
	return pi.errorRecorder
}
