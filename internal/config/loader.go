package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load 从文件加载配置
func Load(configPath string) (*Config, error) {
	// 读取配置文件
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// 解析 YAML
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// 验证配置
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// validate 验证配置有效性
func validate(cfg *Config) error {
	// 验证源配置
	if cfg.Source.DDLFile == "" {
		return fmt.Errorf("source.ddl_file is required")
	}
	if cfg.Source.CSVDirectory == "" {
		return fmt.Errorf("source.csv_directory is required")
	}

	// 验证目标配置
	if cfg.Target.Host == "" {
		return fmt.Errorf("target.host is required")
	}
	if cfg.Target.Port == 0 {
		return fmt.Errorf("target.port is required")
	}
	if cfg.Target.Database == "" {
		return fmt.Errorf("target.database is required")
	}
	if cfg.Target.User == "" {
		return fmt.Errorf("target.user is required")
	}

	// 验证迁移配置
	if cfg.Migration.BatchSize <= 0 {
		cfg.Migration.BatchSize = 1000 // 默认值
	}
	if cfg.Migration.MaxWorkers <= 0 {
		cfg.Migration.MaxWorkers = 4 // 默认值
	}
	if cfg.Migration.OnDuplicate != "replace" && cfg.Migration.OnDuplicate != "ignore" {
		return fmt.Errorf("migration.on_duplicate must be 'replace' or 'ignore'")
	}
	if cfg.Migration.StateDir == "" {
		cfg.Migration.StateDir = "." // 根目录
	}

	// 验证日志配置
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "INFO" // 默认值
	}

	return nil
}
