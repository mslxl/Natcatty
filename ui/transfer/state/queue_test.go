package state

import (
	"context"
	"testing"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// newQueuedFixture builds the same two-pane surface as newFixture, with the
// global Transfer queue behind its copy actions.
func newQueuedFixture(t *testing.T) (*fixture, *apptransfer.Queue) {
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
	queue := apptransfer.NewQueue(service)
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
