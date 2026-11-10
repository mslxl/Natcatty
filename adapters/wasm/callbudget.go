package wasm

import (
	"sync"
	"sync/atomic"
	"time"
)

// callBudget enforces the guest compute slice of one dispatch call. A service
// host import pauses the budget while its host work runs, so a user trust
// confirmation or a bounded network dial is not charged to the guest, while
// ordinary guest compute before and after the import stays bounded.
//
// The budget owns a generation-tagged timer: pausing invalidates any callback
// that has already fired but has not yet observed the pause, so a resumed
// budget never expires from a stale timer.
type callBudget struct {
	mu        sync.Mutex
	remaining time.Duration
	deadline  time.Time
	timer     *time.Timer
	gen       uint64
	paused    bool
	stopped   bool
	expired   bool
	onExpire  func()
}

// newCallBudget starts a budget of total guest compute time. onExpire runs at
// most once when the budget is exhausted.
func newCallBudget(total time.Duration, onExpire func()) *callBudget {
	budget := &callBudget{remaining: total, onExpire: onExpire}
	budget.deadline = time.Now().Add(total)
	budget.armLocked(total)
	return budget
}

func loadCallBudget(ref *atomic.Pointer[callBudget]) *callBudget {
	if ref == nil {
		return nil
	}
	return ref.Load()
}

func (b *callBudget) armLocked(after time.Duration) {
	b.gen++
	gen := b.gen
	b.timer = time.AfterFunc(after, func() { b.fire(gen) })
}

// fire expires the budget unless it was paused, stopped, or re-armed first.
func (b *callBudget) fire(gen uint64) {
	b.mu.Lock()
	if b.stopped || b.paused || b.expired || gen != b.gen {
		b.mu.Unlock()
		return
	}
	b.expired = true
	kill := b.onExpire
	b.mu.Unlock()
	if kill != nil {
		kill()
	}
}

// pause stops the compute clock while a host import runs. It expires a budget
// that was already spent rather than granting host work after the limit.
func (b *callBudget) pause() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.stopped || b.paused || b.expired {
		b.mu.Unlock()
		return
	}
	b.remaining = time.Until(b.deadline)
	b.paused = true
	// Invalidate a callback that may already be waiting for the lock.
	b.gen++
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	spent := b.remaining <= 0
	if spent {
		b.expired = true
	}
	kill := b.onExpire
	b.mu.Unlock()
	if spent && kill != nil {
		kill()
	}
}

// resume restarts the compute clock after host work returned.
func (b *callBudget) resume() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.stopped || !b.paused || b.expired {
		b.mu.Unlock()
		return
	}
	b.paused = false
	if b.remaining <= 0 {
		b.expired = true
		kill := b.onExpire
		b.mu.Unlock()
		if kill != nil {
			kill()
		}
		return
	}
	b.deadline = time.Now().Add(b.remaining)
	b.armLocked(b.remaining)
	b.mu.Unlock()
}

// stop releases the budget when the dispatch call returns. It never expires
// the budget.
func (b *callBudget) stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.stopped = true
	b.gen++
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
}
