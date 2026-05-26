package config

import (
	"fmt"
	"time"
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
	TruncateBeforeImport   bool     `yaml:"truncate_before_import"`
	CreateMissingTables    bool     `yaml:"create_missing_tables"`
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
