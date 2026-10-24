package transfer

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strings"
	"sync"
	"testing"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// shortSourceFS reports a bigger size than Open returns, so a staged copy is
// shorter than the source stat promised. A transport that loses bytes while
// reporting a successful close must not be able to replace the original.
type shortSourceFS struct {
	*fakeFS
	path  string
	extra int64
}

func (f *shortSourceFS) Stat(ctx context.Context, p string) (domainsftp.Entry, error) {
	entry, err := f.fakeFS.Stat(ctx, p)
	if err == nil && path.Clean(p) == f.path {
		entry.Size += f.extra
	}
	return entry, err
}

// promotionSnapshot records the tree at the instant a staging file was
// renamed onto its destination. It is the observable proof that the original
// was backed up before promotion instead of being overwritten in place.
type promotionSnapshot struct {
	stage      string
	stageThere bool
	destThere  bool
	backupName string
	backupData string
}

// recordingDestFS records staging stats, mode changes, and the state at every
// promotion. It delegates every other operation to the in-memory filesystem.
type recordingDestFS struct {
	*fakeFS
	mu sync.Mutex

	stageStats int
	promotions []promotionSnapshot
	chmods     map[string]uint32
	failChmod  bool
	// stageMode, when set, is the mode Stat reports for a staging file, so a
	// test can model a server that ignored the applied mode.
	stageMode uint32
}

func newRecordingDest(name string) *recordingDestFS {
	return &recordingDestFS{fakeFS: newFakeFS(name), chmods: map[string]uint32{}}
}

func (f *recordingDestFS) Stat(ctx context.Context, p string) (domainsftp.Entry, error) {
	entry, err := f.fakeFS.Stat(ctx, p)
	if err == nil && IsStagingName(path.Base(p)) {
		f.mu.Lock()
		f.stageStats++
		if f.stageMode != 0 {
			entry.Mode = f.stageMode
		}
		f.mu.Unlock()
	}
	return entry, err
}

func (f *recordingDestFS) Chmod(ctx context.Context, p string, mode uint32) error {
	f.mu.Lock()
	f.chmods[path.Clean(p)] = mode
	fail := f.failChmod && IsStagingName(path.Base(p))
	f.mu.Unlock()
	if fail {
		return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrPermission}
	}
	return f.fakeFS.Chmod(ctx, p, mode)
}

func (f *recordingDestFS) Rename(ctx context.Context, oldPath, newPath string) error {
	if IsStagingName(path.Base(oldPath)) {
		snapshot := promotionSnapshot{stage: oldPath}
		if _, err := f.fakeFS.Stat(ctx, oldPath); err == nil {
			snapshot.stageThere = true
		}
		if _, err := f.fakeFS.Stat(ctx, newPath); err == nil {
			snapshot.destThere = true
		}
		entries, _ := f.fakeFS.List(ctx, f.fakeFS.Parent(newPath))
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name, ".netcatty-backup-") {
				continue
			}
			snapshot.backupName = entry.Name
			if data, ok := f.fakeFS.file(f.fakeFS.Join(f.fakeFS.Parent(newPath), entry.Name)); ok {
				snapshot.backupData = string(data)
			}
		}
		f.mu.Lock()
		f.promotions = append(f.promotions, snapshot)
		f.mu.Unlock()
	}
	return f.fakeFS.Rename(ctx, oldPath, newPath)
}

func (f *recordingDestFS) snapshot() (int, []promotionSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stageStats, append([]promotionSnapshot(nil), f.promotions...)
}

