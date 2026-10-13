package runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConcurrencyLimitChanges(t *testing.T) {
	m := NewManager(nil, nil, 1, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.acquireSlot(ctx); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- m.acquireSlot(ctx) }()
	select {
	case err := <-acquired:
		t.Fatalf("exceeded initial limit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	m.SetMaxConcurrentTasks(2)
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("increasing the limit did not wake queued work")
	}
	m.SetMaxConcurrentTasks(1)
	// Both existing slots remain held after lowering the limit.
	for remaining := 2; remaining > 0; remaining-- {
		blocked, stop := context.WithTimeout(ctx, 20*time.Millisecond)
		err := m.acquireSlot(blocked)
		stop()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("admitted work with %d slots held at limit 1: %v", remaining, err)
		}
		m.releaseSlot()
	}
	if err := m.acquireSlot(ctx); err != nil {
		t.Fatal("did not admit work after slots drained", err)
	}
	m.releaseSlot()
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := m.acquireSlot(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled work acquired a free slot", err)
	}
}

func TestSourceWaitReacquiresCurrentLimit(t *testing.T) {
	m := NewManager(nil, nil, 2, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.acquireSlot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.acquireSlot(ctx); err != nil {
		t.Fatal(err)
	}
	held := true
	waited := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.waitWithoutSlot(ctx, &held)(func() error {
			m.SetMaxConcurrentTasks(1)
			close(waited)
			return nil
		})
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		t.Fatal("source wait did not release its slot")
	}
	select {
	case err := <-done:
		t.Fatalf("source wait exceeded the reduced limit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	m.releaseSlot()
	select {
	case err := <-done:
		if err != nil || !held {
			t.Fatal("source wait did not reacquire a slot", err)
		}
	case <-ctx.Done():
		t.Fatal("source wait remained blocked")
	}
	m.releaseSlot()
	held = true
	if err := m.acquireSlot(ctx); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("source failed")
	if err := m.waitWithoutSlot(ctx, &held)(func() error { return failure }); !errors.Is(err, failure) || held {
		t.Fatal("failed source wait retained its slot", err)
	}
}
