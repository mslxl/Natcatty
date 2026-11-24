package phoneshell

// The phone-side view and actions of the shared global Transfer queue. Files
// copies and device uploads are enqueued into the same application queue the
// desktop uses, so a row survives leaving the page, backgrounding the app, and
// a process restart. Both the Files page and the Sessions home draw this one
// panel; neither owns the work.
//
// The queue, its directory checkpoint, its per-source conflict decisions, and
// its retry semantics come from application/transfer. This file only turns
// queued rows into phone rows and supplies the phone's re-selection path for a
// system-provided source copy that disappeared.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// TransferQueueProvider lets the Session opener hand the Shell the global
// queue the app root built, so the phone root passes one option instead of
// duplicating the queue's construction.
type TransferQueueProvider interface {
	TransferQueue() *apptransfer.Queue
}

// WithTransferQueue gives the Files page and the Sessions home the shared
// global queue. Without it a Files copy keeps the page's direct-copy behavior
// and no queue panel is drawn; no other page is affected.
func WithTransferQueue(queue *apptransfer.Queue) Option {
	return func(s *Shell) { s.queue = queue }
}

// WithTransferSourcePicker supplies the system document picker used to upload
// files from the device and to re-select a source copy that vanished. The
// composition root wires MyGo's Dialog.Open; a test supplies a fake.
func WithTransferSourcePicker(pick func() ([]string, error)) Option {
	return func(s *Shell) { s.transferPicker = pick }
}

// transferQueuePages is the phone's queue surface. It is shared by every page
// that shows the queue, so the Files page and the Sessions home read one
// state and issue one set of controls.
type transferQueuePages struct {
	queue  *apptransfer.Queue
	files  FilesService
	pick   func() ([]string, error)
	notify func()

	mu sync.Mutex
	// handles are the transports a phone-enqueued row owns. They are closed
	// when the row reaches a terminal state, so a completed Transfer does not
	// pin an SFTP lease.
	handles map[domainsftp.TaskID][]apptransfer.FileSystem
	// notice is the last action's failure, shown above the rows.
	notice string
	// noticeProblem marks notice as an error rather than information.
	noticeProblem bool
}

func newTransferQueuePages(queue *apptransfer.Queue, files FilesService,
	pick func() ([]string, error), notify func()) *transferQueuePages {
	pages := &transferQueuePages{
		queue:   queue,
		files:   files,
		pick:    pick,
		notify:  notify,
		handles: map[domainsftp.TaskID][]apptransfer.FileSystem{},
	}
	if queue != nil {
		queue.Observe(func(task domainsftp.Task) {
			if task.Terminal() {
				pages.closeHandles(task.ID)
			}
			pages.invalidate()
		})
	}
	return pages
}

func (p *transferQueuePages) available() bool { return p != nil && p.queue != nil }

// canUpload reports whether this build can pick device files and open the
// phone's own filesystem. The Files page hides the action otherwise.
func (p *transferQueuePages) canUpload() bool {
	return p.available() && p.files != nil && p.pick != nil
}

func (p *transferQueuePages) invalidate() {
	if p == nil || p.notify == nil {
		return
	}
	p.notify()
}

func (p *transferQueuePages) setNotice(text string, problem bool) {
	p.mu.Lock()
	p.notice = text
	p.noticeProblem = problem
	p.mu.Unlock()
	p.invalidate()
}

func (p *transferQueuePages) clearNotice() {
	p.mu.Lock()
	p.notice = ""
	p.noticeProblem = false
	p.mu.Unlock()
}

func (p *transferQueuePages) noticeText() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.notice, p.noticeProblem
}

// own transfers handle ownership to the page after a queue call. Enqueue,
// Reattach, and Resume may run the worker before returning, so the row can
// already be terminal when the handles are recorded; the observer would then
// have fired before they existed. Recording first and closing a settled row
// afterwards leaves no ordering in which a handle is stranded. The observer
// closes handles recorded before it fires; this closes the reverse order.
func (p *transferQueuePages) own(id domainsftp.TaskID, files ...apptransfer.FileSystem) {
	p.recordHandles(id, files...)
	if id == "" || p.queue == nil {
		return
	}
	if task, ok := p.queue.Task(id); ok && task.Terminal() {
		p.closeHandles(id)
	}
}

