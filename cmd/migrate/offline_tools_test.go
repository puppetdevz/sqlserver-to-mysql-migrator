package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

func TestExtractCLIDoesNotLoadConfig(t *testing.T) {
	oldCSV, oldOut, oldN, oldConfig := *extractCSV, *extractOutput, *extractRecords, *configPath
	t.Cleanup(func() { *extractCSV, *extractOutput, *extractRecords, *configPath = oldCSV, oldOut, oldN, oldConfig })
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(src, []byte("a,b\n1,\"x\ny\"\n2,z\n"), 0600); err != nil {
		t.Fatal(err)
	}
	*extractCSV = src
	*extractOutput = filepath.Join(dir, "out.csv")
	*extractRecords = 1
	*configPath = "must-not-be-read.yaml"
	handled, code := runOfflineTools()
	if !handled || code != 0 {
		t.Fatalf("handled=%t code=%d", handled, code)
	}
}

func TestCSVInventoryUsesMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	csvDir := filepath.Join(dir, "csv")
	if err := os.Mkdir(csvDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(csvDir, "secret.csv"), []byte("password,dsn\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "inventory.jsonl")
	cfg := &config.Config{Source: config.SourceConfig{CSVDirectory: csvDir}}
	if err := writeCSVInventory(cfg, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "password") {
		t.Fatal("inventory scanned file contents")
	}
	if !strings.Contains(string(data), "secret.csv") {
		t.Fatalf("missing file metadata: %s", data)
	}
}

func TestProfileDurationRejectedOutOfRange(t *testing.T) {
	oldCPU, oldDur := *cpuProfile, *profileDuration
	t.Cleanup(func() { *cpuProfile, *profileDuration = oldCPU, oldDur })
	*cpuProfile = filepath.Join(t.TempDir(), "cpu.pprof")
	*profileDuration = time.Hour
	if _, err := startLocalProfiles(); err == nil {
		t.Fatal("unbounded profile duration accepted")
	}
}
