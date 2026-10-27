//go:build selftest

package app

// Ticket 33 self-test. Press F11, then 3, then 3. The check proves the five
// destination-conflict policies through the real surface state, the global
// Transfer queue, the local adapter, and the on-disk trees:
//
//   - Stop cancels the Transfer, Skip leaves the destination and counts it,
//     Replace writes the source over it, Duplicate writes beside it with the
//     original "(copy)" name, and Merge keeps destination-only folder content.
//   - Apply-to-all answers a batch of two conflicts with one prompt, and the
//     second file takes the same choice without asking again.
//   - Two duplicate Transfers planning at the same time reserve their chosen
//     names and land on "(copy)" and "(copy 2)". A folder Transfer reserves its
//     whole destination subtree, so a file aimed inside it is refused.
//   - A merged folder skips a destination file whose size and whole-second
//     mtime match the source, even when the source has a sub-second difference.
//
// The check drives the same State.Transfer, conflict dialog, and
// Queue.ResolveConflict paths the app wires. Every wait polls a real task,
// dialog, or filesystem condition with a hard deadline; there is no fixed
// sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-33.json.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo/ui"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/selftest"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const selftest33Ticket = 33

const selftest33Wait = 30 * time.Second

type selftest33 struct {
	workdir  string
	service  *apptransfer.Service
	files    apptransfer.FileSystem
	queue    *apptransfer.Queue
	state    *transferstate.State
	cleanup  func()
	deadline time.Time

	policyPhase int
	policyIndex int
	policyID    domainsftp.TaskID

	batchPhase int
	batchID    domainsftp.TaskID

	duplicatePhase int
	duplicateGate  *selftest32Gate
	duplicateFirst domainsftp.TaskID
	duplicateNext  domainsftp.TaskID

	dupFolderGate *selftest32Gate
	dupFolderID   domainsftp.TaskID

	skipPhase int
	skipID    domainsftp.TaskID

	finished bool
}

func (t *selftest33) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest33Ticket,
		Name:   "Transfer conflict policies",
		Steps: []selftest.Step{
			{Name: "stop-skip-replace-duplicate-merge", Run: t.policyStep},
			{Name: "apply-to-all-covers-the-batch", Run: t.batchStep},
			{Name: "duplicate-names-and-path-reservations", Run: t.duplicateStep},
			{Name: "merge-skips-unchanged-by-size-and-whole-second-mtime", Run: t.skipStep},
		},
	}
}

// selftest33PolicyCase is one conflict choice driven through the surface.
type selftest33PolicyCase struct {
	action  apptransfer.ConflictPolicy
	left    string
	right   string
	folder  string
	status  domainsftp.TaskStatus
	skipped int
	verify  func(*selftest33) error
}

func (t *selftest33) policyCases() []selftest33PolicyCase {
	return []selftest33PolicyCase{
		{
			action: apptransfer.ConflictStop, left: "stop-src", right: "stop-dst",
			status: domainsftp.TaskCancelled,
			verify: func(t *selftest33) error {
				got, err := t.read("stop-dst/stop.txt")
				if err != nil || got != "old-stop" {
					return fmt.Errorf("stop left the destination as %q (%v)", got, err)
				}
				return nil
			},
		},
		{
			action: apptransfer.ConflictSkip, left: "skip-src", right: "skip-dst",
			status: domainsftp.TaskCompleted, skipped: 1,
			verify: func(t *selftest33) error {
				got, err := t.read("skip-dst/skip.txt")
				if err != nil || got != "old-skip" {
					return fmt.Errorf("skip left the destination as %q (%v)", got, err)
				}
				return nil
			},
		},
		{
			action: apptransfer.ConflictReplace, left: "replace-src", right: "replace-dst",
			status: domainsftp.TaskCompleted,
			verify: func(t *selftest33) error {
				got, err := t.read("replace-dst/replace.txt")
				if err != nil || got != "new-replace" {
					return fmt.Errorf("replace wrote %q (%v)", got, err)
				}
				return nil
			},
		},
		{
			action: apptransfer.ConflictDuplicate, left: "duplicate-src", right: "duplicate-dst",
			status: domainsftp.TaskCompleted,
			verify: func(t *selftest33) error {
				original, err := t.read("duplicate-dst/dup.txt")
				if err != nil || original != "old-dup" {
					return fmt.Errorf("duplicate changed the original to %q (%v)", original, err)
				}
				copied, err := t.read("duplicate-dst/dup (copy).txt")
				if err != nil || copied != "new-dup" {
					return fmt.Errorf("duplicate copy = %q (%v)", copied, err)
				}
				return nil
			},
		},
		{
			action: apptransfer.ConflictMerge, left: "merge-src", right: "merge-dst", folder: "tree",
			status: domainsftp.TaskCompleted,
			verify: func(t *selftest33) error {
				inside, err := t.read("merge-dst/tree/inside.txt")
				if err != nil || inside != "inside" {
					return fmt.Errorf("merge did not write inside.txt: %q (%v)", inside, err)
				}
				keep, err := t.read("merge-dst/tree/keep.txt")
				if err != nil || keep != "keep" {
					return fmt.Errorf("merge did not keep destination-only content: %q (%v)", keep, err)
				}
				return nil
			},
		},
	}
}

