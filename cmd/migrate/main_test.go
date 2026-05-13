package main

import (
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/progress"
)

func TestCSVNotFoundShouldAdvanceOverallSkip(t *testing.T) {
	tracker, err := progress.NewTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(1)

	if err := tracker.SkipTable("missing_csv_table", "CSV file not found"); err != nil {
		t.Fatalf("SkipTable() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.SkippedCount != 1 {
		t.Fatalf("SkippedCount = %d, want 1", info.SkippedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
	if !info.IsCompleted {
		t.Fatal("IsCompleted = false, want true")
	}
}

func TestPartialImportShouldNotBeMarkedCompleted(t *testing.T) {
	tracker, err := progress.NewTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(1)

	if err := tracker.StartTable("partial_table", "/tmp/partial.csv", false); err != nil {
		t.Fatalf("StartTable() error = %v", err)
	}
	if err := tracker.FailTable("partial_table", "partial import: 3 row errors"); err != nil {
		t.Fatalf("FailTable() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 0 {
		t.Fatalf("CompletedCount = %d, want 0", info.CompletedCount)
	}
	if info.FailedCount != 1 {
		t.Fatalf("FailedCount = %d, want 1", info.FailedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
}

func TestCreateOnlyExpectedOverallClosure(t *testing.T) {
	tracker, err := progress.NewTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(2)

	if err := tracker.StartTable("new_table", "", false); err != nil {
		t.Fatalf("StartTable(new_table) error = %v", err)
	}
	if err := tracker.CompleteTable("new_table", 0, 0, 0); err != nil {
		t.Fatalf("CompleteTable(new_table) error = %v", err)
	}
	if err := tracker.SkipTable("existing_table", "table already exists"); err != nil {
		t.Fatalf("SkipTable(existing_table) error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 1 {
		t.Fatalf("CompletedCount = %d, want 1", info.CompletedCount)
	}
	if info.SkippedCount != 1 {
		t.Fatalf("SkippedCount = %d, want 1", info.SkippedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
	if !info.IsCompleted {
		t.Fatal("IsCompleted = false, want true")
	}
}

func TestFinalizeCreateOnlyProgressSkipsUncreatedMissingTablesWhenCreationDisabled(t *testing.T) {
	tracker, err := progress.NewTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(2)

	if err := finalizeCreateOnlyProgress(tracker, []string{"existing_table"}, []string{"missing_table"}, false); err != nil {
		t.Fatalf("finalizeCreateOnlyProgress() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 0 {
		t.Fatalf("CompletedCount = %d, want 0", info.CompletedCount)
	}
	if info.SkippedCount != 2 {
		t.Fatalf("SkippedCount = %d, want 2", info.SkippedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress = %v, want 100", info.Progress)
	}
	if !info.IsCompleted {
		t.Fatal("IsCompleted = false, want true")
	}
}
