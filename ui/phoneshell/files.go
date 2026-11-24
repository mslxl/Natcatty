package phoneshell

// The Files pages. They browse one SSH Host's filesystem, let the user pick
// another Host and copy the selection there, and resolve a destination that
// already exists by overwriting, skipping, or renaming it. A Session's Files
// tool can follow the terminal's working directory, so a `cd` in the shell
// moves the browser with it.
//
// The directory transport, the copy engine, and the conflict policies are the
// shared application transfer service the desktop Files panel also uses. This
// file owns only the phone's view state and the routing of a remote copy
// through that service; it never implements SFTP or a copy loop itself.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// FilesService is the phone Files pages' seam onto the shared transfer
// application service. Opening a Host, listing a directory, and copying
// between two hosts go through the same implementation the desktop panel uses,
// so protocol selection, conflict rules, and the copy engine cannot drift.
type FilesService interface {
	// OpenRemote opens one saved Host's filesystem.
	OpenRemote(ctx context.Context, hostID model.HostID, label string) (apptransfer.FileSystem, error)
	// OpenLocal opens the phone's own filesystem. Device uploads read the
	// system picker's copies through it.
	OpenLocal(ctx context.Context) (apptransfer.FileSystem, error)
	// List returns one directory's entries, including the typed parent entry.
	List(ctx context.Context, files apptransfer.FileSystem, dir string) ([]domainsftp.Entry, error)
	// Copy runs one copy request through the shared engine.
	Copy(ctx context.Context, request apptransfer.CopyRequest) (apptransfer.CopyResult, error)
}

// FilesProvider is the phone Session runtime's Files seam. The Shell takes the
// service from the Session opener when the composition root did not pass one
// explicitly, which keeps the phone root's wiring to one option.
type FilesProvider interface {
	FilesService() FilesService
}

// WithFiles gives the Host detail pages and the Session Tools surface the
// shared transfer service. Without it the Files entry is absent and the tool
// reports that this build cannot browse files; no other page is affected.
func WithFiles(service FilesService) Option {
	return func(s *Shell) { s.files = service }
}

// filesStatus is the source Host's open state.
type filesStatus int

const (
	filesLoading filesStatus = iota
	filesReady
	filesFailed
)

// filesConflict is one selected source whose name already exists in the
// destination directory.
type filesConflict struct {
	source string
	name   string
}

// filesCopyPlan is the request a Retry repeats: the target Host, the
// destination directory, the selected source paths, and the answers already
// given. A failure never discards it, so copying can be tried again without
// re-selecting anything.
type filesCopyPlan struct {
	targetLabel string
	// targetID is the Host the plan and its answers belong to. A Retry only
	// reuses them when it reopens that same Host.
	targetID model.HostID
	destDir  string
	sources  []string
	answers  map[string]apptransfer.ConflictPolicy
}

// filesState is the Files page state. It is touched only on the UI thread;
// background work posts outcomes to filesPages.
type filesState struct {
	gen      int
	hostID   model.HostID
	files    apptransfer.FileSystem
	dir      string
	entries  []domainsftp.Entry
	status   filesStatus
	problem  string
	attempt  bool
	selected map[string]bool

	pathInput string
	// listProblem is a failed navigation. The current directory is kept, so a
	// Retry reads the requested place again instead of starting over.
	listProblem string
	// listRetryDir is the directory the failed navigation asked for.
	listRetryDir string

	pickingTarget bool
	// targetHost is the chosen copy target, kept after a failure so Retry can
	// reopen it.
	targetHost  *domainvault.Host
	targetFS    apptransfer.FileSystem
	targetDir   string
	targetOpen  bool
	targetError string
	conflicts   []filesConflict
	conflictAt  int
	plan        *filesCopyPlan

	// follow is the Session Files tool's Follow terminal directory toggle.
	follow bool
	// followTried remembers the terminal directory last attempted, so a
	// failing path is not re-read on every frame.
	followTried string

	// taskID is the queue row this page enqueued, when the shell has the
	// global queue. The page watches it for the result the direct path used to
	// report; leaving the page does not cancel it.
	taskID domainsftp.TaskID
	// taskDest names the queued row's destination in the result line. It is
	// kept because an upload has no target Host picked into targetHost.
	taskDest string
	// hostLabel names the source Host for the queued request.
	hostLabel string

	result        string
	resultProblem bool
}

