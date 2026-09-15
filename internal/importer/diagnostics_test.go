package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/diagnostics"
)

func TestPipelineDiagnosticCountsAndPrivacy(t *testing.T) {
	t.Chdir(t.TempDir())
	data := "id,value\n1,\"secret business\nUnicode中文\"\n2,\n"
	path := filepath.Join(t.TempDir(), "sensitive.csv")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := diagnostics.Open(t.TempDir(), []byte(strings.Repeat("x", 32)), time.Second, diagnostics.Settings{}, "test", "private-host")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(false, false) })
	if err = r.Scope([]string{"T"}, map[string]string{"T": path}, 0); err != nil {
		t.Fatal(err)
	}
	db, mock := newMockDB(t)
	mock.ExpectQuery("SHOW TABLES").WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("T"))
	mock.ExpectQuery("DESCRIBE `T`").WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).AddRow("id", "int", "NO", "", nil, "").AddRow("value", "text", "YES", "", nil, ""))
	mock.ExpectPrepare("INSERT IGNORE INTO `T` (`id`, `value`) VALUES (?, ?), (?, ?)").ExpectExec().WithArgs("1", "secret business\nUnicode中文", "2", nil).WillReturnResult(sqlmock.NewResult(0, 1))
	cfg := &config.Config{Diagnostics: r, Source: config.SourceConfig{CSVHasHeader: boolPtr(true)}, Migration: config.MigrationConfig{BatchSize: 10, OnDuplicate: "ignore", CountCSVRowsBeforeImport: boolPtr(false)}}
	ti := newPipelineImporter(t, db, cfg, "T", path)
	result, err, _ := ti.Import()
	if err != nil {
		t.Fatal(err)
	}
	if result.ProcessedRows != 2 || result.InsertedRows != 1 {
		t.Fatalf("counts %+v", result)
	}
	r.State("T", "success", "disabled", 0)
	if err = r.Close(true, false); err != nil {
		t.Fatal(err)
	}
	report, err := diagnostics.ReadReport(r.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics[len(report.Metrics)-1].Times["exec"].Samples != 1 {
		t.Fatal("periodic/final snapshot omitted table Exec timing")
	}
	c := report.Summary.Counters
	if c.ParsedRows != 2 || c.ConsumedRows != 2 || c.AffectedRows != 1 || c.CSVBytes != int64(len(data)) || c.PrescanBytes != 0 || c.Prepares != 1 || c.SubBatches != 1 {
		t.Fatalf("counters %+v", c)
	}
	for _, stage := range []string{"pipeline", "metadata", "read_parse", "prepare", "exec", "result_read", "enqueue_wait", "dequeue_wait"} {
		if report.Summary.Times[stage].Samples == 0 {
			t.Errorf("no %s metric", stage)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