func (t *selftest33) setup() error {
	workdir, err := os.MkdirTemp("", "natcatty-selftest-33-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	for _, dir := range []string{
		"stop-src", "stop-dst",
		"skip-src", "skip-dst",
		"replace-src", "replace-dst",
		"duplicate-src", "duplicate-dst",
		"merge-src/tree", "merge-dst/tree",
		"batch-src", "batch-dst",
		"dupname-src", "dupname-dst",
		"reserve-src/tree", "reserve-dst",
		"skipunchanged-src/tree", "skipunchanged-dst/tree",
	} {
		if err := os.MkdirAll(filepath.Join(workdir, dir), 0o755); err != nil {
			return err
		}
	}
	files := map[string]string{
		"stop-src/stop.txt":               "new-stop",
		"stop-dst/stop.txt":               "old-stop",
		"skip-src/skip.txt":               "new-skip",
		"skip-dst/skip.txt":               "old-skip",
		"replace-src/replace.txt":         "new-replace",
		"replace-dst/replace.txt":         "old-replace",
		"duplicate-src/dup.txt":           "new-dup",
		"duplicate-dst/dup.txt":           "old-dup",
		"merge-src/tree/inside.txt":       "inside",
		"merge-dst/tree/keep.txt":         "keep",
		"batch-src/alpha.txt":             "new-alpha",
		"batch-src/beta.txt":              "new-beta",
		"batch-dst/alpha.txt":             "old-alpha",
		"batch-dst/beta.txt":              "old-beta",
		"dupname-src/same.txt":            "same-new",
		"dupname-dst/same.txt":            "same-old",
		"reserve-src/tree/inside.txt":     "reserve-inside",
		"skipunchanged-src/tree/keep.txt": "new!",
		"skipunchanged-dst/tree/keep.txt": "old!",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(data), 0o644); err != nil {
			return err
		}
	}
	// Same size and same whole second, different sub-second timestamps: only
	// a whole-second comparison may treat the destination as unchanged.
	stamp := time.Unix(1700000000, 0)
	source := filepath.Join(workdir, "skipunchanged-src/tree/keep.txt")
	destination := filepath.Join(workdir, "skipunchanged-dst/tree/keep.txt")
	if err := os.Chtimes(source, stamp.Add(500*time.Millisecond), stamp.Add(500*time.Millisecond)); err != nil {
		return err
	}
	if err := os.Chtimes(destination, stamp, stamp); err != nil {
		return err
	}

	local := adaptersftp.NewLocal()
	handle, err := local.OpenLocal(context.Background())
	if err != nil {
		return err
	}
	t.files = handle
	opener := selftest32Opener{local: local}
	t.service = apptransfer.NewService(opener, opener)
	t.queue = apptransfer.NewQueue(t.service, apptransfer.WithQueueConcurrency(2))
	if err := t.queue.Start(context.Background()); err != nil {
		_ = handle.Close()
		return err
	}
	t.state = transferstate.NewState(t.service, transferstate.WithQueue(t.queue))
	t.state.EnsureStarted()
	t.state.OpenLocal(transferstate.Right)
	t.cleanup = func() {
		if t.queue != nil {
			t.queue.Shutdown()
		}
		if t.state != nil {
			t.state.Shutdown()
		}
		if t.files != nil {
			_ = t.files.Close()
		}
		_ = os.RemoveAll(t.workdir)
	}
	return nil
}

func (t *selftest33) expired(what string) selftest.Outcome {
	if time.Now().After(t.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not settle within %s", what, selftest33Wait))
	}
	return selftest.Pending
}

