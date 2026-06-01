package importer

import (
	"context"
	"testing"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

func TestClassifyImportCandidateUsesMBThresholds(t *testing.T) {
	cfg := config.MigrationConfig{
		AdaptiveImport: config.AdaptiveImportConfig{
			ImportTokens: 10,
			LargeTableMB: 1024,
			HugeTableMB:  5120,
		},
	}

	tests := []struct {
		name       string
		sizeBytes  int64
		wantClass  ImportTableClass
		wantWeight int
	}{
		{"small", 64 * mb, ImportTableSmall, 1},
		{"medium", 512 * mb, ImportTableMedium, 2},
		{"large", 2 * 1024 * mb, ImportTableLarge, 4},
		{"huge", 6 * 1024 * mb, ImportTableHuge, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewImportCandidate(tt.name, "/tmp/"+tt.name+".csv", tt.sizeBytes, cfg)
			if got.Class != tt.wantClass {
				t.Fatalf("Class = %s, want %s", got.Class, tt.wantClass)
			}
			if got.Weight != tt.wantWeight {
				t.Fatalf("Weight = %d, want %d", got.Weight, tt.wantWeight)
			}
		})
	}
}

func TestClassifyImportCandidateBoundaryThresholds(t *testing.T) {
	cfg := config.MigrationConfig{
		AdaptiveImport: config.AdaptiveImportConfig{
			ImportTokens: 10,
			LargeTableMB: 1024,
			HugeTableMB:  5120,
		},
	}

	tests := []struct {
		name       string
		sizeBytes  int64
		wantClass  ImportTableClass
		wantWeight int
	}{
		{"large_boundary", 1024 * mb, ImportTableLarge, 4},
		{"huge_boundary", 5120 * mb, ImportTableHuge, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewImportCandidate(tt.name, "/tmp/"+tt.name+".csv", tt.sizeBytes, cfg)
			if got.Class != tt.wantClass {
				t.Fatalf("Class = %s, want %s", got.Class, tt.wantClass)
			}
			if got.Weight != tt.wantWeight {
				t.Fatalf("Weight = %d, want %d", got.Weight, tt.wantWeight)
			}
		})
	}
}

func TestOrderImportCandidatesStartsLargeEarlyAndFillsGaps(t *testing.T) {
	candidates := []ImportCandidate{
		{TableName: "small_a", Class: ImportTableSmall, Weight: 1},
		{TableName: "large_a", Class: ImportTableLarge, Weight: 4},
		{TableName: "small_b", Class: ImportTableSmall, Weight: 1},
		{TableName: "huge_a", Class: ImportTableHuge, Weight: 6},
		{TableName: "medium_a", Class: ImportTableMedium, Weight: 2},
		{TableName: "large_b", Class: ImportTableLarge, Weight: 4},
	}

	ordered := OrderImportCandidates(candidates)
	got := make([]string, len(ordered))
	for i, candidate := range ordered {
		got[i] = candidate.TableName
	}

	want := []string{"huge_a", "small_a", "large_a", "medium_a", "small_b", "large_b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order[%d] = %s, want %s; full order=%v", i, got[i], want[i], got)
		}
	}
}

func TestOrderImportCandidatesPreservesUnknownClassAsSmall(t *testing.T) {
	candidates := []ImportCandidate{
		{TableName: "unknown_empty", Class: "", Weight: 1},
		{TableName: "large_a", Class: ImportTableLarge, Weight: 4},
		{TableName: "unknown_custom", Class: ImportTableClass("custom"), Weight: 1},
	}

	ordered := OrderImportCandidates(candidates)
	got := make([]string, len(ordered))
	for i, candidate := range ordered {
		got[i] = candidate.TableName
	}

	want := []string{"unknown_empty", "large_a", "unknown_custom"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order[%d] = %s, want %s; full order=%v", i, got[i], want[i], got)
		}
	}
}

func TestAdaptiveLimiterAcquireReleaseAndPressureRecovery(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	limiter := NewAdaptiveLimiter(10, 2, time.Minute, func() time.Time { return now })

	if err := limiter.Acquire(context.Background(), 6); err != nil {
		t.Fatalf("Acquire huge error = %v", err)
	}
	if err := limiter.Acquire(context.Background(), 4); err != nil {
		t.Fatalf("Acquire large error = %v", err)
	}
	if got := limiter.UsedTokens(); got != 10 {
		t.Fatalf("UsedTokens() = %d, want 10", got)
	}

	limiter.Release(6)
	if got := limiter.UsedTokens(); got != 4 {
		t.Fatalf("UsedTokens() after release = %d, want 4", got)
	}

	limiter.RecordPressure(ImportPressureEvent{TableName: "large_a", Signal: PressureRetry})
	if got := limiter.DynamicLimit(); got != 8 {
		t.Fatalf("DynamicLimit() after pressure = %d, want 8", got)
	}

	now = now.Add(59 * time.Second)
	limiter.TryRecover()
	if got := limiter.DynamicLimit(); got != 8 {
		t.Fatalf("DynamicLimit() before window = %d, want 8", got)
	}

	now = now.Add(2 * time.Second)
	limiter.TryRecover()
	if got := limiter.DynamicLimit(); got != 9 {
		t.Fatalf("DynamicLimit() after recovery = %d, want 9", got)
	}
}

