package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

func TestDiagnosticsFailsBeforeDatabaseForInvalidKey(t *testing.T) {
	oldDir, oldKey := *diagnosticsDir, *diagnosticsKey
	t.Cleanup(func() { *diagnosticsDir = oldDir; *diagnosticsKey = oldKey })
	*diagnosticsDir = t.TempDir()
	*diagnosticsKey = ""
	if _, err := openDiagnostics(&config.Config{}, time.Now()); err == nil {
		t.Fatal("missing key accepted")
	}
	*diagnosticsKey = filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(*diagnosticsKey, []byte("password"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDiagnostics(&config.Config{}, time.Now()); err == nil {
		t.Fatal("short key accepted")
	}
}
func TestOfflineModeDoesNotLoadConfiguration(t *testing.T) {
	oldExport, oldOutput, oldCompare, oldWith, oldConfig := *reportExport, *reportOutput, *reportCompare, *reportWith, *configPath
	t.Cleanup(func() {
		*reportExport = oldExport
		*reportOutput = oldOutput
		*reportCompare = oldCompare
		*reportWith = oldWith
		*configPath = oldConfig
	})
	*reportExport = filepath.Join(t.TempDir(), "missing-report")
	*reportOutput = filepath.Join(t.TempDir(), "out.tar.gz")
	*reportCompare = ""
	*reportWith = ""
	*configPath = "must-not-be-read.yaml"
	if handled, code := runOfflineReport(); !handled || code != 1 {
		t.Fatalf("handled %t code %d", handled, code)
	}
	if _, err := os.Stat(*reportOutput); !os.IsNotExist(err) {
		t.Fatal("bad report produced output")
	}
}