// filesPages owns one Files surface. There is one per Host-detail page and one
// per Pane's Tools surface, so two open Files views never share a directory or
// a selection. Background work reports through a mutex-guarded queue; the UI
// thread drains it before drawing.
type filesPages struct {
	service FilesService
	// transferQueue is the shared global Transfer queue panel. A nil value
	// keeps the page's direct single-operation copy.
	transferQueue *transferQueuePages
	// targetHosts lists the saved Hosts the copy target picker offers. The
	// Shell points it at the Hosts section's list.
	targetHosts func() []domainvault.Host
	// followCWD reads the terminal's working directory for a Session tool. It
	// is nil on a Host-detail page, which has no terminal to follow.
	followCWD func() string

	mu         sync.Mutex
	generation int
	busy       bool
	busyLabel  string
	queue      []filesOutcome
	services   ui.Services

	ui filesState
}

// filesOutcome is one background result. gen is the generation the work
// started in; a page that reset, closed, or changed Host meanwhile drops it.
type filesOutcome struct {
	gen  int
	kind filesOutcomeKind

	files   apptransfer.FileSystem
	dir     string
	entries []domainsftp.Entry
	err     error

	targetFS  apptransfer.FileSystem
	targetDir string
	conflicts []filesConflict
	copied    int
	copyErr   error
	// taskID is the queue row a filesQueued outcome created.
	taskID domainsftp.TaskID
}

type filesOutcomeKind int

const (
	filesOpenedSource filesOutcomeKind = iota
	filesListedSource
	filesOpenedTarget
	filesPreflighted
	filesCopied
	filesQueued
)

func newFilesPages(service FilesService, targetHosts func() []domainvault.Host) *filesPages {
	return &filesPages{service: service, targetHosts: targetHosts}
}

// available reports whether this build wired the shared transfer service.
func (p *filesPages) available() bool { return p != nil && p.service != nil }

// release closes the open transports and drops any in-flight result. It is
// safe to call even when nothing was opened.
func (p *filesPages) release() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.generation++
	p.ui.gen = p.generation
	p.queue = nil
	p.busy = false
	p.busyLabel = ""
	p.mu.Unlock()
	p.closeTarget()
	if p.ui.files != nil {
		_ = p.ui.files.Close()
		p.ui.files = nil
	}
	p.ui = filesState{gen: p.ui.gen}
}

// closeTarget closes the destination transport and clears the target state.
func (p *filesPages) closeTarget() {
	if p.ui.targetFS != nil {
		_ = p.ui.targetFS.Close()
		p.ui.targetFS = nil
	}
	p.ui.targetOpen = false
	p.ui.conflicts = nil
	p.ui.conflictAt = 0
	p.ui.targetError = ""
}

// resetForHost drops the previous Host's transport and view state.
func (p *filesPages) resetForHost(host domainvault.Host) {
	p.closeTarget()
	if p.ui.files != nil {
		_ = p.ui.files.Close()
	}
	p.mu.Lock()
	p.generation++
	gen := p.generation
	p.mu.Unlock()
	p.ui = filesState{
		gen:       gen,
		hostID:    host.ID,
		hostLabel: hostName(host),
		status:    filesLoading,
	}
}

// setServices remembers the frame's invalidator so a background result can
// repaint the page.
func (p *filesPages) setServices(services ui.Services) { p.services = services }

// run starts one background operation unless another is already running. The
// first return value reports whether the work started.
func (p *filesPages) run(label string, work func(gen int) filesOutcome) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return false
	}
	p.busy = true
	p.busyLabel = label
	gen := p.generation
	services := p.services
	p.mu.Unlock()
	go func() {
		out := work(gen)
		out.gen = gen
		p.mu.Lock()
		p.busy = false
		p.busyLabel = ""
		p.queue = append(p.queue, out)
		p.mu.Unlock()
		services.Invalidate()
	}()
	return true
}

// drain applies every finished background operation. Stale generations are
// discarded, so leaving the page or changing Host cannot show an old result.
func (p *filesPages) drain() {
	p.mu.Lock()
	queue := p.queue
	p.queue = nil
	p.mu.Unlock()
	for _, out := range queue {
		if out.gen != p.ui.gen {
			if out.files != nil {
				_ = out.files.Close()
			}
			if out.targetFS != nil {
				_ = out.targetFS.Close()
			}
			continue
		}
		p.apply(out)
	}
}

