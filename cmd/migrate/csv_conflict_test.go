package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/matcher"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
)

func TestBuildCSVTableMapCaseInsensitiveConflictFails(t *testing.T) {
	foo := "/data/Foo.csv"
	bar := "/data/foo.csv"
	_, err := buildCSVTableMap([]string{foo, bar}, "", matcher.NewTableNameMatcher(false))
	if err == nil {
		t.Fatal("expected CSV table name conflict")
	}
	if !strings.Contains(err.Error(), foo) || !strings.Contains(err.Error(), bar) {
		t.Fatalf("error %v should contain both paths", err)
	}
}

func TestBuildCSVTableMapCaseSensitiveAllowsDistinctFiles(t *testing.T) {
	foo := "/data/Foo.csv"
	bar := "/data/foo.csv"
	tableMap, err := buildCSVTableMap([]string{foo, bar}, "", matcher.NewTableNameMatcher(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(tableMap) != 2 {
		t.Fatalf("len=%d want 2", len(tableMap))
	}
}

func TestTruncateUsesIsFastFailWhenPointerNil(t *testing.T) {
	t.Chdir(t.TempDir())
	conn, mock := newMockConnection(t)
	mock.ExpectExec("TRUNCATE TABLE `A`").WillReturnError(fmt.Errorf("disk full"))
	cfg := &config.Config{Migration: config.MigrationConfig{MaxWorkers: 1}}
	if cfg.Migration.FastFail != nil {
		t.Fatal("precondition: FastFail must be nil")
	}
	tracker, err := progress.NewTracker()
	if err != nil {
		t.Fatal(err)
	}
	defer tracker.Close()
	mCtx := migration.NewMigrationContext()
	err = truncateExistingTables(conn, []string{"A", "B"}, tracker, mCtx, cfg)
	if err == nil {
		t.Fatal("expected truncate failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("B must not be truncated when fast_fail defaults true: %v", err)
	}
}

func TestAppendCompletedFailsOnClosedFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/completed_tables.txt"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	sink := newImportCompletionSink(f, nil, 0)
	if err := sink.appendCompleted("T", 0); err == nil {
		t.Fatal("closed completed file must fail registration")
	}
}