func (t *selftest33) tabsReady() bool {
	left := t.state.Tab(transferstate.Left)
	right := t.state.Tab(transferstate.Right)
	return left != nil && left.FS != nil && right != nil && right.FS != nil
}

func (t *selftest33) loaded(leftDir, rightDir string) bool {
	left := t.state.Tab(transferstate.Left)
	right := t.state.Tab(transferstate.Right)
	return left != nil && right != nil && !left.Loading && !right.Loading &&
		filepath.Clean(left.Path) == filepath.Clean(filepath.Join(t.workdir, leftDir)) &&
		filepath.Clean(right.Path) == filepath.Clean(filepath.Join(t.workdir, rightDir))
}

func (t *selftest33) openDirs(leftDir, rightDir string) {
	t.state.Navigate(transferstate.Left, filepath.Join(t.workdir, leftDir))
	t.state.Navigate(transferstate.Right, filepath.Join(t.workdir, rightDir))
}

func (t *selftest33) selectAll() error {
	t.state.SelectAll(transferstate.Left)
	if paths := t.state.Tab(transferstate.Left).SelectedPaths(); len(paths) == 0 {
		return fmt.Errorf("the source listing selected nothing")
	}
	return nil
}

func (t *selftest33) selectFolder(name string) error {
	tab := t.state.Tab(transferstate.Left)
	for _, entry := range tab.Entries {
		if entry.Name != name || !entry.IsDir() {
			continue
		}
		t.state.Click(transferstate.Left, entry, 0, false)
		if tab.Selection[entry.Path] {
			return nil
		}
	}
	return fmt.Errorf("the source listing has no selectable folder %q", name)
}

func (t *selftest33) task(id domainsftp.TaskID) (domainsftp.Task, bool) {
	return t.queue.Task(id)
}

func (t *selftest33) read(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(t.workdir, name))
	return string(data), err
}

func (t *selftest33) policyStep(*ui.Context) selftest.Outcome {
	if t.finished {
		*t = selftest33{}
	}
	if t.state != nil {
		t.state.Flush()
	}
	switch t.policyPhase {
	case 0:
		if err := t.setup(); err != nil {
			return selftest.Failed(fmt.Errorf("the conflict fixtures were not created: %v", err))
		}
		t.deadline = time.Now().Add(selftest33Wait)
		t.policyPhase = 1
		return selftest.Pending
	case 1:
		if !t.tabsReady() {
			return t.expired("the two local panes")
		}
		current := t.policyCases()[t.policyIndex]
		t.openDirs(current.left, current.right)
		t.policyPhase = 2
		return selftest.Pending
	case 2:
		current := t.policyCases()[t.policyIndex]
		if !t.loaded(current.left, current.right) {
			return t.expired("the source and destination listings")
		}
		if current.folder != "" {
			if err := t.selectFolder(current.folder); err != nil {
				return selftest.Failed(err)
			}
		} else if err := t.selectAll(); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.policyPhase = 3
		return selftest.Pending
	case 3:
		current := t.policyCases()[t.policyIndex]
		dialog := t.state.Dialog()
		if dialog == nil {
			return t.expired("the conflict prompt for " + string(current.action))
		}
		if dialog.Kind != transferstate.DialogConflict || dialog.Conflict == nil || dialog.TaskID == "" {
			return selftest.Failed(fmt.Errorf("the prompt is %+v", dialog))
		}
		if dialog.Conflict.Name == "" || dialog.Conflict.TargetPath == "" {
			return selftest.Failed(fmt.Errorf("the conflict prompt lost its destination: %+v", dialog.Conflict))
		}
		t.policyID = dialog.TaskID
		t.state.ResolveDialogConflict(current.action, false)
		t.policyPhase = 4
		return selftest.Pending
	case 4:
		current := t.policyCases()[t.policyIndex]
		task, ok := t.task(t.policyID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the %s Transfer disappeared", current.action))
		}
		if task.Status == domainsftp.TaskFailed {
			return selftest.Failed(fmt.Errorf("the %s Transfer ended %s: %v", current.action, task.Status, task.Err))
		}
		// The answer reaches the waiting worker asynchronously, so an
		// Attention row here may simply not have resumed yet. Wait for the
		// terminal row within the step deadline instead of sampling once.
		if !task.Terminal() {
			return t.expired("the " + string(current.action) + " Transfer")
		}
		if task.Status != current.status {
			return selftest.Failed(fmt.Errorf("%s Transfer status = %s, want %s", current.action, task.Status, current.status))
		}
		if task.Skipped != current.skipped {
			return selftest.Failed(fmt.Errorf("%s skipped %d entries, want %d", current.action, task.Skipped, current.skipped))
		}
		if err := current.verify(t); err != nil {
			return selftest.Failed(err)
		}
		t.policyIndex++
		if t.policyIndex == len(t.policyCases()) {
			return selftest.Passed(
				"stop cancelled the Transfer, skip left and counted the destination, replace wrote it, duplicate wrote \"(copy)\", and merge kept destination-only content",
				"transferstate.State + apptransfer.Queue.ResolveConflict")
		}
		t.policyPhase = 1
		t.deadline = time.Now().Add(selftest33Wait)
		return selftest.Pending
	}
	return selftest.Failed(fmt.Errorf("unexpected policy phase %d", t.policyPhase))
}

