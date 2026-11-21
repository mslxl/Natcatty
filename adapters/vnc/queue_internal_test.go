package vnc

import (
	"errors"
	"testing"
	"time"
)

// TestWriteQueueFailsClosedAtCapacity proves the bounded writer refuses a task
// beyond its capacity, reports ErrWriteQueueFull once, and lets its goroutine
// exit once the blocked send is released.
func TestWriteQueueFailsClosedAtCapacity(t *testing.T) {
	const capacity = 4
	overflow := make(chan error, 1)
	queue := newWriteQueue(capacity, func(err error) { overflow <- err })
	block := make(chan struct{})
	if !queue.submitAsync(func() error {
		<-block
		return nil
	}) {
		t.Fatal("the first task was rejected")
	}

	rejected := false
	for i := 0; i < capacity+2; i++ {
		if !queue.submitAsync(func() error { return nil }) {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatal("the queue accepted more tasks than its capacity")
	}
	select {
	case err := <-overflow:
		if !errors.Is(err, ErrWriteQueueFull) {
			t.Fatalf("overflow error = %v, want ErrWriteQueueFull", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no overflow was reported")
	}

	close(block)
	queue.close()
	select {
	case <-queue.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer goroutine did not exit")
	}
}

// TestWriteQueueOverflowCallbackCanCloseTheQueue proves onOverflow runs without
// the queue lock held: a callback that re-enters close() (as the transport's
// overflow path does) must not deadlock, and the writer must still exit.
func TestWriteQueueOverflowCallbackCanCloseTheQueue(t *testing.T) {
	const capacity = 2
	var queue *writeQueue
	fired := make(chan error, 1)
	queue = newWriteQueue(capacity, func(err error) {
		fired <- err
		queue.close()
	})
	block := make(chan struct{})
	if !queue.submitAsync(func() error {
		<-block
		return nil
	}) {
		t.Fatal("the writer rejected its first task")
	}
	rejected := false
	for i := 0; i < capacity+2; i++ {
		if !queue.submitAsync(func() error { return nil }) {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatal("the queue accepted more tasks than its capacity")
	}
	select {
	case err := <-fired:
		if !errors.Is(err, ErrWriteQueueFull) {
			t.Fatalf("overflow error = %v, want ErrWriteQueueFull", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the overflow callback did not run")
	}
	close(block)
	select {
	case <-queue.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer goroutine deadlocked after the callback closed the queue")
	}
}