func (p *transferQueuePages) recordHandles(id domainsftp.TaskID, files ...apptransfer.FileSystem) {
	if id == "" {
		return
	}
	live := make([]apptransfer.FileSystem, 0, len(files))
	for _, file := range files {
		if file != nil {
			live = append(live, file)
		}
	}
	p.mu.Lock()
	p.handles[id] = append(p.handles[id], live...)
	p.mu.Unlock()
}

func (p *transferQueuePages) closeHandles(id domainsftp.TaskID) {
	p.mu.Lock()
	files := p.handles[id]
	delete(p.handles, id)
	p.mu.Unlock()
	for _, file := range files {
		_ = file.Close()
	}
}

// enqueueCopy opens dedicated transports for one cross-Host copy and hands
// the request to the shared queue. The page keeps its own browsing transport;
// the queue owns these handles until the row settles.
func (p *transferQueuePages) enqueueCopy(ctx context.Context, sourceHost model.HostID,
	sourceLabel string, destHost model.HostID, destLabel string, sources []string,
	destDir string, answers map[string]apptransfer.ConflictPolicy) (domainsftp.TaskID, error) {
	if !p.available() || p.files == nil {
		return "", errors.New("transfer queue is not available")
	}
	if sourceHost == "" || destHost == "" || len(sources) == 0 || destDir == "" {
		return "", errors.New("incomplete copy request")
	}
	source, err := p.files.OpenRemote(ctx, sourceHost, sourceLabel)
	if err != nil {
		return "", err
	}
	dest, err := p.files.OpenRemote(ctx, destHost, destLabel)
	if err != nil {
		_ = source.Close()
		return "", err
	}
	id, err := p.queue.Enqueue(apptransfer.TaskSpec{
		Kind:        domainsftp.OpCopy,
		Source:      source,
		Dest:        dest,
		Sources:     append([]string(nil), sources...),
		DestDir:     destDir,
		Conflict:    apptransfer.ConflictAsk,
		Answers:     answers,
		SourceLabel: sourceLabel,
		DestLabel:   destLabel,
		SourceHost:  string(sourceHost),
		DestHost:    string(destHost),
	})
	if err != nil {
		_ = source.Close()
		_ = dest.Close()
		return "", err
	}
	p.own(id, source, dest)
	return id, nil
}

// enqueueUpload opens the phone's local filesystem and the destination Host
// and queues an upload of the picked paths. The picked paths are app-readable
// copies the system picker made; if Android later evicts one, the row's source
// can be replaced without losing its checkpoint or target.
func (p *transferQueuePages) enqueueUpload(ctx context.Context, hostID model.HostID,
	hostLabel, destDir string, paths []string) (domainsftp.TaskID, error) {
	if !p.available() || p.files == nil {
		return "", errors.New("transfer queue is not available")
	}
	if hostID == "" || len(paths) == 0 || destDir == "" {
		return "", errors.New("incomplete upload request")
	}
	local, err := p.files.OpenLocal(ctx)
	if err != nil {
		return "", err
	}
	dest, err := p.files.OpenRemote(ctx, hostID, hostLabel)
	if err != nil {
		_ = local.Close()
		return "", err
	}
	id, err := p.queue.Enqueue(apptransfer.TaskSpec{
		Kind:        domainsftp.OpUpload,
		Source:      local,
		Dest:        dest,
		Sources:     append([]string(nil), paths...),
		DestDir:     destDir,
		Conflict:    apptransfer.ConflictAsk,
		SourceLabel: "This device",
		DestLabel:   hostLabel,
		DestHost:    string(hostID),
	})
	if err != nil {
		_ = local.Close()
		_ = dest.Close()
		return "", err
	}
	p.own(id, local, dest)
	return id, nil
}

