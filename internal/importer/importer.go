package importer

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
)

const (
	maxRetries   = 3
	retryDelayMs = 100
	bufferSize   = 10 // 流水线缓冲区大小
)

// TableImporter 表数据导入器
type TableImporter struct {
	conn          *database.Connection
	cfg           *config.Config
	tableName     string
	csvPath       string
	errorRecorder *ErrorRecorder
}

// NewTableImporter 创建表导入器
func NewTableImporter(conn *database.Connection, cfg *config.Config, tableName string, csvPath string, errorRecorder *ErrorRecorder) *TableImporter {
	return &TableImporter{
		conn:          conn,
		cfg:           cfg,
		tableName:     tableName,
		csvPath:       csvPath,
		errorRecorder: errorRecorder,
	}
}

// Import 导入表数据（流水线优化：边读边写）
func (ti *TableImporter) Import() (*ImportResult, error, *ImportDiagnostic) {
	logger.Infof("Starting import for table: %s", ti.tableName)

	// 检查表是否存在
	exists, err := ti.conn.TableExists(ti.tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to check table existence: %w", err), nil
	}

	if !exists {
		return nil, fmt.Errorf("table does not exist: %s", ti.tableName), nil
	}

	// 如果配置要求，先清空表
	if ti.cfg.Migration.TruncateBeforeImport {
		logger.Infof("Truncating table: %s", ti.tableName)
		if err := ti.conn.TruncateTable(ti.tableName); err != nil {
			return nil, fmt.Errorf("failed to truncate table: %w", err), nil
		}
	}

	// 打开 CSV 文件
	file, err := os.Open(ti.csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err), nil
	}

	// 获取正确大小写的表名（解决 MySQL 大小写不敏感问题）
	actualTableName := ti.conn.GetActualTableName(ti.tableName)

	// 使用流水线导入
	result, err, diag := ti.pipelinedImport(file, actualTableName)
	if err != nil {
		ti.errorRecorder.RecordError(ti.tableName, "", nil, err)
		return nil, err, diag
	}

	return result, nil, nil
}

// getDBColumns 获取数据库中表的列
func (ti *TableImporter) getDBColumns(tableName string) ([]string, error) {
	// 使用 DESCRIBE 获取列信息
	query := fmt.Sprintf("DESCRIBE `%s`", tableName)
	rows, err := ti.conn.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var field, colType, null, key, extra string
		var defaultVal *string
		if err := rows.Scan(&field, &colType, &null, &key, &defaultVal, &extra); err != nil {
			return nil, err
		}
		columns = append(columns, field)
	}
	return columns, rows.Err()
}

