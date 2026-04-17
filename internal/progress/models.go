package progress

import "time"

// MigrationStatus 迁移状态
type MigrationStatus string

const (
	StatusPending    MigrationStatus = "pending"
	StatusInProgress MigrationStatus = "in_progress"
	StatusCompleted  MigrationStatus = "completed"
	StatusFailed     MigrationStatus = "failed"
	StatusSkipped    MigrationStatus = "skipped"
)

// TableState 表迁移状态
type TableState struct {
	TableName      string          `json:"table_name"`
	Status         MigrationStatus `json:"status"`
	TotalRows      int64           `json:"total_rows"`
	ProcessedRows  int64           `json:"processed_rows"`
	InsertedRows   int64           `json:"inserted_rows"`
	ErrorCount     int64           `json:"error_count"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	StartTime      time.Time       `json:"start_time"`
	EndTime        time.Time       `json:"end_time,omitempty"`
	DurationMs     int64           `json:"duration_ms"`
	CSVPath        string          `json:"csv_path"`
	TableExists    bool            `json:"table_exists"`
	TableCreated   bool            `json:"table_created"`
}

// MigrationState 整体迁移状态
type MigrationState struct {
	StartTime          time.Time              `json:"start_time"`
	EndTime            time.Time              `json:"end_time,omitempty"`
	TotalTables        int                    `json:"total_tables"`       // 总表数（所有状态）
	CompletedCount     int                    `json:"completed_count"`
	FailedCount        int                    `json:"failed_count"`
	SkippedCount       int                    `json:"skipped_count"`
	Tables             map[string]*TableState `json:"tables"`
	SessionStartCount  int                    `json:"session_start_count"` // 当前会话开始时的表数量
	SessionStartCompleted int                 `json:"session_start_completed"` // 当前会话开始时已完成的表数量
}

// NewMigrationState 创建新的迁移状态
func NewMigrationState() *MigrationState {
	return &MigrationState{
		StartTime: time.Now(),
		Tables:    make(map[string]*TableState),
	}
}

// AddTable 添加表状态
func (ms *MigrationState) AddTable(tableName string) *TableState {
	state := &TableState{
		TableName: tableName,
		Status:    StatusPending,
		StartTime: time.Now(),
	}
	ms.Tables[tableName] = state
	ms.TotalTables++
	return state
}

// GetTable 获取表状态
func (ms *MigrationState) GetTable(tableName string) *TableState {
	return ms.Tables[tableName]
}

// UpdateTableStatus 更新表状态
func (ms *MigrationState) UpdateTableStatus(tableName string, status MigrationStatus) {
	if state, ok := ms.Tables[tableName]; ok {
		state.Status = status
		if status == StatusCompleted || status == StatusFailed || status == StatusSkipped {
			state.EndTime = time.Now()
			state.DurationMs = state.EndTime.Sub(state.StartTime).Milliseconds()
		}
		ms.updateCounts()
	}
}

// updateCounts 更新统计计数
func (ms *MigrationState) updateCounts() {
	completed := 0
	failed := 0
	skipped := 0

	for _, state := range ms.Tables {
		switch state.Status {
		case StatusCompleted:
			completed++
		case StatusFailed:
			failed++
		case StatusSkipped:
			skipped++
		}
	}

	ms.CompletedCount = completed
	ms.FailedCount = failed
	ms.SkippedCount = skipped
}

// IsCompleted 检查是否全部完成
func (ms *MigrationState) IsCompleted() bool {
	return ms.CompletedCount+ms.FailedCount+ms.SkippedCount == ms.TotalTables
}

// GetProgress 获取进度百分比
func (ms *MigrationState) GetProgress() float64 {
	if ms.TotalTables == 0 {
		return 0
	}
	return float64(ms.CompletedCount+ms.FailedCount+ms.SkippedCount) / float64(ms.TotalTables) * 100
}

// SetSessionStartCount 设置当前会话开始的表数量
func (ms *MigrationState) SetSessionStartCount(count int) {
	ms.SessionStartCount = count
}

// SetSessionStartCompleted 设置当前会话开始时已完成的表数量
func (ms *MigrationState) SetSessionStartCompleted(count int) {
	ms.SessionStartCompleted = count
}

// GetSessionProgress 获取当前会话的进度百分比
// 计算方式：(当前已完成的表数 - 会话开始时已完成的表数) / 会话开始时的表数量
func (ms *MigrationState) GetSessionProgress() float64 {
	if ms.SessionStartCount == 0 {
		return 0
	}
	newlyCompleted := ms.CompletedCount - ms.SessionStartCompleted
	if newlyCompleted < 0 {
		newlyCompleted = 0
	}
	return float64(newlyCompleted) / float64(ms.SessionStartCount) * 100
}
