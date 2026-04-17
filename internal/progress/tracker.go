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
	state.TotalTables = len(states)
	state.updateCounts()

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

	state.Status = StatusCompleted
	state.ProcessedRows = processedRows
	state.InsertedRows = insertedRows
	state.ErrorCount = errorCount
	state.EndTime = time.Now()
	state.DurationMs = state.EndTime.Sub(state.StartTime).Milliseconds()

	t.state.UpdateTableStatus(tableName, StatusCompleted)

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
		TotalTables:           t.state.TotalTables,
		CompletedCount:        t.state.CompletedCount,
		FailedCount:          t.state.FailedCount,
		SkippedCount:         t.state.SkippedCount,
		Progress:              t.state.GetProgress(),
		SessionProgress:       t.state.GetSessionProgress(),
		SessionStartCount:     t.state.SessionStartCount,
		SessionStartCompleted: t.state.SessionStartCompleted,
		IsCompleted:           t.state.IsCompleted(),
	}
}

// SetSessionStartCount 设置当前会话的初始表数量
func (t *Tracker) SetSessionStartCount(count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.SessionStartCount = count
}

// SetSessionStartCompleted 设置当前会话开始时已完成的表数量
func (t *Tracker) SetSessionStartCompleted(count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.SessionStartCompleted = count
}

// ProgressInfo 进度信息
type ProgressInfo struct {
	TotalTables           int
	CompletedCount        int
	FailedCount           int
	SkippedCount          int
	Progress              float64
	SessionProgress       float64
	SessionStartCount     int
	SessionStartCompleted int
	IsCompleted           bool
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

	// 计算当前会话已处理的表数量（相对于会话开始时的总数）
	processedInSession := info.CompletedCount + info.FailedCount + info.SkippedCount

	// 如果设置了会话开始时的表数量，显示会话进度
	if info.SessionStartCount > 0 {
		logger.Infof("Progress: %.2f%% [Session: %d/%d tables] (Total: %d/%d) - Completed: %d, Failed: %d, Skipped: %d",
			info.SessionProgress, processedInSession, info.SessionStartCount,
			info.CompletedCount+info.FailedCount+info.SkippedCount, info.TotalTables,
			info.CompletedCount, info.FailedCount, info.SkippedCount)
	} else {
		logger.Infof("Progress: %.2f%% (%d/%d tables) - Completed: %d, Failed: %d, Skipped: %d",
			info.Progress, info.CompletedCount+info.FailedCount+info.SkippedCount, info.TotalTables,
			info.CompletedCount, info.FailedCount, info.SkippedCount)
	}
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
		for _, state := range t.state.Tables {
			if state.Status == StatusFailed {
				logger.Warnf("  - %s: %s", state.TableName, state.ErrorMessage)
			}
		}
	}

	if t.state.SkippedCount > 0 {
		logger.Info("Skipped tables:")
		for _, state := range t.state.Tables {
			if state.Status == StatusSkipped {
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
