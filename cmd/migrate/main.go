package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/converter"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/diagnostics"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/importer"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/parser"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/tablescope"
)

// BuildLabel is set by scripts/build.sh; it distinguishes delivered A/B artifacts.
var BuildLabel = "development"

var (
	configPath    = flag.String("config", "config.yaml", "配置文件路径")
	tables        = flag.String("tables", "", "仅导入指定表（逗号分隔）")
	createOnly    = flag.Bool("create-tables-only", false, "仅创建缺失表，不导入数据")
	reimport      = flag.Bool("reimport-tables", false, "开启指定表重导，仅处理 --reimport-table-file 中的表")
	reimportFile  = flag.String("reimport-table-file", "", "指定表重导清单 TXT 文件，每行一个表名")
	version       = flag.Bool("version", false, "显示版本信息")
	removePostfix = flag.String("remove-postfix", "", "移除 CSV 文件名的指定后缀")
	dryRun        = flag.Bool("dry-run", false, "预览模式，不实际执行")
	targetDir     = flag.String("target", "", "目标目录路径")
)

const (
	Version = "1.0.0"

	createFailedTablesFile = "create_failed_tables.txt"
	completedTablesFile    = "completed_tables.txt"
	slowTablesFile         = "slow_tables.txt"

	// failedTablesFile and rowCountMismatchFile are forward-declarations
	// for Task 5 integration wiring; they will become used once the
	// row-count validation and failed-table tracking callbacks are wired in.
	failedTablesFile     = "failed_tables.txt"
	rowCountMismatchFile = "row_count_mismatch_tables.txt"
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
	if len(os.Args) > 1 && (os.Args[1] == "export-sqlserver" || os.Args[1] == "import-bundle") {
		os.Exit(runBundleCLI(os.Args[1:]))
	}
	os.Exit(runCLI())
}