func (t *selftest33) batchStep(*ui.Context) selftest.Outcome {
	t.state.Flush()
	switch t.batchPhase {
	case 0:
		t.deadline = time.Now().Add(selftest33Wait)
		t.batchPhase = 1
		return selftest.Pending
	case 1:
		if !t.tabsReady() {
			return t.expired("the two local panes")
		}
		t.openDirs("batch-src", "batch-dst")
		t.batchPhase = 2
		return selftest.Pending
	case 2:
		if !t.loaded("batch-src", "batch-dst") {
			return t.expired("the batch listings")
		}
		if err := t.selectAll(); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.batchPhase = 3
		return selftest.Pending
	case 3:
		dialog := t.state.Dialog()
		if dialog == nil {
			return t.expired("the batch conflict prompt")
		}
		if dialog.Kind != transferstate.DialogConflict || dialog.Conflict == nil || dialog.TaskID == "" {
			return selftest.Failed(fmt.Errorf("the batch prompt is %+v", dialog))
		}
		if dialog.Conflict.ApplyToAllCount != 1 {
			return selftest.Failed(fmt.Errorf("apply-to-all count = %d, want 1", dialog.Conflict.ApplyToAllCount))
		}
		t.batchID = dialog.TaskID
		t.state.SetDialogApplyToAll(true)
		if dialog = t.state.Dialog(); dialog == nil || !dialog.ApplyToAll {
			return selftest.Failed(fmt.Errorf("the apply-to-all checkbox did not reach the prompt"))
		}
		t.state.ResolveDialogConflict(apptransfer.ConflictReplace, true)
		t.batchPhase = 4
		return selftest.Pending
	case 4:
		task, ok := t.task(t.batchID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the batch Transfer disappeared"))
		}
		if task.Status == domainsftp.TaskFailed {
			return selftest.Failed(fmt.Errorf("the batch ended failed: %v", task.Err))
		}
		// The answered worker resumes asynchronously; wait for it to finish
		// rather than reading the pre-answer Attention row as a failure.
		if !task.Terminal() {
			return t.expired("the batch to finish")
		}
		if task.Status != domainsftp.TaskCompleted {
			return selftest.Failed(fmt.Errorf("the batch ended %s", task.Status))
		}
		if t.state.Dialog() != nil {
			return selftest.Failed(fmt.Errorf("a second conflict prompt is still open"))
		}
		for name, want := range map[string]string{"alpha.txt": "new-alpha", "beta.txt": "new-beta"} {
			got, err := t.read(filepath.Join("batch-dst", name))
			if err != nil || got != want {
				return selftest.Failed(fmt.Errorf("%s = %q (%v), want %q", name, got, err, want))
			}
		}
		return selftest.Passed(
			"one Replace answer covered both files and no second prompt opened",
			"transferstate.State.ResolveDialogConflict -> apptransfer.Queue.ResolveConflict")
	}
	return selftest.Failed(fmt.Errorf("unexpected batch phase %d", t.batchPhase))
}

