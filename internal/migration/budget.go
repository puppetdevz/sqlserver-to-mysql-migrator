package migration

import (
	"context"
	"errors"
	"sync"
)

var ErrResourceLimit = errors.New("client resource limit exceeded")

// Budget bounds both simultaneously held batch reservations and their estimated
// bytes. A reservation covers production, queueing, and consumption until Release.
// Fixed full-batch reservations avoid the partial-batch allocation deadlock.
type Budget struct {
	mu         sync.Mutex
	notify     chan struct{}
	maxBytes   int64
	maxBatches int
	bytes      int64
	batches    int
	onBytes    func(int64)
}
type Lease struct {
	once    sync.Once
	release func()
}

func (l *Lease) Release() {
	if l != nil {
		l.once.Do(l.release)
	}
}
func NewBudget(bytes int64, batches int, onBytes func(int64)) (*Budget, error) {
	if bytes <= 0 || batches <= 0 {
		return nil, errors.New("resource budgets must be positive")
	}
	return &Budget{notify: make(chan struct{}), maxBytes: bytes, maxBatches: batches, onBytes: onBytes}, nil
}
func (b *Budget) Acquire(ctx context.Context, bytes int64) (*Lease, error) {
	if bytes <= 0 || bytes > b.maxBytes {
		return nil, errors.New("single reservation exceeds byte budget")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.batches < b.maxBatches && bytes <= b.maxBytes-b.bytes {
			b.batches++
			b.bytes += bytes
			if b.onBytes != nil {
				b.onBytes(bytes)
			}
			b.mu.Unlock()
			return &Lease{release: func() {
				b.mu.Lock()
				defer b.mu.Unlock()
				b.batches--
				b.bytes -= bytes
				if b.onBytes != nil {
					b.onBytes(-bytes)
				}
				close(b.notify)
				b.notify = make(chan struct{})
			}}, nil
		}
		notify := b.notify
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-notify:
		}
	}
}
func (b *Budget) Usage() (int64, int) { b.mu.Lock(); defer b.mu.Unlock(); return b.bytes, b.batches }
