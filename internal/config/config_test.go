package config

import (
	"os"
	"path/filepath"
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
