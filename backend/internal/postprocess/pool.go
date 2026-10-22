package postprocess

import (
	"context"
	"sync"
	"time"
)

// Pool keeps a few recently used VMs so consecutive runs of the same task
// skip runtime setup. Nothing is allocated until a script is first needed,
// and idle VMs are released after IdleTTL so an unused pool holds no runtimes.
type Pool struct {
	// MaxIdle bounds the VMs retained between uses.
	MaxIdle int
	// IdleTTL is how long an unused VM is retained.
	IdleTTL time.Duration

	mu    sync.Mutex
	idle  []*VM
	timer *time.Timer
}

// Shared is the process-wide pool.
var Shared = &Pool{MaxIdle: 4, IdleTTL: 2 * time.Minute}

// Acquire prepares a VM, reusing an idle one with the same key. User code is
// initialized on the first Process call, under its execution deadline.
func (p *Pool) Acquire(key, script string) (*VM, error) {
	p.mu.Lock()
	for i := len(p.idle) - 1; i >= 0; i-- {
		if v := p.idle[i]; v.key == key {
			p.idle = append(p.idle[:i], p.idle[i+1:]...)
			p.mu.Unlock()
			return v, nil
		}
	}
	p.mu.Unlock()
	return newVM(key, script)
}

// Release returns a VM for reuse. Broken VMs are discarded.
func (p *Pool) Release(v *VM) {
	if v == nil || v.broken || p.MaxIdle <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	v.idle = time.Now()
	p.idle = append(p.idle, v)
	if over := len(p.idle) - p.MaxIdle; over > 0 {
		clear(p.idle[:over])
		p.idle = p.idle[over:]
	}
	if p.timer == nil {
		p.timer = time.AfterFunc(p.IdleTTL, p.evict)
	}
}

func (p *Pool) evict() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timer = nil
	cutoff := time.Now().Add(-p.IdleTTL)
	kept := p.idle[:0]
	for _, v := range p.idle {
		if v.idle.After(cutoff) {
			kept = append(kept, v)
		}
	}
	clear(p.idle[len(kept):])
	p.idle = kept
	if len(p.idle) == 0 {
		// Drop the backing array too so an idle pool retains nothing.
		p.idle = nil
		return
	}
	next := time.Until(p.idle[0].idle.Add(p.IdleTTL))
	for _, v := range p.idle[1:] {
		next = min(next, time.Until(v.idle.Add(p.IdleTTL)))
	}
	p.timer = time.AfterFunc(max(next, time.Second), p.evict)
}

// Run processes one document with a throwaway VM (used for interactive tests).
func Run(ctx context.Context, script string, env Env, doc Document) (Result, error) {
	v, err := newVM("", script)
	if err != nil {
		return Result{}, err
	}
	return v.Process(ctx, env, doc)
}

// Idle reports the number of retained VMs.
func (p *Pool) Idle() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle)
}