// specFor rebuilds the live request of a persisted row from its durable Host
// identities. It is the phone's reconnect path: the queue keeps the task ID,
// checkpoint, targets, and answers; only the transports are new.
func (p *transferQueuePages) specFor(ctx context.Context, task domainsftp.Task) (apptransfer.TaskSpec, []apptransfer.FileSystem, error) {
	if p.files == nil {
		return apptransfer.TaskSpec{}, nil, errors.New("Files is not available")
	}
	var opened []apptransfer.FileSystem
	open := func(host string, label string) (apptransfer.FileSystem, error) {
		if host == "" {
			return p.files.OpenLocal(ctx)
		}
		return p.files.OpenRemote(ctx, model.HostID(host), label)
	}
	source, err := open(task.SourceHost, task.SourceLabel)
	if err != nil {
		return apptransfer.TaskSpec{}, nil, err
	}
	opened = append(opened, source)
	dest, err := open(task.DestHost, task.DestLabel)
	if err != nil {
		for _, file := range opened {
			_ = file.Close()
		}
		return apptransfer.TaskSpec{}, nil, err
	}
	opened = append(opened, dest)
	skipUnchanged := task.SkipUnchanged
	return apptransfer.TaskSpec{
		Kind:          task.Kind,
		Source:        source,
		Dest:          dest,
		Sources:       append([]string(nil), task.Sources...),
		DestDir:       task.DestDir,
		Move:          task.Move,
		Conflict:      apptransfer.ConflictPolicy(task.Conflict),
		BatchID:       task.BatchID,
		SkipUnchanged: &skipUnchanged,
		SourceLabel:   task.SourceLabel,
		DestLabel:     task.DestLabel,
		SourceHost:    task.SourceHost,
		DestHost:      task.DestHost,
	}, opened, nil
}

// resumeTask continues a paused or interrupted row. A row restored by a
// previous process has no live request, so its endpoints are rebuilt and
// reattached first; the checkpoint makes Resume continue instead of replay.
func (p *transferQueuePages) resumeTask(id domainsftp.TaskID) {
	go func() {
		task, ok := p.queue.Task(id)
		if !ok {
			p.setNotice("This transfer is no longer in the queue.", true)
			return
		}
		if task.ReconnectRequired {
			spec, opened, err := p.specFor(context.Background(), task)
			if err != nil {
				p.setNotice("Could not reconnect the transfer: "+err.Error(), true)
				return
			}
			if err := p.queue.Reattach(id, spec); err != nil {
				for _, file := range opened {
					_ = file.Close()
				}
				p.setNotice("Could not reconnect the transfer: "+err.Error(), true)
				return
			}
			p.own(id, opened...)
		}
		err := p.queue.Resume(id)
		if errors.Is(err, domainsftp.ErrTaskUnavailable) {
			// The row lost its live request without the durable reconnect
			// flag (for example after a panic in a previous worker). Rebuild
			// and try once more, so Continue is never a dead button.
			spec, opened, openErr := p.specFor(context.Background(), task)
			if openErr != nil {
				p.setNotice("Could not reconnect the transfer: "+openErr.Error(), true)
				return
			}
			if reattachErr := p.queue.Reattach(id, spec); reattachErr != nil {
				for _, file := range opened {
					_ = file.Close()
				}
				p.setNotice("Could not reconnect the transfer: "+reattachErr.Error(), true)
				return
			}
			p.own(id, opened...)
			err = p.queue.Resume(id)
		}
		if err != nil {
			p.setNotice("Could not resume the transfer: "+err.Error(), true)
			return
		}
		p.clearNotice()
		p.invalidate()
	}()
}