// buildColumnMapping 构建 CSV 列索引到有效列索引的映射
// csvColIdx: CSV 列索引 -> -1 表示跳过该列
func buildColumnMapping(csvHeaders []string, dbColumns []string) []int {
	// 构建 DB 列映射（大写 -> 索引）
	dbColMap := make(map[string]int)
	for i, col := range dbColumns {
		dbColMap[strings.ToUpper(col)] = i
	}

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

// filterRowData 根据映射过滤行数据，只保留有效的列
func filterRowData(row []string, mapping []int) []interface{} {
	result := make([]interface{}, 0, len(mapping))
	for i, val := range row {
		if mapping[i] >= 0 {
			result = append(result, val)
		}
	}
	return result
}

// filterRowDataByInterface 过滤已预处理的数据（interface{} 数组）
func filterRowDataByInterface(row []interface{}, mapping []int) []interface{} {
	result := make([]interface{}, 0, len(mapping))
	for i, val := range row {
		if mapping[i] >= 0 {
			result = append(result, val)
		}
	}
	return result
}

// pipelinedImport 流水线导入：边读边写
func (ti *TableImporter) pipelinedImport(file *os.File, actualTableName string) (*ImportResult, error, *ImportDiagnostic) {
	reader := csv.NewReader(file)
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	// 读取表头
	headers, err := reader.Read()
	if err != nil {
		file.Close()
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
		headers[i] = strings.TrimPrefix(headers[i], "\ufeff")
	}

	// 获取数据库中实际的列
	dbColumns, err := ti.getDBColumns(actualTableName)
	if err != nil {
		logger.Warnf("Failed to get DB columns for %s: %v", actualTableName, err)
		// 回退到使用 CSV 列
		dbColumns = headers
	}

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
	defer inserter.Close()

	// 记录跳过的列
	if len(skippedCols) > 0 {
		logger.Warnf("Table %s: skipped %d columns not in DB (%s)",
			ti.tableName, len(skippedCols), strings.Join(skippedCols, ", "))
	}

	// 构建 CSV 列索引到有效列的映射（用于筛选数据）
	mapping := buildColumnMapping(headers, dbColumns)

	// 建立流水线：CSV读取 -> 预处理 -> 数据库插入
	type batchData struct {
		rows     [][]interface{}
		batchNum int
		err      error
	}

	csvDone := make(chan struct{})
	batchChan := make(chan batchData, bufferSize)

	var totalRows int64
	var processedRows int64
	var errorCount int64
	var batchNum int
	var wg sync.WaitGroup

	// 启动 CSV 读取 goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(batchChan)

		for {
			// 读取一批数据
			var batch [][]string
			for i := 0; i < ti.cfg.Migration.BatchSize; i++ {
				row, err := reader.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					logger.Warnf("Failed to read row in %s: %v", ti.tableName, err)
					continue
				}
				batch = append(batch, row)
			}

			if len(batch) == 0 {
				return
			}

			// 预处理数据（转换类型，并过滤掉无效列）
			processedBatch := make([][]interface{}, len(batch))
			for i, row := range batch {
				// 先类型转换，再过滤
				processedRow := PreprocessRow(row)
				filteredRow := filterRowDataByInterface(processedRow, mapping)
				processedBatch[i] = filteredRow
			}

			batchNum++
			select {
			case batchChan <- batchData{rows: processedBatch, batchNum: batchNum}:
			case <-csvDone:
				return
			}
		}
	}()

	// 启动数据库写入 goroutine
	var lastErr error
	wg.Add(1)
	go func() {
		defer wg.Done()

		for bd := range batchChan {
			if bd.err != nil {
				errorCount++
				lastErr = bd.err
				continue
			}

			// 重试机制
			var affected int64
			var insertErr error
			for retry := 0; retry < maxRetries; retry++ {
				affected, insertErr = inserter.InsertBatch(bd.rows)
				if insertErr == nil {
					break
				}
				logger.Warnf("Retry %d/%d for batch %d in table %s: %v", retry+1, maxRetries, bd.batchNum, ti.tableName, insertErr)
				time.Sleep(time.Duration(retry+1) * retryDelayMs * time.Millisecond)
			}

			if insertErr != nil {
				errorCount++
				lastErr = insertErr
				ti.errorRecorder.RecordBatchError(ti.tableName, bd.batchNum, bd.rows, insertErr)
				logger.Errorf("Failed to insert batch %d for table %s after %d retries: %v", bd.batchNum, ti.tableName, maxRetries, insertErr)
			} else {
				processedRows += int64(len(bd.rows))
				totalRows += affected
			}

			// 记录进度
			if processedRows%50000 == 0 && processedRows > 0 {
				logger.Infof("Progress: %d rows processed, %d rows inserted", processedRows, totalRows)
			}
		}
	}()

	wg.Wait()
	file.Close()

	if lastErr != nil && processedRows == 0 {
		return &ImportResult{
			TableName:     ti.tableName,
			ProcessedRows: 0,
			InsertedRows:  0,
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
		ErrorCount:    errorCount,
		Success:       errorCount == 0,
	}, nil, nil
}

// ImportResult 导入结果
type ImportResult struct {
	TableName     string
	ProcessedRows int64
	InsertedRows  int64
	ErrorCount    int64
	Success       bool
	ErrorMessage  string
}

const (
	MaxDiagnosticColumns = 10
	ErrorTypeEOF         = "EOF"
	ErrorTypeNoMatch     = "NO_MATCH"
)

