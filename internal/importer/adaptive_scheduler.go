package importer

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/config"
)

const mb int64 = 1024 * 1024
const mediumTableMB int64 = 128

type ImportTableClass string

const (
	ImportTableSmall  ImportTableClass = "small"
	ImportTableMedium ImportTableClass = "medium"
	ImportTableLarge  ImportTableClass = "large"
	ImportTableHuge   ImportTableClass = "huge"
)

type ImportCandidate struct {
	TableName string
	CSVPath   string
	SizeBytes int64
	SizeMB    int64
	Class     ImportTableClass
	Weight    int
}

func NewImportCandidate(tableName, csvPath string, sizeBytes int64, cfg config.MigrationConfig) ImportCandidate {
	class, weight := classifyImportTable(sizeBytes, cfg)
	return ImportCandidate{
		TableName: tableName,
		CSVPath:   csvPath,
		SizeBytes: sizeBytes,
		SizeMB:    sizeBytes / mb,
		Class:     class,
		Weight:    weight,
	}
}

func classifyImportTable(sizeBytes int64, cfg config.MigrationConfig) (ImportTableClass, int) {
	sizeMB := sizeBytes / mb
	switch {
	case sizeMB >= int64(cfg.EffectiveHugeTableMB()):
		return ImportTableHuge, 6
	case sizeMB >= int64(cfg.EffectiveLargeTableMB()):
		return ImportTableLarge, 4
	case sizeMB >= mediumTableMB:
		return ImportTableMedium, 2
	default:
		return ImportTableSmall, 1
	}
}

func OrderImportCandidates(candidates []ImportCandidate) []ImportCandidate {
	buckets := map[ImportTableClass][]ImportCandidate{
		ImportTableHuge:   nil,
		ImportTableLarge:  nil,
		ImportTableMedium: nil,
		ImportTableSmall:  nil,
	}
	for _, candidate := range candidates {
		candidate.Class = normalizeImportTableClass(candidate.Class)
		buckets[candidate.Class] = append(buckets[candidate.Class], candidate)
	}
	for class := range buckets {
		sort.SliceStable(buckets[class], func(i, j int) bool {
			return buckets[class][i].SizeBytes > buckets[class][j].SizeBytes
		})
	}

	ordered := make([]ImportCandidate, 0, len(candidates))
	for len(buckets[ImportTableHuge])+len(buckets[ImportTableLarge])+len(buckets[ImportTableMedium])+len(buckets[ImportTableSmall]) > 0 {
		ordered, buckets = popOne(ordered, buckets, ImportTableHuge)
		ordered, buckets = popOne(ordered, buckets, ImportTableSmall)
		ordered, buckets = popOne(ordered, buckets, ImportTableLarge)
		ordered, buckets = popOne(ordered, buckets, ImportTableMedium)
	}
	return ordered
}

func popOne(ordered []ImportCandidate, buckets map[ImportTableClass][]ImportCandidate, class ImportTableClass) ([]ImportCandidate, map[ImportTableClass][]ImportCandidate) {
	if len(buckets[class]) == 0 {
		return ordered, buckets
	}
	ordered = append(ordered, buckets[class][0])
	buckets[class] = buckets[class][1:]
	return ordered, buckets
}

func normalizeImportTableClass(class ImportTableClass) ImportTableClass {
	switch class {
	case ImportTableHuge, ImportTableLarge, ImportTableMedium, ImportTableSmall:
		return class
	default:
		return ImportTableSmall
	}
}

type PressureSignal string

const (
	PressureSlowBatch  PressureSignal = "slow_batch"
	PressureRetry      PressureSignal = "retry"
	PressureConnection PressureSignal = "connection"
	PressureLockWait   PressureSignal = "lock_wait"
	PressureCapacity   PressureSignal = "capacity"
)

type ImportPressureEvent struct {
	TableName string
	BatchNum  int
	Signal    PressureSignal
	Detail    string
}

type AdaptiveLimiter struct {
	mu             sync.Mutex
	notify         chan struct{}
	maxTokens      int
	minTokens      int
	dynamicLimit   int
	usedTokens     int
	recoveryWindow time.Duration
	now            func() time.Time
	lastPressure   time.Time
	lastRecovery   time.Time
}

func NewAdaptiveLimiter(maxTokens, minTokens int, recoveryWindow time.Duration, now func() time.Time) *AdaptiveLimiter {
	if maxTokens <= 0 {
		maxTokens = 10
	}
	if minTokens <= 0 {
		minTokens = 2
	}
	if minTokens > maxTokens {
		minTokens = maxTokens
	}
	if now == nil {
		now = time.Now
	}
	l := &AdaptiveLimiter{
		maxTokens:      maxTokens,
		minTokens:      minTokens,
		dynamicLimit:   maxTokens,
		recoveryWindow: recoveryWindow,
		now:            now,
		notify:         make(chan struct{}),
	}
	return l
}

func (l *AdaptiveLimiter) Acquire(ctx context.Context, weight int) error {
	weight = l.normalizeWeight(weight)

	for {
		l.mu.Lock()
		if l.canAcquire(weight) {
			l.usedTokens += weight
			l.mu.Unlock()
			return nil
		}
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		notify := l.notify
		l.mu.Unlock()

		select {
		case <-notify:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *AdaptiveLimiter) canAcquire(weight int) bool {
	if l.usedTokens == 0 && weight > l.dynamicLimit && weight <= l.maxTokens {
		return true
	}
	return l.usedTokens+weight <= l.dynamicLimit
}

func (l *AdaptiveLimiter) Release(weight int) {
	weight = l.normalizeWeight(weight)

	l.mu.Lock()
	l.usedTokens -= weight
	if l.usedTokens < 0 {
		l.usedTokens = 0
	}
	l.broadcastLocked()
	l.mu.Unlock()
}

func (l *AdaptiveLimiter) RecordPressure(event ImportPressureEvent) {
	l.mu.Lock()
	now := l.now()
	l.lastPressure = now
	if l.dynamicLimit > l.minTokens {
		l.dynamicLimit -= 2
		if l.dynamicLimit < l.minTokens {
			l.dynamicLimit = l.minTokens
		}
	}
	l.broadcastLocked()
	l.mu.Unlock()
}

func (l *AdaptiveLimiter) TryRecover() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if l.dynamicLimit >= l.maxTokens {
		return false
	}
	if !l.lastPressure.IsZero() && now.Sub(l.lastPressure) < l.recoveryWindow {
		return false
	}
	if !l.lastRecovery.IsZero() && now.Sub(l.lastRecovery) < l.recoveryWindow {
		return false
	}

	l.dynamicLimit++
	l.lastRecovery = now
	l.broadcastLocked()
	return true
}

func (l *AdaptiveLimiter) DynamicLimit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dynamicLimit
}

func (l *AdaptiveLimiter) UsedTokens() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.usedTokens
}

func (l *AdaptiveLimiter) normalizeWeight(weight int) int {
	if weight < 1 {
		return 1
	}
	if weight > l.maxTokens {
		return l.maxTokens
	}
	return weight
}

func (l *AdaptiveLimiter) broadcastLocked() {
	close(l.notify)
	l.notify = make(chan struct{})
}
