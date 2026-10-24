//go:build selftest

package app

// Ticket 34 self-test. Press F11, then 3, then 4. The check proves the
// transactional replacement contract through the real global Transfer queue,
// the real copy service, and the real local filesystem adapter:
//
//   - A replace stages a sibling temporary file, reads its size and mode back,
//     moves the original to its backup, and promotes the stage atomically. The
//     recording destination proves the backup held the original bytes while
//     the destination was absent at the promotion.
//   - A staged file whose reported size does not match the source is never
//     promoted; the Transfer fails and the original is byte-identical.
//   - A source that fails after the backup was taken restores the original.
//   - A folder replacement that fails part-way removes the partial new tree
//     and puts every original file back.
//
// Every wait polls a real Transfer row with the harness deadline; there is no
// fixed sleep. Temporary and backup artifacts are checked on disk after every
// step.
//
// Result: %TEMP%\natcatty-selftest-34.json.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/selftest"
)

const selftest34Ticket = 34

const selftest34Wait = 30 * time.Second

type selftest34 struct {
	workdir  string
	files    apptransfer.FileSystem
	queue    *apptransfer.Queue
	cleanup  func()
	deadline time.Time

	successPhase    int
	successID       domainsftp.TaskID
	successMode     uint32
	successRecorder *selftest34Recorder

	verifyPhase int
	verifyID    domainsftp.TaskID

	sourceFailurePhase int
	sourceFailureID    domainsftp.TaskID
	sourceFailureMode  uint32

	dirPhase int
	dirID    domainsftp.TaskID

	finished bool
}

func (t *selftest34) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest34Ticket,
		Name:   "Transactional overwrite",
		Steps: []selftest.Step{
			{Name: "replace-stages-verifies-backs-up-and-promotes", Run: t.successStep},
			{Name: "verification-failure-leaves-the-original", Run: t.verifyStep},
			{Name: "failed-transfer-leaves-the-original", Run: t.sourceFailureStep},
			{Name: "failed-folder-replace-restores-the-tree", Run: t.directoryStep},
		},
	}
}

func (t *selftest34) setup() error {
	workdir, err := os.MkdirTemp("", "natcatty-selftest-34-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	for _, dir := range []string{
		"success-src", "success-dst",
		"verify-src", "verify-dst",
		"fail-src", "fail-dst",
		"tree-src/tree", "tree-dst/tree",
	} {
		if err := os.MkdirAll(filepath.Join(workdir, dir), 0o755); err != nil {
			return err
		}
	}
	files := map[string]string{
		"success-src/note.txt":   "new-note",
		"success-dst/note.txt":   "old-note",
		"verify-src/note.txt":    "new-verify",
		"verify-dst/note.txt":    "original-verify",
		"fail-src/note.txt":      "new-fail",
		"fail-dst/note.txt":      "original-fail",
		"tree-src/tree/a.txt":    "new-a",
		"tree-src/tree/b.txt":    "new-b",
		"tree-dst/tree/a.txt":    "old-a",
		"tree-dst/tree/keep.txt": "old-keep",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(data), 0o644); err != nil {
			return err
		}
	}

	local := adaptersftp.NewLocal()
	handle, err := local.OpenLocal(context.Background())
	if err != nil {
		return err
	}
	t.files = handle
	t.queue = apptransfer.NewQueue(apptransfer.NewService(nil, nil),
		apptransfer.WithQueueConcurrency(1),
		apptransfer.WithQueueRetryDelay(0),
	)
	if err := t.queue.Start(context.Background()); err != nil {
		_ = handle.Close()
		return err
	}
	t.cleanup = func() {
		if t.queue != nil {
			t.queue.Shutdown()
		}
		if t.files != nil {
			_ = t.files.Close()
		}
		_ = os.RemoveAll(t.workdir)
	}
	return nil
}

func (t *selftest34) expired(what string) selftest.Outcome {
	if time.Now().After(t.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not settle within %s", what, selftest34Wait))
	}
	return selftest.Pending
}

func (t *selftest34) path(parts ...string) string {
	return filepath.Join(append([]string{t.workdir}, parts...)...)
}

func (t *selftest34) read(parts ...string) (string, error) {
	data, err := os.ReadFile(t.path(parts...))
	return string(data), err
}

// mode reads a file's permission bits through the real adapter.
func (t *selftest34) mode(name string) (uint32, error) {
	entry, err := t.files.Stat(context.Background(), t.path(name))
	return entry.Mode, err
}

