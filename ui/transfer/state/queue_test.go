package state

import (
	"context"
	"errors"
	"testing"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// newQueuedFixture builds the same two-pane surface as newFixture, with the
// global Transfer queue behind its copy actions.
func newQueuedFixture(t *testing.T) (*fixture, *apptransfer.Queue) {
	t.Helper()
	return newQueuedFixtureWith(t)
}

// newQueuedFixtureWith is newQueuedFixture with queue options, so a test can
// force a resource lane to one worker or shorten the automatic retry delay.
func newQueuedFixtureWith(t *testing.T, options ...apptransfer.QueueOption) (*fixture, *apptransfer.Queue) {
	t.Helper()
	localDisk := newFakeDisk()
	localDisk.put("/alpha.txt", "alpha", 0o644)
	localDisk.put("/beta.bin", "beta", 0o644)
	remoteDisk := newFakeDisk()

	localOp := &opener{disk: localDisk, root: "/", backing: "local"}
	remoteOp := &opener{disk: remoteDisk, root: "/", remote: true, backing: "remote:h1"}
	service := apptransfer.NewService(localOp, remoteOp,
		apptransfer.WithHosts(fixedHosts{{ID: "h1", Label: "Test", Hostname: "test.example", Username: "me", Port: 22}}),
	)
	queue := apptransfer.NewQueue(service, options...)
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	state := NewState(service, WithQueue(queue))
	t.Cleanup(func() {
		state.Shutdown()
		queue.Shutdown()
	})
	state.EnsureStarted()
	f := &fixture{state: state, local: localDisk, remote: remoteDisk, localOp: localOp, remoteOp: remoteOp}
	f.waitLoaded(t, Left)
	return f, queue
}

func TestQueueBackedTransferCompletesAndAppearsInTheCenter(t *testing.T) {
	f, _ := newQueuedFixture(t)
	f.openRemote(t, Right)
	selectOne(t, f.state, Left, "alpha.txt")

	f.state.Transfer(Left)
	settle(t, f.state, "the queued transfer to finish", func() bool {
		tasks := f.state.QueueTasks()
		return len(tasks) == 1 && tasks[0].Status == domainsftp.TaskCompleted
	})
	if !f.remote.has("/alpha.txt") {
		t.Fatal("the queued upload did not reach the remote pane")
	}
	snapshot := f.state.Snapshot()
	if snapshot == nil || snapshot.Running || snapshot.Err != nil {
		t.Fatalf("operation bar = %+v, want a completed success", snapshot)
	}
	if snapshot.BytesDone == 0 || snapshot.BytesTotal == 0 {
		t.Errorf("operation bar lost byte progress: %+v", snapshot)
	}
	if f.state.TransferStats().PeakActive != 1 {
		t.Errorf("stats = %+v", f.state.TransferStats())
	}
}

func TestQueueBackedTransferOpensAndClosesTheCenter(t *testing.T) {
	f, _ := newQueuedFixture(t)
	if f.state.CenterOpen() {
		t.Fatal("the center is open before it was requested")
	}
	f.state.ToggleCenter()
	if !f.state.CenterOpen() {
		t.Fatal("ToggleCenter did not open the center")
	}
	f.state.ToggleCenter()
	if f.state.CenterOpen() {
		t.Fatal("ToggleCenter did not close the center")
	}
	f.state.OpenCenter()
	if !f.state.CenterOpen() || f.state.CenterError() != "" {
		t.Fatalf("OpenCenter = open=%v error=%q", f.state.CenterOpen(), f.state.CenterError())
	}
	f.state.CloseCenter()
}

func TestCenterNeedsAQueue(t *testing.T) {
	f := newFixture(t)
	f.state.OpenCenter()
	if f.state.CenterOpen() {
		t.Fatal("a surface without a queue opened the center")
	}
	if notice := f.state.Notice(); notice != "transfer-unavailable" {
		t.Errorf("notice = %q, want transfer-unavailable", notice)
	}
}

// TestTransferWhileBusyEnqueuesASecondTask proves a second Transfer is not
// dropped while one is running: the queue grows, the row reaches the center,
// and both copies finish once a resource slot frees.
func TestTransferWhileBusyEnqueuesASecondTask(t *testing.T) {
	f, queue := newQueuedFixtureWith(t,
		apptransfer.WithQueueConcurrency(1),
		apptransfer.WithQueueRetryDelay(0),
	)
	f.openRemote(t, Right)
	local, ok := f.state.Tab(Left).FS.(*fakeFS)
	if !ok {
		t.Fatalf("local tab filesystem = %T, want *fakeFS", f.state.Tab(Left).FS)
	}
	release := make(chan struct{})
	gate := make(chan struct{}, 4)
	local.block = release
	local.openGate = gate

	selectOne(t, f.state, Left, "alpha.txt")
	f.state.Transfer(Left)
	waitFor(t, "the first transfer to reach its file", func() bool {
		select {
		case <-gate:
			return true
		default:
			return false
		}
	})
	if !f.state.Busy() {
		t.Fatal("the first transfer is not running")
	}
	if tasks := queue.Tasks(); len(tasks) != 1 || tasks[0].Status != domainsftp.TaskTransferring {
		t.Fatalf("after the first Transfer tasks = %+v, want one transferring", tasks)
	}

	// The Upload button stays enabled while busy. A click must enqueue, not
	// return early with a "busy" notice.
	selectOne(t, f.state, Left, "beta.bin")
	f.state.Transfer(Left)
	tasks := queue.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("Transfer while busy left %d queue rows, want 2: %+v", len(tasks), tasks)
	}
	if stats := queue.Stats(); stats.Queued != 1 || stats.Active != 1 {
		t.Fatalf("stats while busy = %+v, want one active and one queued", stats)
	}
	// The queued row is visible in the center.
	f.state.OpenCenter()
	if !f.state.CenterOpen() {
		t.Fatal("the Transfer center did not open")
	}
	found := false
	for _, task := range f.state.QueueTasks() {
		if task.Name() == "beta.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the queued second Transfer is not in the center: %+v", f.state.QueueTasks())
	}

	close(release)
	settle(t, f.state, "both queued transfers to finish", func() bool {
		tasks := queue.Tasks()
		if len(tasks) != 2 {
			return false
		}
		for _, task := range tasks {
			if task.Status != domainsftp.TaskCompleted {
				return false
			}
		}
		return true
	})
	if !f.remote.has("/alpha.txt") || !f.remote.has("/beta.bin") {
		t.Fatalf("the queued transfers did not both reach the remote pane: %+v", f.remote)
	}
}

