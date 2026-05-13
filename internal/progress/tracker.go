package progress

import (
	"fmt"
	"sync"
	"time"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/logger"
)

// Tracker 进度跟踪器
type Tracker struct {
	store  *SQLiteStore
	state  *MigrationState
	mu     sync.RWMutex
	ticker *time.Ticker
	done   chan bool
}

// NewTracker 创建进度跟踪器
func NewTracker(stateDir string) (*Tracker, error) {
	store, err := NewSQLiteStore(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	// 尝试加载已有状态
	state, err := loadOrCreateState(store)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to load state: %w", err)
	}

	tracker := &Tracker{
		store: store,
		state: state,
		done:  make(chan bool),
	}

	return tracker, nil
}

// loadOrCreateState 加载或创建状态
func loadOrCreateState(store *SQLiteStore) (*MigrationState, error) {
	// 尝试从存储加载
	states, err := store.GetAllTableStates()
	if err != nil {
		return nil, err
	}

	if len(states) == 0 {
		// 创建新状态
		return NewMigrationState(), nil
	}

	// 重建状态
	state := NewMigrationState()
	state.Tables = states

	logger.Infof("Loaded existing state: %d tables", len(states))

	return state, nil
}

// Close 关闭跟踪器
func (t *Tracker) Close() error {
	if t.ticker != nil {
		t.ticker.Stop()
		t.done <- true
	}
	return t.store.Close()
}

// StartTable 开始处理表
func (t *Tracker) StartTable(tableName string, csvPath string, tableExists bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.state.GetTable(tableName)
	if state == nil {
		state = t.state.AddTable(tableName)
	}
	t.state.TrackRunTable(tableName)

	state.Status = StatusInProgress
	state.StartTime = time.Now()
	state.CSVPath = csvPath
	state.TableExists = tableExists

	return t.store.SaveTableState(state)
}

// CompleteTable 完成表处理
func (t *Tracker) CompleteTable(tableName string, processedRows, insertedRows, errorCount int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.state.GetTable(tableName)
	if state == nil {
		return fmt.Errorf("table state not found: %s", tableName)
	}
	t.state.TrackRunTable(tableName)

	state.Status = StatusCompleted
	state.ProcessedRows = processedRows
	state.InsertedRows = insertedRows
	state.ErrorCount = errorCount
	state.EndTime = time.Now()
	state.DurationMs = state.EndTime.Sub(state.StartTime).Milliseconds()

	t.state.UpdateTableStatus(tableName, StatusCompleted)

	return t.store.SaveTableState(state)
}

// MarkTableCreated 记录建表成功，但不推进 overall 最终完成计数
func (t *Tracker) MarkTableCreated(tableName string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.state.GetTable(tableName)
	if state == nil {
		return fmt.Errorf("table state not found: %s", tableName)
	}
	t.state.TrackRunTable(tableName)

	state.TableCreated = true
	state.EndTime = time.Now()
	state.DurationMs = state.EndTime.Sub(state.StartTime).Milliseconds()

	return t.store.SaveTableState(state)
}

// FailTable 标记表处理失败
func (t *Tracker) FailTable(tableName string, errorMessage string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.state.GetTable(tableName)
	if state == nil {
		state = t.state.AddTable(tableName)
	}
	t.state.TrackRunTable(tableName)

	state.Status = StatusFailed
	state.ErrorMessage = errorMessage
	state.EndTime = time.Now()
	state.DurationMs = state.EndTime.Sub(state.StartTime).Milliseconds()

	t.state.UpdateTableStatus(tableName, StatusFailed)

	return t.store.SaveTableState(state)
}

// SkipTable 跳过表处理
func (t *Tracker) SkipTable(tableName string, reason string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.state.GetTable(tableName)
	if state == nil {
		state = t.state.AddTable(tableName)
	}
	t.state.TrackRunTable(tableName)

	state.Status = StatusSkipped
	state.ErrorMessage = reason
	state.EndTime = time.Now()

	t.state.UpdateTableStatus(tableName, StatusSkipped)

	return t.store.SaveTableState(state)
}

// GetTableState 获取表状态
func (t *Tracker) GetTableState(tableName string) *TableState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.state.GetTable(tableName)
}

// GetCompletedTables 获取已完成的表列表
func (t *Tracker) GetCompletedTables() ([]string, error) {
	return t.store.GetCompletedTables()
}

