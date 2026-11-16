package state

// Ticket 57: the quit-check state. The surface reports how many Transfers a
// quit would abandon, both for the global queue and for a direct operation
// without one.

import (
	"context"
	"testing"
	"time"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// TestUnfinishedTransfersCountsQueueRows restores a mixed queue history and
// requires the unfinished count to ignore every terminal row. Start downgrades
// queued work to interrupted, which stays unfinished, exactly as it does after
// a real restart.
func TestUnfinishedTransfersCountsQueueRows(t *testing.T) {
	now := time.Now()
	seed := []domainsftp.Task{
		{ID: "transferring", Status: domainsftp.TaskTransferring, CreatedAt: now},
		{ID: "queued", Status: domainsftp.TaskQueued, CreatedAt: now},
		{ID: "attention", Status: domainsftp.TaskAttention, CreatedAt: now},
		{ID: "completed", Status: domainsftp.TaskCompleted, CreatedAt: now},
		{ID: "failed", Status: domainsftp.TaskFailed, CreatedAt: now},
		{ID: "cancelled", Status: domainsftp.TaskCancelled, CreatedAt: now},
	}
	queue := apptransfer.NewQueue(nil, apptransfer.WithQueueRepository(&queueSeedRepo{tasks: seed}))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	state := NewState(nil, WithQueue(queue))
	t.Cleanup(state.Shutdown)

	count, err := state.UnfinishedTransfers()
	if err != nil {
		t.Fatalf("UnfinishedTransfers: %v", err)
	}
	if count != 3 {
		t.Fatalf("unfinished = %d, want the three non-terminal rows", count)
	}
}

// TestUnfinishedTransfersIgnoresTerminalHistory pins the no-Transfers case: a
// queue holding only finished rows reports nothing, so quitting stays
// unchanged.
func TestUnfinishedTransfersIgnoresTerminalHistory(t *testing.T) {
	seed := []domainsftp.Task{{
		ID: "done", Status: domainsftp.TaskCompleted, CreatedAt: time.Now(),
	}}
	queue := apptransfer.NewQueue(nil, apptransfer.WithQueueRepository(&queueSeedRepo{tasks: seed}))
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	state := NewState(nil, WithQueue(queue))
	t.Cleanup(state.Shutdown)

	if count, err := state.UnfinishedTransfers(); err != nil || count != 0 {
		t.Fatalf("unfinished = %d, err = %v; want no unfinished Transfer", count, err)
	}
}

// TestUnfinishedTransfersCountsADirectOperation covers a surface without the
// global queue: a running direct copy is one unfinished Transfer, and a
// finished one is none.
func TestUnfinishedTransfersCountsADirectOperation(t *testing.T) {
	state := NewState(nil)
	t.Cleanup(state.Shutdown)
	if count, err := state.UnfinishedTransfers(); err != nil || count != 0 {
		t.Fatalf("an idle surface reports %d, err = %v", count, err)
	}

	state.operation = &operationState{state: Operation{Running: true}}
	if count, err := state.UnfinishedTransfers(); err != nil || count != 1 {
		t.Fatalf("a running direct operation reports %d, err = %v; want 1", count, err)
	}

	state.operation.state.Running = false
	if count, err := state.UnfinishedTransfers(); err != nil || count != 0 {
		t.Fatalf("a finished direct operation reports %d, err = %v; want 0", count, err)
	}
}