// apply folds one finished operation into the UI state.
func (p *filesPages) apply(out filesOutcome) {
	switch out.kind {
	case filesOpenedSource:
		if out.err != nil {
			if out.files != nil {
				_ = out.files.Close()
			}
			p.ui.status = filesFailed
			p.ui.problem = "Could not open Files for this Host: " + out.err.Error()
			return
		}
		p.ui.files = out.files
		p.ui.status = filesReady
		p.ui.problem = ""
		p.ui.dir = out.dir
		p.ui.entries = out.entries
		p.ui.selected = nil
		p.ui.listProblem = ""
	case filesListedSource:
		if out.err != nil {
			// The current directory and listing stay; Retry reads the place
			// the failed navigation asked for.
			p.ui.listProblem = "Could not open " + out.dir + ": " + out.err.Error()
			p.ui.listRetryDir = out.dir
			return
		}
		p.ui.dir = out.dir
		p.ui.entries = out.entries
		p.ui.selected = nil
		p.ui.listProblem = ""
		p.ui.listRetryDir = ""
		p.ui.result = ""
		p.ui.resultProblem = false
	case filesOpenedTarget:
		if out.err != nil {
			if out.targetFS != nil {
				_ = out.targetFS.Close()
			}
			p.ui.targetError = "Could not open the target Host: " + out.err.Error()
			return
		}
		p.ui.targetFS = out.targetFS
		p.ui.targetDir = out.targetDir
		p.ui.targetOpen = true
		p.ui.targetError = ""
		p.ui.conflicts = nil
		p.ui.conflictAt = 0
	case filesPreflighted:
		if out.err != nil {
			p.ui.targetError = "Could not check the destination: " + out.err.Error()
			return
		}
		p.ui.targetError = ""
		p.ui.conflicts = out.conflicts
		p.ui.conflictAt = 0
		if len(out.conflicts) == 0 {
			p.launchCopy()
		}
	case filesCopied:
		p.ui.targetFS = nil
		p.ui.targetOpen = false
		if out.copyErr != nil {
			p.ui.result = "Could not copy: " + out.copyErr.Error()
			p.ui.resultProblem = true
			return
		}
		noun := "files"
		if out.copied == 1 {
			noun = "file"
		}
		p.ui.result = fmt.Sprintf("Copied %d %s to %s.", out.copied, noun, p.targetName())
		p.ui.resultProblem = false
		p.ui.plan = nil
		p.ui.targetHost = nil
		p.ui.taskID = ""
	case filesQueued:
		p.ui.targetFS = nil
		p.ui.targetOpen = false
		if out.copyErr != nil {
			p.ui.result = "Could not copy: " + out.copyErr.Error()
			p.ui.resultProblem = true
			return
		}
		// The queue owns the copy now. The row reports its own progress in
		// the shared panel; the page keeps the plan so a failure can be
		// retried from here as well.
		p.ui.taskID = out.taskID
		p.ui.result = ""
		p.ui.resultProblem = false
	}
}

// pollQueue folds a queued copy's terminal state into the page's result. The
// row itself keeps running and reporting in the shared panel; this only keeps
// the page's own result line truthful, as the direct-copy path did.
func (p *filesPages) pollQueue() {
	if p.transferQueue == nil || p.ui.taskID == "" {
		return
	}
	task, ok := p.transferQueue.queue.Task(p.ui.taskID)
	if !ok {
		p.ui.taskID = ""
		return
	}
	if !task.Terminal() {
		return
	}
	p.ui.taskID = ""
	switch task.Status {
	case domainsftp.TaskCompleted:
		noun := "files"
		if task.FilesDone == 1 {
			noun = "file"
		}
		target := p.targetName()
		if p.ui.taskDest != "" {
			target = p.ui.taskDest
		}
		p.ui.result = fmt.Sprintf("Copied %d %s to %s.", task.FilesDone, noun, target)
		p.ui.resultProblem = false
		p.ui.plan = nil
		p.ui.targetHost = nil
	case domainsftp.TaskFailed:
		reason := task.Error
		if task.Err != nil {
			reason = task.Err.Error()
		}
		if reason == "" {
			reason = "the transfer failed"
		}
		p.ui.result = "Could not copy: " + reason
		p.ui.resultProblem = true
	case domainsftp.TaskCancelled:
		p.ui.result = "Copy cancelled."
		p.ui.resultProblem = false
		p.ui.plan = nil
		p.ui.targetHost = nil
	}
}

// beginUpload picks device files and queues them into the current directory.
// The system picker returns app-readable copies; the queue row keeps the
// destination Host and directory so an evicted source can be re-selected
// later without starting a new Transfer.
func (p *filesPages) beginUpload() {
	if p.transferQueue == nil || !p.transferQueue.canEnqueueUpload() || p.ui.files == nil {
		return
	}
	paths, err := p.transferQueue.pick()
	if err != nil {
		p.ui.result = "Could not choose files: " + err.Error()
		p.ui.resultProblem = true
		return
	}
	if len(paths) == 0 {
		return
	}
	hostID, hostLabel, dir := p.ui.hostID, p.ui.hostLabel, p.ui.dir
	p.ui.result = ""
	p.ui.resultProblem = false
	p.ui.taskID = ""
	p.ui.taskDest = hostLabel
	started := p.run("Queueing…", func(gen int) filesOutcome {
		id, err := p.transferQueue.enqueueUpload(context.Background(), hostID, hostLabel, dir, paths)
		return filesOutcome{kind: filesQueued, taskID: id, copyErr: err}
	})
	if !started {
		p.ui.result = "Another file operation is still running."
		p.ui.resultProblem = true
	}
}

