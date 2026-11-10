package plugin

import "sync"

// BoundedQueue is a fixed-capacity FIFO for host request and event queues.
// Push returns a LimitError instead of growing without bound; callers decide
// whether to drop the oldest item, coalesce, or report a gap.
type BoundedQueue[T any] struct {
	mu       sync.Mutex
	items    []T
	capacity int
}

// NewBoundedQueue returns a queue with the given capacity. A non-positive
// capacity means MaxQueueDepth.
func NewBoundedQueue[T any](capacity int) *BoundedQueue[T] {
	if capacity <= 0 {
		capacity = MaxQueueDepth
	}
	return &BoundedQueue[T]{capacity: capacity}
}

// Push appends item or returns a LimitError when the queue is full.
func (q *BoundedQueue[T]) Push(item T) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.capacity {
		return &LimitError{Resource: "queue", Limit: q.capacity}
	}
	q.items = append(q.items, item)
	return nil
}

// Pop removes the oldest item. It reports false when the queue is empty.
func (q *BoundedQueue[T]) Pop() (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var zero T
	if len(q.items) == 0 {
		return zero, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}

// Len returns the number of queued items.
func (q *BoundedQueue[T]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Cap returns the queue capacity.
func (q *BoundedQueue[T]) Cap() int { return q.capacity }
