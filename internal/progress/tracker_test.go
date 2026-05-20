package progress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
)

func TestGetPhaseProgress(t *testing.T) {
	tests := []struct {
		name      string
		total     int
		completed int
		failed    int
		skipped   int
		want      float64
	}{
		{name: "completed only", total: 10, completed: 5, want: 50.0},
		{name: "completed failed skipped", total: 10, completed: 4, failed: 3, skipped: 1, want: 80.0},
		{name: "zero total", total: 0, completed: 1, want: 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &MigrationState{
				PhaseTotal:     tt.total,
				PhaseCompleted: tt.completed,
				PhaseFailed:    tt.failed,
				PhaseSkipped:   tt.skipped,
			}

			got := state.GetPhaseProgress()
			if got != tt.want {
				t.Fatalf("GetPhaseProgress() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetProgress(t *testing.T) {
	tests := []struct {
		name      string
		total     int
		completed int
		failed    int
		skipped   int
		want      float64
	}{
		{name: "normal progress", total: 100, completed: 50, want: 50.0},
		{name: "completed failed skipped", total: 100, completed: 95, failed: 3, skipped: 2, want: 100.0},
		{name: "zero total", total: 0, completed: 1, want: 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &MigrationState{
				TotalTables:    tt.total,
				CompletedCount: tt.completed,
				FailedCount:    tt.failed,
				SkippedCount:   tt.skipped,
			}

			got := state.GetProgress()
			if got != tt.want {
				t.Fatalf("GetProgress() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpdateTableStatusUnknownTableDoesNotIncreaseCompletedCount(t *testing.T) {
	state := NewMigrationState()
	if got := state.GetTable("TEST_TABLE"); got != nil {
		t.Fatalf("expected nil initial table state, got %#v", got)
	}

	state.UpdateTableStatus("TEST_TABLE", StatusCompleted)

	if state.CompletedCount != 0 {
		t.Fatalf("CompletedCount without existing state = %d, want 0", state.CompletedCount)
	}
}

func TestMigrationStateUpdateTableStatusCountsTrackedRunState(t *testing.T) {
	state := NewMigrationState()
	state.TrackRunTable("TEST_TABLE")

	state.AddTable("TEST_TABLE")
	state.UpdateTableStatus("TEST_TABLE", StatusCompleted)

	if state.CompletedCount != 1 {
		t.Fatalf("CompletedCount = %d, want 1", state.CompletedCount)
	}
}

func TestSetPlannedTotalTablesDoesNotDriftWhenAddingTables(t *testing.T) {
	tracker, err := NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(2)

	if err := tracker.StartTable("users", "/tmp/users.csv", false); err != nil {
		t.Fatalf("StartTable() error = %v", err)
	}
	if err := tracker.StartTable("orders", "/tmp/orders.csv", false); err != nil {
		t.Fatalf("StartTable() error = %v", err)
	}
	if err := tracker.StartTable("roles", "/tmp/roles.csv", false); err != nil {
		t.Fatalf("StartTable() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.TotalTables != 2 {
		t.Fatalf("TotalTables = %d, want 2", info.TotalTables)
	}

	if got := tracker.GetTableState("users"); got == nil {
		t.Fatal("expected table state to be created")
	}
}

func TestSetPlannedTotalTablesResetsRunScopedOverallCounts(t *testing.T) {
	tracker, err := NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	if err := tracker.StartTable("history_a", "/tmp/history_a.csv", false); err != nil {
		t.Fatalf("StartTable(history_a) error = %v", err)
	}
	if err := tracker.CompleteTable("history_a", 10, 10, 0); err != nil {
		t.Fatalf("CompleteTable(history_a) error = %v", err)
	}
	if err := tracker.StartTable("history_b", "/tmp/history_b.csv", false); err != nil {
		t.Fatalf("StartTable(history_b) error = %v", err)
	}
	if err := tracker.SkipTable("history_b", "already handled"); err != nil {
		t.Fatalf("SkipTable(history_b) error = %v", err)
	}

	tracker.SetPlannedTotalTables(1)

	info := tracker.GetProgress()
	if info.TotalTables != 1 {
		t.Fatalf("TotalTables after SetPlannedTotalTables = %d, want 1", info.TotalTables)
	}
	if info.CompletedCount != 0 || info.FailedCount != 0 || info.SkippedCount != 0 {
		t.Fatalf("run-scoped counts after SetPlannedTotalTables = (%d,%d,%d), want zeroed",
			info.CompletedCount, info.FailedCount, info.SkippedCount)
	}
	if info.Progress != 0 {
		t.Fatalf("Progress after SetPlannedTotalTables = %v, want 0", info.Progress)
	}
	if info.IsCompleted {
		t.Fatal("IsCompleted after SetPlannedTotalTables = true, want false")
	}

	if err := tracker.StartTable("current_a", "/tmp/current_a.csv", false); err != nil {
		t.Fatalf("StartTable(current_a) error = %v", err)
	}
	if err := tracker.CompleteTable("current_a", 5, 5, 0); err != nil {
		t.Fatalf("CompleteTable(current_a) error = %v", err)
	}

	info = tracker.GetProgress()
	if info.CompletedCount != 1 || info.FailedCount != 0 || info.SkippedCount != 0 {
		t.Fatalf("run-scoped counts after current run completion = (%d,%d,%d), want (1,0,0)",
			info.CompletedCount, info.FailedCount, info.SkippedCount)
	}
	if info.Progress != 100.0 {
		t.Fatalf("Progress after current run completion = %v, want 100", info.Progress)
	}
	if !info.IsCompleted {
		t.Fatal("IsCompleted after current run completion = false, want true")
	}
}

func TestPhaseLifecycleUpdatesProgressInfo(t *testing.T) {
	tracker, err := NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.StartPhase("create_tables", 4)
	tracker.CompletePhaseItem()
	tracker.FailPhaseItem()
	tracker.SkipPhaseItem()

	info := tracker.GetProgress()
	if info.CurrentPhase != "create_tables" {
		t.Fatalf("CurrentPhase = %q, want %q", info.CurrentPhase, "create_tables")
	}
	if info.PhaseTotal != 4 {
		t.Fatalf("PhaseTotal = %d, want 4", info.PhaseTotal)
	}
	if info.PhaseCompleted != 1 || info.PhaseFailed != 1 || info.PhaseSkipped != 1 {
		t.Fatalf("phase counts = (%d,%d,%d), want (1,1,1)", info.PhaseCompleted, info.PhaseFailed, info.PhaseSkipped)
	}
	if info.PhaseProgress != 75.0 {
		t.Fatalf("PhaseProgress = %v, want 75", info.PhaseProgress)
	}

	tracker.ClearPhase()

	info = tracker.GetProgress()
	if info.CurrentPhase != "" {
		t.Fatalf("CurrentPhase after ClearPhase = %q, want empty", info.CurrentPhase)
	}
	if info.PhaseTotal != 0 || info.PhaseCompleted != 0 || info.PhaseFailed != 0 || info.PhaseSkipped != 0 {
		t.Fatalf("phase state after ClearPhase = %+v, want zeroed fields", info)
	}
	if info.PhaseProgress != 0 {
		t.Fatalf("PhaseProgress after ClearPhase = %v, want 0", info.PhaseProgress)
	}
}

func TestReportProgressWithActivePhaseLogsPhaseAndOverall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "progress.log")
	if err := logger.Init("INFO", logPath, false, 1, 1, 1); err != nil {
		t.Fatalf("logger.Init() error = %v", err)
	}

	tracker := &Tracker{
		state: &MigrationState{
			TotalTables:    5,
			CompletedCount: 2,
			FailedCount:    1,
			SkippedCount:   1,
			CurrentPhase:   "create_tables",
			PhaseTotal:     4,
			PhaseCompleted: 1,
			PhaseFailed:    1,
			PhaseSkipped:   1,
		},
	}

	tracker.reportProgress()
	logger.Sync()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	logOutput := string(content)
	if !strings.Contains(logOutput, "Phase(create_tables): 75.00% (3/4)") {
		t.Fatalf("phase log missing, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Overall: 80.00% (4/5 tables)") {
		t.Fatalf("overall log missing, got %q", logOutput)
	}
}

func TestReportProgressWithoutPhaseLogsOnlyOverall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "progress.log")
	if err := logger.Init("INFO", logPath, false, 1, 1, 1); err != nil {
		t.Fatalf("logger.Init() error = %v", err)
	}

	tracker := &Tracker{
		state: &MigrationState{
			TotalTables:    3,
			CompletedCount: 1,
			FailedCount:    1,
		},
	}

	tracker.reportProgress()
	logger.Sync()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	logOutput := string(content)
	if strings.Contains(logOutput, "Phase(") {
		t.Fatalf("unexpected phase log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Overall: 66.67% (2/3 tables)") {
		t.Fatalf("overall log missing, got %q", logOutput)
	}
}

func TestReportProgressWithoutPhaseTotalLogsOnlyOverall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "progress.log")
	if err := logger.Init("INFO", logPath, false, 1, 1, 1); err != nil {
		t.Fatalf("logger.Init() error = %v", err)
	}

	tracker := &Tracker{
		state: &MigrationState{
			TotalTables:    3,
			CompletedCount: 1,
			FailedCount:    1,
			CurrentPhase:   "create_tables",
		},
	}

	tracker.reportProgress()
	logger.Sync()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	logOutput := string(content)
	if strings.Contains(logOutput, "Phase(") {
		t.Fatalf("unexpected phase log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "Overall: 66.67% (2/3 tables)") {
		t.Fatalf("overall log missing, got %q", logOutput)
	}
}

func TestStartTableDoesNotAdvanceOverallProgress(t *testing.T) {
	tracker, err := NewTracker()
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	defer tracker.Close()

	tracker.SetPlannedTotalTables(1)

	if err := tracker.StartTable("created_only_table", "", false); err != nil {
		t.Fatalf("StartTable() error = %v", err)
	}

	info := tracker.GetProgress()
	if info.CompletedCount != 0 || info.FailedCount != 0 || info.SkippedCount != 0 {
		t.Fatalf("counts after StartTable = (%d,%d,%d), want (0,0,0)",
			info.CompletedCount, info.FailedCount, info.SkippedCount)
	}
	if info.Progress != 0 {
		t.Fatalf("Progress after StartTable = %v, want 0", info.Progress)
	}
	if info.IsCompleted {
		t.Fatal("IsCompleted after StartTable = true, want false")
	}
}

func TestPrintSummaryOnlyIncludesCurrentRunTables(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "summary.log")
	if err := logger.Init("INFO", logPath, false, 1, 1, 1); err != nil {
		t.Fatalf("logger.Init() error = %v", err)
	}

	tracker := &Tracker{
		state: &MigrationState{
			TotalTables:    2,
			CompletedCount: 0,
			FailedCount:    1,
			SkippedCount:   1,
			Tables: map[string]*TableState{
				"current_failed": {
					TableName:    "current_failed",
					Status:       StatusFailed,
					ErrorMessage: "current failure",
				},
				"current_skipped": {
					TableName:    "current_skipped",
					Status:       StatusSkipped,
					ErrorMessage: "current skip",
				},
				"historical_failed": {
					TableName:    "historical_failed",
					Status:       StatusFailed,
					ErrorMessage: "historical failure",
				},
				"historical_skipped": {
					TableName:    "historical_skipped",
					Status:       StatusSkipped,
					ErrorMessage: "historical skip",
				},
			},
			RunTableNames: map[string]struct{}{
				"current_failed":  {},
				"current_skipped": {},
			},
		},
	}

	tracker.PrintSummary()
	logger.Sync()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	logOutput := string(content)
	if !strings.Contains(logOutput, "current_failed: current failure") {
		t.Fatalf("current failed table missing from summary, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "current_skipped: current skip") {
		t.Fatalf("current skipped table missing from summary, got %q", logOutput)
	}
	if strings.Contains(logOutput, "historical_failed: historical failure") {
		t.Fatalf("historical failed table should not appear in summary, got %q", logOutput)
	}
	if strings.Contains(logOutput, "historical_skipped: historical skip") {
		t.Fatalf("historical skipped table should not appear in summary, got %q", logOutput)
	}
}
