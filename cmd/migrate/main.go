package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/converter"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
)

var (
	configPath = flag.String("config", "configs/config.yaml", "配置文件路径")
	resume     = flag.Bool("resume", false, "断点续传模式")
	tables     = flag.String("tables", "", "仅导入指定表（逗号分隔）")
	createOnly = flag.Bool("create-tables-only", false, "仅创建缺失表，不导入数据")
	version    = flag.Bool("version", false, "显示版本信息")
)

const (
	Version = "1.0.0"
)

func logImportDiagnostic(diag *importer.ImportDiagnostic) {
	if diag == nil {
		return
	}
	if diag.ErrorType == importer.ErrorTypeEOF {
		logger.Warnf("CSV import diagnostic: table=%s\n  Error: %s\n  CSV file: %s\n  Action: skipped — no data to import",
			diag.TableName, diag.ErrorDetail, diag.CSVPath)
	} else if diag.ErrorType == importer.ErrorTypeNoMatch {
		logger.Warnf("CSV import diagnostic: table=%s\n  Error: %s\n  CSV file: %s\n  CSV columns (sample, max %d): %s\n  DB columns (sample, max %d): %s\n  Action: skipped — column names do not match DB schema",
			diag.TableName, diag.ErrorDetail, diag.CSVPath,
			importer.MaxDiagnosticColumns, strings.Join(diag.CSVColumns, ","),
			importer.MaxDiagnosticColumns, strings.Join(diag.DBColumns, ","))
	} else if diag.ErrorType == importer.ErrorTypeCSVNotFound {
		logger.Warnf("CSV import diagnostic: table=%s\n  Error: CSV file not found for table %s\n  CSV file: (none)\n  Tried paths (in order):\n    %s\n  Action: skipped — CSV file does not exist",
			diag.TableName, diag.TableName,
			strings.Join(diag.TriedPaths, "\n    "))
	} else {
		logger.Warnf("CSV import diagnostic: table=%s\n  Error: %s\n  CSV file: %s\n  Action: unknown error type — skipped",
			diag.TableName, diag.ErrorType, diag.CSVPath)
	}
}

func main() {
	flag.Parse()

	// 显示版本信息
	if *version {
		fmt.Printf("db-migration version %s\n", Version)
		os.Exit(0)
	}

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// 初始化日志
	if err := logger.Init(
		cfg.Logging.Level,
		cfg.Logging.File,
		cfg.Logging.Console,
		cfg.Logging.MaxSize,
		cfg.Logging.MaxBackups,
		cfg.Logging.MaxAge,
	); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("=== Database Migration Tool Started ===")
	logger.Infof("Version: %s", Version)
	logger.Infof("Config: %s", *configPath)

	// 连接数据库
	conn, err := database.RetryConnect(&cfg.Target, 3)
	if err != nil {
		logger.Fatalf("Failed to connect to database: %v", err)
	}
	defer conn.Close()

	// 初始化进度跟踪器
	tracker, err := progress.NewTracker(cfg.Migration.StateDir)
	if err != nil {
		logger.Fatalf("Failed to initialize progress tracker: %v", err)
	}
	defer tracker.Close()

	// 启动进度报告器
	tracker.StartProgressReporter(10 * time.Second)

	// 执行迁移
	if err := runMigration(cfg, conn, tracker); err != nil {
		logger.Fatalf("Migration failed: %v", err)
	}

	// 打印摘要
	tracker.PrintSummary()

	logger.Info("=== Database Migration Tool Finished ===")
}