// ensureSource opens the Host's filesystem the first time the page draws for
// it. A failed open is not retried automatically; Retry calls openSource again.
func (p *filesPages) ensureSource(host domainvault.Host) {
	if !p.available() {
		return
	}
	if p.ui.hostID != host.ID {
		p.resetForHost(host)
	}
	if p.ui.attempt || p.ui.files != nil {
		return
	}
	p.openSource(host)
}

// openSource starts the Host's filesystem open and first listing.
func (p *filesPages) openSource(host domainvault.Host) {
	service := p.service
	if service == nil {
		return
	}
	p.ui.status = filesLoading
	p.ui.problem = ""
	p.ui.attempt = true
	source := host
	started := p.run("Opening "+hostName(source)+"…", func(gen int) filesOutcome {
		ctx := context.Background()
		files, err := service.OpenRemote(ctx, source.ID, hostName(source))
		if err != nil {
			return filesOutcome{kind: filesOpenedSource, err: err}
		}
		root, err := files.Root(ctx)
		if err != nil {
			return filesOutcome{kind: filesOpenedSource, files: files, err: err}
		}
		entries, err := service.List(ctx, files, root)
		if err != nil {
			return filesOutcome{kind: filesOpenedSource, files: files, err: err}
		}
		return filesOutcome{kind: filesOpenedSource, files: files, dir: root, entries: entries}
	})
	if !started {
		p.ui.attempt = false
	}
}

// retrySource re-runs a failed open with the current Host and keeps the page
// where it is.
func (p *filesPages) retrySource(host domainvault.Host) {
	p.ui.attempt = false
	p.openSource(host)
}

// navigate reads another directory on the open source filesystem. The current
// path is kept until the read succeeds.
func (p *filesPages) navigate(dir string) {
	if p.ui.files == nil || dir == "" {
		return
	}
	files := p.ui.files
	service := p.service
	p.ui.listProblem = ""
	p.run("Opening "+dir+"…", func(gen int) filesOutcome {
		entries, err := service.List(context.Background(), files, dir)
		return filesOutcome{kind: filesListedSource, dir: dir, entries: entries, err: err}
	})
}

// up navigates to the parent of the current directory.
func (p *filesPages) up() {
	if p.ui.files == nil {
		return
	}
	parent := p.ui.files.Parent(p.ui.dir)
	if parent == p.ui.dir {
		return
	}
	p.navigate(parent)
}

// toggle selects or deselects one entry.
func (p *filesPages) toggle(path string) {
	if p.ui.selected == nil {
		p.ui.selected = make(map[string]bool)
	}
	if p.ui.selected[path] {
		delete(p.ui.selected, path)
		if len(p.ui.selected) == 0 {
			p.ui.selected = nil
		}
		return
	}
	p.ui.selected[path] = true
}

