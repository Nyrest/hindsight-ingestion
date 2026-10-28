package httpx

import (
	"context"
	"sync"
	"time"
)

type pacingEntry struct {
	next, last time.Time
	users      int
}

// PacerRegistry shares reservations across clients without permanently retaining
// retired accounts. A single timer retires keys after their last reserved slot.
type PacerRegistry struct {
	Interval time.Duration
	mu       sync.Mutex
	next     map[string]*pacingEntry
	timer    *time.Timer
}

func (p *PacerRegistry) Wait(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.next == nil {
		p.next = map[string]*pacingEntry{}
	}
	e := p.next[key]
	if e == nil {
		e = &pacingEntry{}
		p.next[key] = e
	}
	now := time.Now()
	slot := e.next
	if slot.Before(now) {
		slot = now
	}
	e.next = slot.Add(p.Interval)
	e.users++
	if p.timer == nil {
		p.timer = time.AfterFunc(p.Interval, p.evict)
	}
	p.mu.Unlock()
	err := sleep(ctx, max(time.Until(slot), 0))
	p.mu.Lock()
	e.users--
	if err == nil {
		e.last = time.Now().Add(p.Interval)
	}
	// Cancelled reservations must not keep an unused key (or throttle a future
	// client) for the cumulative duration of an abandoned queue.
	if e.users == 0 {
		e.next = e.last
	} else if err != nil && e.next.Equal(slot.Add(p.Interval)) {
		e.next = slot
	}
	p.mu.Unlock()
	return err
}

func (p *PacerRegistry) evict() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timer = nil
	now := time.Now()
	for key, e := range p.next {
		if e.users == 0 && !e.next.After(now) {
			delete(p.next, key)
		}
	}
	if len(p.next) != 0 {
		p.timer = time.AfterFunc(p.Interval, p.evict)
	} else {
		p.next = nil
	}
}
