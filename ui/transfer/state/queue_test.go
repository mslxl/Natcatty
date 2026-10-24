package state

import (
	"context"
	"errors"
	"testing"
	"time"

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

// queueSeedRepo restores a fixed history, as the app does after a restart.
type queueSeedRepo struct {
	tasks []domainsftp.Task
}

func (r *queueSeedRepo) LoadTasks(context.Context) ([]domainsftp.Task, error) {
	out := make([]domainsftp.Task, len(r.tasks))
	copy(out, r.tasks)
	return out, nil
}

func (r *queueSeedRepo) SaveTasks(_ context.Context, tasks []domainsftp.Task) error {
	r.tasks = append([]domainsftp.Task(nil), tasks...)
	return nil
}

// TestHostReconnectReattachesAnInterruptedTransfer proves the product reaches
// Queue.ReattachHost when the user reconnects a Host in the SFTP pane: the
// restored row's reconnect prompt is truthful before the connection, the
// reconnected Host binds live filesystems, and Resume then finishes the
// Transfer without touching the completed prefix.
func TestHostReconnectReattachesAnInterruptedTransfer(t *testing.T) {
	now := time.Now()
	seed := []domainsftp.Task{{
		ID: "host-restart", Kind: domainsftp.OpUpload, Status: domainsftp.TaskInterrupted,
		DisplayName: "alpha.txt", SourceHost: "h1", SourceLabel: "Test", DestLabel: "local",
		Sources: []string{"/alpha.txt"}, DestDir: "/dest",
		BytesTotal: 5, FilesTotal: 1, ReconnectRequired: true,
		CreatedAt: now, UpdatedAt: now,
	}}

	localDisk := newFakeDisk()
	localDisk.mkdirs("/dest")
	remoteDisk := newFakeDisk()
	remoteDisk.put("/alpha.txt", "alpha", 0o644)

	localOp := &opener{disk: localDisk, root: "/", backing: "local"}
	remoteOp := &opener{disk: remoteDisk, root: "/", remote: true, backing: "remote:h1"}
	service := apptransfer.NewService(localOp, remoteOp)
	queue := apptransfer.NewQueue(service, apptransfer.WithQueueRepository(&queueSeedRepo{tasks: seed}))
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

	restored, _ := queue.Task("host-restart")
	if restored.Status != domainsftp.TaskInterrupted || !restored.ReconnectRequired {
		t.Fatalf("the restored row = %+v", restored)
	}

	// Before the Host is connected the prompt must stay truthful.
	state.ResumeTransfer("host-restart")
	if got := state.CenterError(); got != "transfer-reconnect-required" {
		t.Fatalf("Resume before the Host reconnect reports %q, want transfer-reconnect-required", got)
	}
	if task, _ := queue.Task("host-restart"); task.Status != domainsftp.TaskInterrupted {
		t.Fatalf("Resume before the Host reconnect moved the row to %q", task.Status)
	}

	// Reopening the centre must not show a refusal that is still on screen
	// from an earlier click; the row itself is the durable status.
	state.OpenCenter()
	if got := state.CenterError(); got != "" {
		t.Fatalf("reopening the centre kept the stale refusal notice %q", got)
	}
	state.CloseCenter()
	// Arm the notice again so the reconnect check below proves the reattach
	// clears it rather than the reopen.
	state.ResumeTransfer("host-restart")
	if got := state.CenterError(); got != "transfer-reconnect-required" {
		t.Fatalf("Resume before the Host reconnect reports %q, want transfer-reconnect-required", got)
	}

	// Reconnecting the Host in the SFTP pane is the production path that
	// binds the fresh filesystems.
	f.openRemote(t, Right)
	// The reattach clears ReconnectRequired on the queue goroutine and then
	// posts the notice update to the UI thread, so the notice is part of the
	// condition to await rather than something read after a partial settle.
	settle(t, state, "the reconnected Host to reattach the row and clear the notice", func() bool {
		task, ok := queue.Task("host-restart")
		return ok && !task.ReconnectRequired && state.CenterError() == ""
	})
	rebound, _ := queue.Task("host-restart")
	if rebound.Status != domainsftp.TaskInterrupted {
		t.Fatalf("the reconnect auto-started the row: %+v", rebound)
	}

	state.ResumeTransfer("host-restart")
	if got := state.CenterError(); got != "" {
		t.Fatalf("Resume after the Host reconnect reports %q", got)
	}
	settle(t, state, "the reconnected transfer to finish", func() bool {
		task, ok := queue.Task("host-restart")
		return ok && task.Status == domainsftp.TaskCompleted
	})
	if got, ok := localDisk.read("/dest/alpha.txt"); !ok || got != "alpha" {
		t.Fatalf("the resumed upload left /dest/alpha.txt = %q, exists=%v", got, ok)
	}
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