func (t *selftest33) duplicateStep(*ui.Context) selftest.Outcome {
	t.state.Flush()
	sourcePath := filepath.Join(t.workdir, "dupname-src", "same.txt")
	destDir := filepath.Join(t.workdir, "dupname-dst")
	switch t.duplicatePhase {
	case 0:
		t.deadline = time.Now().Add(selftest33Wait)
		t.duplicateGate = newSelftest32Gate(t.files, sourcePath, false)
		first, err := t.queue.Enqueue(apptransfer.TaskSpec{
			Kind: domainsftp.OpCopy, Source: t.duplicateGate, Dest: t.files,
			Sources: []string{sourcePath}, DestDir: destDir,
			Conflict: apptransfer.ConflictDuplicate, BatchID: "duplicate-first",
			SourceLabel: "local", DestLabel: "local",
		})
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the first duplicate: %v", err))
		}
		t.duplicateFirst = first
		t.duplicatePhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.duplicateFirst)
		if !ok {
			return selftest.Failed(fmt.Errorf("the first duplicate disappeared"))
		}
		if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
			return selftest.Failed(fmt.Errorf("the first duplicate ended %s: %v", task.Status, task.Err))
		}
		if task.Targets[sourcePath] != filepath.Join(destDir, "same (copy).txt") {
			return t.expired("the first duplicate to reserve \"same (copy).txt\"")
		}
		second, err := t.queue.Enqueue(apptransfer.TaskSpec{
			Kind: domainsftp.OpCopy, Source: t.duplicateGate, Dest: t.files,
			Sources: []string{sourcePath}, DestDir: destDir,
			Conflict: apptransfer.ConflictDuplicate, BatchID: "duplicate-second",
			SourceLabel: "local", DestLabel: "local",
		})
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the second duplicate: %v", err))
		}
		t.duplicateNext = second
		t.duplicatePhase = 2
		return selftest.Pending
	case 2:
		first, firstOK := t.task(t.duplicateFirst)
		second, secondOK := t.task(t.duplicateNext)
		if !firstOK || !secondOK {
			return selftest.Failed(fmt.Errorf("a duplicate Transfer disappeared"))
		}
		for _, task := range []domainsftp.Task{first, second} {
			if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
				return selftest.Failed(fmt.Errorf("a duplicate ended %s: %v", task.Status, task.Err))
			}
		}
		if first.Targets[sourcePath] != filepath.Join(destDir, "same (copy).txt") ||
			second.Targets[sourcePath] != filepath.Join(destDir, "same (copy 2).txt") {
			return t.expired("the two duplicates to reserve distinct names")
		}
		close(t.duplicateGate.release)
		t.duplicatePhase = 3
		return selftest.Pending
	case 3:
		first, firstOK := t.task(t.duplicateFirst)
		second, secondOK := t.task(t.duplicateNext)
		if !firstOK || !secondOK || !first.Terminal() || !second.Terminal() {
			return t.expired("both duplicate Transfers")
		}
		if first.Status != domainsftp.TaskCompleted || second.Status != domainsftp.TaskCompleted {
			return selftest.Failed(fmt.Errorf("duplicates ended %s/%s", first.Status, second.Status))
		}
		for _, name := range []string{"same (copy).txt", "same (copy 2).txt"} {
			got, err := t.read(filepath.Join("dupname-dst", name))
			if err != nil || got != "same-new" {
				return selftest.Failed(fmt.Errorf("%s = %q (%v)", name, got, err))
			}
		}
		// A folder Transfer claims its destination subtree while it runs. A
		// file aimed inside that subtree must be refused before it can write.
		tree := filepath.Join(t.workdir, "reserve-src", "tree")
		t.dupFolderGate = newSelftest32Gate(t.files, filepath.Join(tree, "inside.txt"), false)
		dirID, err := t.queue.Enqueue(apptransfer.TaskSpec{
			Kind: domainsftp.OpCopy, Source: t.dupFolderGate, Dest: t.files,
			Sources: []string{tree}, DestDir: filepath.Join(t.workdir, "reserve-dst"),
			Conflict: apptransfer.ConflictReplace, BatchID: "reserve-tree",
			SourceLabel: "local", DestLabel: "local",
		})
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the folder Transfer: %v", err))
		}
		t.dupFolderID = dirID
		t.duplicatePhase = 4
		return selftest.Pending
	case 4:
		tree := filepath.Join(t.workdir, "reserve-src", "tree")
		task, ok := t.task(t.dupFolderID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the folder Transfer disappeared"))
		}
		if task.Status == domainsftp.TaskFailed || task.Status == domainsftp.TaskCancelled {
			return selftest.Failed(fmt.Errorf("the folder Transfer ended %s: %v", task.Status, task.Err))
		}
		if task.Status != domainsftp.TaskTransferring {
			return t.expired("the folder Transfer to start")
		}
		// The target is recorded after the plan claims the destination, so it
		// is the observable proof that the subtree is reserved.
		if task.Targets[tree] != filepath.Join(t.workdir, "reserve-dst", "tree") {
			return t.expired("the folder Transfer to reserve its subtree")
		}
		child, err := t.queue.Enqueue(apptransfer.TaskSpec{
			Kind: domainsftp.OpCopy, Source: t.files, Dest: t.files,
			Sources:  []string{sourcePath},
			DestDir:  filepath.Join(t.workdir, "reserve-dst", "tree"),
			Conflict: apptransfer.ConflictReplace, BatchID: "reserve-child",
			SourceLabel: "local", DestLabel: "local",
		})
		if err == nil {
			_ = t.queue.Cancel(child)
			_ = t.queue.Cancel(t.dupFolderID)
			return selftest.Failed(fmt.Errorf(
				"a file under a live folder reservation was accepted"))
		}
		if apptransfer.KindOf(err) != apptransfer.KindReserved {
			return selftest.Failed(fmt.Errorf(
				"a file under a live folder reservation was refused as %q: %v",
				apptransfer.KindOf(err), err))
		}
		_ = t.queue.Cancel(t.dupFolderID)
		t.duplicatePhase = 5
		return selftest.Pending
	case 5:
		task, ok := t.task(t.dupFolderID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the folder Transfer disappeared"))
		}
		if !task.Terminal() {
			return t.expired("the cancelled folder Transfer")
		}
		return selftest.Passed(
			"two duplicates landed on \"(copy)\" and \"(copy 2)\"; a file under a live folder reservation was refused",
			"apptransfer.Queue.ReserveName + apptransfer.KindReserved")
	}
	return selftest.Failed(fmt.Errorf("unexpected duplicate phase %d", t.duplicatePhase))
}

