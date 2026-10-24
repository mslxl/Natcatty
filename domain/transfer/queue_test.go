package transfer

import (
	"testing"
	"time"
)

func TestTaskStatusClassifiesTerminalAndActive(t *testing.T) {
	terminal := []TaskStatus{TaskCompleted, TaskFailed, TaskCancelled}
	for _, status := range terminal {
		if !status.Terminal() {
			t.Errorf("%q is not terminal", status)
		}
		if status.Active() {
			t.Errorf("%q is both terminal and active", status)
		}
	}
	for _, status := range []TaskStatus{TaskPending, TaskQueued, TaskPausing, TaskPaused,
		TaskAttention, TaskInterrupted} {
		if status.Terminal() {
			t.Errorf("%q is terminal", status)
		}
	}
	if !TaskTransferring.Active() || !TaskPausing.Active() {
		t.Error("transferring and pausing are the active statuses")
	}
	for _, status := range []TaskStatus{TaskPending, TaskQueued, TaskTransferring, TaskPausing,
		TaskPaused, TaskAttention, TaskInterrupted, TaskCompleted, TaskFailed, TaskCancelled} {
		if !status.Valid() {
			t.Errorf("%q is not valid", status)
		}
	}
	if TaskStatus("nonsense").Valid() {
		t.Error("an unknown status was accepted")
	}
}

func TestTaskControlEligibility(t *testing.T) {
	cases := []struct {
		status                                TaskStatus
		pause, resume, retry, cancel, retryOK bool
	}{
		{TaskPending, true, false, false, true, false},
		{TaskQueued, true, false, false, true, false},
		{TaskTransferring, true, false, false, true, false},
		{TaskPausing, false, false, false, true, false},
		{TaskPaused, false, true, false, true, false},
		{TaskAttention, false, false, true, true, true},
		{TaskInterrupted, false, true, true, true, true},
		{TaskCompleted, false, false, false, false, false},
		{TaskFailed, false, false, true, false, true},
		{TaskCancelled, false, false, true, false, true},
	}
	for _, test := range cases {
		if got := test.status.CanPause(); got != test.pause {
			t.Errorf("%q CanPause = %v, want %v", test.status, got, test.pause)
		}
		if got := test.status.CanResume(); got != test.resume {
			t.Errorf("%q CanResume = %v, want %v", test.status, got, test.resume)
		}
		if got := test.status.CanRetry(); got != test.retryOK {
			t.Errorf("%q CanRetry = %v, want %v", test.status, got, test.retryOK)
		}
		if got := test.status.CanCancel(); got != test.cancel {
			t.Errorf("%q CanCancel = %v, want %v", test.status, got, test.cancel)
		}
	}
}