// selectedSources returns the selected paths in a stable order.
func (p *filesPages) selectedSources() []string {
	paths := make([]string, 0, len(p.ui.selected))
	for path := range p.ui.selected {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// beginTargetPick shows the Hosts a cross-Host copy can target. Only a Host
// that supports file transfer and is not the source Host is offered.
func (p *filesPages) beginTargetPick() {
	if len(p.ui.selected) == 0 || !p.available() {
		return
	}
	p.ui.pickingTarget = true
	p.ui.targetError = ""
	p.ui.result = ""
	p.ui.resultProblem = false
}

func (p *filesPages) targetCandidates() []domainvault.Host {
	if p.targetHosts == nil {
		return nil
	}
	hosts := p.targetHosts()
	out := make([]domainvault.Host, 0, len(hosts))
	for _, host := range hosts {
		if !host.Protocol.SupportsFileTransfer() || host.ID == p.ui.hostID {
			continue
		}
		out = append(out, host)
	}
	return out
}

// chooseTarget opens the chosen destination Host and reads its root, so the
// destination directory field starts at a place that exists.
func (p *filesPages) chooseTarget(host domainvault.Host) {
	p.chooseTargetAt(host, "")
}

// chooseTargetAt opens the chosen destination Host. An empty dir reads the
// Host's root, which is where a first choice starts. A Retry passes the plan's
// remembered directory, so the destination the user chose is reopened instead
// of reset to the root.
func (p *filesPages) chooseTargetAt(host domainvault.Host, dir string) {
	if !p.available() {
		return
	}
	p.ui.pickingTarget = false
	p.ui.targetHost = &host
	p.ui.targetError = ""
	p.ui.targetOpen = false
	p.ui.result = ""
	p.ui.resultProblem = false
	service := p.service
	dest := host
	p.run("Opening "+hostName(dest)+"…", func(gen int) filesOutcome {
		ctx := context.Background()
		files, err := service.OpenRemote(ctx, dest.ID, hostName(dest))
		if err != nil {
			return filesOutcome{kind: filesOpenedTarget, err: err}
		}
		root, err := files.Root(ctx)
		if err != nil {
			return filesOutcome{kind: filesOpenedTarget, targetFS: files, err: err}
		}
		if dir != "" {
			return filesOutcome{kind: filesOpenedTarget, targetFS: files, targetDir: dir}
		}
		return filesOutcome{kind: filesOpenedTarget, targetFS: files, targetDir: root}
	})
}

// startCopy checks the destination for conflicts and copies when there are
// none. The plan is kept so a failure can be retried without re-selecting.
func (p *filesPages) startCopy() {
	if p.ui.targetFS == nil || p.ui.targetHost == nil {
		return
	}
	dir := strings.TrimSpace(p.ui.targetDir)
	if dir == "" {
		p.ui.targetError = "Enter a destination directory."
		return
	}
	sources := p.selectedSources()
	answers := make(map[string]apptransfer.ConflictPolicy)
	// A Retry repeats the same destination and selection, so the answers the
	// user already gave are reused instead of asked again.
	if previous := p.ui.plan; previous != nil &&
		previous.targetID == p.ui.targetHost.ID &&
		previous.destDir == dir && samePaths(previous.sources, sources) {
		for source, action := range previous.answers {
			answers[source] = action
		}
	}
	plan := &filesCopyPlan{
		targetLabel: hostName(*p.ui.targetHost),
		targetID:    p.ui.targetHost.ID,
		destDir:     dir,
		sources:     sources,
		answers:     answers,
	}
	p.ui.plan = plan
	p.preflight(plan)
}

// preflight lists the destination names and records the selected sources that
// already exist there. The answers collected from the UI are reused on a
// retry, so a later attempt does not ask the same question again.
func (p *filesPages) preflight(plan *filesCopyPlan) {
	target := p.ui.targetFS
	if target == nil || plan == nil {
		return
	}
	answers := plan.answers
	p.ui.targetError = ""
	copiedPlan := *plan
	started := p.run("Checking the destination…", func(gen int) filesOutcome {
		ctx := context.Background()
		conflicts := make([]filesConflict, 0, len(copiedPlan.sources))
		for _, source := range copiedPlan.sources {
			if _, answered := answers[source]; answered {
				continue
			}
			name := pathBase(source)
			destination := target.Join(copiedPlan.destDir, name)
			if _, err := target.Stat(ctx, destination); err == nil {
				conflicts = append(conflicts, filesConflict{source: source, name: name})
			}
		}
		return filesOutcome{kind: filesPreflighted, conflicts: conflicts}
	})
	if !started {
		p.ui.targetError = "Another file operation is still running."
	}
}

// answerConflict records one conflict choice and either asks the next conflict
// or starts the copy. Overwrite and Skip are the shared engine's policies;
// Rename is its duplicate policy, which places "(copy)" before the extension,
// so the extension is preserved.
func (p *filesPages) answerConflict(action apptransfer.ConflictPolicy) {
	if p.ui.plan == nil || p.ui.conflictAt >= len(p.ui.conflicts) {
		return
	}
	conflict := p.ui.conflicts[p.ui.conflictAt]
	p.ui.plan.answers[conflict.source] = action
	p.ui.conflictAt++
	if p.ui.conflictAt < len(p.ui.conflicts) {
		return
	}
	p.launchCopy()
}

// cancelConflict abandons the copy and closes the destination transport.
func (p *filesPages) cancelConflict() {
	p.ui.conflicts = nil
	p.ui.conflictAt = 0
	p.ui.plan = nil
	p.ui.targetHost = nil
	p.closeTarget()
}

// launchCopy starts the shared copy engine with the answers collected so far.
// A conflict the preflight did not see stops the copy instead of overwriting
// without permission.
func (p *filesPages) launchCopy() {
	if p.ui.targetFS == nil || p.ui.plan == nil {
		return
	}
	plan := *p.ui.plan
	answers := make(map[string]apptransfer.ConflictPolicy, len(plan.answers))
	for source, action := range plan.answers {
		answers[source] = action
	}
	target := p.ui.targetFS
	source := p.ui.files
	service := p.service
	// The copy goroutine owns the destination transport from here; the page
	// must not close it a second time. The destination panel is hidden until a
	// Retry opens a new transport, so its action can never be a dead button.
	p.ui.targetFS = nil
	p.ui.targetOpen = false
	p.ui.taskID = ""
	p.ui.taskDest = plan.targetLabel
	if p.transferQueue != nil && p.transferQueue.available() {
		// The global queue owns the work, so leaving this page or backgrounding
		// the app does not stop it. It opens its own transports: the page's
		// browsing transport is closed here and the queue's row carries the
		// request, checkpoint, and answers.
		_ = target.Close()
		sourceHost, sourceLabel := p.ui.hostID, p.ui.hostLabel
		started := p.run("Queueing…", func(gen int) filesOutcome {
			id, err := p.transferQueue.enqueueCopy(context.Background(), sourceHost, sourceLabel,
				plan.targetID, plan.targetLabel, plan.sources, plan.destDir, answers)
			return filesOutcome{kind: filesQueued, taskID: id, copyErr: err}
		})
		if !started {
			p.ui.targetError = "Another file operation is still running."
		}
		return
	}
	started := p.run("Copying…", func(gen int) filesOutcome {
		defer func() { _ = target.Close() }()
		request := apptransfer.CopyRequest{
			Source:   source,
			Dest:     target,
			Sources:  plan.sources,
			DestDir:  plan.destDir,
			Kind:     domainsftp.OpCopy,
			Conflict: apptransfer.ConflictAsk,
			OnConflict: func(conflict domainsftp.Conflict) apptransfer.ConflictResolution {
				if action, ok := answers[conflict.SourcePath]; ok {
					return apptransfer.ConflictResolution{Action: action}
				}
				return apptransfer.ConflictResolution{Action: apptransfer.ConflictStop}
			},
		}
		result, err := service.Copy(context.Background(), request)
		return filesOutcome{kind: filesCopied, copied: len(result.Written), copyErr: err}
	})
	if !started {
		_ = target.Close()
	}
}

// retryCopy reopens the target Host at the remembered destination and repeats
// the remembered plan. The destination directory and the conflict answers the
// user already gave are kept, so Retry continues the copy instead of falling
// back to the target root and asking the same conflicts again.
func (p *filesPages) retryCopy() {
	if p.ui.plan == nil || p.ui.targetHost == nil {
		return
	}
	plan := p.ui.plan
	p.ui.targetError = ""
	p.ui.resultProblem = false
	// Only a plan that belongs to this target carries its destination and
	// answers. A plan for another Host starts at the new target's root.
	if plan.targetID == p.ui.targetHost.ID {
		p.chooseTargetAt(*p.ui.targetHost, plan.destDir)
		return
	}
	p.chooseTarget(*p.ui.targetHost)
}

// targetName is the label the copy result names.
func (p *filesPages) targetName() string {
	if p.ui.plan != nil && p.ui.plan.targetLabel != "" {
		return p.ui.plan.targetLabel
	}
	if p.ui.targetHost != nil {
		return hostName(*p.ui.targetHost)
	}
	return "the target"
}

// isBusy reports whether a background operation is running.
func (p *filesPages) isBusy() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.busy
}

func (p *filesPages) busyText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.busyLabel
}