func runCLI() (exitCode int) {
	var migrationErr error
	flag.Parse()
	if handled, code := runOfflineReport(); handled {
		return code
	}
	if handled, code := runOfflineTools(); handled {
		return code
	}
	stopProfiles, err := startLocalProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Profile initialization failed: %v\n", err)
		return 1
	}
	defer stopProfiles()

	// 显示版本信息
	if *version {
		fmt.Printf("sqlserver-to-mysql-migrator version %s (%s)\n", Version, BuildLabel)
		return 0
	}

	// 检测是否启用后缀移除模式
	if *removePostfix != "" {
		if *targetDir == "" {
			fmt.Fprintln(os.Stderr, "Error: --target is required when using --remove-postfix")
			return 1
		}
		if err := logger.Init("INFO", "", true, 0, 0, 0); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
			return 1
		}
		defer logger.Sync()
		if err := renameCSVFiles(*removePostfix, *targetDir, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	migrationStartedAt := time.Now()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		fmt.Fprintln(os.Stderr, formatMigrationTotalDuration(migrationStartedAt, time.Now()))
		return 1
	}
	if err := requireDiagnosticsOrError(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	if *csvInventory != "" {
		if err := writeCSVInventory(cfg, *csvInventory); err != nil {
			fmt.Fprintf(os.Stderr, "CSV inventory failed: %v\n", err)
			return 1
		}
		fmt.Printf("CSV inventory written to %s\n", *csvInventory)
		return 0
	}
	recorder, err := openDiagnostics(cfg, migrationStartedAt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Diagnostics initialization failed before database access: %v\n", err)
		return 1
	}
	cfg.Diagnostics = recorder
	defer func() {
		if err := recorder.Close(exitCode == 0, errors.Is(migrationErr, context.Canceled)); err != nil {
			fmt.Fprintln(os.Stderr, "Diagnostics output failed; report is incomplete and not valid performance evidence. No database writes will be replayed.")
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()
	cfg.Logging.File = config.ExpandLogFilePattern(cfg.Logging.File, time.Now())

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
		fmt.Fprintln(os.Stderr, formatMigrationTotalDuration(migrationStartedAt, time.Now()))
		return 1
	}
	defer logger.Sync()
	defer func() {
		logMigrationCompletion(migrationStartedAt, time.Now(), migrationErr)
	}()

	logger.Info("=== Database Migration Tool Started ===")
	logger.Infof("Version: %s (%s)", Version, BuildLabel)
	logger.Infof("Config: %s", cfg.ConfigFilesSummary())
	if recorder != nil {
		logger.Infof("Diagnostics directory: %s", recorder.Dir())
	}

	tableScope, err := tablescope.Resolve(*tables, *reimport, *reimportFile)
	if err != nil {
		migrationErr = err
		logger.Errorf("Invalid table scope configuration: %v", err)
		return 1
	}

	config.LogEffective(cfg, config.CLIArgs{
		BaselineAlgorithms: importer.BaselineAlgorithms,
		Tables:             *tables,
		CreateOnly:         *createOnly,
		ReimportTables:     *reimport,
		ReimportTableFile:  *reimportFile,
		DryRun:             *dryRun,
	})

	tableMatcher := matcher.NewTableNameMatcher(cfg.Migration.IsTableNameCaseSensitive())
	logger.Infof("Table name case sensitive: %t", tableMatcher.CaseSensitive())

	// 连接数据库
	conn, err := database.RetryConnectWithMatcher(&cfg.Target, 3, tableMatcher)
	if err != nil {
		migrationErr = err
		logger.Errorf("Failed to connect to database: %v", err)
		return 1
	}
	defer conn.Close()
	recorder.SetDB(conn.DB)
	recorder.Global().Observe(diagnostics.Initialization, time.Since(migrationStartedAt))

	// 初始化进度跟踪器
	tracker, err := progress.NewTracker()
	if err != nil {
		migrationErr = err
		logger.Errorf("Failed to initialize progress tracker: %v", err)
		return 1
	}
	defer tracker.Close()

	// 启动进度报告器
	tracker.StartProgressReporter(10 * time.Second)

	if *dryRun {
		logger.Info("=== DRY RUN MODE: no actual changes will be made ===")
	}
	if err := runMigration(cfg, conn, tracker, tableMatcher, migrationRunOptions{
		DryRun:     *dryRun,
		CreateOnly: *createOnly,
		TableScope: tableScope,
	}); err != nil {
		migrationErr = err
		logger.Errorf("Migration failed: %v", err)
		return cliExitCode(err)
	}

	tracker.PrintSummary()

	return cliExitCode(nil)
}

func cliExitCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

func formatMigrationTotalDuration(start, end time.Time) string {
	elapsedSeconds := int64(end.Sub(start).Seconds())
	if elapsedSeconds < 0 {
		elapsedSeconds = 0
	}
	return fmt.Sprintf("本次迁移工作总耗时: %d 秒", elapsedSeconds)
}

func migrationCompletionMessages(start, end time.Time, err error) []string {
	messages := make([]string, 0, 2)
	if err == nil {
		messages = append(messages, "=== Database Migration Tool Finished ===")
	}
	return append(messages, formatMigrationTotalDuration(start, end))
}

func logMigrationCompletion(start, end time.Time, err error) {
	for _, message := range migrationCompletionMessages(start, end, err) {
		logger.Info(message)
	}
}

type migrationRunOptions struct {
	DryRun     bool
	CreateOnly bool
	TableScope tablescope.Scope
}

func logSelectedTableScopeResult(scope tablescope.Scope, result tablescope.Result) {
	if !scope.Enabled {
		return
	}

	mode := "table scope"
	if scope.Reimport {
		mode = "reimport table scope"
	}
	logger.Infof("%s loaded: %d requested from %s, %d selected, %d skipped",
		mode, len(scope.Tables), scope.Source, len(result.Tables), len(result.Skipped))
	for _, skipped := range result.Skipped {
		logger.Warnf("%s skipped: table=%s reason=%s source=%s",
			mode, skipped.Table, skipped.Reason, scope.Source)
	}
}

// runMigration executes the full migration pipeline.
func runMigration(cfg *config.Config, conn *database.Connection, tracker *progress.Tracker, tableMatcher matcher.TableNameMatcher, opts migrationRunOptions) (runErr error) {
	// 创建迁移上下文
	migrationCtx := migration.NewMigrationContext()
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	runDone := make(chan struct{})
	defer close(runDone)
	go func() {
		select {
		case <-signalCtx.Done():
			migrationCtx.Stop(context.Canceled)
		case <-runDone:
		}
	}()
	defer func() {
		if err := migrationCtx.Err(); err != nil {
			logger.Errorf("Migration failed: %v", err)
		}
	}()

	// ========== 步骤 1: 表分类 ==========
	// 解析 DDL 文件
	ddlParser := parser.NewDDLParser(cfg.Source.DDLFile)
	ddlDone := cfg.Diagnostics.Global().Start(diagnostics.DDL)
	allDDLs, err := ddlParser.ParseAll()
	ddlDone()
	if err != nil {
		return fmt.Errorf("failed to parse DDL file: %w", err)
	}
	logger.Infof("Parsed %d DDL definitions from %s", len(allDDLs), cfg.Source.DDLFile)
	ddlLookup, err := buildDDLLookup(allDDLs, tableMatcher)
	if err != nil {
		return err
	}

	// 从 DDL 获取所有表名列表
	allTableNames := collectDDLTableNames(allDDLs)
	logger.Infof("Total tables from DDL: %d", len(allTableNames))

	completedTables, err := loadCompletedTables()
	if err != nil {
		return err
	}

	if opts.TableScope.Enabled {
		selectionResult := tablescope.Apply(allTableNames, opts.TableScope, cfg.Migration.SkipTables, completedTables, tableMatcher)
		allTableNames = selectionResult.Tables
		logSelectedTableScopeResult(opts.TableScope, selectionResult)
	} else {
		if len(cfg.Migration.SkipTables) > 0 {
			before := len(allTableNames)
			allTableNames = tablescope.ExcludeTables(allTableNames, cfg.Migration.SkipTables, tableMatcher)
			logger.Infof("Skipped %d tables per skip_tables config: %v", before-len(allTableNames), cfg.Migration.SkipTables)
		}

		if len(completedTables) > 0 {
			before := len(allTableNames)
			allTableNames = tablescope.ExcludeTables(allTableNames, completedTables, tableMatcher)
			logger.Infof("Skipped %d tables per %s: %v", before-len(allTableNames), completedTablesFile, completedTables)
		}
	}

	// A prior CREATE TABLE may have succeeded before a later CREATE INDEX failed.
	// Never treat that incomplete table as an ordinary existing table on rerun.
	failedCreates, err := loadTableList(createFailedTablesFile)
	if err != nil {
		return err
	}
	selected := tableMatcher.BuildSet(allTableNames)
	for _, table := range failedCreates {
		if _, ok := selected[tableMatcher.Key(table)]; ok {
			return fmt.Errorf("table %s remains in %s; verify/repair its schema and remove it from the list before rerunning", table, createFailedTablesFile)
		}
	}

	// Validate and hold state files before any CREATE/TRUNCATE. A later open failure
	// must never be interpreted as a successful import without a durable record.
	var completionSink *importCompletionSink
	if !opts.DryRun && !opts.CreateOnly {
		var closeLists func() error
		completionSink, closeLists, err = openImportCompletionSink(cfg)
		if err != nil {
			return err
		}
		defer func() {
			runErr = errors.Join(runErr, closeLists())
		}()
	}

	// Initialize the bounded scope before create/TRUNCATE, using metadata only.
	if cfg.Diagnostics != nil {
		scopeFiles := make(map[string]string, len(allTableNames))
		csvPaths, scanErr := scanCSVFiles(cfg.Source.CSVDirectory)
		if scanErr != nil {
			return scanErr
		}
		csvMap, mapErr := buildCSVTableMap(csvPaths, cfg.Source.CSVTimestamp, tableMatcher)
		if mapErr != nil {
			return mapErr
		}
		for _, name := range allTableNames {
			scopeFiles[name] = csvMap[tableMatcher.Key(name)]
		}
		if err := cfg.Diagnostics.Scope(allTableNames, scopeFiles, len(allDDLs)-len(allTableNames)); err != nil {
			return err
		}
	}
	// 分类表（已存在 vs 缺失）
	inspector := database.NewInspector(conn, tableMatcher)
	metadataDone := cfg.Diagnostics.Global().Start(diagnostics.Metadata)
	classification, err := inspector.ClassifyTables(allTableNames)
	metadataDone()
	if err != nil {
		return fmt.Errorf("failed to classify tables: %w", err)
	}

	logger.Infof("Table classification: %d existing, %d missing",
		len(classification.ExistingTables), len(classification.MissingTables))

	if opts.TableScope.Reimport && len(classification.MissingTables) > 0 {
		logger.Warnf("Reimport mode only truncates/imports existing tables; %d requested tables are missing and will not be created: %v",
			len(classification.MissingTables), classification.MissingTables)
		for _, tableName := range classification.MissingTables {
			logger.Warnf("reimport table scope skipped: table=%s reason=target table does not exist; reimport mode does not create missing tables source=%s",
				tableName, opts.TableScope.Source)
		}
		for _, name := range classification.MissingTables {
			cfg.Diagnostics.State(name, "skipped", "not_run", 0)
		}
		allTableNames = classification.ExistingTables
		classification.MissingTables = nil
	}

	if opts.DryRun || opts.CreateOnly {
		for _, name := range allTableNames {
			cfg.Diagnostics.State(name, "skipped", "not_run", 0)
		}
	}
	tracker.SetPlannedTotalTables(len(allTableNames))
	logger.Infof("Overall migration target: %d tables", len(allTableNames))

	// ========== 步骤 2: DDL 转换 + 表创建 ==========
	var stepErrs []error
	if opts.DryRun {
		logWritePreview(cfg, classification.MissingTables, classification.ExistingTables)
	} else if len(classification.MissingTables) > 0 {
		// Journal every candidate before the first CREATE TABLE. If the process dies
		// between CREATE TABLE and CREATE INDEX, a subsequent run cannot silently
		// treat the partially-created table as complete.
		if err := writeCreateFailedTables(classification.MissingTables); err != nil {
			return fmt.Errorf("stage create candidates: %w", err)
		}
		createDone := cfg.Diagnostics.Global().Start(diagnostics.Create)
		createFailed, createErr := createAndTrackTables(cfg, conn, classification.MissingTables, ddlLookup, tracker, migrationCtx, tableMatcher, opts.CreateOnly)
		createDone()
		for _, name := range createFailed {
			cfg.Diagnostics.State(name, "failed", "not_run", 0)
		}
		if createErr != nil {
			logger.Errorf("Some tables failed to create: %v", createErr)
			stepErrs = append(stepErrs, createErr)
		}
		if stopErr := migrationCtx.Err(); stopErr != nil {
			return errors.Join(append(stepErrs, stopErr)...)
		}
		if refreshErr := conn.RefreshTableNameMap(); refreshErr != nil {
			return errors.Join(append(stepErrs, fmt.Errorf("refresh table map after create: %w", refreshErr))...)
		}
		if writeErr := finalizeCreateJournal(failedCreates, createFailed, migrationCtx.Err()); writeErr != nil {
			logger.Errorf("Failed to finalize create failed tables file: %v", writeErr)
			return errors.Join(append(stepErrs, writeErr)...)
		}
		if len(createFailed) > 0 {
			allTableNames = tablescope.ExcludeTables(allTableNames, createFailed, tableMatcher)
			logger.Warnf("Excluded %d create-failed tables from subsequent phases: %v",
				len(createFailed), createFailed)
		}
	}

	if opts.CreateOnly {
		if err := finalizeCreateOnlyProgress(tracker, classification.ExistingTables); err != nil {
			logger.Warnf("Failed to finalize create-only progress: %v", err)
			stepErrs = append(stepErrs, err)
		}
		logger.Info("Create tables only mode: skipping data import")
		return errors.Join(stepErrs...)
	}

	// ========== 步骤 2.5: TRUNCATE 所有已存在表 ==========
	if opts.DryRun {
		logger.Infof("[DRY RUN] Would truncate %d existing tables (skipped)", len(classification.ExistingTables))
	} else if len(classification.ExistingTables) > 0 {
		if err := truncateExistingTables(conn, classification.ExistingTables, tracker, migrationCtx, cfg); err != nil {
			logger.Errorf("Failed to truncate existing tables: %v", err)
			return errors.Join(append(stepErrs, err)...)
		}
	}

	// ========== 步骤 3: 数据导入 ==========
	csvFiles, err := scanCSVFiles(cfg.Source.CSVDirectory)
	if err != nil {
		return errors.Join(append(stepErrs, fmt.Errorf("failed to scan CSV files: %w", err))...)
	}
	logger.Infof("Found %d CSV files", len(csvFiles))

	if opts.DryRun {
		logger.Infof("[DRY RUN] Would import data from %d CSV files into %d tables (skipped)", len(csvFiles), len(allTableNames))
		if err := previewCSVImport(cfg, csvFiles, allTableNames, classification.ExistingTables, tableMatcher); err != nil {
			logger.Errorf("Preview analysis failed: %v", err)
			stepErrs = append(stepErrs, err)
		}
	} else {
		if err := importDataWithCSVMapping(cfg, conn, csvFiles, allTableNames, tracker, migrationCtx, tableMatcher, completionSink); err != nil {
			return errors.Join(append(stepErrs, fmt.Errorf("failed to import data: %w", err))...)
		}
	}

	return errors.Join(stepErrs...)
}

// scanCSVFiles 扫描 CSV 文件
func scanCSVFiles(directory string) ([]string, error) {
	pattern := filepath.Join(directory, "*.csv")
	return filepath.Glob(pattern)
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

func buildDDLLookup(allDDLs map[string]*parser.TableDDL, tableMatcher matcher.TableNameMatcher) (map[string]*parser.TableDDL, error) {
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
			return nil, fmt.Errorf("DDL table name conflict under current case-sensitivity setting: %s and %s", existing.TableName, tableDDL.TableName)
		}
		lookup[key] = tableDDL
	}
	return lookup, nil
}