// runMigration 执行迁移（新流程）
func runMigration(cfg *config.Config, conn *database.Connection, tracker *progress.Tracker) error {
	// ========== 步骤 1: 表分类 ==========
	// 解析 DDL 文件
	ddlParser := parser.NewDDLParser(cfg.Source.DDLFile)
	allDDLs, err := ddlParser.ParseAll()
	if err != nil {
		return fmt.Errorf("failed to parse DDL file: %w", err)
	}
	logger.Infof("Parsed %d DDL definitions from %s", len(allDDLs), cfg.Source.DDLFile)

	// 从 DDL 获取所有表名列表
	var allTableNames []string
	for tableName := range allDDLs {
		allTableNames = append(allTableNames, tableName)
	}
	logger.Infof("Total tables from DDL: %d", len(allTableNames))

	// 如果指定了表列表，过滤
	if *tables != "" {
		specifiedTables := strings.Split(*tables, ",")
		allTableNames = filterTables(allTableNames, specifiedTables)
		logger.Infof("Filtered to %d specified tables", len(allTableNames))
	}

	// 检查断点续传
	if *resume {
		completedTables, err := tracker.GetCompletedTables()
		if err != nil {
			return fmt.Errorf("failed to get completed tables: %w", err)
		}
		logger.Infof("Resume mode: skipping %d completed tables", len(completedTables))
		allTableNames = excludeTables(allTableNames, completedTables)
	}

	// 分类表（已存在 vs 缺失）
	inspector := database.NewInspector(conn)
	classification, err := inspector.ClassifyTables(allTableNames)
	if err != nil {
		return fmt.Errorf("failed to classify tables: %w", err)
	}

	logger.Infof("Table classification: %d existing, %d missing",
		len(classification.ExistingTables), len(classification.MissingTables))

	// ========== 步骤 2: DDL 转换 + 表创建 ==========
	// 注意：classification.MissingTables 是基于 DDL 全量表的缺失部分
	// 这里会创建所有 DDL 中有但数据库中不存在的表
	if cfg.Migration.CreateMissingTables && len(classification.MissingTables) > 0 {
		if err := createAndTrackTables(conn, classification.MissingTables, allDDLs, tracker); err != nil {
			logger.Errorf("Some tables failed to create: %v", err)
			// 继续执行，允许部分表创建失败
		}
	}

	// 如果仅创建表，则退出
	if *createOnly {
		logger.Info("Create tables only mode: skipping data import")
		return nil
	}

	// ========== 步骤 2.5: TRUNCATE 所有已存在表 ==========
	// 注意：这里 TRUNCATE 的是 classification.ExistingTables（基于 DDL 全量表）
	// 但 classification.ExistingTables 只包含 DDL 中存在的表，不是全部已存在表
	if len(classification.ExistingTables) > 0 {
		if err := truncateExistingTables(conn, classification.ExistingTables, tracker); err != nil {
			logger.Warnf("Some tables failed to truncate: %v", err)
			// 继续执行，不阻断
		}
	}

	// ========== 步骤 3: 数据导入 ==========
	// 扫描 CSV 文件用于数据导入
	csvFiles, err := scanCSVFiles(cfg.Source.CSVDirectory)
	if err != nil {
		return fmt.Errorf("failed to scan CSV files: %w", err)
	}
	logger.Infof("Found %d CSV files", len(csvFiles))

	// 导入数据（仅处理有 CSV 文件的表）
	if err := importDataWithCSVMapping(cfg, conn, csvFiles, allTableNames, tracker); err != nil {
		return fmt.Errorf("failed to import data: %w", err)
	}

	return nil
}

// scanCSVFiles 扫描 CSV 文件
func scanCSVFiles(directory string) ([]string, error) {
	pattern := filepath.Join(directory, "*.csv")
	return filepath.Glob(pattern)
}

// extractTableNames 从 CSV 文件名提取表名
func extractTableNames(csvFiles []string, timestamp string) []string {
	tableMap := make(map[string]bool)

	for _, csvFile := range csvFiles {
		baseName := filepath.Base(csvFile)
		// 移除 .csv 后缀
		baseName = strings.TrimSuffix(baseName, ".csv")
		// 检测双下划线模式：文件名格式为 sample_main_101__202602261058
		// 双下划线在时间戳之前，需要在去除时间戳之前检测
		hasDoubleUnderscore := strings.Contains(baseName, "__")
		// 移除时间戳后缀
		if timestamp != "" {
			baseName = strings.TrimSuffix(baseName, "_"+timestamp)
		}
		// 转换为大写（统一处理）
		tableName := strings.ToUpper(baseName)
		// 去除尾部下划线
		tableName = strings.TrimSuffix(tableName, "_")
		// 关键修复：如果原始文件名包含双下划线（sample_main_101__），
		// 说明对应 DDL 中的带 $ 表名（sample_main_101$）
		if hasDoubleUnderscore {
			tableName = tableName + "$"
		}
		tableMap[tableName] = true
	}

	var tableNames []string
	for tableName := range tableMap {
		tableNames = append(tableNames, tableName)
	}

	return tableNames
}

// filterTables 过滤表列表
func filterTables(allTables []string, specifiedTables []string) []string {
	specifiedMap := make(map[string]bool)
	for _, table := range specifiedTables {
		specifiedMap[strings.ToUpper(strings.TrimSpace(table))] = true
	}

	var filtered []string
	for _, table := range allTables {
		if specifiedMap[strings.ToUpper(table)] {
			filtered = append(filtered, table)
		}
	}

	return filtered
}

