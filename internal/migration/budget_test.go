package migration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBudgetBackpressureCancelAndOversize(t *testing.T) {
	b, err := NewBudget(10, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(context.Background(), 11); err == nil {
		t.Fatal("oversize did not fail immediately")
	}
	first, err := b.Acquire(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.Acquire(ctx, 3); done <- err }()
	select {
	case <-done:
		t.Fatal("batch count limit ignored")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel leaked waiter")
	}
	first.Release()
	first.Release()
	if bytes, count := b.Usage(); bytes != 0 || count != 0 {
		t.Fatal("lease release not idempotent")
	}
}
func TestBudgetConcurrentBoundedUsage(t *testing.T) {
	var high atomic.Int64
	var current atomic.Int64
	b, err := NewBudget(100, 3, func(delta int64) {
		n := current.Add(delta)
		for old := high.Load(); n > old; old = high.Load() {
			if high.CompareAndSwap(old, n) {
				break
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				lease, err := b.Acquire(context.Background(), 40)
				if err != nil {
					t.Error(err)
					return
				}
				lease.Release()
			}
		})
	}
	wg.Wait()
	if high.Load() > 100 || current.Load() != 0 {
		t.Fatalf("high %d remaining %d", high.Load(), current.Load())
	}
}