// truncateExistingTables 清空所有已存在的表
func truncateExistingTables(conn *database.Connection, existingTables []string, tracker *progress.Tracker, migrationCtx *migration.MigrationContext, cfg *config.Config) error {
	defer cfg.Diagnostics.Global().Start(diagnostics.Truncate)()
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
					cfg.Diagnostics.State(tableName, "failed", "not_run", 0)
					cfg.Diagnostics.Error(tableName, diagnostics.Truncate, err)
					logger.Errorf("[Worker %d] Failed to truncate table %s: %v", workerID, tableName, err)
					tracker.FailPhaseItem()
					totalFail.Add(1)
					if cfg.Migration.IsFastFail() {
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
func createAndTrackTables(cfg *config.Config, conn *database.Connection, missingTables []string, ddlLookup map[string]*parser.TableDDL, tracker *progress.Tracker, migrationCtx *migration.MigrationContext, tableMatcher matcher.TableNameMatcher, createOnly bool) (failed []string, err error) {
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
					continue
				}

				// 执行 CREATE TABLE
				logger.Infof("[Worker %d] Creating table: %s", workerID, tableName)
				if _, err := createTableDDL(conn, tableConverter, tableDDL); err != nil {
					cfg.Diagnostics.Error(tableName, diagnostics.Create, err)
					logger.Errorf("[Worker %d] Failed to create table %s: %v", workerID, tableName, err)
					tracker.FailTable(tableName, fmt.Sprintf("Table creation failed: %v", err))
					tracker.FailPhaseItem()

					mu.Lock()
					failedTableNames = append(failedTableNames, tableName)
					mu.Unlock()
					totalFail.Add(1)
					continue
				}

				logger.Infof("[Worker %d] Table created: %s", workerID, tableName)
				if createOnly {
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
		return failedTableNames, err
	}

	successCount := int(totalSuccess.Load())
	failCount := int(totalFail.Load())
	logger.Infof("Table creation completed: %d success, %d failed", successCount, failCount)

	// 生成表创建报告
	generateMigrationReport("Table Creation Report", nil, successCount, failCount, failedTableNames)

	if failCount > 0 {
		return failedTableNames, fmt.Errorf("%d tables failed to create", failCount)
	}

	return failedTableNames, nil
}

type ddlExecutor interface {
	ExecuteDDL(string) error
	ExecuteStatements([]string) error
}

type rowCounter interface {
	GetRowCount(tableName string) (int64, error)
}

type rowCountValidationResult struct {
	CountErr     error
	CountError   bool
	TableName    string
	ExpectedRows int64
	ActualRows   int64
	Valid        bool
	ErrorMessage string
}

type skippedImportTable struct {
	TableName string
	Reason    string
	Err       error
}

func createTableDDL(executor ddlExecutor, tableConverter *converter.TableConverter, tableDDL *parser.TableDDL) (converter.ConvertResult, error) {
	result, err := tableConverter.ConvertToMySQLResult(tableDDL, converter.ConvertOptions{})
	if err != nil {
		return converter.ConvertResult{}, err
	}

	stmts := result.Statements
	if len(stmts) == 0 && result.SQL != "" {
		stmts = []string{result.SQL}
	}
	if err := executor.ExecuteStatements(stmts); err != nil {
		return result, err
	}
	return result, nil
}

func validateImportedRowCount(counter rowCounter, tableName string, expectedRows int64) rowCountValidationResult {
	actualRows, err := counter.GetRowCount(tableName)
	if err != nil {
		return rowCountValidationResult{
			TableName:    tableName,
			ExpectedRows: expectedRows,
			ActualRows:   0,
			Valid:        false,
			ErrorMessage: fmt.Sprintf("failed to count rows for %s: %v", tableName, err),
			CountError:   true,
			CountErr:     err,
		}
	}
	if actualRows != expectedRows {
		return rowCountValidationResult{
			TableName:    tableName,
			ExpectedRows: expectedRows,
			ActualRows:   actualRows,
			Valid:        false,
			ErrorMessage: fmt.Sprintf("row count mismatch: expected=%d actual=%d", expectedRows, actualRows),
		}
	}
	return rowCountValidationResult{
		TableName:    tableName,
		ExpectedRows: expectedRows,
		ActualRows:   actualRows,
		Valid:        true,
	}
}

type importCompletionSink struct {
	mu             sync.Mutex
	completedFile  *os.File
	slowFile       *os.File
	slowThreshold  time.Duration
	failedTables   []string
	mismatchTables []string
	failedSeen     map[string]struct{}
	mismatchSeen   map[string]struct{}
}

func newImportCompletionSink(completedFile, slowFile *os.File, slowThreshold time.Duration) *importCompletionSink {
	return &importCompletionSink{
		completedFile: completedFile,
		slowFile:      slowFile,
		slowThreshold: slowThreshold,
		failedSeen:    make(map[string]struct{}),
		mismatchSeen:  make(map[string]struct{}),
	}
}

func openImportCompletionSink(cfg *config.Config) (*importCompletionSink, func() error, error) {
	completed, err := os.OpenFile(completedTablesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", completedTablesFile, err)
	}
	slow, err := os.OpenFile(slowTablesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		completed.Close()
		return nil, nil, fmt.Errorf("open %s: %w", slowTablesFile, err)
	}
	closeLists := func() error {
		return errors.Join(completed.Close(), slow.Close())
	}
	threshold := time.Duration(cfg.Migration.EffectiveSlowTableThresholdMinutes()) * time.Minute
	return newImportCompletionSink(completed, slow, threshold), closeLists, nil
}

func (s *importCompletionSink) addFailed(table string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.failedSeen[table]; ok {
		return
	}
	s.failedSeen[table] = struct{}{}
	s.failedTables = append(s.failedTables, table)
}

func (s *importCompletionSink) addMismatch(table string) {
	s.addFailed(table)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mismatchSeen[table]; ok {
		return
	}
	s.mismatchSeen[table] = struct{}{}
	s.mismatchTables = append(s.mismatchTables, table)
}

func (s *importCompletionSink) appendCompleted(table string, elapsed time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Persist optional slow-table metadata first. A failed auxiliary write must
	// not leave this table registered as completed while its result says failed.
	if s.slowThreshold > 0 && elapsed > s.slowThreshold {
		logger.Infof("Table %s exceeded slow threshold: %s > %s",
			table, elapsed.Truncate(time.Second), s.slowThreshold)
		if s.slowFile != nil {
			if _, err := s.slowFile.WriteString(table + "\n"); err != nil {
				return fmt.Errorf("record slow table %s: %w", table, err)
			}
			if err := s.slowFile.Sync(); err != nil {
				return fmt.Errorf("sync slow table %s: %w", table, err)
			}
		}
	}
	if s.completedFile != nil {
		if _, err := s.completedFile.WriteString(table + "\n"); err != nil {
			return fmt.Errorf("record completed table %s: %w", table, err)
		}
		if err := s.completedFile.Sync(); err != nil {
			return fmt.Errorf("sync completed table %s: %w", table, err)
		}
	}
	return nil
}

func (s *importCompletionSink) writeLists() error {
	s.mu.Lock()
	failed := append([]string(nil), s.failedTables...)
	mismatch := append([]string(nil), s.mismatchTables...)
	s.mu.Unlock()
	return errors.Join(
		writeTableList(failedTablesFile, failed),
		writeTableList(rowCountMismatchFile, mismatch),
	)
}

func finalizeImportedTable(
	cfg *config.Config,
	counter rowCounter,
	tracker *progress.Tracker,
	migrationCtx *migration.MigrationContext,
	sink *importCompletionSink,
	result *importer.ImportResult,
	elapsed time.Duration,
) {
	if result == nil {
		return
	}
	if result.Skipped {
		cfg.Diagnostics.State(result.TableName, "missing", "not_run", 0)
		return
	}
	stats := cfg.Diagnostics.TableStats(result.TableName)
	countStatus := "not_run"
	var count int64
	defer func() {
		state := "failed"
		if result.Success && result.ErrorCount == 0 {
			state = "success"
		}
		cfg.Diagnostics.State(result.TableName, state, countStatus, count)
	}()
	if !cfg.Migration.ShouldValidateRowCount() {
		countStatus = "disabled"
	}

	validationFailed := false
	if result.Success && result.ErrorCount == 0 && cfg.Migration.ShouldValidateRowCount() {
		validationDone := stats.Start(diagnostics.Validation)
		validation := validateImportedRowCount(counter, result.TableName, result.ProcessedRows)
		validationDone()
		count = validation.ActualRows
		countStatus = "match"
		if !validation.Valid {
			countStatus = "mismatch"
		}
		if validation.CountError {
			countStatus = "error"
			cfg.Diagnostics.Error(result.TableName, diagnostics.Validation, validation.CountErr)
		}
		logger.Infof("Row count validation: table=%s csv_rows=%d mysql_rows=%d valid=%t",
			result.TableName, validation.ExpectedRows, validation.ActualRows, validation.Valid)
		if !validation.Valid {
			result.Success = false
			result.ErrorCount++
			result.ErrorMessage = validation.ErrorMessage
			validationFailed = true
			sink.addMismatch(result.TableName)
			if cfg.Migration.IsFastFail() {
				migrationCtx.Stop(fmt.Errorf("failed to import table %s: %s", result.TableName, validation.ErrorMessage))
			}
		}
	}

	if result.Success && result.ErrorCount == 0 {
		registrationDone := stats.Start(diagnostics.Registration)
		err := sink.appendCompleted(result.TableName, elapsed)
		registrationDone()
		if err != nil {
			logger.Errorf("Failed to persist completion for %s: %v", result.TableName, err)
			result.Success = false
			result.ErrorCount++
			result.ErrorMessage = err.Error()
			if cfg.Migration.IsFastFail() {
				migrationCtx.Stop(fmt.Errorf("failed to register table %s: %w", result.TableName, err))
			}
		} else {
			if err := tracker.SetTableTotalRows(result.TableName, result.TotalRows); err != nil {
				logger.Warnf("Failed to set total rows for %s: %v", result.TableName, err)
			}
			if err := tracker.CompleteTable(result.TableName, result.ProcessedRows, result.InsertedRows, result.ErrorCount); err != nil {
				logger.Warnf("Failed to mark table %s as completed: %v", result.TableName, err)
			}
			tracker.CompletePhaseItem()
			return
		}
	}

	failMsg := result.ErrorMessage
	if failMsg == "" {
		failMsg = fmt.Sprintf("partial import: %d row errors", result.ErrorCount)
	}
	if err := tracker.FailTable(result.TableName, failMsg); err != nil {
		logger.Warnf("Failed to mark table %s as failed: %v", result.TableName, err)
	}
	tracker.FailPhaseItem()
	if !validationFailed {
		sink.addFailed(result.TableName)
	}
}

func writeCreateFailedTables(tables []string) error {
	previous, err := loadTableList(createFailedTablesFile)
	if err != nil {
		return err
	}
	return persistCreateFailedTables(append(previous, tables...))
}

func finalizeCreateJournal(previous, failed []string, interrupted error) error {
	if interrupted != nil {
		return interrupted // Unknown in-flight tables must stay blocked on the next run.
	}
	return persistCreateFailedTables(append(previous, failed...))
}

func persistCreateFailedTables(tables []string) error {
	seen := make(map[string]struct{}, len(tables))
	var pending []string
	for _, table := range tables {
		if _, ok := seen[table]; !ok {
			seen[table] = struct{}{}
			pending = append(pending, table)
		}
	}
	// Never truncate an earlier marker when a scoped run adds new failures.
	f, err := os.CreateTemp(".", ".create-failed-*.tmp")
	if err != nil {
		return fmt.Errorf("create %s replacement: %w", createFailedTablesFile, err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(strings.Join(pending, "\n") + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), createFailedTablesFile); err != nil {
		return fmt.Errorf("replace %s: %w", createFailedTablesFile, err)
	}
	dir, err := os.Open(".")
	if err != nil {
		return fmt.Errorf("open create failure list directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync create failure list directory: %w", err)
	}
	return nil
}

// writeTableList writes a newline-separated list of table names to path.
// An empty or nil tables slice produces an empty file at path.
func writeTableList(path string, tables []string) error {
	content := ""
	if len(tables) > 0 {
		content = strings.Join(tables, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("writeTableList(%s): %w", path, err)
	}
	return nil
}

func buildImportCandidates(tableNames []string, csvTableMap map[string]string, tableMatcher matcher.TableNameMatcher, cfg *config.Config) ([]importer.ImportCandidate, []skippedImportTable) {
	candidates := make([]importer.ImportCandidate, 0, len(tableNames))
	var skipped []skippedImportTable
	for _, tableName := range tableNames {
		csvPath, ok := csvTableMap[tableMatcher.Key(tableName)]
		if !ok {
			skipped = append(skipped, skippedImportTable{TableName: tableName, Reason: "CSV file not found"})
			continue
		}
		info, err := os.Stat(csvPath)
		if err != nil {
			statErr := fmt.Errorf("CSV stat failed for %s (%s): %w", tableName, csvPath, err)
			skipped = append(skipped, skippedImportTable{TableName: tableName, Reason: statErr.Error(), Err: statErr})
			continue
		}
		candidates = append(candidates, importer.NewImportCandidate(tableName, csvPath, info.Size(), cfg.Migration))
	}
	return importer.OrderImportCandidates(candidates), skipped
}

func loadCompletedTables() ([]string, error) {
	return loadTableList(completedTablesFile)
}

func loadTableList(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var tables []string
	for _, line := range strings.Split(string(data), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			tables = append(tables, name)
		}
	}
	return tables, nil
}

// importDataWithCSVMapping 导入数据（基于 CSV 文件映射）
func importDataWithCSVMapping(cfg *config.Config, conn *database.Connection, csvFiles []string, allowedTables []string, tracker *progress.Tracker, migrationCtx *migration.MigrationContext, tableMatcher matcher.TableNameMatcher, preopened *importCompletionSink) (importErr error) {
	if err := migrationCtx.Err(); err != nil {
		return err
	}

	// 从 CSV 文件名提取表名 -> CSV 文件路径 的映射
	csvTableMap, err := buildCSVTableMap(csvFiles, cfg.Source.CSVTimestamp, tableMatcher)
	if err != nil {
		return err
	}

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
		if err := tracker.SetTableTotalRows(tableName, totalRows); err != nil {
			logger.Warnf("Failed to set total rows for %s: %v", tableName, err)
		}
		tracker.UpdateTableProgress(tableName, processedRows, insertedRows)
	})
	dataImporter.WithContext(migrationCtx.Context())
	defer dataImporter.Close()

	sink := preopened
	if sink == nil {
		var closeLists func() error
		sink, closeLists, err = openImportCompletionSink(cfg)
		if err != nil {
			return err
		}
		defer func() {
			importErr = errors.Join(importErr, closeLists())
		}()
	}

	if cfg.Migration.ShouldUseAdaptiveImport() {
		results, failedTables, err := importDataAdaptive(conn, cfg, tablesToImport, csvTableMap, tableMatcher, tracker, migrationCtx, dataImporter, sink)
		successCount := 0
		failCount := 0
		var totalRows int64
		for _, result := range results {
			if result.Skipped {
				continue
			}
			if result.Success && result.ErrorCount == 0 {
				successCount++
			} else {
				failCount++
			}
			totalRows += result.InsertedRows
		}

		printErrorSummary(dataImporter.GetErrorRecorder(), failedTables)
		logger.Infof("Data import completed: %d success, %d failed, %d total rows",
			successCount, failCount, totalRows)
		generateMigrationReport("Data Import Report", nil, successCount, failCount, failedTables)

		writeErr := sink.writeLists()
		if err != nil {
			return errors.Join(err, writeErr)
		}
		return errors.Join(finalError(failCount, "tables failed to import", migrationCtx), writeErr)
	}

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
					cfg.Diagnostics.State(tableName, "missing", "not_run", 0)
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
						Skipped:       true,
						InsertedRows:  0,
						ProcessedRows: 0,
						ErrorCount:    0,
						ErrorMessage:  "",
					}
					continue
				}

				// 开始跟踪
				tracker.StartTable(tableName, csvPath, false)

				importStart := time.Now()
				result, err, diag := dataImporter.ImportTable(tableName)
				elapsed := time.Since(importStart)
				if err != nil {
					logger.Errorf("[Worker %d] Failed to import table %s: %v", workerID, tableName, err)
					logImportDiagnostic(diag)
					failed := &importer.ImportResult{
						TableName:    tableName,
						Success:      false,
						ErrorMessage: err.Error(),
					}
					if result != nil {
						failed.ProcessedRows = result.ProcessedRows
						failed.InsertedRows = result.InsertedRows
						failed.ErrorCount = result.ErrorCount
						if failed.ErrorCount == 0 {
							failed.ErrorCount = 1
						}
					}
					finalizeImportedTable(cfg, conn, tracker, migrationCtx, sink, failed, elapsed)
					if cfg.Migration.IsFastFail() {
						stopErr := fmt.Errorf("failed to import table %s: %w", tableName, err)
						migrationCtx.Stop(stopErr)
						failed.ErrorMessage = stopErr.Error()
						resultChan <- failed
						return
					}
					resultChan <- failed
					continue
				}

				finalizeImportedTable(cfg, conn, tracker, migrationCtx, sink, result, elapsed)
				if result.Success && result.ErrorCount == 0 {
					logger.Infof("[Worker %d] Table imported: %s (%d rows)", workerID, tableName, result.InsertedRows)
				} else {
					logger.Warnf("[Worker %d] Table partially imported: %s (%d rows, %d errors)",
						workerID, tableName, result.InsertedRows, result.ErrorCount)
				}
				resultChan <- result
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
		if result.Skipped {
			continue
		}
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

	writeErr := sink.writeLists()
	return errors.Join(finalError(failCount, "tables failed to import", migrationCtx), writeErr)
}

