package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/converter"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
)

var (
	configPath    = flag.String("config", "config.yaml", "配置文件路径")
	tables        = flag.String("tables", "", "仅导入指定表（逗号分隔）")
	createOnly    = flag.Bool("create-tables-only", false, "仅创建缺失表，不导入数据")
	version       = flag.Bool("version", false, "显示版本信息")
	removePostfix = flag.String("remove-postfix", "", "移除 CSV 文件名的指定后缀")
	dryRun        = flag.Bool("dry-run", false, "预览模式，不实际执行")
	targetDir     = flag.String("target", "", "目标目录路径")
)

const (
	Version = "1.0.0"

	rowSizeFailedTablesFile = "row_size_failed_tables.txt"
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
		logger.Warnf("CSV import diagnostic: table=%s\n  Error: CSV file not found\n  CSV file: (none)\n  Tried paths (in order):\n    %s\n  Action: skipped — CSV file does not exist",
			diag.TableName,
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

	// 检测是否启用后缀移除模式
	if *removePostfix != "" {
		if *targetDir == "" {
			fmt.Fprintln(os.Stderr, "Error: --target is required when using --remove-postfix")
			os.Exit(1)
		}
		if err := logger.Init("INFO", "", true, 0, 0, 0); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
			os.Exit(1)
		}
		if err := renameCSVFiles(*removePostfix, *targetDir, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
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

	tableMatcher := matcher.NewTableNameMatcher(cfg.Migration.IsTableNameCaseSensitive())
	logger.Infof("Table name case sensitive: %t", tableMatcher.CaseSensitive())

	// 连接数据库
	conn, err := database.RetryConnectWithMatcher(&cfg.Target, 3, tableMatcher)
	if err != nil {
		logger.Fatalf("Failed to connect to database: %v", err)
	}
	defer conn.Close()

	// 初始化进度跟踪器
	tracker, err := progress.NewTracker()
	if err != nil {
		logger.Fatalf("Failed to initialize progress tracker: %v", err)
	}
	defer tracker.Close()

	// 启动进度报告器
	tracker.StartProgressReporter(10 * time.Second)

	if *dryRun {
		logger.Info("=== DRY RUN MODE: no actual changes will be made ===")
	}
	if err := runMigration(cfg, conn, tracker, tableMatcher, *dryRun); err != nil {
		logger.Fatalf("Migration failed: %v", err)
	}

	tracker.PrintSummary()

	logger.Info("=== Database Migration Tool Finished ===")
}

// runMigration executes the full migration pipeline.
func runMigration(cfg *config.Config, conn *database.Connection, tracker *progress.Tracker, tableMatcher matcher.TableNameMatcher, dryRun bool) error {
	// 创建迁移上下文
	migrationCtx := migration.NewMigrationContext()
	defer func() {
		if err := migrationCtx.Err(); err != nil {
			logger.Errorf("Migration failed: %v", err)
		}
	}()

	// ========== 步骤 1: 表分类 ==========
	// 解析 DDL 文件
	ddlParser := parser.NewDDLParser(cfg.Source.DDLFile)
	allDDLs, err := ddlParser.ParseAll()
	if err != nil {
		return fmt.Errorf("failed to parse DDL file: %w", err)
	}
	logger.Infof("Parsed %d DDL definitions from %s", len(allDDLs), cfg.Source.DDLFile)
	ddlLookup := buildDDLLookup(allDDLs, tableMatcher)

	// 从 DDL 获取所有表名列表
	allTableNames := collectDDLTableNames(allDDLs)
	logger.Infof("Total tables from DDL: %d", len(allTableNames))

	// 如果指定了表列表，过滤
	if *tables != "" {
		specifiedTables := strings.Split(*tables, ",")
		allTableNames = filterTables(allTableNames, specifiedTables, tableMatcher)
		logger.Infof("Filtered to %d specified tables", len(allTableNames))
	}

	if len(cfg.Migration.SkipTables) > 0 {
		before := len(allTableNames)
		allTableNames = excludeTables(allTableNames, cfg.Migration.SkipTables, tableMatcher)
		logger.Infof("Skipped %d tables per skip_tables config: %v", before-len(allTableNames), cfg.Migration.SkipTables)
	}

	tracker.SetPlannedTotalTables(len(allTableNames))
	logger.Infof("Overall migration target: %d tables", len(allTableNames))

	// 分类表（已存在 vs 缺失）
	inspector := database.NewInspector(conn, tableMatcher)
	classification, err := inspector.ClassifyTables(allTableNames)
	if err != nil {
		return fmt.Errorf("failed to classify tables: %w", err)
	}

	logger.Infof("Table classification: %d existing, %d missing",
		len(classification.ExistingTables), len(classification.MissingTables))

	// ========== 步骤 2: DDL 转换 + 表创建 ==========
	if dryRun {
		logger.Infof("[DRY RUN] Would create %d missing tables", len(classification.MissingTables))
		for _, tableName := range classification.MissingTables {
			logger.Infof("[DRY RUN]   - %s", tableName)
		}
	} else if len(classification.MissingTables) > 0 {
		rowSizeFailed, allFailed, err := createAndTrackTables(cfg, conn, classification.MissingTables, ddlLookup, tracker, migrationCtx, tableMatcher)
		if err != nil {
			logger.Errorf("Some tables failed to create: %v", err)
		}
		if len(allFailed) > 0 {
			if len(rowSizeFailed) > 0 {
				if writeErr := writeRowSizeFailedTables(rowSizeFailed); writeErr != nil {
					logger.Errorf("Failed to write row size failed tables file: %v", writeErr)
				}
			}
			allTableNames = excludeTables(allTableNames, allFailed, tableMatcher)
			logger.Warnf("Excluded %d failed tables from subsequent phases (row_size: %d, other: %d): %v",
				len(allFailed), len(rowSizeFailed), len(allFailed)-len(rowSizeFailed), allFailed)
		}
	}

	if *createOnly {
		if err := finalizeCreateOnlyProgress(tracker, classification.ExistingTables); err != nil {
			logger.Warnf("Failed to finalize create-only progress: %v", err)
		}
		logger.Info("Create tables only mode: skipping data import")
		return nil
	}

	// ========== 步骤 2.5: TRUNCATE 所有已存在表 ==========
	if dryRun {
		logger.Infof("[DRY RUN] Would truncate %d existing tables (skipped)", len(classification.ExistingTables))
	} else if len(classification.ExistingTables) > 0 {
		if err := truncateExistingTables(conn, classification.ExistingTables, tracker, migrationCtx, cfg); err != nil {
			logger.Errorf("Failed to truncate existing tables: %v", err)
			return err
		}
	}

	// ========== 步骤 3: 数据导入 ==========
	csvFiles, err := scanCSVFiles(cfg.Source.CSVDirectory)
	if err != nil {
		return fmt.Errorf("failed to scan CSV files: %w", err)
	}
	logger.Infof("Found %d CSV files", len(csvFiles))

	if dryRun {
		logger.Infof("[DRY RUN] Would import data from %d CSV files into %d tables (skipped)", len(csvFiles), len(allTableNames))
		if err := previewCSVImport(cfg, csvFiles, allTableNames, classification.ExistingTables, tableMatcher); err != nil {
			logger.Errorf("Preview analysis failed: %v", err)
		}
	} else {
		if err := importDataWithCSVMapping(cfg, conn, csvFiles, allTableNames, tracker, migrationCtx, tableMatcher); err != nil {
			return fmt.Errorf("failed to import data: %w", err)
		}
	}

	return nil
}

// scanCSVFiles 扫描 CSV 文件
func scanCSVFiles(directory string) ([]string, error) {
	pattern := filepath.Join(directory, "*.csv")
	return filepath.Glob(pattern)
}

// filterTables 过滤表列表
func filterTables(allTables []string, specifiedTables []string, tableMatcher matcher.TableNameMatcher) []string {
	specifiedSet := tableMatcher.BuildSet(specifiedTables)

	var filtered []string
	for _, table := range allTables {
		if _, ok := specifiedSet[tableMatcher.Key(table)]; ok {
			filtered = append(filtered, table)
		}
	}

	return filtered
}

func excludeTables(allTables []string, excludeList []string, tableMatcher matcher.TableNameMatcher) []string {
	excludeSet := tableMatcher.BuildSet(excludeList)

	var kept []string
	for _, table := range allTables {
		if _, ok := excludeSet[tableMatcher.Key(table)]; ok {
			continue
		}
		kept = append(kept, table)
	}

	return kept
}

func collectDDLTableNames(allDDLs map[string]*parser.TableDDL) []string {
	tableNames := make([]string, 0, len(allDDLs))
	for _, tableDDL := range allDDLs {
		if tableDDL == nil {
			continue
		}
		tableNames = append(tableNames, tableDDL.TableName)
	}
	sort.Strings(tableNames)
	return tableNames
}

func buildDDLLookup(allDDLs map[string]*parser.TableDDL, tableMatcher matcher.TableNameMatcher) map[string]*parser.TableDDL {
	lookup := make(map[string]*parser.TableDDL, len(allDDLs))
	tableDDLs := make([]*parser.TableDDL, 0, len(allDDLs))
	for _, tableDDL := range allDDLs {
		if tableDDL == nil {
			continue
		}
		tableDDLs = append(tableDDLs, tableDDL)
	}
	sort.Slice(tableDDLs, func(i, j int) bool {
		return tableDDLs[i].TableName < tableDDLs[j].TableName
	})

	for _, tableDDL := range tableDDLs {
		key := tableMatcher.Key(tableDDL.TableName)
		if existing, ok := lookup[key]; ok {
			logger.Warnf("DDL table name conflict under current case-sensitivity setting: %s and %s", existing.TableName, tableDDL.TableName)
			continue
		}
		lookup[key] = tableDDL
	}
	return lookup
}

// truncateExistingTables 清空所有已存在的表
func truncateExistingTables(conn *database.Connection, existingTables []string, tracker *progress.Tracker, migrationCtx *migration.MigrationContext, cfg *config.Config) error {
	logger.Infof("Truncating %d existing tables...", len(existingTables))

	tracker.StartPhase("truncate-existing-tables", len(existingTables))
	defer tracker.ClearPhase()

	tableChan := make(chan string, len(existingTables))

	var wg sync.WaitGroup
	var totalSuccess atomic.Int64
	var totalFail atomic.Int64

	// 启动 workers
	for i := range cfg.Migration.MaxWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for tableName := range tableChan {
				select {
				case <-migrationCtx.Context().Done():
					return
				default:
				}

				if err := conn.TruncateTable(tableName); err != nil {
					logger.Errorf("[Worker %d] Failed to truncate table %s: %v", workerID, tableName, err)
					tracker.FailPhaseItem()
					totalFail.Add(1)
					if cfg.Migration.FastFail != nil && *cfg.Migration.FastFail {
						migrationCtx.Stop(fmt.Errorf("failed to truncate table %s: %w", tableName, err))
						return
					}
				} else {
					logger.Infof("[Worker %d] Table truncated: %s", workerID, tableName)
					tracker.CompletePhaseItem()
					totalSuccess.Add(1)
				}
			}
		}(i)
	}

	// 分发任务
	for _, tableName := range existingTables {
		if migrationCtx.Context().Err() != nil {
			break
		}
		tableChan <- tableName
	}
	close(tableChan)

	wg.Wait()

	// context 停止且有错误时返回
	failCount := int(totalFail.Load())
	successCount := int(totalSuccess.Load())
	logger.Infof("Truncate completed: %d success, %d failed", successCount, failCount)

	return finalError(failCount, "tables failed to truncate", migrationCtx)
}