// assertNoArtifacts fails when the tree under dir still holds a staging or
// backup file from the transaction. Folder replacement stages each file beside
// its destination, so the scan is recursive.
func (t *selftest34) assertNoArtifacts(dir string) error {
	return filepath.WalkDir(t.path(dir), func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if apptransfer.IsStagingName(entry.Name()) ||
			strings.HasPrefix(entry.Name(), ".netcatty-backup-") {
			return fmt.Errorf("a transfer artifact survived in %s: %s", dir, entry.Name())
		}
		return nil
	})
}

func (t *selftest34) task(id domainsftp.TaskID) (domainsftp.Task, bool) {
	return t.queue.Task(id)
}

// enqueueReplace queues one local copy with the Replace policy. Source and
// destination may be wrappers over the real adapter, so fault injection stays
// at the filesystem seam the production service uses.
func (t *selftest34) enqueueReplace(source, dest apptransfer.FileSystem,
	sources []string, destDir, batch string) (domainsftp.TaskID, error) {
	return t.queue.Enqueue(apptransfer.TaskSpec{
		Kind: domainsftp.OpCopy, Source: source, Dest: dest,
		Sources: sources, DestDir: destDir,
		Conflict: apptransfer.ConflictReplace, BatchID: batch,
		SourceLabel: "local", DestLabel: "local",
	})
}

func (t *selftest34) successStep(*ui.Context) selftest.Outcome {
	if t.finished {
		*t = selftest34{}
	}
	switch t.successPhase {
	case 0:
		if err := t.setup(); err != nil {
			return selftest.Failed(fmt.Errorf("the replacement fixtures were not created: %v", err))
		}
		mode, err := t.mode("success-dst/note.txt")
		if err != nil {
			return selftest.Failed(fmt.Errorf("stat the original: %v", err))
		}
		t.successMode = mode
		recorder := &selftest34Recorder{FileSystem: t.files}
		t.successRecorder = recorder
		id, err := t.enqueueReplace(t.files, recorder,
			[]string{t.path("success-src/note.txt")}, t.path("success-dst"), "t34-success")
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the replacement: %v", err))
		}
		t.successID = id
		t.successPhase = 1
		t.deadline = time.Now().Add(selftest34Wait)
		return selftest.Pending
	case 1:
		task, ok := t.task(t.successID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the replacement Transfer disappeared"))
		}
		if task.Status == domainsftp.TaskFailed {
			return selftest.Failed(fmt.Errorf("the replacement failed: %v", task.Err))
		}
		if !task.Terminal() {
			return t.expired("the replacement Transfer")
		}
		if task.Status != domainsftp.TaskCompleted {
			return selftest.Failed(fmt.Errorf("the replacement ended %s", task.Status))
		}
		if got, err := t.read("success-dst/note.txt"); err != nil || got != "new-note" {
			return selftest.Failed(fmt.Errorf("destination = %q (%v)", got, err))
		}
		mode, err := t.mode("success-dst/note.txt")
		if err != nil || mode != t.successMode {
			return selftest.Failed(fmt.Errorf("replacement mode = %04o (%v), want the original %04o",
				mode, err, t.successMode))
		}
		if err := t.assertNoArtifacts("success-dst"); err != nil {
			return selftest.Failed(err)
		}
		stageStats, promotions := t.successRecorder.result()
		if stageStats == 0 {
			return selftest.Failed(fmt.Errorf("the staged file was never read back for verification"))
		}
		if len(promotions) != 1 {
			return selftest.Failed(fmt.Errorf("promotions = %d, want one", len(promotions)))
		}
		promotion := promotions[0]
		if !promotion.StageExists {
			return selftest.Failed(fmt.Errorf("the stage was gone before its promotion"))
		}
		if promotion.DestExists {
			return selftest.Failed(fmt.Errorf("the destination still existed at the promotion"))
		}
		if promotion.BackupName == "" || promotion.BackupData != "old-note" {
			return selftest.Failed(fmt.Errorf("backup at promotion = %q (%q), want the original",
				promotion.BackupName, promotion.BackupData))
		}
		return selftest.Passed(
			"the stage was read back for size and mode, the original was a backup at promotion, and no artifact survived",
			"apptransfer.Queue + apptransfer.Service.Copy -> adapters/sftp.Local")
	}
	return selftest.Failed(fmt.Errorf("unexpected success phase %d", t.successPhase))
}

