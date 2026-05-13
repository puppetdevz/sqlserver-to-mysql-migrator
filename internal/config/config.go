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
}

// SourceConfig 源数据配置
type SourceConfig struct {
	DDLFile       string `yaml:"ddl_file"`
	CSVDirectory  string `yaml:"csv_directory"`
	CSVTimestamp  string `yaml:"csv_timestamp"`
	CSVHasHeader  bool   `yaml:"csv_has_header"` // CSV 文件是否包含表头，默认 true
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
}

// MigrationConfig 迁移配置
type MigrationConfig struct {
	FastFail             bool   `yaml:"fast_fail"`              // 遇错即停（true）或记录错误跳过（false）
	BatchSize            int    `yaml:"batch_size"`
	MaxWorkers           int    `yaml:"max_workers"`
	EnableResume         bool   `yaml:"enable_resume"`
	TruncateBeforeImport bool   `yaml:"truncate_before_import"`
	CreateMissingTables  bool   `yaml:"create_missing_tables"`
	OnDuplicate          string `yaml:"on_duplicate"` // "replace" or "ignore"
	StateDir             string `yaml:"state_dir"`
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

// GetDSN 生成 MySQL DSN 连接字符串
func (t *TargetConfig) GetDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=true&loc=Local",
		t.User, t.Password, t.Host, t.Port, t.Database, t.Charset)
}

// GetConnMaxLifetime 获取连接最大生命周期
func (t *TargetConfig) GetConnMaxLifetime() time.Duration {
	return time.Duration(t.ConnMaxLifetime) * time.Second
}
