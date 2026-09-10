package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
)

func boolPtr(v bool) *bool { return &v }

func writeTempDDL(t *testing.T, dir string, tables ...string) string {
	t.Helper()
	var b strings.Builder
	for _, name := range tables {
		fmt.Fprintf(&b, `-- V80.dbo.%s definition
CREATE TABLE V80.dbo.%s (
id int NULL
);
`, name, name)
	}
	path := filepath.Join(dir, "origin_database_ddl.sql")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

func writeTempDDLRaw(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "origin_database_ddl.sql")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

func writeCSV(t *testing.T, dir, tableName, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	path := filepath.Join(dir, tableName+".csv")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

func newMockConnection(t *testing.T) (*database.Connection, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &database.Connection{DB: db}, mock
}

func expectShowTables(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"Tables_in_db"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(rows)
}

func expectDescribe(mock sqlmock.Sqlmock, table string, columns ...[]string) {
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, col := range columns {
		field, colType, null, key := col[0], col[1], col[2], col[3]
		rows.AddRow(field, colType, null, key, nil, "")
	}
	mock.ExpectQuery(fmt.Sprintf("DESCRIBE `%s`", table)).WillReturnRows(rows)
}

func expectTruncate(mock sqlmock.Sqlmock, table string) {
	mock.ExpectExec(fmt.Sprintf("TRUNCATE TABLE `%s`", table)).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectCount(mock sqlmock.Sqlmock, table string, count int64) {
	mock.ExpectQuery(fmt.Sprintf("SELECT COUNT(*) FROM `%s`", table)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func testMigrationConfig(ddlPath, csvDir string, adaptive bool) *config.Config {
	return &config.Config{
		Source: config.SourceConfig{
			DDLFile:      ddlPath,
			CSVDirectory: csvDir,
			CSVHasHeader: boolPtr(true),
		},
		Migration: config.MigrationConfig{
			MaxWorkers:  1,
			BatchSize:   100,
			OnDuplicate: "ignore",
			AdaptiveImport: config.AdaptiveImportConfig{
				Enabled:      boolPtr(adaptive),
				ImportTokens: 10,
				MinTokens:    2,
			},
			RowCountValidation: boolPtr(true),
			FastFail:           boolPtr(true),
		},
		Logging: config.LoggingConfig{File: ""},
		Converter: config.ConverterConfig{
			MaxVarcharToTextColumns:  999,
			MaxNvarcharToTextColumns: 999,
			MaxVarcharToTextSize:     9999,
			MaxNvarcharToTextSize:    9999,
		},
	}
}

func newTracker(t *testing.T) *progress.Tracker {
	t.Helper()
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	t.Cleanup(func() { tracker.Close() })
	return tracker
}

func assertTableNotInFile(t *testing.T, path, table string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == table {
			t.Fatalf("%s contains table %q:\n%s", path, table, data)
		}
	}
}

func assertFileContainsTable(t *testing.T, path, table string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == table {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s does not contain table %q:\n%s", path, table, data)
	}
}

func tableFileOccurrences(t *testing.T, path, table string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == table {
			count++
		}
	}
	return count
}

func defaultMatcher() matcher.TableNameMatcher {
	return matcher.NewTableNameMatcher(true)
}

func unusedSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_ = mock
	return db
}