func (t *selftest34) verifyStep(*ui.Context) selftest.Outcome {
	switch t.verifyPhase {
	case 0:
		t.deadline = time.Now().Add(selftest34Wait)
		dest := &selftest34ShortStageStat{FileSystem: t.files}
		id, err := t.enqueueReplace(t.files, dest,
			[]string{t.path("verify-src/note.txt")}, t.path("verify-dst"), "t34-verify")
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the verified replacement: %v", err))
		}
		t.verifyID = id
		t.verifyPhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.verifyID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the verified replacement disappeared"))
		}
		if !task.Terminal() {
			return t.expired("the verification failure")
		}
		if task.Status != domainsftp.TaskFailed {
			return selftest.Failed(fmt.Errorf("a short staged file ended %s, want failed", task.Status))
		}
		if task.Error != string(apptransfer.KindInternal) {
			return selftest.Failed(fmt.Errorf("verification error = %q, want %q",
				task.Error, apptransfer.KindInternal))
		}
		if got, err := t.read("verify-dst/note.txt"); err != nil || got != "original-verify" {
			return selftest.Failed(fmt.Errorf("the original was not restored: %q (%v)", got, err))
		}
		if err := t.assertNoArtifacts("verify-dst"); err != nil {
			return selftest.Failed(err)
		}
		return selftest.Passed(
			"a staged file that failed the size check was never promoted; the original is byte-identical",
			"apptransfer.Service.Copy -> verifyStaged (staging Stat)")
	}
	return selftest.Failed(fmt.Errorf("unexpected verify phase %d", t.verifyPhase))
}

func (t *selftest34) sourceFailureStep(*ui.Context) selftest.Outcome {
	switch t.sourceFailurePhase {
	case 0:
		t.deadline = time.Now().Add(selftest34Wait)
		mode, err := t.mode("fail-dst/note.txt")
		if err != nil {
			return selftest.Failed(fmt.Errorf("stat the original: %v", err))
		}
		t.sourceFailureMode = mode
		source := selftest34FailSource{FileSystem: t.files, path: t.path("fail-src/note.txt")}
		id, err := t.enqueueReplace(source, t.files,
			[]string{t.path("fail-src/note.txt")}, t.path("fail-dst"), "t34-source")
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the failing replacement: %v", err))
		}
		t.sourceFailureID = id
		t.sourceFailurePhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.sourceFailureID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the failing replacement disappeared"))
		}
		if !task.Terminal() {
			return t.expired("the source failure")
		}
		if task.Status != domainsftp.TaskFailed {
			return selftest.Failed(fmt.Errorf("the failed source ended %s, want failed", task.Status))
		}
		if got, err := t.read("fail-dst/note.txt"); err != nil || got != "original-fail" {
			return selftest.Failed(fmt.Errorf("the original was not restored: %q (%v)", got, err))
		}
		mode, err := t.mode("fail-dst/note.txt")
		if err != nil || mode != t.sourceFailureMode {
			return selftest.Failed(fmt.Errorf("the original mode = %04o (%v), want %04o",
				mode, err, t.sourceFailureMode))
		}
		if err := t.assertNoArtifacts("fail-dst"); err != nil {
			return selftest.Failed(err)
		}
		return selftest.Passed(
			"a source failure after the backup restored the original byte-for-byte and removed both artifacts",
			"apptransfer.Queue.KeepReplacement + apptransfer.restoreBackup")
	}
	return selftest.Failed(fmt.Errorf("unexpected source-failure phase %d", t.sourceFailurePhase))
}