func (t *selftest33) skipStep(*ui.Context) selftest.Outcome {
	t.state.Flush()
	switch t.skipPhase {
	case 0:
		t.deadline = time.Now().Add(selftest33Wait)
		t.skipPhase = 1
		return selftest.Pending
	case 1:
		if !t.tabsReady() {
			return t.expired("the two local panes")
		}
		t.openDirs("skipunchanged-src", "skipunchanged-dst")
		t.skipPhase = 2
		return selftest.Pending
	case 2:
		if !t.loaded("skipunchanged-src", "skipunchanged-dst") {
			return t.expired("the skip-unchanged listings")
		}
		if err := t.selectFolder("tree"); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.skipPhase = 3
		return selftest.Pending
	case 3:
		dialog := t.state.Dialog()
		if dialog == nil {
			return t.expired("the folder merge prompt")
		}
		if dialog.Kind != transferstate.DialogConflict || dialog.Conflict == nil || !dialog.Conflict.CanMerge() {
			return selftest.Failed(fmt.Errorf("the merge prompt is %+v", dialog))
		}
		t.skipID = dialog.TaskID
		t.state.ResolveDialogConflict(apptransfer.ConflictMerge, false)
		t.skipPhase = 4
		return selftest.Pending
	case 4:
		task, ok := t.task(t.skipID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the merged Transfer disappeared"))
		}
		if task.Status == domainsftp.TaskFailed {
			return selftest.Failed(fmt.Errorf("the merged Transfer ended %s: %v", task.Status, task.Err))
		}
		// The merge answer reaches the worker asynchronously; a row still in
		// Attention is in progress, not failed.
		if !task.Terminal() {
			return t.expired("the merged Transfer")
		}
		if task.Status != domainsftp.TaskCompleted {
			return selftest.Failed(fmt.Errorf("the merged Transfer ended %s", task.Status))
		}
		if task.Skipped != 1 {
			return selftest.Failed(fmt.Errorf("skipped %d entries, want the unchanged file", task.Skipped))
		}
		got, err := t.read("skipunchanged-dst/tree/keep.txt")
		if err != nil || got != "old!" {
			return selftest.Failed(fmt.Errorf("the unchanged destination was rewritten to %q (%v)", got, err))
		}
		// The operation bar mirrors the row through a posted snapshot, so let
		// it catch up within the step deadline instead of sampling once.
		if snapshot := t.state.Snapshot(); snapshot == nil || snapshot.Skipped != 1 {
			return t.expired("the operation bar to show the skipped entry")
		}
		if t.cleanup != nil {
			t.cleanup()
			t.cleanup = nil
		}
		t.finished = true
		return selftest.Passed(
			"a same-size file within the same whole second was left untouched despite a sub-second mtime difference",
			"domainsftp.UnchangedTransfer + transferstate.State")
	}
	return selftest.Failed(fmt.Errorf("unexpected skip phase %d", t.skipPhase))
}