// restartTask replays a row from the beginning. Unlike Resume it does not keep
// the checkpoint or the pinned targets, matching the shared queue's Retry.
func (p *transferQueuePages) restartTask(id domainsftp.TaskID) {
	go func() {
		task, ok := p.queue.Task(id)
		if !ok {
			p.setNotice("This transfer is no longer in the queue.", true)
			return
		}
		if task.ReconnectRequired {
			spec, opened, err := p.specFor(context.Background(), task)
			if err != nil {
				p.setNotice("Could not reconnect the transfer: "+err.Error(), true)
				return
			}
			if err := p.queue.Reattach(id, spec); err != nil {
				for _, file := range opened {
					_ = file.Close()
				}
				p.setNotice("Could not reconnect the transfer: "+err.Error(), true)
				return
			}
			p.own(id, opened...)
		}
		if err := p.queue.Retry(id); err != nil {
			p.setNotice("Could not restart the transfer: "+err.Error(), true)
			return
		}
		p.clearNotice()
		p.invalidate()
	}()
}

// reselectTask replaces the source paths of an existing row after the
// system-provided copies disappeared. It keeps the task ID, destination Host,
// destination directory, checkpoint, pinned targets, and conflict answers.
func (p *transferQueuePages) reselectTask(id domainsftp.TaskID) {
	if p.pick == nil || p.files == nil {
		return
	}
	paths, err := p.pick()
	if err != nil {
		p.setNotice("Could not choose a source: "+err.Error(), true)
		return
	}
	if len(paths) == 0 {
		return
	}
	go func() {
		task, ok := p.queue.Task(id)
		if !ok {
			p.setNotice("This transfer is no longer in the queue.", true)
			return
		}
		local, err := p.files.OpenLocal(context.Background())
		if err != nil {
			p.setNotice("Could not open the chosen source: "+err.Error(), true)
			return
		}
		rebind := task.ReconnectRequired
		if !rebind {
			err := p.queue.ReplaceSource(id, local, paths)
			if err == nil {
				p.own(id, local)
			} else if errors.Is(err, domainsftp.ErrTaskUnavailable) {
				// A row restored by a previous process has no live request to
				// replace (this includes a terminal not-found upload), so the
				// whole request is rebuilt around the chosen source.
				rebind = true
			} else {
				_ = local.Close()
				p.setNotice("Could not re-select the source: "+err.Error(), true)
				return
			}
		}
		if rebind {
			// A persisted row binds the whole request first, with the newly
			// chosen source in place of the evicted one. Reattach keeps the
			// checkpoint and conflict decisions.
			spec, opened, err := p.specWithSource(context.Background(), task, local, paths)
			if err != nil {
				_ = local.Close()
				p.setNotice("Could not reconnect the transfer: "+err.Error(), true)
				return
			}
			if err := p.queue.Reattach(id, spec); err != nil {
				for _, file := range opened {
					_ = file.Close()
				}
				p.setNotice("Could not reconnect the transfer: "+err.Error(), true)
				return
			}
			p.own(id, opened...)
		}
		if err := p.queue.Resume(id); err != nil {
			p.setNotice("Could not resume the transfer: "+err.Error(), true)
			return
		}
		p.clearNotice()
		p.invalidate()
	}()
}

// specWithSource is specFor with the phone-local source replaced by a fresh
// selection. It is used when a persisted upload's source copy is gone.
func (p *transferQueuePages) specWithSource(ctx context.Context, task domainsftp.Task,
	local apptransfer.FileSystem, sources []string) (apptransfer.TaskSpec, []apptransfer.FileSystem, error) {
	spec, opened, err := p.specForPeer(ctx, task)
	if err != nil {
		_ = local.Close()
		return apptransfer.TaskSpec{}, nil, err
	}
	spec.Source = local
	spec.Sources = append([]string(nil), sources...)
	opened = append(opened, local)
	return spec, opened, nil
}