// followTerminal moves the browser to the directory the terminal reported
// when Follow is on. Each new directory is tried once; a directory the Host
// cannot read leaves the current path untouched.
func (p *filesPages) followTerminal() {
	if !p.ui.follow || p.followCWD == nil || p.ui.files == nil || p.isBusy() {
		return
	}
	cwd := strings.TrimSpace(p.followCWD())
	if cwd == "" || cwd == p.ui.dir || cwd == p.ui.followTried {
		return
	}
	p.ui.followTried = cwd
	p.navigate(cwd)
}

// draw renders the Files page for one Host. tools marks the Session Tools
// surface, which already draws inside a scroll column; a Host-detail route
// needs the page to provide its own.
func (p *filesPages) draw(c *ui.Context, host domainvault.Host, tools bool) {
	if p == nil {
		return
	}
	p.setServices(c.Services())
	p.drain()
	if tools {
		p.drawBody(c, host)
		return
	}
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(pageContentWidth(c)).Padding(16).Gap(12).Children(func() {
			p.drawBody(c, host)
		})
	})
}

func (p *filesPages) drawBody(c *ui.Context, host domainvault.Host) {
	title := "Files · " + hostName(host)
	kit.SectionBar(c, title, nil)
	if !p.available() {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"Files is not available in this build.", nil)
		return
	}
	p.ensureSource(host)
	p.followTerminal()
	p.drain()
	p.pollQueue()

	if p.ui.status == filesFailed {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.ui.problem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				p.retrySource(host)
			}
		})
		return
	}
	if p.ui.status == filesLoading && p.ui.files == nil {
		kit.EmptyHint(c, "Opening "+hostName(host)+"…")
		return
	}
	if busy := p.busyText(); busy != "" {
		kit.Notice(c, icons.Loader, c.Theme().TextMuted, busy, nil)
	}
	if p.ui.result != "" {
		tone := c.Theme().Success
		icon := icons.CircleCheck
		actions := func() {}
		if p.ui.resultProblem {
			tone, icon = c.Theme().Danger, icons.TriangleAlert
			actions = func() {
				if p.ui.plan != nil && p.ui.targetHost != nil {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						p.retryCopy()
					}
				}
			}
		}
		kit.Notice(c, icon, tone, p.ui.result, actions)
	}
	if p.ui.targetError != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.ui.targetError, func() {
			if p.ui.targetHost != nil && kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				if p.ui.targetOpen {
					p.startCopy()
					return
				}
				// Reopening after an open failure must resume the remembered
				// plan, not reset the destination to the target root.
				if p.ui.plan != nil && p.ui.plan.targetID == p.ui.targetHost.ID {
					p.chooseTargetAt(*p.ui.targetHost, p.ui.plan.destDir)
					return
				}
				p.chooseTarget(*p.ui.targetHost)
			}
		})
	}
	// The global Transfer queue is the same panel the Sessions home draws, so
	// a copy started here keeps reporting after the page is left.
	if p.transferQueue != nil {
		p.transferQueue.draw(c)
	}
	if p.ui.conflictAt < len(p.ui.conflicts) {
		p.drawConflict(c)
		return
	}
	if p.ui.targetOpen {
		p.drawTarget(c)
		return
	}
	if p.ui.pickingTarget {
		p.drawTargetPicker(c)
		return
	}
	p.drawBrowser(c)
}