// ImportDiagnostic carries structured diagnostic info for failed imports.
// Returned alongside error so the caller can log it before continuing.
type ImportDiagnostic struct {
	TableName   string
	ErrorType   string  // ErrorTypeEOF or ErrorTypeNoMatch
	ErrorDetail string  // e.g. "failed to read CSV header (EOF)"
	CSVPath     string
	CSVColumns  []string
	DBColumns   []string
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
func NewErrorRecorder(logDir string) (*ErrorRecorder, error) {
	recorder := &ErrorRecorder{enabled: true}

	if logDir != "" {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			logger.Warnf("Failed to create error log dir: %v", err)
		} else {
			f, err := os.OpenFile(
				filepath.Join(logDir, "migration.log"),
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
		fmt.Fprintf(er.file, "[%s] Table: %s, Error: %s\n", record.Timestamp.Format(time.RFC3339), tableName, err)
		if sql != "" {
			fmt.Fprintf(er.file, "  SQL: %s\n", sql)
		}
		if len(rowData) > 0 {
			fmt.Fprintf(er.file, "  Row data (first 3): %v\n", record.RowData)
		}
	}
}

// RecordBatchError 记录批次错误
func (er *ErrorRecorder) RecordBatchError(tableName string, batchNum int, rows [][]interface{}, err error) {
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
		fmt.Fprintf(er.file, "[%s] Table: %s, Batch: %d, Error: %s\n", record.Timestamp.Format(time.RFC3339), tableName, batchNum, err)
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
	conn          *database.Connection
	cfg           *config.Config
	errorRecorder *ErrorRecorder
}

// NewDataImporter 创建数据导入协调器
func NewDataImporter(conn *database.Connection, cfg *config.Config) *DataImporter {
	errorRecorder, err := NewErrorRecorder(filepath.Dir(cfg.Logging.File))
	if err != nil {
		logger.Warnf("Failed to create error recorder: %v", err)
	}

	return &DataImporter{
		conn:          conn,
		cfg:           cfg,
		errorRecorder: errorRecorder,
	}
}

// ImportTable 导入单个表
func (di *DataImporter) ImportTable(tableName string) (*ImportResult, error, *ImportDiagnostic) {
	// 查找 CSV 文件
	csvPath, err := di.findCSVFile(tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to find CSV file for table %s: %w", tableName, err), nil
	}

	// 创建表导入器
	importer := NewTableImporter(di.conn, di.cfg, tableName, csvPath, di.errorRecorder)

	// 执行导入
	return importer.Import()
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

// findCSVFile 查找表对应的 CSV 文件
func (di *DataImporter) findCSVFile(tableName string) (string, error) {
	timestamp := di.cfg.Source.CSVTimestamp

	// 优先使用精确时间戳匹配
	if timestamp != "" {
		patterns := []string{
			filepath.Join(di.cfg.Source.CSVDirectory, fmt.Sprintf("%s_%s.csv", tableName, timestamp)),
			filepath.Join(di.cfg.Source.CSVDirectory, fmt.Sprintf("%s_%s.csv", strings.ToLower(tableName), timestamp)),
			filepath.Join(di.cfg.Source.CSVDirectory, fmt.Sprintf("%s_%s.csv", strings.ToUpper(tableName), timestamp)),
		}
		for _, pattern := range patterns {
			if _, err := os.Stat(pattern); err == nil {
				return pattern, nil
			}
		}
	}

	// 回退到 glob 模糊匹配
	patterns := []string{
		filepath.Join(di.cfg.Source.CSVDirectory, fmt.Sprintf("%s_*.csv", tableName)),
		filepath.Join(di.cfg.Source.CSVDirectory, fmt.Sprintf("%s_*.csv", strings.ToLower(tableName))),
		filepath.Join(di.cfg.Source.CSVDirectory, fmt.Sprintf("%s_*.csv", strings.ToUpper(tableName))),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}

	// 处理 $ 表名对应 __ CSV 文件名
	if strings.HasSuffix(tableName, "$") {
		csvTableName := strings.TrimSuffix(tableName, "$") + "__"
		subPattern := filepath.Join(di.cfg.Source.CSVDirectory, csvTableName+"*.csv")
		matches, err := filepath.Glob(subPattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
		subPattern = filepath.Join(di.cfg.Source.CSVDirectory, strings.ToLower(csvTableName)+"*.csv")
		matches, err = filepath.Glob(subPattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}

	return "", fmt.Errorf("CSV file not found for table: %s", tableName)
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
func PreprocessRow(row []string) []interface{} {
	processed := make([]interface{}, len(row))
	for i, value := range row {
		// 空字符串转换为 NULL
		if strings.TrimSpace(value) == "" {
			processed[i] = nil
		} else {
			processed[i] = value
		}
	}
	return processed
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
	errorRecorder, _ := NewErrorRecorder(filepath.Dir(cfg.Logging.File))

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
	var failedTables []string

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
					failedTables = append(failedTables, tableName)
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

	// 收集结果
	for result := range resultChan {
		results = append(results, result)
	}

	return results, failedTables
}

// GetErrorRecorder 获取错误记录器
func (pi *PipelinedImporter) GetErrorRecorder() *ErrorRecorder {
	return pi.errorRecorder
}