func importDataAdaptive(
	conn *database.Connection,
	cfg *config.Config,
	tablesToImport []string,
	csvTableMap map[string]string,
	tableMatcher matcher.TableNameMatcher,
	tracker *progress.Tracker,
	migrationCtx *migration.MigrationContext,
	dataImporter *importer.DataImporter,
	sink *importCompletionSink,
) ([]*importer.ImportResult, []string, error) {
	candidates, skipped := buildImportCandidates(tablesToImport, csvTableMap, tableMatcher, cfg)
	results := make([]*importer.ImportResult, 0, len(tablesToImport))

	for _, skippedTable := range skipped {
		if skippedTable.Err != nil {
			cfg.Diagnostics.State(skippedTable.TableName, "failed", "not_run", 0)
			cfg.Diagnostics.Error(skippedTable.TableName, diagnostics.Metadata, skippedTable.Err)
			tracker.FailTable(skippedTable.TableName, skippedTable.Reason)
			tracker.FailPhaseItem()
			sink.addFailed(skippedTable.TableName)
			results = append(results, &importer.ImportResult{TableName: skippedTable.TableName, Success: false, ErrorCount: 1, ErrorMessage: skippedTable.Reason})
			if cfg.Migration.IsFastFail() {
				migrationCtx.Stop(skippedTable.Err)
				break
			}
			continue
		}
		cfg.Diagnostics.State(skippedTable.TableName, "missing", "not_run", 0)
		_, _, diag := dataImporter.FindCSVFile(skippedTable.TableName)
		logImportDiagnostic(diag)
		if err := tracker.SkipTable(skippedTable.TableName, skippedTable.Reason); err != nil {
			logger.Warnf("Failed to mark table %s as skipped: %v", skippedTable.TableName, err)
		}
		tracker.SkipPhaseItem()
		results = append(results, &importer.ImportResult{
			TableName:     skippedTable.TableName,
			Success:       true,
			Skipped:       true,
			InsertedRows:  0,
			ProcessedRows: 0,
			ErrorCount:    0,
		})
	}

	limiter := importer.NewAdaptiveLimiter(
		cfg.Migration.EffectiveImportTokens(),
		cfg.Migration.EffectiveMinImportTokens(),
		time.Duration(cfg.Migration.EffectiveRecoveryWindowSeconds())*time.Second,
		time.Now,
	)
	dataImporter.WithPressureCallback(func(event importer.ImportPressureEvent) {
		limiter.RecordPressure(event)
		cfg.Diagnostics.Scheduling(limiter.UsedTokens(), limiter.DynamicLimit(), string(event.Signal), false)
		logger.Warnf("Adaptive import pressure: table=%s batch=%d signal=%s detail=%s dynamic_tokens=%d",
			event.TableName, event.BatchNum, event.Signal, event.Detail, limiter.DynamicLimit())
	})

	maxWorkers := cfg.Migration.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	workerSlots := make(chan struct{}, maxWorkers)
	resultChan := make(chan *importer.ImportResult, len(candidates))
	var wg sync.WaitGroup

	for _, candidate := range candidates {
		if migrationCtx.Err() != nil {
			break
		}
		recovered := limiter.TryRecover()
		tokenDone := cfg.Diagnostics.TableStats(candidate.TableName).Start(diagnostics.TokenWait)
		acquireErr := limiter.Acquire(migrationCtx.Context(), candidate.Weight)
		tokenDone()
		cfg.Diagnostics.Scheduling(limiter.UsedTokens(), limiter.DynamicLimit(), "", recovered)
		if acquireErr != nil {
			break
		}
		workerSlots <- struct{}{}
		wg.Add(1)
		go func(candidate importer.ImportCandidate) {
			defer wg.Done()
			defer func() {
				limiter.Release(candidate.Weight)
				cfg.Diagnostics.Scheduling(limiter.UsedTokens(), limiter.DynamicLimit(), "", false)
			}()
			defer func() { <-workerSlots }()

			logger.Infof("Adaptive import start: table=%s class=%s size_mb=%d weight=%d dynamic_tokens=%d",
				candidate.TableName, candidate.Class, candidate.SizeMB, candidate.Weight, limiter.DynamicLimit())

			if err := tracker.StartTable(candidate.TableName, candidate.CSVPath, false); err != nil {
				logger.Warnf("Failed to start tracking table %s: %v", candidate.TableName, err)
			}
			importStart := time.Now()
			result, err, diag := dataImporter.ImportTable(candidate.TableName)
			elapsed := time.Since(importStart)
			if err != nil {
				logger.Errorf("Failed to import table %s: %v", candidate.TableName, err)
				logImportDiagnostic(diag)
				failed := &importer.ImportResult{TableName: candidate.TableName, Success: false, ErrorMessage: err.Error()}
				if result != nil {
					failed.ProcessedRows = result.ProcessedRows
					failed.InsertedRows = result.InsertedRows
					failed.ErrorCount = result.ErrorCount
					if failed.ErrorCount == 0 {
						failed.ErrorCount = 1
					}
				}
				finalizeImportedTable(cfg, conn, tracker, migrationCtx, sink, failed, elapsed)
				resultChan <- failed
				if cfg.Migration.IsFastFail() {
					migrationCtx.Stop(fmt.Errorf("failed to import table %s: %w", candidate.TableName, err))
				}
				return
			}

			finalizeImportedTable(cfg, conn, tracker, migrationCtx, sink, result, elapsed)
			resultChan <- result
		}(candidate)
	}

	wg.Wait()
	close(resultChan)

	var failedTables []string
	for result := range resultChan {
		results = append(results, result)
		if result.Skipped {
			continue
		}
		if !result.Success || result.ErrorCount > 0 {
			failedTables = append(failedTables, result.TableName)
		}
	}
	return results, failedTables, finalError(len(failedTables), "tables failed to import", migrationCtx)
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
func buildCSVTableMap(csvFiles []string, timestamp string, tableMatcher matcher.TableNameMatcher) (map[string]string, error) {
	tableMap := make(map[string]string)

	for _, csvPath := range csvFiles {
		fileName := filepath.Base(csvPath)
		tableName := extractTableNameFromFile(fileName, timestamp)
		if tableName == "" {
			continue
		}
		key := tableMatcher.Key(tableName)
		if existing, ok := tableMap[key]; ok {
			return nil, fmt.Errorf("CSV table name conflict under current case-sensitivity setting: %s and %s", existing, csvPath)
		}
		tableMap[key] = csvPath
	}

	return tableMap, nil
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

	total := recorder.GetErrorCount()
	if total == 0 {
		return
	}
	errors := recorder.GetErrors()

	logger.Warn("=== Error Summary ===")
	logger.Warnf("Total errors recorded: %d (retained samples: %d)", total, len(errors))

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

	logger.Warn("Error types breakdown (retained samples only):")
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
	csvTableMap, err := buildCSVTableMap(csvFiles, cfg.Source.CSVTimestamp, tableMatcher)
	if err != nil {
		return err
	}
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