// excludeTables 排除表列表
func excludeTables(allTables []string, excludedTables []string) []string {
	excludedMap := make(map[string]bool)
	for _, table := range excludedTables {
		excludedMap[strings.ToUpper(table)] = true
	}

	var filtered []string
	for _, table := range allTables {
		if !excludedMap[strings.ToUpper(table)] {
			filtered = append(filtered, table)
		}
	}

	return filtered
}

// truncateExistingTables 清空所有已存在的表
func truncateExistingTables(conn *database.Connection, existingTables []string, tracker *progress.Tracker) error {
	logger.Infof("Truncating %d existing tables...", len(existingTables))

	progressInfo := tracker.GetProgress()
	tracker.SetSessionStartCount(len(existingTables))
	tracker.SetSessionStartCompleted(progressInfo.CompletedCount)

	successCount := 0
	failCount := 0

	for i, tableName := range existingTables {
		if err := conn.TruncateTable(tableName); err != nil {
			logger.Warnf("Failed to truncate table %s: %v", tableName, err)
			failCount++
		} else {
			successCount++
		}

		// 每 100 张表输出一次进度
		if (i+1)%100 == 0 {
			logger.Infof("Truncate progress: %d/%d (success: %d, failed: %d)",
				i+1, len(existingTables), successCount, failCount)
		}
	}

	logger.Infof("Truncate completed: %d success, %d failed", successCount, failCount)

	if failCount > 0 {
		return fmt.Errorf("%d tables failed to truncate", failCount)
	}
	return nil
}

// createAndTrackTables 创建缺失的表并跟踪结果
func createAndTrackTables(conn *database.Connection, missingTables []string, allDDLs map[string]*parser.TableDDL, tracker *progress.Tracker) error {
	logger.Infof("Creating %d missing tables...", len(missingTables))

	progressInfo := tracker.GetProgress()
	tracker.SetSessionStartCount(len(missingTables))
	tracker.SetSessionStartCompleted(progressInfo.CompletedCount)
	logger.Infof("Table creation progress tracking: %d tables to process, %d already completed",
		len(missingTables), progressInfo.CompletedCount)

	// 创建转换器
	tableConverter := converter.NewTableConverter()

	// 创建表
	successCount := 0
	failCount := 0
	var failedTableNames []string

	for _, tableName := range missingTables {
		upperTableName := strings.ToUpper(tableName)
		tableDDL, ok := allDDLs[upperTableName]

		// CSV 文件名尾部 _ 对应 DDL 表名尾部 $，做映射修复
		if !ok && strings.HasSuffix(upperTableName, "_") {
			mappedName := upperTableName[:len(upperTableName)-1] + "$"
			tableDDL, ok = allDDLs[mappedName]
			if ok {
				logger.Infof("Table name mapping applied: %s -> %s", tableName, mappedName)
			}
		}

		if !ok {
			attemptedNames := upperTableName
			if strings.HasSuffix(upperTableName, "_") {
				attemptedNames = fmt.Sprintf("%s, %s (with $ suffix)", upperTableName, upperTableName[:len(upperTableName)-1]+"$")
			}
			logger.Warnf("Table DDL not found: %s (attempted: %s)", tableName, attemptedNames)
			tracker.SkipTable(tableName, fmt.Sprintf("DDL not found (attempted: %s)", attemptedNames))
			failedTableNames = append(failedTableNames, tableName)
			failCount++
			continue
		}

		// 转换为 MySQL DDL
		mysqlDDL, err := tableConverter.ConvertToMySQL(tableDDL)
		if err != nil {
			logger.Errorf("Failed to convert DDL for table %s: %v", tableName, err)
			tracker.FailTable(tableName, fmt.Sprintf("DDL conversion failed: %v", err))
			failedTableNames = append(failedTableNames, tableName)
			failCount++
			continue
		}

		// 执行 CREATE TABLE
		if err := conn.ExecuteDDL(mysqlDDL); err != nil {
			logger.Errorf("Failed to create table %s: %v", tableName, err)
			tracker.FailTable(tableName, fmt.Sprintf("Table creation failed: %v", err))
			failedTableNames = append(failedTableNames, tableName)
			failCount++
			continue
		}

		logger.Infof("Table created: %s", tableName)
		tracker.CompleteTable(tableName, 0, 0, 0)
		successCount++
	}

	logger.Infof("Table creation completed: %d success, %d failed", successCount, failCount)

	// 生成表创建报告
	generateMigrationReport("Table Creation Report", nil, successCount, failCount, failedTableNames)

	// 刷新表名映射（让后续导入能识别新创建的表）
	if err := conn.RefreshTableNameMap(); err != nil {
		logger.Warnf("Failed to refresh table name map: %v", err)
	}

	if failCount > 0 {
		return fmt.Errorf("%d tables failed to create", failCount)
	}

	return nil
}

