package importer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/database"
)

func newPipelineImporter(t *testing.T, db *sql.DB, cfg *config.Config, tableName, csvPath string) *TableImporter {
	t.Helper()
	recorder, err := NewErrorRecorder("")
	if err != nil {
		t.Fatalf("NewErrorRecorder: %v", err)
	}
	t.Cleanup(func() { recorder.Close() })
	return NewTableImporter(&database.Connection{DB: db}, cfg, tableName, csvPath, recorder)
}

func TestRepairQuotedCommaFieldIsUnchanged(t *testing.T) {
	row := []string{"Alice,Bob", "Carol"}
	infos := []dbColumnInfo{{Name: "first", Type: "text"}, {Name: "second", Type: "text"}}
	got, err := repairDelimitedRow(row, infos)
	if err != nil {
		t.Fatalf("quoted comma field should be valid: %v", err)
	}
	if strings.Join(got, "|") != "Alice,Bob|Carol" {
		t.Fatalf("got %v, want original quoted-comma split", got)
	}
}

func TestRepairPartialHeaderLayoutUsesHeaderCountNotDBCount(t *testing.T) {
	row := []string{"1", "hello,world"}
	infos := []dbColumnInfo{
		{Name: "id", Type: "int"},
		{Name: "name", Type: "varchar"},
	}
	got, err := repairDelimitedRow(row, infos)
	if err != nil {
		t.Fatalf("partial header layout should accept matching field count: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (header layout), not DB width", len(got))
	}
}

func TestSendPipelineBatchDeliversErrorWhenBufferFull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	csvDone := make(chan struct{})
	batchChan := make(chan batchData, 1)
	batchChan <- batchData{batchNum: 0}

	errCh := make(chan error, 1)
	go func() {
		ok := sendPipelineBatch(ctx, batchChan, csvDone, batchData{
			batchNum: 1,
			err:      errors.New("ambiguous CSV field repair"),
		})
		if !ok {
			errCh <- errors.New("send returned false")
			return
		}
		errCh <- nil
	}()

	select {
	case <-time.After(50 * time.Millisecond):
	case err := <-errCh:
		t.Fatalf("send finished before buffer was drained: %v", err)
	}

	first := <-batchChan
	if first.batchNum != 0 {
		t.Fatalf("first receive batchNum = %d, want filler 0", first.batchNum)
	}
	second := <-batchChan
	if second.err == nil || second.err.Error() != "ambiguous CSV field repair" {
		t.Fatalf("second receive err = %v, want delivered structure error", second.err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("sendPipelineBatch: %v", err)
	}
}

func TestPipelinedImportAmbiguousRowDoesNotInsertAndFailsFast(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "T.csv")
	if err := os.WriteFile(csvPath, []byte("first,second\nAlice,Bob,Carol\nok,fine\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("first", "text", "YES", "", nil, "").
		AddRow("second", "text", "YES", "", nil, ""))

	cfg := &config.Config{
		Source: config.SourceConfig{CSVHasHeader: boolPtr(true)},
		Migration: config.MigrationConfig{
			BatchSize:   10,
			OnDuplicate: "ignore",
			FastFail:    boolPtr(true),
		},
	}
	ti := newPipelineImporter(t, db, cfg, "T", csvPath)

	done := make(chan struct{})
	var result *ImportResult
	var err error
	go func() {
		defer close(done)
		result, err, _ = ti.Import()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Import timed out")
	}
	if err == nil {
		t.Fatal("expected structure error")
	}
	var csvErr *CSVStructureError
	if !errors.As(err, &csvErr) {
		t.Fatalf("err = %v, want CSVStructureError", err)
	}
	if csvErr.RecordNumber != 1 {
		t.Fatalf("RecordNumber = %d, want 1 (logical data record)", csvErr.RecordNumber)
	}
	if result == nil || result.Success {
		t.Fatalf("result = %#v, want failed", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ambiguous row was written: %v", err)
	}
}

func TestPipelinedImportFastFailFalseInsertsLaterValidRowButStillFails(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "T.csv")
	if err := os.WriteFile(csvPath, []byte("first,second\nAlice,Bob,Carol\nok,fine\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("first", "text", "YES", "", nil, "").
		AddRow("second", "text", "YES", "", nil, ""))

	query := "INSERT IGNORE INTO `T` (`first`, `second`) VALUES (?, ?)"
	prep := mock.ExpectPrepare(query)
	prep.ExpectExec().WithArgs("ok", "fine").WillReturnResult(sqlmock.NewResult(0, 1))

	cfg := &config.Config{
		Source: config.SourceConfig{CSVHasHeader: boolPtr(true)},
		Migration: config.MigrationConfig{
			BatchSize:   10,
			OnDuplicate: "ignore",
			FastFail:    boolPtr(false),
		},
	}
	ti := newPipelineImporter(t, db, cfg, "T", csvPath)

	done := make(chan struct{})
	var result *ImportResult
	var err error
	go func() {
		defer close(done)
		result, err, _ = ti.Import()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Import timed out")
	}
	if err != nil {
		t.Fatalf("fast_fail=false should collect row errors without returning err, got %v", err)
	}
	if result == nil || result.Success || result.ErrorCount == 0 {
		t.Fatalf("result = %#v, want failed with error count", result)
	}
	if result.InsertedRows != 1 {
		t.Fatalf("InsertedRows = %d, want 1 valid row", result.InsertedRows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("valid row not inserted or bad row written: %v", err)
	}
}

func TestPipelinedImportNoHeaderAmbiguousRowDoesNotInsert(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "T.csv")
	if err := os.WriteFile(csvPath, []byte("Alice,Bob,Carol\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("first", "text", "YES", "", nil, "").
		AddRow("second", "text", "YES", "", nil, ""))

	cfg := &config.Config{
		Source: config.SourceConfig{CSVHasHeader: boolPtr(false)},
		Migration: config.MigrationConfig{
			BatchSize:   10,
			OnDuplicate: "ignore",
			FastFail:    boolPtr(true),
		},
	}
	ti := newPipelineImporter(t, db, cfg, "T", csvPath)
	_, err, _ := ti.Import()
	if err == nil {
		t.Fatal("expected column-count or structure error for no-header ragged row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no-header ambiguous row was written: %v", err)
	}
}
