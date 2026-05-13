package migration

import (
	"context"
	"sync"
)

// MigrationContext 迁移上下文，用于传播停止信号
type MigrationContext struct {
	ctx    context.Context
	cancel context.CancelFunc
	stop   sync.Once
	err    error // 记录第一个错误
	errMu  sync.RWMutex
}

// NewMigrationContext 创建迁移上下文
func NewMigrationContext() *MigrationContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &MigrationContext{
		ctx:    ctx,
		cancel: cancel,
	}
}

// Context 获取 context
func (mc *MigrationContext) Context() context.Context {
	return mc.ctx
}

// Stop 停止所有 worker
func (mc *MigrationContext) Stop(err error) {
	mc.stop.Do(func() {
		mc.errMu.Lock()
		mc.err = err
		mc.errMu.Unlock()
		mc.cancel()
	})
}

// Err 获取第一个错误
func (mc *MigrationContext) Err() error {
	mc.errMu.RLock()
	defer mc.errMu.RUnlock()
	return mc.err
}