// createAndTrackTables 创建缺失的表并跟踪结果
func createAndTrackTables(cfg *config.Config, conn *database.Connection, missingTables []string, ddlLookup map[string]*parser.TableDDL, tracker *progress.Tracker, migrationCtx *migration.MigrationContext, tableMatcher matcher.TableNameMatcher) (rowSizeFailed []string, allFailed []string, err error) {
	logger.Infof("Creating %d missing tables...", len(missingTables))

	tracker.StartPhase("create-missing-tables", len(missingTables))
	defer tracker.ClearPhase()

	// 创建转换器
	tableConverter := converter.NewTableConverter(cfg.Converter)

	tableChan := make(chan string, len(missingTables))
	var totalSuccess atomic.Int64
	var totalFail atomic.Int64
	var mu sync.Mutex
	var failedTableNames []string
	var rowSizeFailedTables []string

	var wg sync.WaitGroup

	// 启动 workers
	for i := range cfg.Migration.MaxWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for tableName := range tableChan {
				select {
				case <-migrationCtx.Context().Done():
					return
				default:
				}

				// 查找 DDL
				lookupName := tableName
				tableDDL, ok := ddlLookup[tableMatcher.Key(lookupName)]

				// CSV 文件名尾部 _ 对应 DDL 表名尾部 $，做映射修复
				if !ok && strings.HasSuffix(lookupName, "_") {
					mappedName := lookupName[:len(lookupName)-1] + "$"
					tableDDL, ok = ddlLookup[tableMatcher.Key(mappedName)]
					if ok {
						logger.Infof("[Worker %d] Table name mapping applied: %s -> %s", workerID, tableName, mappedName)
					}
				}

				if !ok {
					attemptedNames := lookupName
					if strings.HasSuffix(lookupName, "_") {
						attemptedNames = fmt.Sprintf("%s, %s (with $ suffix)", lookupName, lookupName[:len(lookupName)-1]+"$")
					}
					logger.Warnf("[Worker %d] Table DDL not found: %s (attempted: %s)", workerID, tableName, attemptedNames)
					tracker.SkipTable(tableName, fmt.Sprintf("DDL not found (attempted: %s)", attemptedNames))
					tracker.SkipPhaseItem()

					mu.Lock()
					failedTableNames = append(failedTableNames, tableName)
					mu.Unlock()
					totalFail.Add(1)

					if cfg.Migration.FastFail != nil && *cfg.Migration.FastFail {
						migrationCtx.Stop(fmt.Errorf("table DDL not found: %s", tableName))
						return
					}
					continue
				}

				// 执行 CREATE TABLE
				logger.Infof("[Worker %d] Creating table: %s", workerID, tableName)
				if _, err := createTableDDLWithRetry(conn, tableConverter, tableDDL); err != nil {
					logger.Errorf("[Worker %d] Failed to create table %s: %v", workerID, tableName, err)
					tracker.FailTable(tableName, fmt.Sprintf("Table creation failed: %v", err))
					tracker.FailPhaseItem()

					mu.Lock()
					failedTableNames = append(failedTableNames, tableName)
					if isMySQLRowSizeTooLarge(err) {
						rowSizeFailedTables = append(rowSizeFailedTables, tableName)
					}
					mu.Unlock()
					totalFail.Add(1)

					// Error 1118 有兜底机制（自动写 TXT + 排除），豁免 fast_fail 以收集完整列表
					if isMySQLRowSizeTooLarge(err) {
						continue
					}
					if cfg.Migration.FastFail != nil && *cfg.Migration.FastFail {
						migrationCtx.Stop(fmt.Errorf("failed to create table %s: %w", tableName, err))
						return
					}
					continue
				}

				logger.Infof("[Worker %d] Table created: %s", workerID, tableName)
				if *createOnly {
					if err := tracker.CompleteTable(tableName, 0, 0, 0); err != nil {
						logger.Warnf("[Worker %d] Failed to mark table %s as completed: %v", workerID, tableName, err)
					}
				} else {
					if err := tracker.MarkTableCreated(tableName); err != nil {
						logger.Warnf("[Worker %d] Failed to mark table %s as created: %v", workerID, tableName, err)
					}
				}
				tracker.CompletePhaseItem()
				totalSuccess.Add(1)
			}
		}(i)
	}

	// 分发任务
	for _, tableName := range missingTables {
		if migrationCtx.Context().Err() != nil {
			break
		}
		tracker.StartTable(tableName, "", false)
		tableChan <- tableName
	}
	close(tableChan)

	wg.Wait()

	// context 停止且有错误时返回
	if err := migrationCtx.Err(); err != nil {
		return rowSizeFailedTables, failedTableNames, err
	}

	successCount := int(totalSuccess.Load())
	failCount := int(totalFail.Load())
	logger.Infof("Table creation completed: %d success, %d failed", successCount, failCount)

	// 生成表创建报告
	generateMigrationReport("Table Creation Report", nil, successCount, failCount, failedTableNames)

	// 刷新表名映射（让后续导入能识别新创建的表）
	if err := conn.RefreshTableNameMap(); err != nil {
		logger.Warnf("Failed to refresh table name map: %v", err)
	}

	if failCount > 0 {
		return rowSizeFailedTables, failedTableNames, fmt.Errorf("%d tables failed to create", failCount)
	}

	return rowSizeFailedTables, failedTableNames, nil
}