func (f *recordingDestFS) modeOf(t *testing.T, p string) uint32 {
	t.Helper()
	entry, err := f.fakeFS.Stat(context.Background(), p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return entry.Mode
}

// appliedMode reports the mode the transaction asked the destination to put on
// one staging file.
func (f *recordingDestFS) appliedMode(p string) (uint32, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mode, ok := f.chmods[p]
	return mode, ok
}

// assertNoTransferArtifacts fails when a staging or backup file survived.
func assertNoTransferArtifacts(t *testing.T, files *fakeFS) {
	t.Helper()
	for _, full := range files.entriesAll() {
		base := path.Base(full)
		if IsStagingName(base) || strings.HasPrefix(base, ".netcatty-backup-") {
			t.Errorf("a transfer artifact survived: %s", full)
		}
	}
}

func replaceOne(t *testing.T, source, dest FileSystem, name string) error {
	t.Helper()
	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest,
		Sources: []string{"/" + name}, DestDir: "/",
		Conflict: ConflictReplace,
	})
	return err
}

// TestReplaceVerifiesTheStagedSizeBeforePromotion covers the size half of the
// ticket: a staged file whose bytes do not match the source it was planned
// from is never promoted, and the original stays exactly as it was.
func TestReplaceVerifiesTheStagedSizeBeforePromotion(t *testing.T) {
	source := &shortSourceFS{fakeFS: newFakeFS("source"), path: "/a.txt", extra: 8}
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newRecordingDest("dest")
	dest.addFile("/", "a.txt", "original", 0o600)

	err := replaceOne(t, source, dest, "a.txt")
	if err == nil {
		t.Fatal("a short staging file was promoted as success")
	}
	if KindOf(err) != KindInternal {
		t.Fatalf("verification failure kind = %q, want %q", KindOf(err), KindInternal)
	}
	if got, ok := dest.file("/a.txt"); !ok || string(got) != "original" {
		t.Fatalf("destination = %q, exists = %v; want the original", got, ok)
	}
	if dest.modeOf(t, "/a.txt") != 0o600 {
		t.Fatalf("the original mode changed to %04o", dest.modeOf(t, "/a.txt"))
	}
	if stageStats, promotions := dest.snapshot(); stageStats == 0 || len(promotions) != 0 {
		t.Fatalf("stage stats = %d, promotions = %d; want a verification read and no promotion",
			stageStats, len(promotions))
	}
	assertNoTransferArtifacts(t, dest.fakeFS)
}

// TestReplaceVerifiesTheStagedModeBeforePromotion covers the mode half: a
// replacement publishes the original destination's mode, checks that the
// staging file carries it, and only then promotes.
func TestReplaceVerifiesTheStagedModeBeforePromotion(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o600)
	dest := newRecordingDest("dest")
	dest.addFile("/", "a.txt", "original", 0o750)

	if err := replaceOne(t, source, dest, "a.txt"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, _ := dest.file("/a.txt"); string(got) != "new" {
		t.Fatalf("destination = %q", got)
	}
	if mode := dest.modeOf(t, "/a.txt"); mode != 0o750 {
		t.Fatalf("replacement mode = %04o, want the original 0750", mode)
	}
	stageStats, promotions := dest.snapshot()
	if stageStats == 0 {
		t.Fatal("the staged file was promoted without a verification read")
	}
	if len(promotions) != 1 {
		t.Fatalf("promotions = %d, want one", len(promotions))
	}
	wantStage := promotions[0].stage
	applied, ok := dest.appliedMode(wantStage)
	if !ok || applied != 0o750 {
		t.Fatalf("staged mode = %04o (applied=%v), want the original 0750", applied, ok)
	}
	assertNoTransferArtifacts(t, dest.fakeFS)
}

