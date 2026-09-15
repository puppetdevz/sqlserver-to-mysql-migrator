package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func writeConfigForTest(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestTableNameCaseSensitiveDefaultsToTrueWhenOmitted(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Migration.IsTableNameCaseSensitive() {
		t.Fatal("IsTableNameCaseSensitive() = false, want true when omitted")
	}
}

func TestGetDSNInitializesNetworkTimeoutsPerConnection(t *testing.T) {
	cfg := TargetConfig{
		Host:         "127.0.0.1",
		Port:         3306,
		Database:     "migration_example",
		User:         "root",
		Password:     "pass",
		Charset:      "utf8mb4",
		WriteTimeout: 600,
		ReadTimeout:  700,
	}

	driverCfg, err := cfg.DriverConfig()
	if err != nil {
		t.Fatal(err)
	}
	if driverCfg.WriteTimeout != 600*time.Second || driverCfg.ReadTimeout != 700*time.Second {
		t.Fatalf("timeouts write=%s read=%s", driverCfg.WriteTimeout, driverCfg.ReadTimeout)
	}
	dsn := cfg.GetDSN()
	for _, want := range []string{
		"net_write_timeout=600",
		"net_read_timeout=700",
		"innodb_strict_mode=OFF",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("GetDSN() = %q, want it to contain %q", dsn, want)
		}
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.WriteTimeout != 600*time.Second || parsed.ReadTimeout != 700*time.Second {
		t.Fatalf("parsed timeouts write=%s read=%s", parsed.WriteTimeout, parsed.ReadTimeout)
	}
}

func TestDriverConfigEscapesPasswordAndRejectsBadCharset(t *testing.T) {
	cfg := TargetConfig{Host: "h", Port: 3306, Database: "db", User: "u", Password: "p@ss:w/d", Charset: "utf8mb4"}
	driverCfg, err := cfg.DriverConfig()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mysql.ParseDSN(driverCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Passwd != "p@ss:w/d" || parsed.User != "u" || parsed.DBName != "db" {
		t.Fatalf("parsed %+v", parsed)
	}
	if parsed.Params["innodb_strict_mode"] != "OFF" {
		t.Fatalf("session whitelist missing: %+v", parsed.Params)
	}
	bad := cfg
	bad.Charset = "utf8mb4;drop"
	if _, err := bad.DriverConfig(); err == nil {
		t.Fatal("invalid charset accepted")
	}
}

func TestTableNameCaseSensitivePreservesExplicitFalse(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  table_name_case_sensitive: false
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Migration.IsTableNameCaseSensitive() {
		t.Fatal("IsTableNameCaseSensitive() = true, want false when explicitly configured")
	}
}

func TestCSVHasHeaderDefaultsToTrue(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Source.CSVHasHeader == nil || !*cfg.Source.CSVHasHeader {
		t.Fatal("CSVHasHeader = nil or false, want true when omitted")
	}
}

func TestFastFailDefaultsToTrue(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Migration.FastFail == nil || !*cfg.Migration.FastFail {
		t.Fatal("FastFail = nil or false, want true when omitted")
	}
}

func TestCountCSVRowsBeforeImportDefaultsToTrue(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Migration.ShouldCountCSVRowsBeforeImport() {
		t.Fatal("ShouldCountCSVRowsBeforeImport() = false, want true when omitted")
	}
}

func TestCountCSVRowsBeforeImportPreservesExplicitFalse(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  count_csv_rows_before_import: false
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Migration.ShouldCountCSVRowsBeforeImport() {
		t.Fatal("ShouldCountCSVRowsBeforeImport() = true, want false when explicitly configured")
	}
}

func TestLoadWithoutLocalConfig(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Target.Host != "localhost" {
		t.Fatalf("Target.Host = %s, want localhost", cfg.Target.Host)
	}

	baseAbs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Abs(path) error = %v", err)
	}
	if got := cfg.ConfigFilesSummary(); got != baseAbs {
		t.Fatalf("ConfigFilesSummary() = %q, want %q", got, baseAbs)
	}
}

func TestLoadWithLocalScalarOverride(t *testing.T) {
	base := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
  password: "base-pass"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	// 写入 local 文件
	local := writeConfigForTest(t, `
target:
  password: "real-secret"
`)
	// 重命名为对应的 .local.yaml 路径
	localPath := base[:len(base)-len(".yaml")] + ".local.yaml"
	if err := os.Rename(local, localPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Target.Password != "real-secret" {
		t.Fatalf("Target.Password = %s, want real-secret", cfg.Target.Password)
	}
	if cfg.Target.Host != "localhost" {
		t.Fatalf("Target.Host = %s, want localhost (should not be overwritten)", cfg.Target.Host)
	}
}

func TestLoadWithLocalSliceReplace(t *testing.T) {
	base := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
  skip_tables: [table_a, table_b]
logging:
  level: "INFO"
`)

	local := writeConfigForTest(t, `
migration:
  skip_tables: [table_c]
`)
	localPath := base[:len(base)-len(".yaml")] + ".local.yaml"
	if err := os.Rename(local, localPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Migration.SkipTables) != 1 || cfg.Migration.SkipTables[0] != "table_c" {
		t.Fatalf("SkipTables = %v, want [table_c] (replaced, not merged)", cfg.Migration.SkipTables)
	}
}

func TestLoadWithLocalNestedMerge(t *testing.T) {
	base := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
  password: "base-pass"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	local := writeConfigForTest(t, `
target:
  password: "real-secret"
  write_timeout: 60
converter:
  max_varchar_to_text_size: 300
`)
	localPath := base[:len(base)-len(".yaml")] + ".local.yaml"
	if err := os.Rename(local, localPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// base fields preserved
	if cfg.Target.Host != "localhost" {
		t.Fatalf("Target.Host = %s, want localhost", cfg.Target.Host)
	}
	// local scalar override
	if cfg.Target.Password != "real-secret" {
		t.Fatalf("Target.Password = %s, want real-secret", cfg.Target.Password)
	}
	// local added new key to nested map
	if cfg.Target.WriteTimeout != 60 {
		t.Fatalf("Target.WriteTimeout = %d, want 60", cfg.Target.WriteTimeout)
	}
	// local added new top-level section
	if cfg.Converter.MaxVarcharToTextSize != 300 {
		t.Fatalf("Converter.MaxVarcharToTextSize = %d, want 300", cfg.Converter.MaxVarcharToTextSize)
	}
}

func TestLoadBaseConfigNotFound(t *testing.T) {
	_, err := Load("nonexistent.yaml")
	if err == nil {
		t.Fatal("Load() should return error for nonexistent base config")
	}
}

func TestSlowTableThresholdMinutesDefaultsTo15(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Migration.EffectiveSlowTableThresholdMinutes() != 15 {
		t.Fatalf("EffectiveSlowTableThresholdMinutes() = %d, want 15 when omitted",
			cfg.Migration.EffectiveSlowTableThresholdMinutes())
	}
}

func TestSlowTableThresholdMinutesPreservesExplicitValue(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  slow_table_threshold_minutes: 30
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Migration.EffectiveSlowTableThresholdMinutes() != 30 {
		t.Fatalf("EffectiveSlowTableThresholdMinutes() = %d, want 30",
			cfg.Migration.EffectiveSlowTableThresholdMinutes())
	}
}

func TestSlowTableThresholdMinutesZeroReturnsDefault(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  slow_table_threshold_minutes: 0
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Migration.EffectiveSlowTableThresholdMinutes() != 15 {
		t.Fatalf("EffectiveSlowTableThresholdMinutes() = %d, want 15 when zero",
			cfg.Migration.EffectiveSlowTableThresholdMinutes())
	}
}

func TestAdaptiveImportDefaults(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 4
  on_duplicate: "ignore"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Migration.ShouldUseAdaptiveImport() {
		t.Fatal("ShouldUseAdaptiveImport() = false, want true by default")
	}
	if got := cfg.Migration.EffectiveImportTokens(); got != 10 {
		t.Fatalf("EffectiveImportTokens() = %d, want 10", got)
	}
	if got := cfg.Migration.EffectiveMinImportTokens(); got != 2 {
		t.Fatalf("EffectiveMinImportTokens() = %d, want 2", got)
	}
	if got := cfg.Migration.EffectiveLargeTableMB(); got != 1024 {
		t.Fatalf("EffectiveLargeTableMB() = %d, want 1024", got)
	}
	if got := cfg.Migration.EffectiveHugeTableMB(); got != 5120 {
		t.Fatalf("EffectiveHugeTableMB() = %d, want 5120", got)
	}
	if got := cfg.Migration.EffectiveSlowBatchSeconds(); got != 10 {
		t.Fatalf("EffectiveSlowBatchSeconds() = %d, want 10", got)
	}
	if got := cfg.Migration.EffectiveRecoveryWindowSeconds(); got != 60 {
		t.Fatalf("EffectiveRecoveryWindowSeconds() = %d, want 60", got)
	}
	if !cfg.Migration.ShouldValidateRowCount() {
		t.Fatal("ShouldValidateRowCount() = false, want true by default")
	}
}

func TestAdaptiveImportExplicitConfig(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  adaptive_import:
    enabled: false
    import_tokens: 7
    min_tokens: 3
    large_table_mb: 2048
    huge_table_mb: 8192
    slow_batch_seconds: 15
    recovery_window_seconds: 90
  row_count_validation: false
  batch_size: 100
  max_workers: 4
  on_duplicate: "ignore"
logging:
  level: "INFO"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Migration.ShouldUseAdaptiveImport() {
		t.Fatal("ShouldUseAdaptiveImport() = true, want false")
	}
	if got := cfg.Migration.EffectiveImportTokens(); got != 7 {
		t.Fatalf("EffectiveImportTokens() = %d, want 7", got)
	}
	if got := cfg.Migration.EffectiveMinImportTokens(); got != 3 {
		t.Fatalf("EffectiveMinImportTokens() = %d, want 3", got)
	}
	if got := cfg.Migration.EffectiveLargeTableMB(); got != 2048 {
		t.Fatalf("EffectiveLargeTableMB() = %d, want 2048", got)
	}
	if got := cfg.Migration.EffectiveHugeTableMB(); got != 8192 {
		t.Fatalf("EffectiveHugeTableMB() = %d, want 8192", got)
	}
	if got := cfg.Migration.EffectiveSlowBatchSeconds(); got != 15 {
		t.Fatalf("EffectiveSlowBatchSeconds() = %d, want 15", got)
	}
	if got := cfg.Migration.EffectiveRecoveryWindowSeconds(); got != 90 {
		t.Fatalf("EffectiveRecoveryWindowSeconds() = %d, want 90", got)
	}
	if cfg.Migration.ShouldValidateRowCount() {
		t.Fatal("ShouldValidateRowCount() = true, want false")
	}
}

func TestAdaptiveImportRejectsHugeTableSmallerThanLargeTable(t *testing.T) {
	path := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  adaptive_import:
    large_table_mb: 5120
    huge_table_mb: 1024
  batch_size: 100
  max_workers: 4
  on_duplicate: "ignore"
logging:
  level: "INFO"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid adaptive import threshold error")
	}

	want := "migration.adaptive_import.huge_table_mb must be greater than or equal to migration.adaptive_import.large_table_mb"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Load() error = %v, want it to contain %q", err, want)
	}
}

func TestAdaptiveImportMinTokensClampedToImportTokens(t *testing.T) {
	cfg := MigrationConfig{
		AdaptiveImport: AdaptiveImportConfig{
			ImportTokens: 2,
			MinTokens:    8,
		},
	}

	if got := cfg.EffectiveMinImportTokens(); got != 2 {
		t.Fatalf("EffectiveMinImportTokens() = %d, want 2", got)
	}
}

func TestExpandLogFilePatternReplacesDateTimeTokens(t *testing.T) {
	now := time.Date(2026, 6, 5, 13, 39, 8, 0, time.Local)

	got := ExpandLogFilePattern("logs/migration.{yyyy}.{MMdd}.{HHmmss}.log", now)
	want := "logs/migration.2026.0605.133908.log"
	if got != want {
		t.Fatalf("ExpandLogFilePattern() = %q, want %q", got, want)
	}
}

func TestExpandLogFilePatternKeepsUnknownTokens(t *testing.T) {
	now := time.Date(2026, 6, 5, 13, 39, 8, 0, time.Local)

	got := ExpandLogFilePattern("migration.{yyyy}.{unknown}.log", now)
	want := "migration.2026.{unknown}.log"
	if got != want {
		t.Fatalf("ExpandLogFilePattern() = %q, want %q", got, want)
	}
}

func TestLoadRecordsEffectiveConfigFilesInOverrideOrder(t *testing.T) {
	base := writeConfigForTest(t, `
source:
  ddl_file: "ddl.sql"
  csv_directory: "csv"
target:
  host: "localhost"
  port: 3306
  database: "migration_example"
  user: "root"
migration:
  batch_size: 100
  max_workers: 1
  on_duplicate: "replace"
logging:
  level: "INFO"
`)

	local := writeConfigForTest(t, `
target:
  password: "real-secret"
`)
	localPath := base[:len(base)-len(".yaml")] + ".local.yaml"
	if err := os.Rename(local, localPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		t.Fatalf("Abs(base) error = %v", err)
	}
	localAbs, err := filepath.Abs(localPath)
	if err != nil {
		t.Fatalf("Abs(localPath) error = %v", err)
	}

	gotFiles := cfg.ConfigFiles()
	wantFiles := []string{localAbs, baseAbs}
	if len(gotFiles) != len(wantFiles) {
		t.Fatalf("ConfigFiles() = %v, want %v", gotFiles, wantFiles)
	}
	for i := range wantFiles {
		if gotFiles[i] != wantFiles[i] {
			t.Fatalf("ConfigFiles()[%d] = %q, want %q", i, gotFiles[i], wantFiles[i])
		}
	}

	gotSummary := cfg.ConfigFilesSummary()
	wantSummary := localAbs + " > " + baseAbs
	if gotSummary != wantSummary {
		t.Fatalf("ConfigFilesSummary() = %q, want %q", gotSummary, wantSummary)
	}
}