// importDataWithCSVMapping 导入数据（基于 CSV 文件映射）
func importDataWithCSVMapping(cfg *config.Config, conn *database.Connection, csvFiles []string, allowedTables []string, tracker *progress.Tracker) error {
	// 从 CSV 文件名提取表名 -> CSV 文件路径 的映射
	csvTableMap := buildCSVTableMap(csvFiles, cfg.Source.CSVTimestamp)

	// 构建允许表名的查找集合（O(1) 查找）
	allowedSet := make(map[string]struct{}, len(allowedTables))
	for _, t := range allowedTables {
		allowedSet[t] = struct{}{}
	}

	var tablesToImport []string
	for tableName := range csvTableMap {
		if allowedTables == nil {
			tablesToImport = append(tablesToImport, tableName)
		} else if _, ok := allowedSet[tableName]; ok {
			tablesToImport = append(tablesToImport, tableName)
		}
	}

	logger.Infof("Starting data import for %d tables (with CSV files)...", len(tablesToImport))

	// 设置进度跟踪
	progressInfo := tracker.GetProgress()
	tracker.SetSessionStartCount(len(tablesToImport))
	tracker.SetSessionStartCompleted(progressInfo.CompletedCount)

	// 创建数据导入器
	dataImporter := importer.NewDataImporter(conn, cfg)
	defer dataImporter.Close()

	// 使用 worker pool 并发导入
	var wg sync.WaitGroup
	tableChan := make(chan string, len(tablesToImport))
	resultChan := make(chan *importer.ImportResult, len(tablesToImport))

	// 启动 workers
	for i := 0; i < cfg.Migration.MaxWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for tableName := range tableChan {
				logger.Infof("[Worker %d] Processing table: %s", workerID, tableName)

				// 查找 CSV 文件路径
				csvPath, ok := csvTableMap[tableName]
				if !ok {
					// Call FindCSVFile directly to get the diagnostic with TriedPaths
					_, _, diag := dataImporter.FindCSVFile(tableName)
					logImportDiagnostic(diag)
					continue
				}

				// 开始跟踪
				tracker.StartTable(tableName, csvPath, false)

				// 导入数据
				result, err, diag := dataImporter.ImportTable(tableName)
				if err != nil {
					logger.Errorf("[Worker %d] Failed to import table %s: %v", workerID, tableName, err)
					logImportDiagnostic(diag)
					tracker.FailTable(tableName, err.Error())
					resultChan <- &importer.ImportResult{
						TableName:    tableName,
						Success:      false,
						ErrorMessage: err.Error(),
					}
					continue
				}

				if result.Success {
					logger.Infof("[Worker %d] Table imported: %s (%d rows)", workerID, tableName, result.InsertedRows)
					tracker.CompleteTable(tableName, result.InsertedRows, 0, 0)
					resultChan <- result
				} else {
					logger.Warnf("[Worker %d] Table partially imported: %s (%d rows, %d errors)",
						workerID, tableName, result.InsertedRows, result.ErrorCount)
					tracker.CompleteTable(tableName, result.InsertedRows, 0, 0)
					resultChan <- result
				}
			}
		}(i)
	}

	// 分发任务
	for _, tableName := range tablesToImport {
		tableChan <- tableName
	}
	close(tableChan)

	// 等待所有 worker 完成
	wg.Wait()
	close(resultChan)

	// 收集结果
	successCount := 0
	failCount := 0
	var failedTables []string
	var totalRows int64
	var totalBytes int64

	for result := range resultChan {
		if result.Success && result.ErrorCount == 0 {
			successCount++
		} else {
			failCount++
			failedTables = append(failedTables, result.TableName)
		}
		totalRows += result.InsertedRows
		totalBytes += result.ProcessedRows
	}

	// 打印详细错误汇总
	printErrorSummary(dataImporter.GetErrorRecorder(), failedTables)

	logger.Infof("Data import completed: %d success, %d failed, %d total rows, %d bytes",
		successCount, failCount, totalRows, totalBytes)

	// 生成数据导入报告
	generateMigrationReport("Data Import Report", nil, successCount, failCount, failedTables)

	if failCount > 0 {
		return fmt.Errorf("%d tables failed to import", failCount)
	}
	return nil
}

// buildCSVTableMap 从 CSV 文件列表构建表名 -> 文件路径映射
func buildCSVTableMap(csvFiles []string, timestamp string) map[string]string {
	tableMap := make(map[string]string)

	for _, csvPath := range csvFiles {
		// 从文件路径提取表名
		// 格式: origin_data_csvfiles/{TABLE_NAME}_{TIMESTAMP}.csv
		fileName := filepath.Base(csvPath)
		tableName := extractTableNameFromFile(fileName, timestamp)
		if tableName != "" {
			tableMap[tableName] = csvPath
		}
	}

	return tableMap
}