func (t *selftest34) directoryStep(*ui.Context) selftest.Outcome {
	switch t.dirPhase {
	case 0:
		t.deadline = time.Now().Add(selftest34Wait)
		source := selftest34FailSource{FileSystem: t.files, path: t.path("tree-src/tree/b.txt")}
		id, err := t.enqueueReplace(source, t.files,
			[]string{t.path("tree-src/tree")}, t.path("tree-dst"), "t34-tree")
		if err != nil {
			return selftest.Failed(fmt.Errorf("enqueue the folder replacement: %v", err))
		}
		t.dirID = id
		t.dirPhase = 1
		return selftest.Pending
	case 1:
		task, ok := t.task(t.dirID)
		if !ok {
			return selftest.Failed(fmt.Errorf("the folder replacement disappeared"))
		}
		if !task.Terminal() {
			return t.expired("the folder replacement failure")
		}
		if task.Status != domainsftp.TaskFailed {
			return selftest.Failed(fmt.Errorf("the failed folder replacement ended %s, want failed", task.Status))
		}
		for name, want := range map[string]string{"a.txt": "old-a", "keep.txt": "old-keep"} {
			got, err := t.read("tree-dst/tree", name)
			if err != nil || got != want {
				return selftest.Failed(fmt.Errorf("%s = %q (%v), want the original %q", name, got, err, want))
			}
		}
		if _, err := os.Stat(t.path("tree-dst/tree/b.txt")); err == nil {
			return selftest.Failed(fmt.Errorf("a file from the failed replacement survived"))
		}
		if err := t.assertNoArtifacts("tree-dst"); err != nil {
			return selftest.Failed(err)
		}
		if t.cleanup != nil {
			t.cleanup()
			t.cleanup = nil
		}
		t.finished = true
		return selftest.Passed(
			"a folder replacement that failed part-way removed the partial tree and restored every original file",
			"apptransfer.Service.Copy abort -> restoreBackup")
	}
	return selftest.Failed(fmt.Errorf("unexpected directory phase %d", t.dirPhase))
}

// selftest34Promotion is the tree observed at the instant a staging file took
// its destination name.
type selftest34Promotion struct {
	StageExists bool
	DestExists  bool
	BackupName  string
	BackupData  string
}

// selftest34Recorder counts verification reads of staging files and records
// what the destination directory held at every promotion.
type selftest34Recorder struct {
	apptransfer.FileSystem
	mu         sync.Mutex
	stageStats int
	promotions []selftest34Promotion
}

func (r *selftest34Recorder) Stat(ctx context.Context, p string) (domainsftp.Entry, error) {
	if apptransfer.IsStagingName(r.Base(p)) {
		r.mu.Lock()
		r.stageStats++
		r.mu.Unlock()
	}
	return r.FileSystem.Stat(ctx, p)
}

func (r *selftest34Recorder) Rename(ctx context.Context, oldPath, newPath string) error {
	if apptransfer.IsStagingName(r.Base(oldPath)) {
		promotion := selftest34Promotion{}
		if _, err := r.FileSystem.Stat(ctx, oldPath); err == nil {
			promotion.StageExists = true
		}
		if _, err := r.FileSystem.Stat(ctx, newPath); err == nil {
			promotion.DestExists = true
		}
		parent := r.Parent(newPath)
		if entries, err := r.FileSystem.List(ctx, parent); err == nil {
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name, ".netcatty-backup-") {
					continue
				}
				promotion.BackupName = entry.Name
				if data, err := selftest34Read(r.FileSystem,
					r.Join(parent, entry.Name)); err == nil {
					promotion.BackupData = data
				}
			}
		}
		r.mu.Lock()
		r.promotions = append(r.promotions, promotion)
		r.mu.Unlock()
	}
	return r.FileSystem.Rename(ctx, oldPath, newPath)
}

func (r *selftest34Recorder) result() (int, []selftest34Promotion) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stageStats, append([]selftest34Promotion(nil), r.promotions...)
}

// selftest34ShortStageStat reports one byte less for a staging file, modelling
// a transport that closed successfully after dropping bytes.
type selftest34ShortStageStat struct {
	apptransfer.FileSystem
}

func (s selftest34ShortStageStat) Stat(ctx context.Context, p string) (domainsftp.Entry, error) {
	entry, err := s.FileSystem.Stat(ctx, p)
	if err == nil && apptransfer.IsStagingName(s.Base(p)) && entry.Size > 0 {
		entry.Size--
	}
	return entry, err
}

// selftest34FailSource refuses one source file after the replacement backup
// was already taken, so the failure exercises the restore path.
type selftest34FailSource struct {
	apptransfer.FileSystem
	path string
}

func (f selftest34FailSource) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if filepath.Clean(p) == filepath.Clean(f.path) {
		return nil, &apptransfer.Error{
			Kind:  apptransfer.KindPermission,
			Op:    domainsftp.OpCopy,
			Path:  p,
			Cause: errors.New("refused by the self-test source"),
		}
	}
	return f.FileSystem.Open(ctx, p)
}

func selftest34Read(files apptransfer.FileSystem, path string) (string, error) {
	reader, err := files.Open(context.Background(), path)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	return string(data), err
}
