package config

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/diagnostics"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
)

// Config 全局配置结构
type Config struct {
	Source    SourceConfig    `yaml:"source"`
	Target    TargetConfig    `yaml:"target"`
	Migration MigrationConfig `yaml:"migration"`
	Logging   LoggingConfig   `yaml:"logging"`
	Converter ConverterConfig `yaml:"converter"`

	// Diagnostics is runtime-only and must never be serialized as effective configuration.
	Diagnostics *diagnostics.Recorder `yaml:"-" json:"-"`
	budgetOnce  sync.Once
	budget      *migration.Budget
	budgetErr   error
	configFiles []string
}

// SourceConfig 源数据配置
type SourceConfig struct {
	DDLFile      string `yaml:"ddl_file"`
	CSVDirectory string `yaml:"csv_directory"`
	CSVTimestamp string `yaml:"csv_timestamp"`
	CSVHasHeader *bool  `yaml:"csv_has_header"` // CSV 文件是否包含表头，默认 true
}

// TargetConfig 目标数据库配置
type TargetConfig struct {
	Host            string `yaml:"host"`
	Port            int    `yaml:"port"`
	Database        string `yaml:"database"`
	User            string `yaml:"user"`
	Password        string `yaml:"password"`
	Charset         string `yaml:"charset"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime int    `yaml:"conn_max_lifetime"` // 秒
	WriteTimeout    int    `yaml:"write_timeout"`     // 秒，默认 30s
	ReadTimeout     int    `yaml:"read_timeout"`      // 秒，默认 30s
}

// AdaptiveImportConfig controls weighted scheduling for large CSV imports.
type AdaptiveImportConfig struct {
	Enabled               *bool `yaml:"enabled"`
	ImportTokens          int   `yaml:"import_tokens"`
	MinTokens             int   `yaml:"min_tokens"`
	LargeTableMB          int   `yaml:"large_table_mb"`
	HugeTableMB           int   `yaml:"huge_table_mb"`
	SlowBatchSeconds      int   `yaml:"slow_batch_seconds"`
	RecoveryWindowSeconds int   `yaml:"recovery_window_seconds"`
}

// MigrationConfig 迁移配置
type MigrationConfig struct {
	Resources                 ResourceConfig       `yaml:"resources"`
	FastFail                  *bool                `yaml:"fast_fail"` // 遇错即停（true）或记录错误跳过（false）
	TableNameCaseSensitive    *bool                `yaml:"table_name_case_sensitive"`
	CountCSVRowsBeforeImport  *bool                `yaml:"count_csv_rows_before_import"`
	BatchSize                 int                  `yaml:"batch_size"`
	MaxWorkers                int                  `yaml:"max_workers"`
	AdaptiveImport            AdaptiveImportConfig `yaml:"adaptive_import"`
	RowCountValidation        *bool                `yaml:"row_count_validation"`
	OnDuplicate               string               `yaml:"on_duplicate"`                 // "replace" or "ignore"
	MaxRowsPerTable           int                  `yaml:"max_rows_per_table"`           // 每表最大导入行数，0 表示不限制
	SkipTables                []string             `yaml:"skip_tables"`                  // 要跳过的表名列表
	MaxBatchBytes             int                  `yaml:"max_batch_bytes"`              // 单批次最大字节数（估算），默认 32MB
	SlowTableThresholdMinutes int                  `yaml:"slow_table_threshold_minutes"` // 慢表耗时阈值（分钟），默认 15
}

// LoggingConfig 日志配置
type LoggingConfig struct {
	Level      string `yaml:"level"`
	File       string `yaml:"file"`
	Console    bool   `yaml:"console"`
	MaxSize    int    `yaml:"max_size"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAge     int    `yaml:"max_age"`
}

// ConfigFiles returns the effective configuration files in override order.
func (c *Config) ConfigFiles() []string {
	if c == nil {
		return nil
	}
	files := make([]string, len(c.configFiles))
	copy(files, c.configFiles)
	return files
}

// ConfigFilesSummary returns effective configuration files as "higher > lower".
func (c *Config) ConfigFilesSummary() string {
	return strings.Join(c.ConfigFiles(), " > ")
}

