package plugin

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEffectRunsExactlyOnceUnderConcurrentDisposal(t *testing.T) {
	var calls atomic.Int32
	item := &effect{dispose: func(context.Context) error {
		calls.Add(1)
		return nil
	}}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_ = item.run(context.Background())
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("disposer ran %d times, want exactly once", calls.Load())
	}
}

func TestDisposeEffectsRunsInReverseRegistrationOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	fiber := &Fiber{}
	for _, name := range []string{"first", "second", "third"} {
		name := name
		if err := fiber.addEffect(func(context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("addEffect(%s): %v", name, err)
		}
	}
	container := &Container{}
	if err := container.disposeEffects(context.Background(), fiber); err != nil {
		t.Fatalf("disposeEffects: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "third" || order[1] != "second" || order[2] != "first" {
		t.Fatalf("disposal order = %v, want reverse registration order", order)
	}
	if fiber.EffectCount() != 0 {
		t.Fatalf("effects left = %d", fiber.EffectCount())
	}
	// A second disposal finds an empty stack and does nothing.
	if err := container.disposeEffects(context.Background(), fiber); err != nil {
		t.Fatalf("second disposeEffects: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("second disposal reran disposers: %v", order)
	}
}