// specForPeer opens only the non-local endpoint; the local endpoint is the
// caller's.
func (p *transferQueuePages) specForPeer(ctx context.Context, task domainsftp.Task) (apptransfer.TaskSpec, []apptransfer.FileSystem, error) {
	if task.DestHost == "" {
		return apptransfer.TaskSpec{}, nil, errors.New("the destination is not a saved Host")
	}
	dest, err := p.files.OpenRemote(ctx, model.HostID(task.DestHost), task.DestLabel)
	if err != nil {
		return apptransfer.TaskSpec{}, nil, err
	}
	skipUnchanged := task.SkipUnchanged
	return apptransfer.TaskSpec{
		Kind:          task.Kind,
		Dest:          dest,
		Sources:       append([]string(nil), task.Sources...),
		DestDir:       task.DestDir,
		Move:          task.Move,
		Conflict:      apptransfer.ConflictPolicy(task.Conflict),
		BatchID:       task.BatchID,
		SkipUnchanged: &skipUnchanged,
		SourceLabel:   task.SourceLabel,
		DestLabel:     task.DestLabel,
		SourceHost:    task.SourceHost,
		DestHost:      task.DestHost,
	}, []apptransfer.FileSystem{dest}, nil
}

// needsSource reports whether a row is waiting for a source the phone can no
// longer read. Only a device upload can reach this: remote sources stay
// addressable by their Host.
func (p *transferQueuePages) needsSource(task domainsftp.Task) bool {
	if task.Kind != domainsftp.OpUpload || task.Status == domainsftp.TaskQueued ||
		task.Status == domainsftp.TaskPending {
		return false
	}
	// A terminal upload normally offers only Restart. A not-found failure is
	// the evicted-cache case, so Re-select must stay available even after the
	// row settled (Restart alone would replay the missing path). The same
	// rule applies to a row restored from disk, which is already terminal.
	if task.Terminal() && !(task.Status == domainsftp.TaskFailed &&
		task.Error == string(apptransfer.KindNotFound)) {
		return false
	}
	if len(task.Sources) == 0 {
		return false
	}
	for _, source := range task.Sources {
		if _, err := os.Stat(source); err != nil {
			return true
		}
	}
	return false
}

// canEnqueueUpload reports whether the Files page can offer an upload at all.
func (p *transferQueuePages) canEnqueueUpload() bool { return p.canUpload() }

// draw renders the global queue. Both the Files page and the Sessions home
// call this one function, so the same rows, statuses, progress, and controls
// appear in both places.
func (p *transferQueuePages) draw(c *ui.Context) {
	if !p.available() {
		return
	}
	tasks := p.queue.Tasks()
	kit.SectionBar(c, "Transfers", nil)
	if notice, problem := p.noticeText(); notice != "" {
		tone := c.Theme().TextMuted
		icon := icons.Loader
		if problem {
			tone, icon = c.Theme().Danger, icons.TriangleAlert
		}
		kit.Notice(c, icon, tone, notice, func() {
			if kit.GhostButton(c, icons.X, "Dismiss").Clicked() {
				p.clearNotice()
			}
		})
	}
	if len(tasks) == 0 {
		kit.EmptyHint(c, "No transfers yet. Copies and device uploads appear here.")
		return
	}
	for _, task := range tasks {
		task := task
		p.drawRow(c, task)
	}
}

