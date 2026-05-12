package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	_ "github.com/go-sql-driver/mysql"
)

func TestNoHeaderCSVImport(t *testing.T) {
	// Setup test
	tmpDir := os.TempDir()
	csvPath := filepath.Join(tmpDir, "test_no_header.csv")

	// Create test CSV without header
	csvContent := `1,John,2020-01-01
2,Jane,2020-01-02
3,Bob,2020-01-03`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("Failed to create test CSV: %v", err)
	}
	defer os.Remove(csvPath)

	// Connect to MySQL
	cfg := &config.Config{
		Source: config.SourceConfig{
			CSVHasHeader: false,
		},
		Migration: config.MigrationConfig{
			BatchSize:       100,
			OnDuplicate:     "replace",
		},
	}

	conn, err := database.NewConnection(&config.TargetConfig{
		Host:     "localhost",
		Port:     3306,
		Database: "migration_example",
		User:     "root",
		Password: "REDACTED_PRIVATE_CREDENTIAL",
		Charset:  "utf8mb4",
	})
	if err != nil {
		t.Skipf("Skipping test: failed to connect to MySQL: %v", err)
	}
	defer conn.Close()

	// Create test table
	_, err = conn.DB.Exec("CREATE TABLE IF NOT EXISTS test_no_header_t (id INT PRIMARY KEY, name VARCHAR(100), created_date VARCHAR(50))")
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}
	defer conn.DB.Exec("DROP TABLE IF EXISTS test_no_header_t")

	// Truncate table
	conn.DB.Exec("TRUNCATE TABLE test_no_header_t")

	// Create error recorder
	recorder, _ := NewErrorRecorder("")
	defer recorder.Close()

	// Enable debug logging for testing
	// Note: logger should be configured externally

	// Test pipelinedImport with no header
	ti := &TableImporter{
		conn:          conn,
		cfg:           cfg,
		tableName:     "test_no_header_t",
		csvPath:       csvPath,
		errorRecorder: recorder,
	}

	file, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("Failed to open CSV: %v", err)
	}
	defer file.Close()

	result, err, diag := ti.pipelinedImport(file, "test_no_header_t")
	if err != nil {
		if diag != nil {
			t.Errorf("Import failed with diagnostic: %+v", diag)
		}
		t.Fatalf("Import failed: %v", err)
	}

	if result.ProcessedRows != 3 {
		t.Errorf("Expected 3 processed rows, got %d", result.ProcessedRows)
	}
	if result.InsertedRows != 3 {
		t.Errorf("Expected 3 inserted rows, got %d", result.InsertedRows)
	}

	// Verify data
	var count int
	conn.DB.QueryRow("SELECT COUNT(*) FROM test_no_header_t").Scan(&count)
	if count != 3 {
		t.Errorf("Expected 3 rows in DB, got %d", count)
	}
}

func TestNoHeaderCSVColumnMismatch(t *testing.T) {
	// Setup test
	tmpDir := os.TempDir()
	csvPath := filepath.Join(tmpDir, "test_mismatch.csv")

	// Create test CSV with wrong number of columns (4 instead of 3)
	csvContent := `1,John,2020-01-01,extra`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("Failed to create test CSV: %v", err)
	}
	defer os.Remove(csvPath)

	cfg := &config.Config{
		Source: config.SourceConfig{
			CSVHasHeader: false,
		},
		Migration: config.MigrationConfig{
			BatchSize:   100,
			OnDuplicate: "replace",
		},
	}

	conn, err := database.NewConnection(&config.TargetConfig{
		Host:     "localhost",
		Port:     3306,
		Database: "migration_example",
		User:     "root",
		Password: "REDACTED_PRIVATE_CREDENTIAL",
		Charset:  "utf8mb4",
	})
	if err != nil {
		t.Skipf("Skipping test: failed to connect to MySQL: %v", err)
	}
	defer conn.Close()

	// Create test table (3 columns)
	conn.DB.Exec("CREATE TABLE IF NOT EXISTS test_no_header_t (id INT PRIMARY KEY, name VARCHAR(100), created_date VARCHAR(50))")
	conn.DB.Exec("TRUNCATE TABLE test_no_header_t")
	defer conn.DB.Exec("DROP TABLE IF EXISTS test_no_header_t")

	recorder2, _ := NewErrorRecorder("")
	defer recorder2.Close()
	ti2 := &TableImporter{
		conn:          conn,
		cfg:           cfg,
		tableName:     "test_no_header_t",
		csvPath:       csvPath,
		errorRecorder: recorder2,
	}

	file, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("Failed to open CSV: %v", err)
	}
	defer file.Close()

	_, err, _ = ti2.pipelinedImport(file, "test_no_header_t")
	if err == nil {
		t.Error("Expected error for column mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "column count mismatch") {
		t.Errorf("Expected 'column count mismatch' error, got: %v", err)
	}
}

