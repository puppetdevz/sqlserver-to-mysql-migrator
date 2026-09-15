package config

import (
	"fmt"

	"github.com/zhongyuming/sqlserver-to-mysql-migrator/internal/migration"
)

// ResourceConfig separates client memory/queue limits from SQL packet estimates,
// workers, connection pool limits, tokens, and Go CPU parallelism.
type ResourceConfig struct {
	MaxInflightBytes   int64 `yaml:"max_inflight_bytes"`
	MaxInflightBatches int   `yaml:"max_inflight_batches"`
	QueueBytes         int64 `yaml:"queue_bytes"`
	QueueBatches       int   `yaml:"queue_batches"`
	BatchMemoryBytes   int64 `yaml:"batch_memory_bytes"`
	SQLCacheBytes      int64 `yaml:"sql_cache_bytes"`
}

func (r ResourceConfig) Effective() ResourceConfig {
	if r.MaxInflightBytes == 0 {
		r.MaxInflightBytes = 256 << 20
	}
	if r.MaxInflightBatches == 0 {
		r.MaxInflightBatches = 32
	}
	if r.QueueBytes == 0 {
		r.QueueBytes = 32 << 20
	}
	if r.QueueBatches == 0 {
		r.QueueBatches = 10
	}
	if r.BatchMemoryBytes == 0 {
		r.BatchMemoryBytes = 8 << 20
	}
	if r.SQLCacheBytes == 0 {
		r.SQLCacheBytes = 1 << 20
	}
	return r
}
func (r ResourceConfig) Validate() error {
	r = r.Effective()
	if r.MaxInflightBytes <= 0 || r.MaxInflightBatches <= 0 || r.QueueBytes <= 0 || r.QueueBatches <= 0 || r.BatchMemoryBytes < 1024 || r.SQLCacheBytes <= 0 {
		return fmt.Errorf("resource limits must be positive and batch_memory_bytes >=1024")
	}
	if r.BatchMemoryBytes > r.MaxInflightBytes || r.BatchMemoryBytes > r.QueueBytes {
		return fmt.Errorf("batch_memory_bytes must fit both global and per-table queue budgets")
	}
	// Keep lengths/capacities representable on every supported 64-bit client.
	if r.QueueBatches > 10000 || r.MaxInflightBatches > 100000 || r.SQLCacheBytes > 64<<20 {
		return fmt.Errorf("resource batch/cache capacity exceeds supported bound")
	}
	return nil
}
func (c *Config) ImportBudget() (*migration.Budget, error) {
	c.budgetOnce.Do(func() {
		r := c.Migration.Resources.Effective()
		if err := r.Validate(); err != nil {
			c.budgetErr = err
			return
		}
		c.budget, c.budgetErr = migration.NewBudget(r.MaxInflightBytes, r.MaxInflightBatches, c.Diagnostics.Inflight)
	})
	return c.budget, c.budgetErr
}