type ddlExecutor interface {
	ExecuteDDL(string) error
}

func createTableDDLWithRetry(executor ddlExecutor, tableConverter *converter.TableConverter, tableDDL *parser.TableDDL) (converter.ConvertResult, error) {
	result, err := tableConverter.ConvertToMySQLResult(tableDDL, converter.ConvertOptions{})
	if err != nil {
		return converter.ConvertResult{}, err
	}
	logColumnDegradations(result.Degradations)

	if err := executor.ExecuteDDL(result.SQL); err == nil {
		return result, nil
	} else if !isMySQLRowSizeTooLarge(err) {
		return result, err
	} else {
		firstErr := err
		retryResult, retryErr := tableConverter.ConvertToMySQLResult(tableDDL, converter.ConvertOptions{
			Mode:                    converter.ConvertModeAggressive,
			Reason:                  converter.DegradationReasonError1118,
			AllowNumericDegradation: true,
		})
		if retryErr != nil {
			return result, fmt.Errorf("normal create failed with row size error: %w; aggressive conversion failed: %w", firstErr, retryErr)
		}
		logColumnDegradations(retryResult.Degradations)
		if err := executor.ExecuteDDL(retryResult.SQL); err != nil {
			return retryResult, fmt.Errorf("normal create failed with row size error: %w; aggressive retry failed: %w", firstErr, err)
		}
		return retryResult, nil
	}
}

