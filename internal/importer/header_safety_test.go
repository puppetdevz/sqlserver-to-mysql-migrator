package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

func TestConfiguredHeaderWithNoMatchingColumnsNeverInsertsHeaderAsData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T.csv")
	if err := os.WriteFile(path, []byte("legacy_first,legacy_second\nAlice,Bob\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("first", "text", "YES", "", nil, "").AddRow("second", "text", "YES", "", nil, ""))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore"}}
	result, err, _ := newPipelineImporter(t, db, cfg, "T", path).Import()
	if err == nil || !strings.Contains(err.Error(), "csv_has_header") {
		t.Fatalf("expected explicit no-header instruction, result=%+v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected INSERT: %v", err)
	}
}

func TestExplicitNoHeaderImportsUnmatchedFirstRowAsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("legacy_first,legacy_second\nAlice,Bob\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("first", "text", "YES", "", nil, "").AddRow("second", "text", "YES", "", nil, ""))
	mock.ExpectPrepare("INSERT IGNORE INTO `T` (`first`, `second`) VALUES (?, ?), (?, ?)").ExpectExec().WithArgs("legacy_first", "legacy_second", "Alice", "Bob").WillReturnResult(sqlmock.NewResult(0, 2))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(false)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore"}}
	result, err, _ := newPipelineImporter(t, db, cfg, "T", path).Import()
	if err != nil || result == nil || !result.Success || result.ProcessedRows != 2 {
		t.Fatalf("explicit no-header should allow positional import, result=%+v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIncompletePrefixHeaderRetainsLegacyColumnOrderFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("first,second\nAlice,Bob,extra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("first", "text", "YES", "", nil, "").AddRow("second", "text", "YES", "", nil, "").AddRow("third", "text", "YES", "", nil, ""))
	mock.ExpectPrepare("INSERT IGNORE INTO `T` (`first`, `second`, `third`) VALUES (?, ?, ?)").ExpectExec().WithArgs("Alice", "Bob", "extra").WillReturnResult(sqlmock.NewResult(0, 1))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore"}}
	result, err, _ := newPipelineImporter(t, db, cfg, "T", path).Import()
	if err != nil || result == nil || !result.Success || result.ProcessedRows != 1 {
		t.Fatalf("prefix header should remain compatible, result=%+v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIncompleteReorderedHeaderNeverFallsBackToColumnOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("second,first\nBob,Alice,extra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("first", "text", "YES", "", nil, "").AddRow("second", "text", "YES", "", nil, "").AddRow("third", "text", "YES", "", nil, ""))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore"}}
	result, err, _ := newPipelineImporter(t, db, cfg, "T", path).Import()
	if err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("expected unsafe header order to fail, result=%+v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected INSERT: %v", err)
	}
}