// drawBrowser renders the path controls, the actions, and the listing.
func (p *filesPages) drawBrowser(c *ui.Context) {
	kit.FormRow(c, "Path", func() {
		ui.TextInput(c, &p.ui.pathInput).FillWidth().Placeholder("/var/log")
	})
	ui.Text(c, p.ui.dir).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.GhostButton(c, icons.ArrowUp, "Up").Clicked() {
			p.up()
		}
		if kit.GhostButton(c, icons.RefreshCw, "Go").Clicked() {
			p.navigate(strings.TrimSpace(p.ui.pathInput))
		}
		if p.ui.listProblem != "" {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				retryDir := p.ui.listRetryDir
				if retryDir == "" {
					retryDir = p.ui.dir
				}
				p.navigate(retryDir)
			}
		}
	})
	if p.ui.listProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.ui.listProblem, nil)
	}
	p.drawCopyControls(c)
	if len(p.ui.entries) == 0 {
		kit.EmptyHint(c, "This directory is empty.")
		return
	}
	for _, entry := range p.ui.entries {
		entry := entry
		if entry.Name == ".." {
			row := kit.ListRow(c).Label("Open ..")
			row.Children(func() {
				ui.Icon(c, icons.Lucide(icons.Folder)).TextColor(c.Theme().Accent)
				ui.Text(c, "..").Grow(1)
			})
			if row.Clicked() {
				p.navigate(entry.Path)
			}
			continue
		}
		if entry.IsDir() {
			row := kit.ListRow(c).Label("Open " + entry.Name)
			row.Children(func() {
				ui.Icon(c, icons.Lucide(icons.Folder)).TextColor(c.Theme().Accent)
				ui.Text(c, entry.Name).Grow(1).SingleLine()
				if kit.IconButton(c, selectIcon(p.ui.selected[entry.Path]),
					"Select "+entry.Name).Clicked() {
					p.toggle(entry.Path)
				}
			})
			if row.Clicked() {
				p.navigate(entry.Path)
			}
			continue
		}
		row := kit.ListRow(c).Label("Select " + entry.Name)
		row.Children(func() {
			ui.Icon(c, icons.Lucide(icons.FileText)).TextColor(c.Theme().TextMuted)
			ui.Text(c, entry.Name).Grow(1).SingleLine()
			ui.Icon(c, icons.Lucide(selectIcon(p.ui.selected[entry.Path]))).TextColor(c.Theme().Accent)
		})
		if row.Clicked() {
			p.toggle(entry.Path)
		}
	}
}