func TestTaskPercentUsesBytesThenFilesAndClamps(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want int
	}{
		{"bytes", Task{BytesDone: 25, BytesTotal: 100}, 25},
		{"files fallback", Task{FilesDone: 3, FilesTotal: 4}, 75},
		{"unknown size", Task{}, 0},
		{"overrun clamps", Task{BytesDone: 200, BytesTotal: 100}, 100},
		{"completed", Task{Status: TaskCompleted, BytesDone: 1, BytesTotal: 100}, 100},
	}
	for _, test := range cases {
		if got := test.task.Percent(); got != test.want {
			t.Errorf("%s: Percent = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestETASecondsNeedsAStableSpeedSample(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want int
	}{
		{"needs transfer", Task{BytesDone: 1, BytesTotal: 10, Speed: 2048}, 0},
		{"no remaining", Task{Status: TaskTransferring, BytesDone: 10, BytesTotal: 10, Speed: 2048}, 0},
		{"slow sample", Task{Status: TaskTransferring, BytesDone: 1, BytesTotal: 10, Speed: 1023}, 0},
		{"remaining over speed", Task{Status: TaskTransferring, BytesDone: 1024, BytesTotal: 4096, Speed: 1024}, 3},
		{"absurd", Task{Status: TaskTransferring, BytesDone: 0, BytesTotal: 1 << 50, Speed: 1024}, 0},
	}
	for _, test := range cases {
		if got := test.task.ETASeconds(); got != test.want {
			t.Errorf("%s: ETASeconds = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestResourceKeysNameTheirLane(t *testing.T) {
	cases := []struct {
		key  ResourceKey
		want string
	}{
		{LocalResourceKey(), "local"},
		{HostResourceKey("h1"), "host:h1"},
		{SessionResourceKey("s1"), "session:s1"},
	}
	for _, test := range cases {
		if string(test.key) != test.want {
			t.Errorf("key = %q, want %q", test.key, test.want)
		}
		if !test.key.Valid() {
			t.Errorf("%q is not valid", test.key)
		}
	}
	for _, key := range []ResourceKey{"", "host:", "session:", "device:x"} {
		if key.Valid() {
			t.Errorf("%q was accepted", key)
		}
	}
}

func TestResolveQueueLimitsClampToTheirRange(t *testing.T) {
	if got := ResolveConcurrency(0); got != DefaultConcurrency {
		t.Errorf("ResolveConcurrency(0) = %d, want default", got)
	}
	if got := ResolveConcurrency(20); got != DefaultConcurrency {
		t.Errorf("ResolveConcurrency(20) = %d, want default", got)
	}
	if got := ResolveConcurrency(3); got != 3 {
		t.Errorf("ResolveConcurrency(3) = %d, want 3", got)
	}
	if got := ResolveListingWorkers(0); got != DefaultListingWorkers {
		t.Errorf("ResolveListingWorkers(0) = %d, want default", got)
	}
	if got := ResolveListingWorkers(9); got != DefaultListingWorkers {
		t.Errorf("ResolveListingWorkers(9) = %d, want default", got)
	}
	if MinConcurrency != 1 || MaxConcurrency != 16 || DefaultConcurrency != 6 {
		t.Error("the concurrency range is 1-16 with a default of 6")
	}
	if RequestSize != 32*1024 || InFlightRequestsPerFile != 64 || FastDownloadChannelsPerSession != 1 {
		t.Error("the transfer request defaults changed")
	}
}

func TestDowngradeInterruptedMarksEveryUnfinishedTask(t *testing.T) {
	now := time.Unix(5000, 0)
	tasks := []Task{
		{ID: "queued", Status: TaskQueued, Speed: 100, Epoch: 7, PauseRequested: true},
		{ID: "paused", Status: TaskPaused, ReconnectRequired: false},
		{ID: "transferring", Status: TaskTransferring, Speed: 900},
		{ID: "attention", Status: TaskAttention},
		{ID: "interrupted", Status: TaskInterrupted, ReconnectRequired: true},
		{ID: "done", Status: TaskCompleted, FinishedAt: now, BytesDone: 10, BytesTotal: 10},
		{ID: "failed", Status: TaskFailed, Error: "not-found", FinishedAt: now},
	}
	got := DowngradeInterrupted(tasks)
	for _, task := range got {
		switch task.ID {
		case "done", "failed":
			if task.Status != tasks[statusIndex(tasks, task.ID)].Status {
				t.Errorf("%s changed terminal status to %q", task.ID, task.Status)
			}
			continue
		}
		if task.Status != TaskInterrupted {
			t.Errorf("%s status = %q, want interrupted", task.ID, task.Status)
		}
		if !task.ReconnectRequired {
			t.Errorf("%s is not marked reconnect-required", task.ID)
		}
		if task.Speed != 0 {
			t.Errorf("%s kept speed %d", task.ID, task.Speed)
		}
		if task.PauseRequested || task.CancelRequested {
			t.Errorf("%s kept a control latch", task.ID)
		}
	}
	// The input rows are not mutated; a reload can be repeated.
	if tasks[0].Status != TaskQueued || !tasks[0].PauseRequested {
		t.Error("DowngradeInterrupted mutated its input")
	}
}

func statusIndex(tasks []Task, id TaskID) int {
	for index, task := range tasks {
		if task.ID == id {
			return index
		}
	}
	return -1
}

func TestPruneTasksKeepsUnfinishedAndCapsTerminalHistory(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tasks := []Task{
		{ID: "active", Status: TaskTransferring},
		{ID: "interrupted", Status: TaskInterrupted},
	}
	// 205 terminal rows: 200 recent and 5 old.
	for i := 0; i < 205; i++ {
		finished := now.Add(-time.Duration(i) * time.Minute)
		if i >= 200 {
			finished = now.Add(-40 * 24 * time.Hour)
		}
		tasks = append(tasks, Task{
			ID:         TaskID(rune('a' + i)),
			Status:     TaskCompleted,
			CreatedAt:  finished.Add(-time.Minute),
			FinishedAt: finished,
		})
	}
	got := PruneTasks(tasks, now)
	var terminal, unfinished int
	for _, task := range got {
		if task.Terminal() {
			terminal++
			if now.Sub(task.FinishedAt) > HistoryRetention {
				t.Errorf("%s survived beyond the retention window", task.ID)
			}
		} else {
			unfinished++
		}
	}
	if unfinished != 2 {
		t.Errorf("unfinished = %d, want 2", unfinished)
	}
	if terminal != HistoryLimit {
		t.Errorf("terminal = %d, want %d", terminal, HistoryLimit)
	}
	if len(got) != 0 && got[0].ID != "active" && got[0].ID != "interrupted" {
		t.Errorf("unfinished rows are not ordered first: %q", got[0].ID)
	}
}

func TestTaskNameFallsBackToTheSource(t *testing.T) {
	task := Task{Sources: []string{"/tmp/report.txt"}}
	if got := task.Name(); got != "report.txt" {
		t.Errorf("Name = %q, want report.txt", got)
	}
	task = Task{DisplayName: "folder"}
	if got := task.Name(); got != "folder" {
		t.Errorf("Name = %q, want folder", got)
	}
	if got := (Task{Sources: []string{"/a", "/b"}}).Name(); got != "" {
		t.Errorf("a multi-file Name = %q, want a UI-localized count", got)
	}
}