// GetProgress 获取进度信息
func (t *Tracker) GetProgress() ProgressInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return ProgressInfo{
		TotalTables:    t.state.TotalTables,
		CompletedCount: t.state.CompletedCount,
		FailedCount:    t.state.FailedCount,
		SkippedCount:   t.state.SkippedCount,
		Progress:       t.state.GetProgress(),
		CurrentPhase:   t.state.CurrentPhase,
		PhaseTotal:     t.state.PhaseTotal,
		PhaseCompleted: t.state.PhaseCompleted,
		PhaseFailed:    t.state.PhaseFailed,
		PhaseSkipped:   t.state.PhaseSkipped,
		PhaseProgress:  t.state.GetPhaseProgress(),
		IsCompleted:    t.state.IsCompleted(),
	}
}

// StartPhase 开始新的阶段统计
func (t *Tracker) StartPhase(name string, total int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.state.CurrentPhase = name
	t.state.PhaseTotal = total
	t.state.PhaseCompleted = 0
	t.state.PhaseFailed = 0
	t.state.PhaseSkipped = 0
}

// CompletePhaseItem 增加阶段完成计数
func (t *Tracker) CompletePhaseItem() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.PhaseCompleted++
}

// FailPhaseItem 增加阶段失败计数
func (t *Tracker) FailPhaseItem() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.PhaseFailed++
}

// SkipPhaseItem 增加阶段跳过计数
func (t *Tracker) SkipPhaseItem() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.PhaseSkipped++
}

// ClearPhase 清空当前阶段统计
func (t *Tracker) ClearPhase() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.state.CurrentPhase = ""
	t.state.PhaseTotal = 0
	t.state.PhaseCompleted = 0
	t.state.PhaseFailed = 0
	t.state.PhaseSkipped = 0
}

// SetPlannedTotalTables 设置本次运行计划处理的总表数
func (t *Tracker) SetPlannedTotalTables(total int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.ResetRunCounts(total)
}

// ProgressInfo 进度信息
type ProgressInfo struct {
	TotalTables    int
	CompletedCount int
	FailedCount    int
	SkippedCount   int
	Progress       float64
	CurrentPhase   string
	PhaseTotal     int
	PhaseCompleted int
	PhaseFailed    int
	PhaseSkipped   int
	PhaseProgress  float64
	IsCompleted    bool
}

// StartProgressReporter 启动进度报告器
func (t *Tracker) StartProgressReporter(interval time.Duration) {
	t.ticker = time.NewTicker(interval)

	go func() {
		for {
			select {
			case <-t.ticker.C:
				t.reportProgress()
			case <-t.done:
				return
			}
		}
	}()
}

// reportProgress 报告进度
func (t *Tracker) reportProgress() {
	info := t.GetProgress()
	overallProcessed := info.CompletedCount + info.FailedCount + info.SkippedCount

	if info.CurrentPhase != "" && info.PhaseTotal > 0 {
		phaseProcessed := info.PhaseCompleted + info.PhaseFailed + info.PhaseSkipped
		logger.Infof("Phase(%s): %.2f%% (%d/%d) - Completed: %d, Failed: %d, Skipped: %d",
			info.CurrentPhase, info.PhaseProgress, phaseProcessed, info.PhaseTotal,
			info.PhaseCompleted, info.PhaseFailed, info.PhaseSkipped)
	}

	logger.Infof("Overall: %.2f%% (%d/%d tables) - Completed: %d, Failed: %d, Skipped: %d",
		info.Progress, overallProcessed, info.TotalTables,
		info.CompletedCount, info.FailedCount, info.SkippedCount)
}

// PrintSummary 打印摘要
func (t *Tracker) PrintSummary() {
	t.mu.RLock()
	defer t.mu.RUnlock()

	logger.Info("=== Migration Summary ===")
	logger.Infof("Total Tables: %d", t.state.TotalTables)
	logger.Infof("Completed: %d", t.state.CompletedCount)
	logger.Infof("Failed: %d", t.state.FailedCount)
	logger.Infof("Skipped: %d", t.state.SkippedCount)

	if t.state.FailedCount > 0 {
		logger.Warn("Failed tables:")
		for tableName := range t.state.RunTableNames {
			state, ok := t.state.Tables[tableName]
			if ok && state.Status == StatusFailed {
				logger.Warnf("  - %s: %s", state.TableName, state.ErrorMessage)
			}
		}
	}

	if t.state.SkippedCount > 0 {
		logger.Info("Skipped tables:")
		for tableName := range t.state.RunTableNames {
			state, ok := t.state.Tables[tableName]
			if ok && state.Status == StatusSkipped {
				logger.Infof("  - %s: %s", state.TableName, state.ErrorMessage)
			}
		}
	}
}

// Clear 清空状态
func (t *Tracker) Clear() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.state = NewMigrationState()
	return t.store.Clear()
}