// TestTransportFailureRetryRequiresReconnect proves a lost transport settles
// as connection and reconnect-required, and that Retry no longer replays the
// dead request silently.
func TestTransportFailureRetryRequiresReconnect(t *testing.T) {
	f, queue := newQueuedFixtureWith(t, apptransfer.WithQueueRetryDelay(0))
	f.openRemote(t, Right)
	local, ok := f.state.Tab(Left).FS.(*fakeFS)
	if !ok {
		t.Fatalf("local tab filesystem = %T, want *fakeFS", f.state.Tab(Left).FS)
	}
	local.transportErr = errors.New("connection reset by peer")

	selectOne(t, f.state, Left, "alpha.txt")
	f.state.Transfer(Left)
	settle(t, f.state, "the transport failure to settle", func() bool {
		tasks := queue.Tasks()
		return len(tasks) == 1 && tasks[0].Status == domainsftp.TaskFailed
	})
	task := queue.Tasks()[0]
	if !task.ReconnectRequired {
		t.Errorf("the failed row is not reconnect-required: %+v", task)
	}
	if task.Error != string(apptransfer.KindConnection) {
		t.Errorf("failure code = %q, want %q", task.Error, apptransfer.KindConnection)
	}

	f.state.RetryTransfer(task.ID)
	if got := f.state.CenterError(); got != "transfer-reconnect-required" {
		t.Errorf("retry without a connection reports %q, want transfer-reconnect-required", got)
	}
	again, ok := queue.Task(task.ID)
	if !ok {
		t.Fatal("the failed row disappeared")
	}
	if again.Status != domainsftp.TaskFailed || again.Epoch != task.Epoch {
		t.Errorf("Retry replayed a dead transport: %+v (was epoch %d)", again, task.Epoch)
	}
}

func TestQueueTaskControlsReportUnavailableAfterRestart(t *testing.T) {
	f, queue := newQueuedFixture(t)
	f.openRemote(t, Right)
	selectOne(t, f.state, Left, "alpha.txt")
	f.state.Transfer(Left)
	settle(t, f.state, "the queued transfer to finish", func() bool {
		tasks := queue.Tasks()
		return len(tasks) == 1 && tasks[0].Status == domainsftp.TaskCompleted
	})
	id := queue.Tasks()[0].ID
	f.state.RetryTransfer(id) // completed rows are not retryable
	if got := f.state.CenterError(); got != "transfer-not-retryable" {
		t.Errorf("center error = %q, want transfer-not-retryable", got)
	}
	f.state.CancelTransfer("missing")
	if got := f.state.CenterError(); got != "transfer-control-failed" {
		t.Errorf("center error = %q, want transfer-control-failed", got)
	}
}