func TestAdaptiveLimiterBlockedAcquireSucceedsAfterRelease(t *testing.T) {
	limiter := NewAdaptiveLimiter(2, 1, time.Minute, time.Now)

	if err := limiter.Acquire(context.Background(), 2); err != nil {
		t.Fatalf("Acquire initial tokens error = %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- limiter.Acquire(context.Background(), 1)
	}()

	assertNoAcquireBeforeTimeout(t, errCh, 25*time.Millisecond)

	limiter.Release(2)
	if err := waitForAcquire(t, errCh); err != nil {
		t.Fatalf("blocked Acquire error = %v", err)
	}
	if got := limiter.UsedTokens(); got != 1 {
		t.Fatalf("UsedTokens() = %d, want 1", got)
	}
}

func TestAdaptiveLimiterBlockedAcquireReturnsOnContextCancel(t *testing.T) {
	limiter := NewAdaptiveLimiter(1, 1, time.Minute, time.Now)

	if err := limiter.Acquire(context.Background(), 1); err != nil {
		t.Fatalf("Acquire initial token error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- limiter.Acquire(ctx, 1)
	}()

	assertNoAcquireBeforeTimeout(t, errCh, 25*time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("Acquire error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Acquire did not return after context cancellation")
	}
}

func TestAdaptiveLimiterPressureBelowUsedTokensWaitsUntilReleaseMakesRoom(t *testing.T) {
	limiter := NewAdaptiveLimiter(10, 2, time.Minute, time.Now)

	if err := limiter.Acquire(context.Background(), 10); err != nil {
		t.Fatalf("Acquire initial tokens error = %v", err)
	}
	limiter.RecordPressure(ImportPressureEvent{TableName: "t", Signal: PressureRetry})
	if got := limiter.DynamicLimit(); got != 8 {
		t.Fatalf("DynamicLimit() = %d, want 8", got)
	}
	if got := limiter.UsedTokens(); got != 10 {
		t.Fatalf("UsedTokens() after pressure = %d, want 10", got)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- limiter.Acquire(context.Background(), 1)
	}()

	assertNoAcquireBeforeTimeout(t, errCh, 25*time.Millisecond)

	limiter.Release(3)
	if err := waitForAcquire(t, errCh); err != nil {
		t.Fatalf("blocked Acquire error = %v", err)
	}
	if got := limiter.UsedTokens(); got != 8 {
		t.Fatalf("UsedTokens() after release and acquire = %d, want 8", got)
	}
}

func TestAdaptiveLimiterAllowsHugeTableAloneBelowDynamicLimit(t *testing.T) {
	limiter := NewAdaptiveLimiter(10, 2, time.Minute, time.Now)
	limiter.RecordPressure(ImportPressureEvent{TableName: "t", Signal: PressureRetry})
	limiter.RecordPressure(ImportPressureEvent{TableName: "t", Signal: PressureRetry})
	limiter.RecordPressure(ImportPressureEvent{TableName: "t", Signal: PressureRetry})

	if got := limiter.DynamicLimit(); got != 4 {
		t.Fatalf("DynamicLimit() = %d, want 4", got)
	}
	if err := limiter.Acquire(context.Background(), 6); err != nil {
		t.Fatalf("Acquire huge alone error = %v", err)
	}
	if got := limiter.UsedTokens(); got != 6 {
		t.Fatalf("UsedTokens() = %d, want 6", got)
	}
}

func assertNoAcquireBeforeTimeout(t *testing.T, errCh <-chan error, d time.Duration) {
	t.Helper()

	select {
	case err := <-errCh:
		t.Fatalf("Acquire returned before it should block: %v", err)
	case <-time.After(d):
	}
}

func waitForAcquire(t *testing.T, errCh <-chan error) error {
	t.Helper()

	select {
	case err := <-errCh:
		return err
	case <-time.After(time.Second):
		t.Fatal("Acquire did not return before timeout")
		return nil
	}
}
