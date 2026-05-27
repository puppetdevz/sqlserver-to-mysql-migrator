package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load 从文件加载配置，自动合并同目录下的 .local.yaml 文件。
// local 文件路径由基础文件路径推导：config.yaml → config.local.yaml
func Load(configPath string) (*Config, error) {
	// 读取基础配置文件
	baseData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// 检查并加载 local 配置文件
	localPath := localPath(configPath)
	localData, err := os.ReadFile(localPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read local config file: %w", err)
	}

	// 解析 base YAML 到泛型 map
	var baseMap map[string]any
	if err := yaml.Unmarshal(baseData, &baseMap); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// 如果 local 文件存在，解析并合并
	if localData != nil {
		var localMap map[string]any
		if err := yaml.Unmarshal(localData, &localMap); err != nil {
			return nil, fmt.Errorf("failed to parse local config file: %w", err)
		}
		baseMap = mergeMaps(baseMap, localMap)
	}

	// 将合并后的 map 重新 marshal 为 YAML，再 unmarshal 到 Config 结构体
	mergedData, err := yaml.Marshal(baseMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(mergedData, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse merged config: %w", err)
	}

	// 验证配置
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func localPath(configPath string) string {
	ext := filepath.Ext(configPath)
	base := configPath[:len(configPath)-len(ext)]
	return base + ".local" + ext
}

func mergeMaps(base, override map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		if overrideMap, ok := v.(map[string]any); ok {
			if baseMap, ok := result[k].(map[string]any); ok {
				result[k] = mergeMaps(baseMap, overrideMap)
				continue
			}
		}
		result[k] = v
	}
	return result
}

func newBool(v bool) *bool {
	return &v
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

	// 验证日志配置
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "INFO" // 默认值
	}

	// CSVHasHeader 默认 true
	if cfg.Source.CSVHasHeader == nil {
		cfg.Source.CSVHasHeader = newBool(true)
	}

	// FastFail 默认 true
	if cfg.Migration.FastFail == nil {
		cfg.Migration.FastFail = newBool(true)
	}

	return nil
}
