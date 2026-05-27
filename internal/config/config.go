package config

import (
	"fmt"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
)

// Config 全局配置结构
type Config struct {
	Source    SourceConfig    `yaml:"source"`
	Target    TargetConfig    `yaml:"target"`
	Migration MigrationConfig `yaml:"migration"`
	Logging   LoggingConfig   `yaml:"logging"`
	Converter ConverterConfig `yaml:"converter"`
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

// MigrationConfig 迁移配置
type MigrationConfig struct {
	FastFail               *bool    `yaml:"fast_fail"` // 遇错即停（true）或记录错误跳过（false）
	TableNameCaseSensitive *bool    `yaml:"table_name_case_sensitive"`
	BatchSize              int      `yaml:"batch_size"`
	MaxWorkers             int      `yaml:"max_workers"`
	OnDuplicate            string   `yaml:"on_duplicate"`       // "replace" or "ignore"
	MaxRowsPerTable        int      `yaml:"max_rows_per_table"` // 每表最大导入行数，0 表示不限制
	SkipTables             []string `yaml:"skip_tables"`        // 要跳过的表名列表
	MaxBatchBytes          int      `yaml:"max_batch_bytes"`    // 单批次最大字节数（估算），默认 32MB
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
	}
	if t.EffectiveReadTimeout() > 0 {
		dsn += fmt.Sprintf("&readTimeout=%ds", t.EffectiveReadTimeout())
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

// CLIArgs holds CLI flag values for diagnostic logging.
type CLIArgs struct {
	Tables        string
	CreateOnly    bool
	DryRun        bool
	RemovePostfix string
}

// LogEffective prints all effective configuration to the logger.
func LogEffective(cfg *Config, cli CLIArgs) {
	logger.Info("=== Effective Configuration ===")

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

	logger.Info("[migration]")
	logger.Infof("  fast_fail: %t", cfg.Migration.IsFastFail())
	logger.Infof("  table_name_case_sensitive: %t", cfg.Migration.IsTableNameCaseSensitive())
	logger.Infof("  batch_size: %d", cfg.Migration.BatchSize)
	logger.Infof("  max_workers: %d", cfg.Migration.MaxWorkers)
	logger.Infof("  on_duplicate: %s", cfg.Migration.OnDuplicate)
	logger.Infof("  max_rows_per_table: %d", cfg.Migration.MaxRowsPerTable)
	if len(cfg.Migration.SkipTables) > 0 {
		logger.Infof("  skip_tables: %v", cfg.Migration.SkipTables)
	} else {
		logger.Info("  skip_tables: (none)")
	}
	logger.Infof("  max_batch_bytes: %s", cfg.Migration.EffectiveMaxBatchBytes())

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