// drawCopyControls renders the selection summary and the copy action.
func (p *filesPages) drawCopyControls(c *ui.Context) {
	if p.transferQueue != nil && p.transferQueue.canEnqueueUpload() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Upload files from this device into the directory above.").
				FontSize(12).TextColor(c.Theme().TextMuted)
			if kit.GhostButton(c, icons.Upload, "Upload from device").Clicked() {
				p.beginUpload()
			}
		})
	}
	count := len(p.ui.selected)
	if count == 0 {
		ui.Text(c, "Select files or folders to copy to another Host.").FontSize(12).
			TextColor(c.Theme().TextMuted)
		return
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("%d selected", count)).FontSize(12).
			TextColor(c.Theme().TextMuted)
		if kit.PrimaryButton(c, icons.Copy, "Copy to another Host").Clicked() {
			p.beginTargetPick()
		}
	})
}

// drawTargetPicker lists the eligible target Hosts.
func (p *filesPages) drawTargetPicker(c *ui.Context) {
	kit.SectionBar(c, "Copy to", func() {
		if kit.GhostButton(c, icons.X, "Cancel").Clicked() {
			p.ui.pickingTarget = false
		}
	})
	candidates := p.targetCandidates()
	if len(candidates) == 0 {
		kit.EmptyHint(c, "No other SSH Host can receive files. Hosts that support file transfer appear here.")
		return
	}
	for _, host := range candidates {
		host := host
		row := kit.ListRow(c).Label("Copy to " + hostName(host))
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, hostName(host)).FontWeight(600).SingleLine()
				ui.Text(c, host.Hostname).FontSize(12).
					TextColor(c.Theme().TextMuted).SingleLine()
			})
			ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
		})
		if row.Clicked() {
			p.chooseTarget(host)
		}
	}
}

// drawTarget renders the destination directory and the copy start action.
func (p *filesPages) drawTarget(c *ui.Context) {
	target := "the target Host"
	if p.ui.targetHost != nil {
		target = hostName(*p.ui.targetHost)
	}
	kit.SectionBar(c, "Copy to "+target, func() {
		if kit.GhostButton(c, icons.X, "Cancel").Clicked() {
			p.cancelConflict()
		}
	})
	kit.FormRow(c, "Destination", func() {
		ui.TextInput(c, &p.ui.targetDir).FillWidth()
	})
	ui.Text(c, p.ui.targetDir).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
	if kit.PrimaryButton(c, icons.Copy, "Start copy").Clicked() {
		p.startCopy()
	}
}

// drawConflict renders one destination conflict with the three choices. The
// Rename choice uses the shared duplicate policy, whose "(copy)" suffix is
// placed before the extension.
func (p *filesPages) drawConflict(c *ui.Context) {
	conflict := p.ui.conflicts[p.ui.conflictAt]
	kit.SectionBar(c, fmt.Sprintf("Conflict %d of %d", p.ui.conflictAt+1, len(p.ui.conflicts)), nil)
	ui.Text(c, fmt.Sprintf("%q already exists in the destination.", conflict.name)).
		FontWeight(600)
	ui.Text(c, p.ui.targetDir).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
	ui.Text(c, "Overwrite replaces it, Skip leaves it, and Rename keeps both with the extension intact.").
		FontSize(12).TextColor(c.Theme().TextMuted)
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.DangerButton(c, icons.Trash, "Overwrite").Clicked() {
			p.answerConflict(apptransfer.ConflictReplace)
		}
		if kit.GhostButton(c, icons.ChevronRight, "Skip").Clicked() {
			p.answerConflict(apptransfer.ConflictSkip)
		}
		if kit.PrimaryButton(c, icons.Copy, "Rename").Clicked() {
			p.answerConflict(apptransfer.ConflictDuplicate)
		}
		if kit.GhostButton(c, icons.X, "Cancel copy").Clicked() {
			p.cancelConflict()
		}
	})
}

// selectIcon is the checkbox an unselected and a selected entry show.
func selectIcon(selected bool) string {
	if selected {
		return icons.CircleCheck
	}
	return icons.Square
}

// samePaths reports whether two sorted path lists are identical.
func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pathBase returns the last path component of a POSIX remote path. It is used
// for the copy destination name before the target filesystem joins it.
func pathBase(name string) string {
	name = strings.TrimSuffix(name, "/")
	if index := strings.LastIndexByte(name, '/'); index >= 0 {
		return name[index+1:]
	}
	return name
}