// TestReplaceFailsWhenTheStageModeCannotBeApplied proves a refused chmod fails
// the replacement rather than publishing a file with the wrong permissions.
func TestReplaceFailsWhenTheStageModeCannotBeApplied(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o600)
	dest := newRecordingDest("dest")
	dest.addFile("/", "a.txt", "original", 0o750)
	dest.failChmod = true

	err := replaceOne(t, source, dest, "a.txt")
	if err == nil {
		t.Fatal("a refused staged chmod was reported as success")
	}
	if KindOf(err) != KindPermission {
		t.Fatalf("chmod failure kind = %q, want %q", KindOf(err), KindPermission)
	}
	if got, ok := dest.file("/a.txt"); !ok || string(got) != "original" {
		t.Fatalf("destination = %q, exists = %v; want the original", got, ok)
	}
	if mode := dest.modeOf(t, "/a.txt"); mode != 0o750 {
		t.Fatalf("the original mode changed to %04o", mode)
	}
	assertNoTransferArtifacts(t, dest.fakeFS)
}

// TestReplaceRejectsAStageThatLostThePreservedMode covers the verification
// half of the mode rule: a server that accepted the chmod but reports a
// different mode back must not be allowed to publish the file, because the
// user's original permissions would silently change.
func TestReplaceRejectsAStageThatLostThePreservedMode(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o600)
	dest := newRecordingDest("dest")
	dest.addFile("/", "a.txt", "original", 0o750)
	dest.stageMode = 0o600

	err := replaceOne(t, source, dest, "a.txt")
	if err == nil {
		t.Fatal("a staged file with the wrong mode was promoted")
	}
	if KindOf(err) != KindInternal {
		t.Fatalf("mode verification failure kind = %q, want %q", KindOf(err), KindInternal)
	}
	if got, ok := dest.file("/a.txt"); !ok || string(got) != "original" {
		t.Fatalf("destination = %q, exists = %v; want the original", got, ok)
	}
	stageStats, promotions := dest.snapshot()
	if stageStats == 0 || len(promotions) != 0 {
		t.Fatalf("stage stats = %d, promotions = %d; want a verification read and no promotion",
			stageStats, len(promotions))
	}
	assertNoTransferArtifacts(t, dest.fakeFS)
}

// TestReplaceBacksUpTheOriginalBeforePromotion proves the old file is a
// sibling backup with its bytes intact at the moment the staging file takes
// the destination name, so a failed promotion always has something to restore.
func TestReplaceBacksUpTheOriginalBeforePromotion(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newRecordingDest("dest")
	dest.addFile("/", "a.txt", "original", 0o644)

	if err := replaceOne(t, source, dest, "a.txt"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	_, promotions := dest.snapshot()
	if len(promotions) != 1 {
		t.Fatalf("promotions = %d, want one", len(promotions))
	}
	promotion := promotions[0]
	if !promotion.stageThere {
		t.Fatal("the staging file was gone before its promotion")
	}
	if promotion.destThere {
		t.Fatal("the destination still held a file when the staging file was promoted")
	}
	if promotion.backupName == "" || promotion.backupData != "original" {
		t.Fatalf("backup at promotion = %q (%q), want the original bytes",
			promotion.backupName, promotion.backupData)
	}
	if got, _ := dest.file("/a.txt"); string(got) != "new" {
		t.Fatalf("destination = %q", got)
	}
	assertNoTransferArtifacts(t, dest.fakeFS)
}

// TestFailedDirectoryReplaceRestoresTheOriginalTree proves an ordinary failure
// part-way through replacing a folder puts every original file back with its
// bytes and mode, and leaves no partial tree or transfer artifact.
func TestFailedDirectoryReplaceRestoresTheOriginalTree(t *testing.T) {
	source := newQueueFailingFS("source", "/src/b.txt", KindPermission)
	source.addDir("/src")
	source.addFile("/src", "a.txt", "new-a", 0o600)
	source.addFile("/src", "b.txt", "new-b", 0o600)
	source.addFile("/src", "c.txt", "new-c", 0o600)

	dest := newFakeFS("dest")
	dest.addDir("/dest")
	dest.addDir("/dest/src")
	dest.addFile("/dest/src", "a.txt", "old-a", 0o644)
	dest.addFile("/dest/src", "b.txt", "old-b", 0o640)
	dest.addFile("/dest/src", "keep.txt", "old-keep", 0o755)

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		Conflict: ConflictReplace,
	})
	if err == nil {
		t.Fatal("the failed folder replacement was reported as success")
	}
	for name, want := range map[string]struct {
		data string
		mode uint32
	}{
		"a.txt":    {"old-a", 0o644},
		"b.txt":    {"old-b", 0o640},
		"keep.txt": {"old-keep", 0o755},
	} {
		got, ok := dest.file("/dest/src/" + name)
		if !ok || string(got) != want.data {
			t.Fatalf("%s = %q, exists = %v; want the original %q", name, got, ok, want.data)
		}
		entry, err := dest.Stat(context.Background(), "/dest/src/"+name)
		if err != nil || entry.Mode != want.mode {
			t.Fatalf("%s mode = %04o (%v), want %04o", name, entry.Mode, err, want.mode)
		}
	}
	if dest.has("/dest/src/c.txt") {
		t.Error("a new file from the failed replacement survived")
	}
	assertNoTransferArtifacts(t, dest)
}

