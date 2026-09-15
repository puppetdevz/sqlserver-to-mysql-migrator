package config

import (
	"testing"
)

func TestWarehouseDefaultsKeepProgressPreScanAndValidation(t *testing.T) {
	var m MigrationConfig
	if !m.ShouldCountCSVRowsBeforeImport() || !m.ShouldValidateRowCount() {
		t.Fatal("warehouse defaults must keep progress pre-scan and COUNT validation")
	}
}

func TestResourceBudgetsDefaultsAndValidation(t *testing.T) {
	defaults := (ResourceConfig{}).Effective()
	if defaults.MaxInflightBytes != 256<<20 || defaults.QueueBatches != 10 || defaults.BatchMemoryBytes != 8<<20 {
		t.Fatalf("defaults %+v", defaults)
	}
	for _, r := range []ResourceConfig{{MaxInflightBytes: -1}, {QueueBatches: -1}, {BatchMemoryBytes: 1023}, {BatchMemoryBytes: 2048, QueueBytes: 1024}, {BatchMemoryBytes: 2048, MaxInflightBytes: 1024}, {SQLCacheBytes: 65 << 20}} {
		if r.Validate() == nil {
			t.Fatalf("invalid resource config accepted %+v", r)
		}
	}
	cfg := &Config{}
	first, err := cfg.ImportBudget()
	if err != nil {
		t.Fatal(err)
	}
	second, err := cfg.ImportBudget()
	if err != nil || first != second {
		t.Fatal("budget not shared across importers")
	}
}
