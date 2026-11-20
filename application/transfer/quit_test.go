package transfer

// Ticket 57: the quit guard's counting rule. Every non-terminal row is
// unfinished; a status the queue does not know is an error so the guard asks
// rather than quitting silently.

import (
	"testing"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

func TestCountUnfinishedCountsEveryNonTerminalRow(t *testing.T) {
	tasks := []domainsftp.Task{
		{ID: "pending", Status: domainsftp.TaskPending},
		{ID: "queued", Status: domainsftp.TaskQueued},
		{ID: "transferring", Status: domainsftp.TaskTransferring},
		{ID: "pausing", Status: domainsftp.TaskPausing},
		{ID: "paused", Status: domainsftp.TaskPaused},
		{ID: "attention", Status: domainsftp.TaskAttention},
		{ID: "interrupted", Status: domainsftp.TaskInterrupted},
		{ID: "completed", Status: domainsftp.TaskCompleted},
		{ID: "failed", Status: domainsftp.TaskFailed},
		{ID: "cancelled", Status: domainsftp.TaskCancelled},
	}
	count, err := CountUnfinished(tasks)
	if err != nil {
		t.Fatalf("CountUnfinished: %v", err)
	}
	if count != 7 {
		t.Fatalf("unfinished = %d, want 7 (every status that is not terminal)", count)
	}
}

func TestCountUnfinishedFailsClosedOnAnUnknownStatus(t *testing.T) {
	tasks := []domainsftp.Task{
		{ID: "queued", Status: domainsftp.TaskQueued},
		{ID: "corrupt", Status: domainsftp.TaskStatus("mystery")},
	}
	if count, err := CountUnfinished(tasks); err == nil {
		t.Fatalf("an unknown status counted %d with no error; the quit guard would quit silently", count)
	}
}