// ExpandLogFilePattern expands date/time tokens in logging.file.
func ExpandLogFilePattern(pattern string, now time.Time) string {
	if pattern == "" {
		return ""
	}

	var out strings.Builder
	for i := 0; i < len(pattern); {
		if pattern[i] != '{' {
			out.WriteByte(pattern[i])
			i++
			continue
		}

		end := strings.IndexByte(pattern[i+1:], '}')
		if end < 0 {
			out.WriteString(pattern[i:])
			break
		}
		end += i + 1
		token := pattern[i+1 : end]
		if expanded, ok := expandLogFileToken(token, now); ok {
			out.WriteString(expanded)
		} else {
			out.WriteString(pattern[i : end+1])
		}
		i = end + 1
	}
	return out.String()
}

func expandLogFileToken(token string, now time.Time) (string, bool) {
	if token == "" {
		return "", false
	}

	replacements := []struct {
		token string
		value string
	}{
		{"yyyy", now.Format("2006")},
		{"yy", now.Format("06")},
		{"MM", now.Format("01")},
		{"M", fmt.Sprintf("%d", int(now.Month()))},
		{"dd", now.Format("02")},
		{"d", fmt.Sprintf("%d", now.Day())},
		{"HH", now.Format("15")},
		{"mm", now.Format("04")},
		{"ss", now.Format("05")},
	}

	var out strings.Builder
	for i := 0; i < len(token); {
		matched := false
		for _, replacement := range replacements {
			if strings.HasPrefix(token[i:], replacement.token) {
				out.WriteString(replacement.value)
				i += len(replacement.token)
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		if (token[i] >= 'A' && token[i] <= 'Z') || (token[i] >= 'a' && token[i] <= 'z') {
			return "", false
		}
		out.WriteByte(token[i])
		i++
	}

	return out.String(), true
}

// ConverterConfig 类型转换器配置
type ConverterConfig struct {
	MaxVarcharToTextColumns  int `yaml:"max_varchar_to_text_columns"`  // 默认 10
	MaxNvarcharToTextColumns int `yaml:"max_nvarchar_to_text_columns"` // 默认 10
	MaxNvarcharToTextSize    int `yaml:"max_nvarchar_to_text_size"`    // 默认 500
	MaxVarcharToTextSize     int `yaml:"max_varchar_to_text_size"`     // 默认 500
}

// IsEffectiveMaxVarcharToTextColumns 返回有效阈值（0 时使用默认值 10）
func (c ConverterConfig) IsEffectiveMaxVarcharToTextColumns() int {
	if c.MaxVarcharToTextColumns <= 0 {
		return 10
	}
	return c.MaxVarcharToTextColumns
}

// IsEffectiveMaxNvarcharToTextColumns 返回有效阈值（0 时使用默认值 10）
func (c ConverterConfig) IsEffectiveMaxNvarcharToTextColumns() int {
	if c.MaxNvarcharToTextColumns <= 0 {
		return 10
	}
	return c.MaxNvarcharToTextColumns
}

// IsEffectiveMaxNvarcharToTextSize 返回有效阈值（0 时使用默认值 500）
func (c ConverterConfig) IsEffectiveMaxNvarcharToTextSize() int {
	if c.MaxNvarcharToTextSize <= 0 {
		return 500
	}
	return c.MaxNvarcharToTextSize
}

// IsEffectiveMaxVarcharToTextSize 返回有效阈值（0 时使用默认值 500）
func (c ConverterConfig) IsEffectiveMaxVarcharToTextSize() int {
	if c.MaxVarcharToTextSize <= 0 {
		return 500
	}
	return c.MaxVarcharToTextSize
}

// GetDSN 生成 MySQL DSN 连接字符串
func (t *TargetConfig) GetDSN() string {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=true&loc=Local",
		t.User, t.Password, t.Host, t.Port, t.Database, t.Charset)
	if t.EffectiveWriteTimeout() > 0 {
		dsn += fmt.Sprintf("&writeTimeout=%ds", t.EffectiveWriteTimeout())
		dsn += fmt.Sprintf("&net_write_timeout=%d", t.EffectiveNetWriteTimeout())
	}
	if t.EffectiveReadTimeout() > 0 {
		dsn += fmt.Sprintf("&readTimeout=%ds", t.EffectiveReadTimeout())
		dsn += fmt.Sprintf("&net_read_timeout=%d", t.EffectiveNetReadTimeout())
	}
	return dsn
}

// EffectiveWriteTimeout 返回有效的写超时（秒），0 时返回默认值 30
func (t *TargetConfig) EffectiveWriteTimeout() int {
	if t.WriteTimeout <= 0 {
		return 30
	}
	return t.WriteTimeout
}

// EffectiveReadTimeout 返回有效的读超时（秒），0 时返回默认值 30
func (t *TargetConfig) EffectiveReadTimeout() int {
	if t.ReadTimeout <= 0 {
		return 30
	}
	return t.ReadTimeout
}

// minNetTimeoutSeconds is the floor for session-level net_write_timeout/net_read_timeout.
// MySQL pooled connections need a long enough session timeout to survive idle periods between batches.
const minNetTimeoutSeconds = 600

// EffectiveNetWriteTimeout returns the session net_write_timeout for every pooled MySQL connection.
func (t *TargetConfig) EffectiveNetWriteTimeout() int {
	return effectiveNetTimeout(t.EffectiveWriteTimeout())
}

// EffectiveNetReadTimeout returns the session net_read_timeout for every pooled MySQL connection.
func (t *TargetConfig) EffectiveNetReadTimeout() int {
	return effectiveNetTimeout(t.EffectiveReadTimeout())
}

func effectiveNetTimeout(base int) int {
	if base < minNetTimeoutSeconds {
		return minNetTimeoutSeconds
	}
	return base
}

// GetConnMaxLifetime 获取连接最大生命周期
func (t *TargetConfig) GetConnMaxLifetime() time.Duration {
	return time.Duration(t.ConnMaxLifetime) * time.Second
}

// IsTableNameCaseSensitive returns the effective table-name matching mode.
func (m MigrationConfig) IsTableNameCaseSensitive() bool {
	if m.TableNameCaseSensitive == nil {
		return true
	}
	return *m.TableNameCaseSensitive
}

// IsFastFail returns the effective fast_fail value.
func (m MigrationConfig) IsFastFail() bool {
	if m.FastFail == nil {
		return true
	}
	return *m.FastFail
}

// ShouldCountCSVRowsBeforeImport returns whether imports should pre-scan CSV files for progress totals.
func (m MigrationConfig) ShouldCountCSVRowsBeforeImport() bool {
	if m.CountCSVRowsBeforeImport == nil {
		return true
	}
	return *m.CountCSVRowsBeforeImport
}

// ShouldUseAdaptiveImport returns whether weighted import scheduling is enabled.
func (m MigrationConfig) ShouldUseAdaptiveImport() bool {
	if m.AdaptiveImport.Enabled == nil {
		return true
	}
	return *m.AdaptiveImport.Enabled
}

// EffectiveImportTokens returns the configured DB pressure budget.
func (m MigrationConfig) EffectiveImportTokens() int {
	if m.AdaptiveImport.ImportTokens <= 0 {
		return 10
	}
	return m.AdaptiveImport.ImportTokens
}

// EffectiveMinImportTokens returns the lowest dynamic token ceiling.
func (m MigrationConfig) EffectiveMinImportTokens() int {
	importTokens := m.EffectiveImportTokens()
	minTokens := m.AdaptiveImport.MinTokens
	if minTokens <= 0 {
		minTokens = 2
	}
	if minTokens > importTokens {
		return importTokens
	}
	return minTokens
}

// EffectiveLargeTableMB returns the CSV size threshold for large tables.
func (m MigrationConfig) EffectiveLargeTableMB() int {
	if m.AdaptiveImport.LargeTableMB <= 0 {
		return 1024
	}
	return m.AdaptiveImport.LargeTableMB
}

// EffectiveHugeTableMB returns the CSV size threshold for huge tables.
func (m MigrationConfig) EffectiveHugeTableMB() int {
	if m.AdaptiveImport.HugeTableMB <= 0 {
		return 5120
	}
	return m.AdaptiveImport.HugeTableMB
}

// EffectiveSlowBatchSeconds returns the batch duration threshold for pressure signals.
func (m MigrationConfig) EffectiveSlowBatchSeconds() int {
	if m.AdaptiveImport.SlowBatchSeconds <= 0 {
		return 10
	}
	return m.AdaptiveImport.SlowBatchSeconds
}

// EffectiveRecoveryWindowSeconds returns the stable period needed before token recovery.
func (m MigrationConfig) EffectiveRecoveryWindowSeconds() int {
	if m.AdaptiveImport.RecoveryWindowSeconds <= 0 {
		return 60
	}
	return m.AdaptiveImport.RecoveryWindowSeconds
}

// ShouldValidateRowCount returns whether successful imports should be checked with COUNT(*).
func (m MigrationConfig) ShouldValidateRowCount() bool {
	if m.RowCountValidation == nil {
		return true
	}
	return *m.RowCountValidation
}

// IsCSVHasHeader returns the effective csv_has_header value.
func (s SourceConfig) IsCSVHasHeader() bool {
	if s.CSVHasHeader == nil {
		return true
	}
	return *s.CSVHasHeader
}

// EffectiveMaxBatchBytes returns the effective max_batch_bytes value.
// 0 → default 32MB, negative → no limit.
func (m MigrationConfig) EffectiveMaxBatchBytes() string {
	if m.MaxBatchBytes < 0 {
		return "unlimited"
	}
	if m.MaxBatchBytes == 0 {
		return "33554432 (default)"
	}
	return fmt.Sprintf("%d", m.MaxBatchBytes)
}

// EffectiveSlowTableThresholdMinutes returns the effective slow-table threshold in minutes.
// 0 or negative → default 15.
func (m MigrationConfig) EffectiveSlowTableThresholdMinutes() int {
	if m.SlowTableThresholdMinutes <= 0 {
		return 15
	}
	return m.SlowTableThresholdMinutes
}

// CLIArgs holds CLI flag values for diagnostic logging.
type CLIArgs struct {
	BaselineAlgorithms bool
	Tables             string
	CreateOnly         bool
	ReimportTables     bool
	ReimportTableFile  string
	DryRun             bool
	RemovePostfix      string
}

// LogEffective prints all effective configuration to the logger.
func LogEffective(cfg *Config, cli CLIArgs) {
	logger.Info("=== Effective Configuration ===")
	logger.Infof("[runtime] gomaxprocs=%d gomemlimit_bytes=%d cpus=%d os=%s arch=%s", runtime.GOMAXPROCS(0), debug.SetMemoryLimit(-1), runtime.NumCPU(), runtime.GOOS, runtime.GOARCH)
	logger.Info("  note: GOMAXPROCS is not a CPU affinity/quota; GOMEMLIMIT is a Go soft limit, not RSS")

	logger.Info("[source]")
	logger.Infof("  ddl_file: %s", cfg.Source.DDLFile)
	logger.Infof("  csv_directory: %s", cfg.Source.CSVDirectory)
	if cfg.Source.CSVTimestamp != "" {
		logger.Infof("  csv_timestamp: %s", cfg.Source.CSVTimestamp)
	}
	logger.Infof("  csv_has_header: %t", cfg.Source.IsCSVHasHeader())

	logger.Info("[target]")
	logger.Infof("  host: %s", cfg.Target.Host)
	logger.Infof("  port: %d", cfg.Target.Port)
	logger.Infof("  database: %s", cfg.Target.Database)
	logger.Infof("  user: %s", cfg.Target.User)
	logger.Infof("  password: %s", maskPassword(cfg.Target.Password))
	if cfg.Target.Charset != "" {
		logger.Infof("  charset: %s", cfg.Target.Charset)
	}
	logger.Infof("  max_open_conns: %d", cfg.Target.MaxOpenConns)
	logger.Infof("  max_idle_conns: %d", cfg.Target.MaxIdleConns)
	logger.Infof("  conn_max_lifetime: %ds", cfg.Target.ConnMaxLifetime)
	logger.Infof("  write_timeout: %ds", cfg.Target.EffectiveWriteTimeout())
	logger.Infof("  read_timeout: %ds", cfg.Target.EffectiveReadTimeout())
	logger.Infof("  net_write_timeout: %ds", cfg.Target.EffectiveNetWriteTimeout())
	logger.Infof("  net_read_timeout: %ds", cfg.Target.EffectiveNetReadTimeout())

	limits := cfg.Migration.Resources.Effective()
	logger.Info("[migration.resources]")
	if cli.BaselineAlgorithms {
		logger.Info("  enforcement: disabled in explicit P0 baseline build; count-only queue=10 (not a P1 memory-bounded strategy)")
	} else {
		logger.Infof("  max_inflight_bytes: %d", limits.MaxInflightBytes)
		logger.Infof("  max_inflight_batches: %d", limits.MaxInflightBatches)
		logger.Infof("  queue_bytes: %d", limits.QueueBytes)
		logger.Infof("  queue_batches: %d", limits.QueueBatches)
		logger.Infof("  batch_memory_bytes: %d", limits.BatchMemoryBytes)
		logger.Infof("  sql_cache_bytes: %d", limits.SQLCacheBytes)
	}
	logger.Info("[migration]")
	logger.Infof("  fast_fail: %t", cfg.Migration.IsFastFail())
	logger.Infof("  table_name_case_sensitive: %t", cfg.Migration.IsTableNameCaseSensitive())
	logger.Infof("  batch_size: %d", cfg.Migration.BatchSize)
	logger.Infof("  max_workers: %d", cfg.Migration.MaxWorkers)
	logger.Infof("  adaptive_import.enabled: %t", cfg.Migration.ShouldUseAdaptiveImport())
	logger.Infof("  adaptive_import.import_tokens: %d", cfg.Migration.EffectiveImportTokens())
	logger.Infof("  adaptive_import.min_tokens: %d", cfg.Migration.EffectiveMinImportTokens())
	logger.Infof("  adaptive_import.large_table_mb: %d", cfg.Migration.EffectiveLargeTableMB())
	logger.Infof("  adaptive_import.huge_table_mb: %d", cfg.Migration.EffectiveHugeTableMB())
	logger.Infof("  adaptive_import.slow_batch_seconds: %d", cfg.Migration.EffectiveSlowBatchSeconds())
	logger.Infof("  adaptive_import.recovery_window_seconds: %d", cfg.Migration.EffectiveRecoveryWindowSeconds())
	logger.Infof("  row_count_validation: %t", cfg.Migration.ShouldValidateRowCount())
	logger.Infof("  count_csv_rows_before_import: %t", cfg.Migration.ShouldCountCSVRowsBeforeImport())
	logger.Infof("  on_duplicate: %s", cfg.Migration.OnDuplicate)
	logger.Infof("  max_rows_per_table: %d", cfg.Migration.MaxRowsPerTable)
	if len(cfg.Migration.SkipTables) > 0 {
		logger.Infof("  skip_tables: %v", cfg.Migration.SkipTables)
	} else {
		logger.Info("  skip_tables: (none)")
	}
	logger.Infof("  max_batch_bytes: %s", cfg.Migration.EffectiveMaxBatchBytes())
	logger.Infof("  slow_table_threshold_minutes: %d", cfg.Migration.EffectiveSlowTableThresholdMinutes())

	logger.Info("[logging]")
	logger.Infof("  level: %s", cfg.Logging.Level)
	logger.Infof("  file: %s", cfg.Logging.File)
	logger.Infof("  console: %t", cfg.Logging.Console)
	logger.Infof("  max_size: %d", cfg.Logging.MaxSize)
	logger.Infof("  max_backups: %d", cfg.Logging.MaxBackups)
	logger.Infof("  max_age: %d", cfg.Logging.MaxAge)

	logger.Info("[converter]")
	logger.Infof("  max_varchar_to_text_columns: %d", cfg.Converter.IsEffectiveMaxVarcharToTextColumns())
	logger.Infof("  max_nvarchar_to_text_columns: %d", cfg.Converter.IsEffectiveMaxNvarcharToTextColumns())
	logger.Infof("  max_varchar_to_text_size: %d", cfg.Converter.IsEffectiveMaxVarcharToTextSize())
	logger.Infof("  max_nvarchar_to_text_size: %d", cfg.Converter.IsEffectiveMaxNvarcharToTextSize())

	logger.Info("[cli]")
	if cli.Tables != "" {
		logger.Infof("  tables: %s", cli.Tables)
	} else {
		logger.Info("  tables: (all)")
	}
	logger.Infof("  create_tables_only: %t", cli.CreateOnly)
	logger.Infof("  reimport_tables: %t", cli.ReimportTables)
	if cli.ReimportTableFile != "" {
		logger.Infof("  reimport_table_file: %s", cli.ReimportTableFile)
	}
	logger.Infof("  dry_run: %t", cli.DryRun)
	if cli.RemovePostfix != "" {
		logger.Infof("  remove_postfix: %s", cli.RemovePostfix)
	}

	logger.Info("===============================")
}

func maskPassword(pwd string) string {
	if pwd == "" {
		return "(empty)"
	}
	if len(pwd) <= 2 {
		return "***"
	}
	return pwd[:2] + "***"
}
