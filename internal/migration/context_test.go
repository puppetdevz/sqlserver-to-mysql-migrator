package migration

import (
	"errors"
	"testing"
)

func TestNewMigrationContext(t *testing.T) {
	mc := NewMigrationContext()
	if mc == nil {
		t.Fatal("NewMigrationContext() returned nil")
	}
	if mc.ctx == nil {
		t.Fatal("context should not be nil")
	}
	if mc.Context().Err() != nil {
		t.Fatal("context should not be done initially")
	}
}

func TestContextReturnsLiveContext(t *testing.T) {
	mc := NewMigrationContext()
	select {
	case <-mc.Context().Done():
		t.Fatal("context should not be done")
	default:
	}
}

func TestStopCancelsContext(t *testing.T) {
	mc := NewMigrationContext()
	mc.Stop(nil)
	select {
	case <-mc.Context().Done():
	default:
		t.Fatal("context should be done after Stop()")
	}
}

func TestStopRecordsError(t *testing.T) {
	mc := NewMigrationContext()
	want := errors.New("something went wrong")
	mc.Stop(want)
	if got := mc.Err(); got != want {
		t.Fatalf("Err() = %v, want %v", got, want)
	}
}

func TestStopIdempotentKeepsFirstError(t *testing.T) {
	mc := NewMigrationContext()
	first := errors.New("first")
	second := errors.New("second")
	mc.Stop(first)
	mc.Stop(second)
	if got := mc.Err(); got != first {
		t.Fatalf("Err() = %v, want first error %v", got, first)
	}
}

func TestStopIdempotentDoesNotCancelTwice(t *testing.T) {
	mc := NewMigrationContext()
	mc.Stop(errors.New("first"))
	mc.Stop(errors.New("second"))
	// second call should not panic or change state
	if mc.Err().Error() != "first" {
		t.Fatal("second Stop should not overwrite first error")
	}
}

func TestErrReturnsNilInitially(t *testing.T) {
	mc := NewMigrationContext()
	if err := mc.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
}

func TestErrConcurrentSafety(t *testing.T) {
	mc := NewMigrationContext()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			mc.Stop(errors.New("error"))
		}
		close(done)
	}()
	<-done
	// if Err() panics, the test fails
	_ = mc.Err()
}