// TestFailedDirectoryReplaceRestoresAKilledAttemptsBackup covers the ambiguity
// a directory leaves: an attempt that died before its checkpoint created the
// new root but wrote no original into it, so a backup beside a live root is
// still the only known-good copy. The next attempt must restore it, not delete
// it as stale, and a failure must leave the original tree intact.
func TestFailedDirectoryReplaceRestoresAKilledAttemptsBackup(t *testing.T) {
	source := newQueueFailingFS("source", "/src/b.txt", KindPermission)
	source.addDir("/src")
	source.addFile("/src", "a.txt", "new-a", 0o600)
	source.addFile("/src", "b.txt", "new-b", 0o600)

	dest := newFakeFS("dest")
	dest.addDir("/dest")
	dest.addDir("/dest/src")
	dest.addFile("/dest/src", "a.txt", "partial-a", 0o600)
	backup := "/dest/" + backupName(dest, "/dest/src")
	dest.addDir(backup)
	dest.addFile(backup, "a.txt", "old-a", 0o644)
	dest.addFile(backup, "keep.txt", "old-keep", 0o755)

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		Conflict: ConflictReplace,
	})
	if err == nil {
		t.Fatal("the failed folder replacement was reported as success")
	}
	for name, want := range map[string]struct {
		data string
		mode uint32
	}{
		"a.txt":    {"old-a", 0o644},
		"keep.txt": {"old-keep", 0o755},
	} {
		got, ok := dest.file("/dest/src/" + name)
		if !ok || string(got) != want.data {
			t.Fatalf("%s = %q, exists = %v; want the original %q", name, got, ok, want.data)
		}
		entry, err := dest.Stat(context.Background(), "/dest/src/"+name)
		if err != nil || entry.Mode != want.mode {
			t.Fatalf("%s mode = %04o (%v), want %04o", name, entry.Mode, err, want.mode)
		}
	}
	if dest.has("/dest/src/b.txt") {
		t.Error("a file from the failed attempt survived")
	}
	assertNoTransferArtifacts(t, dest)
}

// TestReplaceReconcilesAStaleBackupFromAKilledProcess proves a backup left by
// a process that died after moving the original aside is put back before the
// new transaction starts, so a fresh replace still has an original to protect
// and both artifacts are cleaned up.
func TestReplaceReconcilesAStaleBackupFromAKilledProcess(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newRecordingDest("dest")
	stale := dest.Join("/", backupName(dest, "/a.txt"))
	dest.addFile("/", path.Base(stale), "original", 0o640)

	if err := replaceOne(t, source, dest, "a.txt"); err != nil {
		t.Fatalf("replace over a killed process's backup: %v", err)
	}
	if got, ok := dest.file("/a.txt"); !ok || string(got) != "new" {
		t.Fatalf("destination = %q, exists = %v", got, ok)
	}
	_, promotions := dest.snapshot()
	if len(promotions) != 1 || promotions[0].backupData != "original" {
		t.Fatalf("promotion did not back up the recovered original: %+v", promotions)
	}
	assertNoTransferArtifacts(t, dest.fakeFS)
}