// extractTableNameFromFile 从 CSV 文件名提取表名
func extractTableNameFromFile(fileName, timestamp string) string {
	// 移除 .csv 后缀
	name := strings.TrimSuffix(fileName, ".csv")

	// 如果有时间戳，移除时间戳部分
	if timestamp != "" {
		suffix := "_" + timestamp
		if strings.HasSuffix(name, suffix) {
			name = strings.TrimSuffix(name, suffix)
		}
	}

	return name
}

// generateMigrationReport 生成迁移报告
func generateMigrationReport(title string, classification *database.TableClassification, successCount, failCount int, failedTables []string) {
	logger.Info("=== " + title + " ===")
	if classification != nil {
		logger.Infof("Total tables: %d", len(classification.ExistingTables)+len(classification.MissingTables))
		logger.Infof("Existing tables (truncated + imported): %d", len(classification.ExistingTables))
		logger.Infof("New tables (directly imported): %d", len(classification.MissingTables))
	}
	logger.Infof("Success: %d, Failed: %d", successCount, failCount)
	if failCount > 0 && len(failedTables) > 0 {
		logger.Warn("Failed tables:")
		for _, table := range failedTables {
			logger.Warnf("  - %s", table)
		}
	}
}

// printErrorSummary 打印错误汇总
func printErrorSummary(recorder *importer.ErrorRecorder, failedTables []string) {
	if recorder == nil {
		return
	}

	errors := recorder.GetErrors()
	if len(errors) == 0 {
		return
	}

	logger.Warn("=== Error Summary ===")
	logger.Warnf("Total errors recorded: %d", len(errors))

	if len(failedTables) > 0 {
		logger.Warn("Failed tables:")
		for _, table := range failedTables {
			logger.Warnf("  - %s", table)
		}
	}

	// 按错误类型分组统计
	errorTypes := make(map[string]int)
	for _, e := range errors {
		// 简化错误信息取第一个关键词
		errMsg := e.Error
		if len(errMsg) > 50 {
			errMsg = errMsg[:50] + "..."
		}
		errorTypes[errMsg]++
	}

	logger.Warn("Error types breakdown:")
	for errType, count := range errorTypes {
		logger.Warnf("  - %s: %d occurrences", errType, count)
	}

	logger.Warn("See migration.log for detailed error information")
}

// findCSVFile 查找 CSV 文件
func findCSVFile(directory, tableName, timestamp string) (string, error) {
	// 优先使用精确时间戳匹配，避免匹配到同名不同后缀的表
	// 例如：FORM_TRIGGER_RECORD 和 FORM_TRIGGER_RECORD_0034 是不同的表
	if timestamp != "" {
		patterns := []string{
			filepath.Join(directory, fmt.Sprintf("%s_%s.csv", tableName, timestamp)),
			filepath.Join(directory, fmt.Sprintf("%s_%s.csv", strings.ToLower(tableName), timestamp)),
			filepath.Join(directory, fmt.Sprintf("%s_%s.csv", strings.ToUpper(tableName), timestamp)),
		}
		for _, pattern := range patterns {
			if _, err := os.Stat(pattern); err == nil {
				return pattern, nil
			}
		}
	}

	// 回退到 glob 模糊匹配（仅当精确匹配失败时）
	patterns := []string{
		filepath.Join(directory, fmt.Sprintf("%s_*.csv", tableName)),
		filepath.Join(directory, fmt.Sprintf("%s_*.csv", strings.ToLower(tableName))),
		filepath.Join(directory, fmt.Sprintf("%s_*.csv", strings.ToUpper(tableName))),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			// 返回第一个匹配结果
			return matches[0], nil
		}
	}

	// 处理 DDL 表名尾部 $ 对应 CSV 文件名尾部 __
	if strings.HasSuffix(tableName, "$") {
		csvTableName := strings.TrimSuffix(tableName, "$") + "__"
		// 直接使用 csvTableName 作为前缀，不额外添加下划线
		subPattern := filepath.Join(directory, csvTableName+"*.csv")
		matches, err := filepath.Glob(subPattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
		// 也尝试小写版本
		subPattern = filepath.Join(directory, strings.ToLower(csvTableName)+"*.csv")
		matches, err = filepath.Glob(subPattern)
		if err == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}

	return "", fmt.Errorf("CSV file not found for table: %s", tableName)
}
