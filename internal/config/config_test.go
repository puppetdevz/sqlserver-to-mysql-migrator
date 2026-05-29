package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	dsn := cfg.GetDSN()

	for _, want := range []string{
		"writeTimeout=600s",
		"readTimeout=700s",
		"net_write_timeout=600",
		"net_read_timeout=700",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("GetDSN() = %q, want it to contain %q", dsn, want)
		}
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
