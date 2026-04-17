package progress

import (
	"testing"
)

// TestGetSessionProgress 测试会话进度计算
func TestGetSessionProgress(t *testing.T) {
	tests := []struct {
		name                   string
		sessionStartCount      int
		sessionStartCompleted  int
		completedCount         int
		failedCount            int
		skippedCount           int
		expectedSessionProgress float64
	}{
		{
			name:                   "新会话无历史数据",
			sessionStartCount:      10,
			sessionStartCompleted: 0,
			completedCount:         5,
			failedCount:            0,
			skippedCount:           0,
			expectedSessionProgress: 50.0, // 5/10 = 50%
		},
		{
			name:                   "断点续传-已完成部分",
			sessionStartCount:      10,
			sessionStartCompleted: 100, // 历史已完成 100 表
			completedCount:         105, // 当前会话完成 5 表
			failedCount:            0,
			skippedCount:           0,
			expectedSessionProgress: 50.0, // (105-100)/10 = 5/10 = 50%
		},
		{
			name:                   "断点续传-全部完成",
			sessionStartCount:      10,
			sessionStartCompleted: 100,
			completedCount:         110,
			failedCount:            0,
			skippedCount:           0,
			expectedSessionProgress: 100.0, // (110-100)/10 = 10/10 = 100%
		},
		{
			name:                   "包含失败和跳过-仅计算成功完成",
			sessionStartCount:      10,
			sessionStartCompleted: 50,
			completedCount:         53,
			failedCount:            2,
			skippedCount:           1,
			expectedSessionProgress: 30.0, // (53-50)/10 = 3/10 = 30% (仅计算新完成的表)
		},
		{
			name:                   "零会话开始数量",
			sessionStartCount:      0,
			sessionStartCompleted:  0,
			completedCount:         5,
			failedCount:            0,
			skippedCount:           0,
			expectedSessionProgress: 0.0, // 除以零保护
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &MigrationState{
				SessionStartCount:     tt.sessionStartCount,
				SessionStartCompleted:  tt.sessionStartCompleted,
				CompletedCount:         tt.completedCount,
				FailedCount:            tt.failedCount,
				SkippedCount:           tt.skippedCount,
			}

			progress := state.GetSessionProgress()
			if progress != tt.expectedSessionProgress {
				t.Errorf("GetSessionProgress() = %v, want %v", progress, tt.expectedSessionProgress)
			}
		})
	}
}

// TestGetProgress 测试总进度计算（向后兼容）
func TestGetProgress(t *testing.T) {
	tests := []struct {
		name               string
		totalTables        int
		completedCount     int
		failedCount        int
		skippedCount       int
		expectedProgress   float64
	}{
		{
			name:             "正常进度",
			totalTables:      100,
			completedCount:   50,
			failedCount:       0,
			skippedCount:      0,
			expectedProgress: 50.0,
		},
		{
			name:             "全部完成",
			totalTables:      100,
			completedCount:   95,
			failedCount:       3,
			skippedCount:      2,
			expectedProgress: 100.0,
		},
		{
			name:             "零表",
			totalTables:      0,
			completedCount:   0,
			failedCount:       0,
			skippedCount:      0,
			expectedProgress: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &MigrationState{
				TotalTables:    tt.totalTables,
				CompletedCount: tt.completedCount,
				FailedCount:    tt.failedCount,
				SkippedCount:   tt.skippedCount,
			}

			progress := state.GetProgress()
			if progress != tt.expectedProgress {
				t.Errorf("GetProgress() = %v, want %v", progress, tt.expectedProgress)
			}
		})
	}
}
