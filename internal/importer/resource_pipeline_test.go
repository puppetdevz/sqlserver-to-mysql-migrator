package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
)

func TestPipelineCancellationDoesNotLeakGoroutines(t *testing.T) {
	if BaselineAlgorithms {
		t.Skip("P0 baseline does not acquire the P1 budget")
	}
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("value\na\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("value", "text", "YES", "", nil, ""))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 1, OnDuplicate: "ignore", Resources: config.ResourceConfig{BatchMemoryBytes: 1024, MaxInflightBytes: 1024, QueueBytes: 1024, MaxInflightBatches: 1, QueueBatches: 1}}}
	budget, err := cfg.ImportBudget()
	if err != nil {
		t.Fatal(err)
	}
	held, err := budget.Acquire(context.Background(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ti := newPipelineImporter(t, db, cfg, "T", path).WithContext(ctx)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	done := make(chan error, 1)
	go func() { _, err, _ := ti.pipelinedImport(file, "T", 0); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("pipeline bypassed budget: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not return after cancellation")
	}
	held.Release()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
	if bytes, count := budget.Usage(); bytes != 0 || count != 0 {
		t.Fatalf("leaked reservations %d %d", bytes, count)
	}
}

func TestPipelineStructureErrorDoesNotLeakBudget(t *testing.T) {
	if BaselineAlgorithms {
		t.Skip("P0 baseline does not acquire the P1 budget")
	}
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("first,second\nAlice,Bob,Carol\nok,fine\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("first", "text", "YES", "", nil, "").AddRow("second", "text", "YES", "", nil, ""))
	mock.ExpectPrepare("INSERT IGNORE INTO `T` (`first`, `second`) VALUES (?, ?)").ExpectExec().WithArgs("ok", "fine").WillReturnResult(sqlmock.NewResult(0, 1))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore", FastFail: boolPtr(false), CountCSVRowsBeforeImport: boolPtr(false), Resources: config.ResourceConfig{BatchMemoryBytes: 1024, MaxInflightBytes: 1024, QueueBytes: 1024, MaxInflightBatches: 1, QueueBatches: 1}}}
	result, err, _ := newPipelineImporter(t, db, cfg, "T", path).Import()
	if err != nil || result == nil || result.Success || result.ProcessedRows != 1 {
		t.Fatalf("structure error should fail after inserting later valid row: %+v %v", result, err)
	}
	budget, budgetErr := cfg.ImportBudget()
	if budgetErr != nil {
		t.Fatal(budgetErr)
	}
	if bytes, count := budget.Usage(); bytes != 0 || count != 0 {
		t.Fatalf("leaked reservations %d %d", bytes, count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineByteBudgetSplitsAndConsumesAllRows(t *testing.T) {
	if BaselineAlgorithms {
		t.Skip("P0 baseline intentionally retains count-only queues")
	}
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("value\n"+strings.Repeat("a\n", 10)), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("value", "text", "YES", "", nil, ""))
	q4 := "INSERT IGNORE INTO `T` (`value`) VALUES (?), (?), (?), (?)"
	mock.ExpectPrepare(q4).ExpectExec().WithArgs("a", "a", "a", "a").WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec(q4).WithArgs("a", "a", "a", "a").WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectPrepare("INSERT IGNORE INTO `T` (`value`) VALUES (?), (?)").ExpectExec().WithArgs("a", "a").WillReturnResult(sqlmock.NewResult(0, 2))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 100, OnDuplicate: "ignore", CountCSVRowsBeforeImport: boolPtr(false), Resources: config.ResourceConfig{BatchMemoryBytes: 1024, MaxInflightBytes: 1024, QueueBytes: 1024, MaxInflightBatches: 1, QueueBatches: 1}}}
	result, err, _ := newPipelineImporter(t, db, cfg, "T", path).Import()
	if err != nil || result == nil || !result.Success || result.ProcessedRows != 10 {
		t.Fatalf("lost rows %+v err %v", result, err)
	}
	budget, _ := cfg.ImportBudget()
	if bytes, count := budget.Usage(); bytes != 0 || count != 0 {
		t.Fatalf("leaked reservations %d %d", bytes, count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineCancellationWhileWaitingForBudget(t *testing.T) {
	if BaselineAlgorithms {
		t.Skip("P0 baseline does not acquire the P1 budget")
	}
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("value\na\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("value", "text", "YES", "", nil, ""))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 1, OnDuplicate: "ignore", Resources: config.ResourceConfig{BatchMemoryBytes: 1024, MaxInflightBytes: 1024, QueueBytes: 1024, MaxInflightBatches: 1}}}
	budget, err := cfg.ImportBudget()
	if err != nil {
		t.Fatal(err)
	}
	held, err := budget.Acquire(context.Background(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ti := newPipelineImporter(t, db, cfg, "T", path).WithContext(ctx)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	done := make(chan error, 1)
	go func() {
		result, err, _ := ti.pipelinedImport(file, "T", 0)
		if result != nil && result.Success {
			done <- errors.New("cancelled pipeline reported success")
			return
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("pipeline bypassed full budget: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pipeline cancellation leaked goroutine")
	}
	held.Release()
	if bytes, count := budget.Usage(); bytes != 0 || count != 0 {
		t.Fatalf("leaked reservations %d %d", bytes, count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineDrainsErrorsAfterUnknownCommitWithoutMoreWrites(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("first,second\nok,fine\nAlice,Bob,Carol\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("first", "text", "YES", "", nil, "").AddRow("second", "text", "YES", "", nil, ""))
	mock.ExpectPrepare("INSERT IGNORE INTO `T` (`first`, `second`) VALUES (?, ?)").WillDelayFor(100*time.Millisecond).ExpectExec().WithArgs("ok", "fine").WillReturnError(mysql.ErrInvalidConn)
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 1, OnDuplicate: "ignore", CountCSVRowsBeforeImport: boolPtr(false)}}
	result, err, _ := newPipelineImporter(t, db, cfg, "T", path).Import()
	if !errors.Is(err, ErrUnknownCommit) || result == nil || result.ErrorCount != 2 || result.ProcessedRows != 0 {
		t.Fatalf("lost queued error or unknown status: result %+v err %v", result, err)
	}
	budget, budgetErr := cfg.ImportBudget()
	if budgetErr != nil {
		t.Fatal(budgetErr)
	}
	if bytes, count := budget.Usage(); bytes != 0 || count != 0 {
		t.Fatalf("leaked reservation %d %d", bytes, count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyNoHeaderPipelineReportsSuccessAndIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("value", "text", "YES", "", nil, ""))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(false)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore", CountCSVRowsBeforeImport: boolPtr(false)}}
	ti := newPipelineImporter(t, db, cfg, "T", path)
	result, err, _ := ti.Import()
	if err != nil || result == nil || !result.Success || result.TableName != "T" || result.ProcessedRows != 0 {
		t.Fatalf("empty logical CSV result %+v err %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineOversizedRowFailsAndReleasesBudget(t *testing.T) {
	if BaselineAlgorithms {
		t.Skip("P0 baseline does not enforce the P1 row-memory cap")
	}
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "T.csv")
	if err := os.WriteFile(path, []byte("value\n"+strings.Repeat("s", 2048)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("value", "text", "YES", "", nil, ""))
	cfg := &config.Config{Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore", CountCSVRowsBeforeImport: boolPtr(false), Resources: config.ResourceConfig{BatchMemoryBytes: 1024, MaxInflightBytes: 1024, QueueBytes: 1024, MaxInflightBatches: 1}}}
	ti := newPipelineImporter(t, db, cfg, "T", path)
	result, err, _ := ti.Import()
	if !errors.Is(err, migration.ErrResourceLimit) {
		t.Fatalf("want resource rejection, got %v", err)
	}
	if result == nil || result.Success || result.ProcessedRows != 0 {
		t.Fatalf("bad result %+v", result)
	}
	budget, err := cfg.ImportBudget()
	if err != nil {
		t.Fatal(err)
	}
	if bytes, count := budget.Usage(); bytes != 0 || count != 0 {
		t.Fatalf("leaked reservations %d %d", bytes, count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
