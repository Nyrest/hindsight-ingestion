package httpx

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPacerRegistryRetiresIdleKeys(t *testing.T) {
	p := &PacerRegistry{Interval: 10 * time.Millisecond}
	for i := 0; i < 1000; i++ {
		if err := p.Wait(t.Context(), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		empty := len(p.next) == 0 && p.timer == nil
		p.mu.Unlock()
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle pacing keys retained")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPacerRegistryKeepsFutureReservations(t *testing.T) {
	p := &PacerRegistry{Interval: 20 * time.Millisecond}
	if err := p.Wait(t.Context(), "account"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Wait(ctx, "other"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.timer.Stop()
	p.timer = nil
	p.next["account"] = &pacingEntry{next: time.Now().Add(time.Hour), users: 1}
	p.mu.Unlock()
	p.evict()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.next) != 1 {
		t.Fatal("future reservation was evicted")
	}
	p.timer.Stop()
}

func TestPacerCancelledQueueDoesNotDelayFutureClients(t *testing.T) {
	p := &PacerRegistry{Interval: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	p.mu.Lock()
	p.next = map[string]*pacingEntry{"account": {next: time.Now().Add(time.Hour)}}
	p.mu.Unlock()
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	if err := p.Wait(ctx, "account"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stop()
	if err := p.Wait(deadline, "account"); err != nil {
		t.Fatal("cancelled queue delayed an unused key", err)
	}
	p.mu.Lock()
	p.timer.Stop()
	p.mu.Unlock()
}