// TestFailedReplaceRestoresAStaleBackupTheKilledProcessLeft proves the backup
// is the only known-good copy when the destination is missing: a failure must
// put it back rather than leave the destination gone.
func TestFailedReplaceRestoresAStaleBackupTheKilledProcessLeft(t *testing.T) {
	source := newQueueFailingFS("source", "/a.txt", KindPermission)
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newFakeFS("dest")
	stale := dest.Join("/", backupName(dest, "/a.txt"))
	dest.addFile("/", path.Base(stale), "original", 0o640)

	err := replaceOne(t, source, dest, "a.txt")
	if err == nil {
		t.Fatal("the failed replace was reported as success")
	}
	if got, ok := dest.file("/a.txt"); !ok || string(got) != "original" {
		t.Fatalf("destination = %q, exists = %v; the only known-good copy was lost", got, ok)
	}
	assertNoTransferArtifacts(t, dest)
}

// TestQueueReplaceVerificationFailureLeavesTheOriginal drives the same
// guarantee through the queue: a staged file that fails verification settles
// the Transfer as failed and leaves the original byte-identical.
func TestQueueReplaceVerificationFailureLeavesTheOriginal(t *testing.T) {
	source := &shortSourceFS{fakeFS: newFakeFS("source"), path: "/a.txt", extra: 4}
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "original", 0o600)

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1), WithQueueRetryDelay(0))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/a.txt"}, DestDir: "/", Conflict: ConflictReplace,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	failed := waitQueueTask(t, queue, id, domainsftp.TaskFailed)
	if failed.Error != string(KindInternal) {
		t.Fatalf("failed row error = %q, want %q", failed.Error, KindInternal)
	}
	if got, ok := dest.file("/a.txt"); !ok || string(got) != "original" {
		t.Fatalf("destination = %q, exists = %v; want the original", got, ok)
	}
	entry, err := dest.Stat(context.Background(), "/a.txt")
	if err != nil || entry.Mode != 0o600 {
		t.Fatalf("the original mode = %04o (%v), want 0600", entry.Mode, err)
	}
	assertNoTransferArtifacts(t, dest)
}

// TestReplaceReportsAnUnremovableBackup keeps the original accessible when the
// post-promotion cleanup keeps failing instead of silently hiding the backup.
func TestReplaceReportsAnUnremovableBackup(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := &stuckBackupFS{fakeFS: newFakeFS("dest")}
	dest.addFile("/", "a.txt", "original", 0o644)

	err := replaceOne(t, source, dest, "a.txt")
	if err == nil {
		t.Fatal("a replacement whose backup could not be removed reported success")
	}
	if got, ok := dest.file("/a.txt"); !ok || string(got) != "new" {
		t.Fatalf("destination = %q, exists = %v; the promoted file must stay", got, ok)
	}
	backup := dest.Join("/", backupName(dest, "/a.txt"))
	if got, ok := dest.file(backup); !ok || string(got) != "original" {
		t.Fatalf("backup = %q, exists = %v; the original must stay recoverable", got, ok)
	}
}

// stuckBackupFS refuses to delete a backup, modelling a transport that drops
// the delete after the promotion was already published.
type stuckBackupFS struct {
	*fakeFS
}

func (f *stuckBackupFS) RemoveAll(ctx context.Context, p string) error {
	if strings.HasPrefix(path.Base(p), ".netcatty-backup-") {
		return errors.New("delete refused")
	}
	return f.fakeFS.RemoveAll(ctx, p)
}

var _ FileSystem = (*recordingDestFS)(nil)
var _ FileSystem = (*shortSourceFS)(nil)
var _ FileSystem = (*stuckBackupFS)(nil)