func isMySQLRowSizeTooLarge(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1118
}

func writeRowSizeFailedTables(tables []string) error {
	content := strings.Join(tables, ", ")
	return os.WriteFile(rowSizeFailedTablesFile, []byte(content), 0644)
}

func logColumnDegradations(degradations []converter.ColumnDegradation) {
	for _, degradation := range degradations {
		logger.Warnf(
			"Wide table degradation: table=%s reason=%s column=%s source=%s target=%s estimated_before=%d estimated_after=%d",
			degradation.TableName,
			degradation.Reason,
			degradation.ColumnName,
			degradation.SourceType,
			degradation.TargetType,
			degradation.EstimatedBytesBefore,
			degradation.EstimatedBytesAfter,
		)
	}
}

// importDataWithCSVMapping 导入数据（基于 CSV 文件映射）
func importDataWithCSVMapping(cfg *config.Config, conn *database.Connection, csvFiles []string, allowedTables []string, tracker *progress.Tracker, migrationCtx *migration.MigrationContext, tableMatcher matcher.TableNameMatcher) error {
	if err := migrationCtx.Err(); err != nil {
		return err
	}

	// 从 CSV 文件名提取表名 -> CSV 文件路径 的映射
	csvTableMap := buildCSVTableMap(csvFiles, cfg.Source.CSVTimestamp, tableMatcher)

	// 构建允许表名的查找集合（O(1) 查找）
	allowedByKey := make(map[string]string, len(allowedTables))
	for _, t := range allowedTables {
		allowedByKey[tableMatcher.Key(t)] = t
	}

	var tablesToImport []string
	// First add tables from CSV map that are in allowed set
	for tableKey, csvPath := range csvTableMap {
		if allowedTables == nil {
			tableName := extractTableNameFromFile(filepath.Base(csvPath), cfg.Source.CSVTimestamp)
			tablesToImport = append(tablesToImport, tableName)
		} else if originalName, ok := allowedByKey[tableKey]; ok {
			tablesToImport = append(tablesToImport, originalName)
		}
	}
	// Then add allowed tables not in CSV map (for CSV_NOT_FOUND diagnostic)
	if allowedTables != nil {
		for _, t := range allowedTables {
			if _, inCSV := csvTableMap[tableMatcher.Key(t)]; !inCSV {
				tablesToImport = append(tablesToImport, t)
			}
		}
	}

	logger.Infof("Starting data import for %d tables (with CSV files)...", len(tablesToImport))

	tracker.StartPhase("import-data", len(tablesToImport))
	defer tracker.ClearPhase()

	// 创建数据导入器
	dataImporter := importer.NewDataImporter(conn, cfg)
	dataImporter.WithProgressCallback(func(tableName string, totalRows, processedRows, insertedRows int64) {
		if totalRows > 0 {
			if err := tracker.SetTableTotalRows(tableName, totalRows); err != nil {
				logger.Warnf("Failed to set total rows for %s: %v", tableName, err)
			}
		}
		tracker.UpdateTableProgress(tableName, processedRows, insertedRows)
	})
	dataImporter.WithContext(migrationCtx.Context())
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
				// 检查是否已收到停止信号
				select {
				case <-migrationCtx.Context().Done():
					logger.Warnf("[Worker %d] Migration stopped, aborting import", workerID)
					return
				default:
				}

				logger.Infof("[Worker %d] Processing table: %s", workerID, tableName)

				// 查找 CSV 文件路径
				csvPath, ok := csvTableMap[tableMatcher.Key(tableName)]
				if !ok {
					// Call FindCSVFile directly to get the diagnostic with TriedPaths
					_, _, diag := dataImporter.FindCSVFile(tableName)
					logImportDiagnostic(diag)
					// CSV_NOT_FOUND 是正常情况（数据不存在），不记为失败
					if err := tracker.SkipTable(tableName, "CSV file not found"); err != nil {
						logger.Warnf("[Worker %d] Failed to mark table %s as skipped: %v", workerID, tableName, err)
					}
					tracker.SkipPhaseItem()
					resultChan <- &importer.ImportResult{
						TableName:     tableName,
						Success:       true, // CSV不存在不算失败，只是没有数据
						InsertedRows:  0,
						ProcessedRows: 0,
						ErrorCount:    0,
						ErrorMessage:  "",
					}
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
					tracker.FailPhaseItem()
					if cfg.Migration.FastFail != nil && *cfg.Migration.FastFail {
						stopErr := fmt.Errorf("failed to import table %s: %w", tableName, err)
						migrationCtx.Stop(stopErr)
						resultChan <- &importer.ImportResult{
							TableName:    tableName,
							Success:      false,
							ErrorMessage: stopErr.Error(),
						}
						return
					}
					resultChan <- &importer.ImportResult{
						TableName:    tableName,
						Success:      false,
						ErrorMessage: err.Error(),
					}
					continue
				}

				if result.Success {
					logger.Infof("[Worker %d] Table imported: %s (%d rows)", workerID, tableName, result.InsertedRows)
					if result.TotalRows > 0 {
						if err := tracker.SetTableTotalRows(tableName, result.TotalRows); err != nil {
							logger.Warnf("[Worker %d] Failed to set total rows for %s: %v", workerID, tableName, err)
						}
					}
					if err := tracker.CompleteTable(tableName, result.ProcessedRows, result.InsertedRows, result.ErrorCount); err != nil {
						logger.Warnf("[Worker %d] Failed to mark table %s as completed: %v", workerID, tableName, err)
					}
					tracker.CompletePhaseItem()
					resultChan <- result
				} else {
					logger.Warnf("[Worker %d] Table partially imported: %s (%d rows, %d errors)",
						workerID, tableName, result.InsertedRows, result.ErrorCount)
					if err := tracker.FailTable(tableName, fmt.Sprintf("partial import: %d row errors", result.ErrorCount)); err != nil {
						logger.Warnf("[Worker %d] Failed to mark table %s as failed: %v", workerID, tableName, err)
					}
					tracker.FailPhaseItem()
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

	for result := range resultChan {
		if result.Success && result.ErrorCount == 0 {
			successCount++
		} else {
			failCount++
			failedTables = append(failedTables, result.TableName)
		}
		totalRows += result.InsertedRows
	}

	// 打印详细错误汇总
	printErrorSummary(dataImporter.GetErrorRecorder(), failedTables)

	logger.Infof("Data import completed: %d success, %d failed, %d total rows",
		successCount, failCount, totalRows)

	// 生成数据导入报告
	generateMigrationReport("Data Import Report", nil, successCount, failCount, failedTables)

	return finalError(failCount, "tables failed to import", migrationCtx)
}

func finalError(failCount int, desc string, migrationCtx *migration.MigrationContext) error {
	if err := migrationCtx.Err(); err != nil {
		return err
	}
	if failCount > 0 {
		return fmt.Errorf("%d %s", failCount, desc)
	}
	return nil
}

func finalizeCreateOnlyProgress(tracker *progress.Tracker, existingTables []string) error {
	for _, tableName := range existingTables {
		if err := tracker.SkipTable(tableName, "table already exists"); err != nil {
			return fmt.Errorf("mark existing table %s as skipped: %w", tableName, err)
		}
	}
	return nil
}

// buildCSVTableMap 从 CSV 文件列表构建表名 -> 文件路径映射
func buildCSVTableMap(csvFiles []string, timestamp string, tableMatcher matcher.TableNameMatcher) map[string]string {
	tableMap := make(map[string]string)

	for _, csvPath := range csvFiles {
		// 从文件路径提取表名
		// 格式: origin_data_csvfiles/{TABLE_NAME}_{TIMESTAMP}.csv
		fileName := filepath.Base(csvPath)
		tableName := extractTableNameFromFile(fileName, timestamp)
		if tableName != "" {
			key := tableMatcher.Key(tableName)
			if existing, ok := tableMap[key]; ok {
				logger.Warnf("CSV table name conflict under current case-sensitivity setting: %s and %s", existing, csvPath)
				continue
			}
			tableMap[key] = csvPath
		}
	}

	return tableMap
}

// extractTableNameFromFile 从 CSV 文件名提取表名
func extractTableNameFromFile(fileName, timestamp string) string {
	return matcher.CSVFileNameToTableName(fileName, timestamp)
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

// renameCSVFiles 批量重命名 CSV 文件
func renameCSVFiles(postfix, dir string, dryRun bool) error {
	if postfix == "" || dir == "" {
		return fmt.Errorf("postfix and target directory are required")
	}

	// 验证目录存在且为目录
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("target directory does not exist: %s", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("target is not a directory: %s", dir)
	}

	// 获取目录下所有 CSV 文件
	files, err := filepath.Glob(filepath.Join(dir, "*.csv"))
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	var toRename []struct{ oldPath, newPath string }

	for _, filePath := range files {
		fileName := filepath.Base(filePath)
		nameWithoutExt := strings.TrimSuffix(fileName, ".csv")
		if strings.HasSuffix(nameWithoutExt, postfix) {
			newName := strings.TrimSuffix(nameWithoutExt, postfix) + ".csv"
			newPath := filepath.Join(dir, newName)

			// 检查目标文件是否已存在
			if _, err := os.Stat(newPath); err == nil {
				logger.Warnf("Skipped (file exists): %s", newName)
				continue
			}

			toRename = append(toRename, struct{ oldPath, newPath string }{filePath, newPath})
		}
	}

	// 预览模式
	if dryRun {
		if len(toRename) == 0 {
			logger.Info("No files to rename")
			return nil
		}
		logger.Infof("%d files to rename:", len(toRename))
		for _, r := range toRename {
			logger.Infof("  %s -> %s", filepath.Base(r.oldPath), filepath.Base(r.newPath))
		}
		return nil
	}

	// 执行重命名
	var renamed, skipped int
	for _, r := range toRename {
		if err := os.Rename(r.oldPath, r.newPath); err != nil {
			logger.Errorf("Failed to rename %s: %v", r.oldPath, err)
			skipped++
			continue
		}
		renamed++
	}

	logger.Infof("%d files renamed, %d skipped", renamed, skipped)
	return nil
}

// previewCSVImport analyzes CSV-to-table matching without executing imports.
func previewCSVImport(cfg *config.Config, csvFiles []string, allowedTables, existingTables []string, tableMatcher matcher.TableNameMatcher) error {
	csvTableMap := buildCSVTableMap(csvFiles, cfg.Source.CSVTimestamp, tableMatcher)
	allowedSet := tableMatcher.BuildSet(allowedTables)
	existingSet := tableMatcher.BuildSet(existingTables)

	var matched, unmatched, notAllowed int
	for tableKey, csvPath := range csvTableMap {
		if _, ok := allowedSet[tableKey]; !ok {
			notAllowed++
			continue
		}

		if _, ok := existingSet[tableKey]; !ok {
			unmatched++
			logger.Debugf("[DRY RUN] CSV %s -> table %s: table does not exist in database",
				filepath.Base(csvPath), extractTableNameFromFile(filepath.Base(csvPath), cfg.Source.CSVTimestamp))
			continue
		}
		matched++
	}

	logger.Infof("[DRY RUN] CSV analysis: %d matched, %d no matching table, %d not in scope",
		matched, unmatched, notAllowed)
	return nil
}
