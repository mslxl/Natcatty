package plugin

import (
	"errors"
	"testing"
)

func TestBoundedQueueIsFIFOAndEnforcesCapacity(t *testing.T) {
	queue := NewBoundedQueue[int](2)
	if err := queue.Push(1); err != nil {
		t.Fatal(err)
	}
	if err := queue.Push(2); err != nil {
		t.Fatal(err)
	}
	var limit *LimitError
	if err := queue.Push(3); !errors.As(err, &limit) {
		t.Fatalf("Push over capacity = %v, want LimitError", err)
	}
	if limit.Resource != "queue" || limit.Limit != 2 {
		t.Fatalf("limit = %+v, want the queue capacity", limit)
	}
	if queue.Len() != 2 || queue.Cap() != 2 {
		t.Fatalf("len/cap = %d/%d, want 2/2", queue.Len(), queue.Cap())
	}
	for want := 1; want <= 2; want++ {
		got, ok := queue.Pop()
		if !ok || got != want {
			t.Fatalf("Pop = %v, %v, want %d, true", got, ok, want)
		}
	}
	if _, ok := queue.Pop(); ok {
		t.Fatal("Pop on an empty queue reported an item")
	}
}

func TestBoundedQueueDefaultsToTheContractDepth(t *testing.T) {
	queue := NewBoundedQueue[string](0)
	if queue.Cap() != MaxQueueDepth {
		t.Fatalf("capacity = %d, want %d", queue.Cap(), MaxQueueDepth)
	}
	for i := 0; i < MaxQueueDepth; i++ {
		if err := queue.Push("item"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	if err := queue.Push("overflow"); err == nil {
		t.Fatal("Push past the default depth succeeded")
	}
}
