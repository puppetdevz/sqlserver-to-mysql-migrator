package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

func boolPtr(v bool) *bool {
	return &v
}

func TestFindCSVFileCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "sample_main_102_20000101000000.csv")
	if err := os.WriteFile(csvPath, []byte("id\n1\n"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	di := NewDataImporter(nil, &config.Config{
		Source: config.SourceConfig{
			CSVDirectory: dir,
			CSVTimestamp: "20000101000000",
		},
		Migration: config.MigrationConfig{
			TableNameCaseSensitive: boolPtr(true),
		},
	})
	defer di.Close()

	got, err, diag := di.FindCSVFile("SAMPLE_MAIN_102")
	if err == nil {
		t.Fatalf("FindCSVFile() path = %q, want CSV_NOT_FOUND", got)
	}
	if diag == nil || diag.ErrorType != ErrorTypeCSVNotFound {
		t.Fatalf("diag = %#v, want CSV_NOT_FOUND", diag)
	}
}

func TestFindCSVFileCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "sample_main_102_20000101000000.csv")
	if err := os.WriteFile(csvPath, []byte("id\n1\n"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	di := NewDataImporter(nil, &config.Config{
		Source: config.SourceConfig{
			CSVDirectory: dir,
			CSVTimestamp: "20000101000000",
		},
		Migration: config.MigrationConfig{
			TableNameCaseSensitive: boolPtr(false),
		},
	})
	defer di.Close()

	got, err, diag := di.FindCSVFile("SAMPLE_MAIN_102")
	if err != nil {
		t.Fatalf("FindCSVFile() error = %v diag=%#v", err, diag)
	}
	if got != csvPath {
		t.Fatalf("FindCSVFile() = %q, want %q", got, csvPath)
	}
}

func TestFindCSVFileCaseInsensitiveDiagnosticDoesNotListEntireDirectory(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		csvPath := filepath.Join(dir, fmt.Sprintf("other_%d.csv", i))
		if err := os.WriteFile(csvPath, []byte("id\n1\n"), 0644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	di := NewDataImporter(nil, &config.Config{
		Source: config.SourceConfig{
			CSVDirectory: dir,
			CSVTimestamp: "20000101000000",
		},
		Migration: config.MigrationConfig{
			TableNameCaseSensitive: boolPtr(false),
		},
	})
	defer di.Close()

	_, err, diag := di.FindCSVFile("SAMPLE_MAIN_102")
	if err == nil {
		t.Fatal("FindCSVFile() error = nil, want CSV_NOT_FOUND")
	}
	if diag == nil || diag.ErrorType != ErrorTypeCSVNotFound {
		t.Fatalf("diag = %#v, want CSV_NOT_FOUND", diag)
	}
	if len(diag.TriedPaths) != 1 {
		t.Fatalf("len(TriedPaths) = %d, want 1: %#v", len(diag.TriedPaths), diag.TriedPaths)
	}
	if got := filepath.Base(diag.TriedPaths[0]); got != "SAMPLE_MAIN_102_20000101000000.csv" {
		t.Fatalf("TriedPaths[0] basename = %q, want %q", got, "SAMPLE_MAIN_102_20000101000000.csv")
	}
}