func TestFirstRowEOF(t *testing.T) {
	// Setup test
	tmpDir := os.TempDir()
	csvPath := filepath.Join(tmpDir, "test_empty.csv")

	// Create empty CSV
	if err := os.WriteFile(csvPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create test CSV: %v", err)
	}
	defer os.Remove(csvPath)

	cfg := &config.Config{
		Source: config.SourceConfig{
			CSVHasHeader: false,
		},
		Migration: config.MigrationConfig{
			BatchSize:   100,
			OnDuplicate: "replace",
		},
	}

	conn, err := database.NewConnection(&config.TargetConfig{
		Host:     "localhost",
		Port:     3306,
		Database: "migration_example",
		User:     "root",
		Password: "REDACTED_PRIVATE_CREDENTIAL",
		Charset:  "utf8mb4",
	})
	if err != nil {
		t.Skipf("Skipping test: failed to connect to MySQL: %v", err)
	}
	defer conn.Close()

	recorder3, _ := NewErrorRecorder("")
	defer recorder3.Close()
	ti3 := &TableImporter{
		conn:          conn,
		cfg:           cfg,
		tableName:     "test_no_header_t",
		csvPath:       csvPath,
		errorRecorder: recorder3,
	}

	file, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("Failed to open CSV: %v", err)
	}
	defer file.Close()

	result, err, _ := ti3.pipelinedImport(file, "test_no_header_t")
	if err != nil {
		t.Errorf("Expected no error for empty file, got: %v", err)
	}
	if result.ProcessedRows != 0 {
		t.Errorf("Expected 0 processed rows, got %d", result.ProcessedRows)
	}
}

// Test for FirstRowData passed to goroutine
func TestFirstRowDataPassedToGoroutine(t *testing.T) {
	// This test verifies that firstRow is correctly passed to the goroutine
	// by checking the column count log output

	tmpDir := os.TempDir()
	csvPath := filepath.Join(tmpDir, "test_goroutine.csv")

	// Create test CSV without header: 3 columns, 2 rows
	csvContent := `1,Alice,2021-01-01
2,Bob,2021-01-02`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
		t.Fatalf("Failed to create test CSV: %v", err)
	}
	defer os.Remove(csvPath)

	cfg := &config.Config{
		Source: config.SourceConfig{
			CSVHasHeader: false,
		},
		Migration: config.MigrationConfig{
			BatchSize:   100,
			OnDuplicate: "replace",
		},
	}

	conn, err := database.NewConnection(&config.TargetConfig{
		Host:     "localhost",
		Port:     3306,
		Database: "migration_example",
		User:     "root",
		Password: "REDACTED_PRIVATE_CREDENTIAL",
		Charset:  "utf8mb4",
	})
	if err != nil {
		t.Skipf("Skipping test: failed to connect to MySQL: %v", err)
	}
	defer conn.Close()

	// Create test table
	conn.DB.Exec("CREATE TABLE IF NOT EXISTS test_goroutine_t (id INT PRIMARY KEY, name VARCHAR(100), created_date VARCHAR(50))")
	conn.DB.Exec("TRUNCATE TABLE test_goroutine_t")
	defer conn.DB.Exec("DROP TABLE IF EXISTS test_goroutine_t")

	recorder4, _ := NewErrorRecorder("")
	defer recorder4.Close()
	ti4 := &TableImporter{
		conn:          conn,
		cfg:           cfg,
		tableName:     "test_goroutine_t",
		csvPath:       csvPath,
		errorRecorder: recorder4,
	}

	file, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("Failed to open CSV: %v", err)
	}
	defer file.Close()

	result, err, _ := ti4.pipelinedImport(file, "test_goroutine_t")
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Both rows should be imported (firstRow + second row from reader)
	if result.ProcessedRows != 2 {
		t.Errorf("Expected 2 processed rows, got %d", result.ProcessedRows)
	}

	// Verify both rows
	var count int
	conn.DB.QueryRow("SELECT COUNT(*) FROM test_goroutine_t").Scan(&count)
	if count != 2 {
		t.Errorf("Expected 2 rows in DB, got %d", count)
	}
}
