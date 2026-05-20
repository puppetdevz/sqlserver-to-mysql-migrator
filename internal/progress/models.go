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
	TableName     string          `json:"table_name"`
	Status        MigrationStatus `json:"status"`
	TotalRows     int64           `json:"total_rows"`
	ProcessedRows int64           `json:"processed_rows"`
	InsertedRows  int64           `json:"inserted_rows"`
	ErrorCount    int64           `json:"error_count"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	StartTime     time.Time       `json:"start_time"`
	EndTime       time.Time       `json:"end_time,omitempty"`
	DurationMs    int64           `json:"duration_ms"`
	CSVPath       string          `json:"csv_path"`
	TableExists   bool            `json:"table_exists"`
	TableCreated  bool            `json:"table_created"`
}

// TableProgress 单表行级进度
type TableProgress struct {
	TableName     string
	TotalRows     int64
	ProcessedRows int64
	InsertedRows  int64
	Percent       float64
}

// MigrationState 整体迁移状态
type MigrationState struct {
	StartTime      time.Time              `json:"start_time"`
	EndTime        time.Time              `json:"end_time,omitempty"`
	TotalTables    int                    `json:"total_tables"` // 本次运行计划处理的总表数
	CompletedCount int                    `json:"completed_count"` // 本次运行已完成表数
	FailedCount    int                    `json:"failed_count"` // 本次运行失败表数
	SkippedCount   int                    `json:"skipped_count"` // 本次运行跳过表数
	Tables         map[string]*TableState `json:"tables"`

	CurrentPhase   string `json:"current_phase"`
	PhaseTotal     int    `json:"phase_total"`
	PhaseCompleted int    `json:"phase_completed"`
	PhaseFailed    int    `json:"phase_failed"`
	PhaseSkipped   int    `json:"phase_skipped"`

	RunTableNames map[string]struct{} `json:"-"`
}

// NewMigrationState 创建新的迁移状态
func NewMigrationState() *MigrationState {
	return &MigrationState{
		StartTime: time.Now(),
		Tables:    make(map[string]*TableState),
		RunTableNames: make(map[string]struct{}),
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

	for tableName := range ms.RunTableNames {
		state, ok := ms.Tables[tableName]
		if !ok {
			continue
		}
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

// ResetRunCounts 重置本次运行的 overall 统计口径
func (ms *MigrationState) ResetRunCounts(total int) {
	ms.TotalTables = total
	ms.CompletedCount = 0
	ms.FailedCount = 0
	ms.SkippedCount = 0
	ms.RunTableNames = make(map[string]struct{})
}

// TrackRunTable 将表纳入本次运行口径
func (ms *MigrationState) TrackRunTable(tableName string) {
	if ms.RunTableNames == nil {
		ms.RunTableNames = make(map[string]struct{})
	}
	ms.RunTableNames[tableName] = struct{}{}
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

// GetPhaseProgress 获取当前阶段进度百分比
func (ms *MigrationState) GetPhaseProgress() float64 {
	if ms.PhaseTotal == 0 {
		return 0
	}

	processed := ms.PhaseCompleted + ms.PhaseFailed + ms.PhaseSkipped
	return float64(processed) / float64(ms.PhaseTotal) * 100
}

// GetActiveTables 返回所有处于 in_progress 状态的表的进度
func (ms *MigrationState) GetActiveTables() map[string]*TableProgress {
	result := make(map[string]*TableProgress)
	for tableName := range ms.RunTableNames {
		state, ok := ms.Tables[tableName]
		if !ok || state.Status != StatusInProgress {
			continue
		}
		p := &TableProgress{
			TableName:     tableName,
			TotalRows:     state.TotalRows,
			ProcessedRows: state.ProcessedRows,
			InsertedRows:  state.InsertedRows,
		}
		if state.TotalRows > 0 {
			p.Percent = float64(state.ProcessedRows) / float64(state.TotalRows) * 100
		}
		result[tableName] = p
	}
	return result
}