// drawRow renders one Transfer: its name, state, progress, endpoints, and the
// actions its lifecycle permits.
func (p *transferQueuePages) drawRow(c *ui.Context, task domainsftp.Task) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Padding(8).
		Background(c.Theme().Surface).Radius(c.Theme().Radius).
		Border(1, c.Theme().Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, transferTaskName(task)).FontSize(13).FontWeight(600).Grow(1).
				MinWidth(0).SingleLine()
			ui.Text(c, transferStatusLabel(task.Status)).FontSize(11).
				TextColor(transferStatusColor(c, task.Status)).NoWrap()
		})
		ui.Progress(c, float64(task.Percent())/100).FillWidth()
		ui.Text(c, transferTaskDetail(task)).FontSize(11).
			TextColor(c.Theme().TextMuted).FillWidth().MinWidth(0)
		if task.ReconnectRequired {
			ui.Text(c, "Reconnect and continue this transfer.").FontSize(11).
				TextColor(c.Theme().Warning).FillWidth().MinWidth(0)
		}
		if task.PendingConflict != nil {
			if kit.PrimaryButton(c, icons.Copy, "Overwrite").Clicked() {
				_ = p.queue.ResolveConflict(task.ID, apptransfer.ConflictReplace, false)
			}
			if kit.GhostButton(c, icons.ChevronRight, "Skip").Clicked() {
				_ = p.queue.ResolveConflict(task.ID, apptransfer.ConflictSkip, false)
			}
			if kit.GhostButton(c, icons.Copy, "Rename").Clicked() {
				_ = p.queue.ResolveConflict(task.ID, apptransfer.ConflictDuplicate, false)
			}
		}
		if p.needsSource(task) {
			ui.Text(c, "The source file is no longer available. Choose it again to continue.").
				FontSize(11).TextColor(c.Theme().Warning).FillWidth().MinWidth(0)
			if kit.PrimaryButton(c, icons.Upload, "Re-select source").Clicked() {
				p.reselectTask(task.ID)
			}
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if task.Status.CanPause() {
				if kit.GhostButton(c, icons.Square, "Pause").Clicked() {
					_ = p.queue.Pause(task.ID)
				}
			}
			if task.Status.CanResume() {
				if kit.PrimaryButton(c, icons.Play, "Resume").Clicked() {
					p.resumeTask(task.ID)
				}
			}
			if task.Status.CanRetry() {
				if kit.GhostButton(c, icons.RefreshCw, "Restart").Clicked() {
					p.restartTask(task.ID)
				}
			}
			if task.Status.CanCancel() {
				if kit.GhostButton(c, icons.X, "Cancel").Clicked() {
					_ = p.queue.Cancel(task.ID)
				}
			}
		})
	})
}

func transferTaskName(task domainsftp.Task) string {
	if name := task.Name(); name != "" {
		return name
	}
	if len(task.Sources) > 1 {
		return fmt.Sprintf("%d items", len(task.Sources))
	}
	return "Transfer"
}

func transferStatusLabel(status domainsftp.TaskStatus) string {
	switch status {
	case domainsftp.TaskPending:
		return "Pending"
	case domainsftp.TaskQueued:
		return "Queued"
	case domainsftp.TaskTransferring:
		return "Transferring"
	case domainsftp.TaskPausing:
		return "Pausing"
	case domainsftp.TaskPaused:
		return "Paused"
	case domainsftp.TaskAttention:
		return "Needs attention"
	case domainsftp.TaskInterrupted:
		return "Interrupted"
	case domainsftp.TaskCompleted:
		return "Completed"
	case domainsftp.TaskFailed:
		return "Failed"
	case domainsftp.TaskCancelled:
		return "Cancelled"
	default:
		return string(status)
	}
}

func transferStatusColor(c *ui.Context, status domainsftp.TaskStatus) ui.Color {
	switch status {
	case domainsftp.TaskCompleted:
		return c.Theme().Success
	case domainsftp.TaskFailed:
		return c.Theme().Danger
	case domainsftp.TaskAttention, domainsftp.TaskInterrupted:
		return c.Theme().Warning
	default:
		return c.Theme().TextMuted
	}
}

func transferTaskDetail(task domainsftp.Task) string {
	parts := make([]string, 0, 5)
	if task.SourceLabel != "" || task.DestLabel != "" {
		source := task.SourceLabel
		if source == "" {
			source = "This device"
		}
		dest := task.DestLabel
		if dest == "" {
			dest = "This device"
		}
		parts = append(parts, source+" → "+dest)
	}
	parts = append(parts, fmt.Sprintf("%d%%", task.Percent()))
	if task.BytesTotal > 0 {
		parts = append(parts, fmt.Sprintf("%s / %s",
			domainsftp.FormatSize(task.BytesDone), domainsftp.FormatSize(task.BytesTotal)))
	}
	if task.FilesTotal > 1 {
		parts = append(parts, fmt.Sprintf("%d/%d files", task.FilesDone, task.FilesTotal))
	}
	if task.Status == domainsftp.TaskTransferring && task.Speed > 0 {
		parts = append(parts, domainsftp.FormatSize(task.Speed)+"/s")
	}
	if task.Error != "" {
		parts = append(parts, strings.ReplaceAll(task.Error, "-", " "))
	}
	return strings.Join(parts, " · ")
}
