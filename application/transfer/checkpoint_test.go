package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

const checkpointWait = 5 * time.Second

// gatedFS blocks the first Open of one path until the gate closes or the
// request context ends, and records how often each source path was opened.
// The open count proves whether a resumed Transfer re-copied a completed file.
type gatedFS struct {
	*fakeFS
	gatePath string
	gate     chan struct{}
	entered  chan struct{}
	once     sync.Once
	// holdCancel, when non-nil, keeps a cancelled Open in flight until it
	// closes, so a latch test can observe the pausing state.
	holdCancel chan struct{}

	mu    sync.Mutex
	opens map[string]int
}

func newGatedFS(name, gatePath string) *gatedFS {
	return &gatedFS{
		fakeFS:   newFakeFS(name),
		gatePath: gatePath,
		gate:     make(chan struct{}),
		entered:  make(chan struct{}),
		opens:    map[string]int{},
	}
}

func (f *gatedFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opens[p]++
	blocked := p == f.gatePath
	f.mu.Unlock()
	if blocked {
		f.once.Do(func() { close(f.entered) })
		select {
		case <-f.gate:
		case <-ctx.Done():
			if f.holdCancel != nil {
				<-f.holdCancel
			}
			return nil, ctx.Err()
		}
	}
	return f.fakeFS.Open(ctx, p)
}

func (f *gatedFS) openCount(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[p]
}

// copyProgress captures the latest snapshot across the copy goroutine.
type copyProgress struct {
	mu   sync.Mutex
	last Progress
}

func (c *copyProgress) record(progress Progress) {
	c.mu.Lock()
	c.last = progress
	if progress.Checkpoint != nil {
		checkpoint := *progress.Checkpoint
		c.last.Checkpoint = &checkpoint
	}
	c.mu.Unlock()
}

func (c *copyProgress) snapshot() Progress {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// waitForProgress polls one observable condition with a hard deadline.
func waitForProgress(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(checkpointWait)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", checkpointWait, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestCopyRecordsDirectoryCheckpointAndResumeSkipsCompletedFiles pauses a
// directory copy in the middle, then resumes it with the saved checkpoint.
// Completed files must not be opened again, and progress must start from the
// saved file count rather than zero.
func TestCopyRecordsDirectoryCheckpointAndResumeSkipsCompletedFiles(t *testing.T) {
	source := newGatedFS("local", "/src/c.txt")
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		source.addFile("/src", name, "payload-"+name, 0o640)
	}
	dest := newFakeFS("remote")
	dest.addDir("/dest")

	service := NewService(nil, nil)
	progress := &copyProgress{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Copy(ctx, CopyRequest{
			Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
			Kind: domainsftp.OpUpload, Progress: progress.record,
		})
		done <- err
	}()

	select {
	case <-source.entered:
		// a.txt and b.txt completed before c.txt opened.
	case <-time.After(checkpointWait):
		t.Fatal("the copy never reached the gated file")
	}
	cancel()
	close(source.gate)
	select {
	case err := <-done:
		if KindOf(err) != KindCancelled {
			t.Fatalf("paused copy = %v, kind %q", err, KindOf(err))
		}
	case <-time.After(checkpointWait):
		t.Fatal("the paused copy did not settle")
	}

	paused := progress.snapshot()
	if paused.Checkpoint == nil || !paused.Checkpoint.Valid() {
		t.Fatalf("paused progress has no checkpoint: %+v", paused)
	}
	// The root directory plus a.txt and b.txt are the completed prefix.
	if paused.Checkpoint.CompletedEntries != 3 || paused.Checkpoint.CoveredEntries != 3 {
		t.Fatalf("paused checkpoint = %+v", paused.Checkpoint)
	}
	if paused.FilesDone != 3 || paused.FilesTotal != 5 {
		t.Fatalf("paused file counts = %d/%d", paused.FilesDone, paused.FilesTotal)
	}
	if _, exists := dest.file("/dest/src/c.txt"); exists {
		t.Fatal("the interrupted file carried its destination name")
	}

	resumeProgress := &copyProgress{}
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		Kind:         domainsftp.OpUpload,
		Checkpoint:   paused.Checkpoint,
		KnownTargets: map[string]string{"/src": "/dest/src"},
		Progress:     resumeProgress.record,
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got, ok := dest.file("/dest/src/" + name); !ok || string(got) != "payload-"+name {
			t.Errorf("%s = %q, exists=%v", name, got, ok)
		}
	}
	if got := source.openCount("/src/a.txt"); got != 1 {
		t.Errorf("a.txt was opened %d times; a completed file was re-copied", got)
	}
	if got := source.openCount("/src/b.txt"); got != 1 {
		t.Errorf("b.txt was opened %d times; a completed file was re-copied", got)
	}
	if got := source.openCount("/src/c.txt"); got != 2 {
		t.Errorf("c.txt opens = %d, want the interrupted attempt plus one resume", got)
	}
	if got := source.openCount("/src/d.txt"); got != 1 {
		t.Errorf("d.txt opens = %d, want 1", got)
	}
	final := resumeProgress.snapshot()
	if final.FilesDone != 5 || final.FilesTotal != 5 {
		t.Errorf("resumed progress = %d/%d", final.FilesDone, final.FilesTotal)
	}
	if final.Checkpoint == nil || final.Checkpoint.CompletedEntries != 5 {
		t.Errorf("resumed checkpoint = %+v", final.Checkpoint)
	}
	if final.CheckpointBytes != int64(len("payload-a.txt")+len("payload-b.txt")+
		len("payload-c.txt")+len("payload-d.txt")) {
		t.Errorf("checkpoint bytes = %d", final.CheckpointBytes)
	}
}

// TestCopyRestartsWhenACoveredEntryChanged keeps a stale checkpoint from
// skipping a file whose source metadata no longer matches.
func TestCopyRestartsWhenACoveredEntryChanged(t *testing.T) {
	source := newGatedFS("local", "/src/c.txt")
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		source.addFile("/src", name, "payload", 0o644)
	}
	dest := newFakeFS("remote")
	dest.addDir("/dest")
	service := NewService(nil, nil)
	progress := &copyProgress{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Copy(ctx, CopyRequest{
			Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
			Progress: progress.record,
		})
		done <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(checkpointWait):
		t.Fatal("the copy never reached the gated file")
	}
	cancel()
	close(source.gate)
	<-done
	paused := progress.snapshot()
	if paused.Checkpoint == nil || paused.Checkpoint.CompletedEntries != 3 {
		t.Fatalf("paused checkpoint = %+v", paused.Checkpoint)
	}
	// The completed b.txt changed after the checkpoint was written.
	source.mu.Lock()
	source.nodes["/src/b.txt"].data = []byte("changed payload")
	source.nodes["/src/b.txt"].modTime = time.Unix(1_700_000_000, 0)
	source.mu.Unlock()

	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		Checkpoint:   paused.Checkpoint,
		KnownTargets: map[string]string{"/src": "/dest/src"},
		Conflict:     ConflictReplace,
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := source.openCount("/src/a.txt"); got != 2 {
		t.Errorf("a.txt opens = %d, want a full restart", got)
	}
	if got, _ := dest.file("/dest/src/b.txt"); string(got) != "changed payload" {
		t.Errorf("changed file = %q", got)
	}
}

// TestCopyEnforcesTraversalLimits proves a hostile tree stops with TooLarge
// instead of growing the operation or its checkpoint.
func TestCopyEnforcesTraversalLimits(t *testing.T) {
	source := newFakeFS("local")
	source.addDir("/src")
	for index := 0; index < 4; index++ {
		source.addDir(fmt.Sprintf("/src/d%d", index))
	}
	dest := newFakeFS("remote")
	dest.addDir("/dest")
	service := NewService(nil, nil)
	_, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		budget: domainsftp.NewDirectoryTraversalBudget(2, 100),
	})
	if KindOf(err) != KindTooLarge || !errors.Is(err, domainsftp.ErrDirectoryTraversalDirectoryLimit) {
		t.Fatalf("directory limit error = %v, kind %q", err, KindOf(err))
	}
	_, err = service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		budget: domainsftp.NewDirectoryTraversalBudget(100, 2),
	})
	if KindOf(err) != KindTooLarge || !errors.Is(err, domainsftp.ErrDirectoryTraversalEntryLimit) {
		t.Fatalf("entry limit error = %v, kind %q", err, KindOf(err))
	}
	if dest.has("/dest/src") {
		t.Fatal("a refused plan wrote part of the tree")
	}
}

// chainFS presents a chain of symlinked directories deeper than the follow
// limit. Each level has a unique canonical path, so only the symlink depth
// can stop the walk.
type chainFS struct {
	*fakeFS
	levels int
}

func newChainFS(levels int) *chainFS {
	return &chainFS{fakeFS: newFakeFS("chain"), levels: levels}
}

func (f *chainFS) chainEntry(full string) domainsftp.Entry {
	return domainsftp.Entry{Name: path.Base(full), Path: full, Kind: domainsftp.KindFile, Size: 2, Mode: 0o644}
}

// chainTarget is the destination path after following n symlinked levels.
func chainTarget(level int) string {
	return "/dest/chain" + strings.Repeat("/next", level)
}

func (f *chainFS) nextEntry(level int) domainsftp.Entry {
	return domainsftp.Entry{
		Name: "next", Path: fmt.Sprintf("/chain/l%d", level),
		Kind: domainsftp.KindDir, Symlink: true,
		LinkTarget: fmt.Sprintf("/chain/l%d", level), Mode: 0o755,
	}
}

func (f *chainFS) List(_ context.Context, dir string) ([]domainsftp.Entry, error) {
	if dir == "/chain" {
		return []domainsftp.Entry{f.chainEntry("/chain/f0.txt"), f.nextEntry(1)}, nil
	}
	level, ok := parseChainLevel(dir)
	if !ok {
		return f.fakeFS.List(context.Background(), dir)
	}
	out := []domainsftp.Entry{f.chainEntry(path.Join(dir, "f.txt"))}
	if level < f.levels {
		out = append(out, f.nextEntry(level+1))
	}
	return out, nil
}

func (f *chainFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if strings.HasPrefix(p, "/chain") {
		return &fakeReader{data: []byte("ok"), ctx: ctx}, nil
	}
	return f.fakeFS.Open(ctx, p)
}

func parseChainLevel(dir string) (int, bool) {
	if !strings.HasPrefix(dir, "/chain/l") {
		return 0, false
	}
	level, err := strconv.Atoi(strings.TrimPrefix(dir, "/chain/l"))
	return level, err == nil
}

// TestCopyFollowsSymlinkDirectoriesToTheDepthLimit follows a download's
// symlinked directories but stops at 32 nested links.
func TestCopyFollowsSymlinkDirectoriesToTheDepthLimit(t *testing.T) {
	source := newChainFS(40)
	source.addDir("/chain")
	dest := newFakeFS("remote")
	dest.addDir("/dest")
	service := NewService(nil, nil)
	result, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/chain"}, DestDir: "/dest",
		Kind: domainsftp.OpDownload, FollowSymlinkDirs: true,
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !dest.has(chainTarget(1)+"/f.txt") || !dest.has(chainTarget(32)+"/f.txt") {
		t.Fatalf("the walk stopped before the depth limit: %d paths", len(result.Written))
	}
	if dest.has(chainTarget(33)) {
		t.Fatal("the walk followed a symlink beyond the 32-level limit")
	}
}

// TestCopyDoesNotFollowSymlinksWithoutThePolicy keeps uploads from expanding
// a symlinked tree.
func TestCopyDoesNotFollowSymlinksWithoutThePolicy(t *testing.T) {
	source := newChainFS(3)
	source.addDir("/chain")
	dest := newFakeFS("remote")
	dest.addDir("/dest")
	service := NewService(nil, nil)
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/chain"}, DestDir: "/dest",
		Kind: domainsftp.OpUpload,
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if dest.has("/dest/chain/next") {
		t.Fatal("an upload followed a symlinked directory")
	}
	if !dest.has("/dest/chain/f0.txt") {
		t.Fatal("the upload skipped an ordinary file")
	}
}

// cycleFS presents a symlinked directory that points back at its ancestor.
type cycleFS struct {
	*fakeFS
}

func (f *cycleFS) List(_ context.Context, dir string) ([]domainsftp.Entry, error) {
	if dir == "/chain" || strings.HasPrefix(dir, "/chain/loop") {
		return []domainsftp.Entry{
			{Name: "f.txt", Path: path.Join(dir, "f.txt"), Kind: domainsftp.KindFile, Size: 2, Mode: 0o644},
			{Name: "loop", Path: path.Join(dir, "loop"), Kind: domainsftp.KindDir,
				Symlink: true, LinkTarget: "/chain", Mode: 0o755},
		}, nil
	}
	return f.fakeFS.List(context.Background(), dir)
}

func (f *cycleFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if strings.HasPrefix(p, "/chain") {
		return &fakeReader{data: []byte("ok"), ctx: ctx}, nil
	}
	return f.fakeFS.Open(ctx, p)
}

// TestCopySkipsASymlinkDirectoryCycle proves cycle detection stops a loop
// before it consumes the traversal budget.
func TestCopySkipsASymlinkDirectoryCycle(t *testing.T) {
	source := &cycleFS{fakeFS: newFakeFS("cycle")}
	source.addDir("/chain")
	dest := newFakeFS("remote")
	dest.addDir("/dest")
	service := NewService(nil, nil)
	result, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/chain"}, DestDir: "/dest",
		Kind: domainsftp.OpDownload, FollowSymlinkDirs: true,
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if dest.has("/dest/chain/loop") {
		t.Fatal("the cyclic symlink was copied")
	}
	if len(result.Written) != 2 {
		t.Fatalf("Written = %v, want the directory and its file", result.Written)
	}
}

// TestCopyRecordsTheResolvedTargetRoot lets a restart keep duplicate naming
// stable instead of choosing a new "(copy N)" directory.
func TestCopyRecordsTheResolvedTargetRoot(t *testing.T) {
	source := newFakeFS("local")
	source.addDir("/src")
	source.addFile("/src", "a.txt", "a", 0o644)
	dest := newFakeFS("remote")
	dest.addDir("/dest")
	dest.addDir("/dest/src")
	service := NewService(nil, nil)
	var targets []string
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		Conflict: ConflictDuplicate,
		OnTarget: func(source, target string) { targets = append(targets, source+"="+target) },
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if len(targets) != 1 || targets[0] != "/src=/dest/src (copy 1)" {
		t.Fatalf("recorded targets = %v", targets)
	}
	if !dest.has("/dest/src (copy 1)/a.txt") {
		t.Fatal("the duplicate destination was not written")
	}
}

// TestCopyWritesChildDirectoriesBeforeFilesInNameOrder fixes the production
// traversal order a checkpoint prefix depends on: depth-first, child
// directories before files, each group by name.
func TestCopyWritesChildDirectoriesBeforeFilesInNameOrder(t *testing.T) {
	source := newFakeFS("local")
	source.addDir("/src")
	source.addDir("/src/zdir")
	source.addDir("/src/adir")
	source.addDir("/src/adir/inner")
	source.addFile("/src", "b.txt", "b", 0o644)
	source.addFile("/src", "a.txt", "a", 0o644)
	source.addFile("/src/adir", "y.txt", "y", 0o644)
	source.addFile("/src/adir/inner", "x.txt", "x", 0o644)
	source.addFile("/src/zdir", "z.txt", "z", 0o644)
	dest := newFakeFS("remote")
	dest.addDir("/dest")

	result, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	want := []string{
		"/dest/src",
		"/dest/src/adir",
		"/dest/src/adir/inner",
		"/dest/src/adir/inner/x.txt",
		"/dest/src/adir/y.txt",
		"/dest/src/zdir",
		"/dest/src/zdir/z.txt",
		"/dest/src/a.txt",
		"/dest/src/b.txt",
	}
	if len(result.Written) != len(want) {
		t.Fatalf("Written = %v, want %v", result.Written, want)
	}
	for index := range want {
		if result.Written[index] != want[index] {
			t.Fatalf("Written[%d] = %q, want %q (order %v)", index, result.Written[index], want[index], result.Written)
		}
	}
}

// backupPaths lists the replacement backups a fake destination currently
// holds.
func backupPaths(dest *fakeFS) []string {
	seen := map[string]bool{}
	for _, full := range dest.entriesAll() {
		index := strings.Index(full, ".netcatty-backup-")
		if index < 0 {
			continue
		}
		end := strings.Index(full[index:], ".bak")
		if end < 0 {
			continue
		}
		root := full[:index+end+len(".bak")]
		seen[root] = true
	}
	paths := make([]string, 0, len(seen))
	for root := range seen {
		paths = append(paths, root)
	}
	return paths
}

// replaceTree prepares an existing destination tree that a replacement must
// preserve until the new tree is complete.
func replaceTree(t *testing.T, dest *fakeFS) {
	t.Helper()
	dest.addDir("/dest")
	dest.addDir("/dest/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		dest.addFile("/dest/src", name, "old-"+name, 0o644)
	}
}

// TestCopyPauseDuringReplaceResumesIntoSameTree keeps the partial replacement
// tree and its backup when the queue pauses. The resumed request must finish
// the new tree, never leave the old prefix in place, and remove the backup.
func TestCopyPauseDuringReplaceResumesIntoSameTree(t *testing.T) {
	source := newGatedFS("local", "/src/c.txt")
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		source.addFile("/src", name, "payload-"+name, 0o644)
	}
	dest := newFakeFS("remote")
	replaceTree(t, dest)
	keep := func(error) bool { return true }

	progress := &copyProgress{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	service := NewService(nil, nil)
	go func() {
		_, err := service.Copy(ctx, CopyRequest{
			Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
			Kind: domainsftp.OpUpload, Conflict: ConflictReplace,
			Progress: progress.record, KeepReplacement: keep,
		})
		done <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(checkpointWait):
		t.Fatal("the copy never reached the gated file")
	}
	cancel()
	close(source.gate)
	select {
	case err := <-done:
		if KindOf(err) != KindCancelled {
			t.Fatalf("paused copy = %v, kind %q", err, KindOf(err))
		}
	case <-time.After(checkpointWait):
		t.Fatal("the paused copy did not settle")
	}

	// The new prefix is in place; the old tree waits in the backup.
	if got, _ := dest.file("/dest/src/a.txt"); string(got) != "payload-a.txt" {
		t.Fatalf("paused destination a.txt = %q, want the new file", got)
	}
	backups := backupPaths(dest)
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one retained tree", backups)
	}
	if got, _ := dest.file(backups[0] + "/a.txt"); string(got) != "old-a.txt" {
		t.Fatalf("backup a.txt = %q, want the original", got)
	}
	paused := progress.snapshot()
	if paused.Checkpoint == nil || paused.Checkpoint.CompletedEntries != 3 {
		t.Fatalf("paused checkpoint = %+v", paused.Checkpoint)
	}

	result, err := service.Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
		Kind: domainsftp.OpUpload, Conflict: ConflictReplace, KeepReplacement: keep,
		Checkpoint: paused.Checkpoint, KnownTargets: map[string]string{"/src": "/dest/src"},
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.ResumeReset {
		t.Fatal("a resumed replacement reported a reset")
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if got, ok := dest.file("/dest/src/" + name); !ok || string(got) != "payload-"+name {
			t.Errorf("%s = %q, exists=%v; the resumed replacement left old content", name, got, ok)
		}
	}
	if backups := backupPaths(dest); len(backups) != 0 {
		t.Fatalf("the completed replacement left backups: %v", backups)
	}
	if got := source.openCount("/src/a.txt"); got != 1 {
		t.Errorf("a.txt opens = %d; the resumed replacement re-copied the prefix", got)
	}
}

// TestCopyCancelDuringReplaceRestoresTheOriginal proves a hard cancel keeps
// the ticket-30 replacement contract and tells the caller to drop its
// checkpoint.
func TestCopyCancelDuringReplaceRestoresTheOriginal(t *testing.T) {
	source := newGatedFS("local", "/src/c.txt")
	source.addDir("/src")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		source.addFile("/src", name, "payload-"+name, 0o644)
	}
	dest := newFakeFS("remote")
	replaceTree(t, dest)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan CopyResult, 1)
	service := NewService(nil, nil)
	go func() {
		result, _ := service.Copy(ctx, CopyRequest{
			Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/dest",
			Kind: domainsftp.OpUpload, Conflict: ConflictReplace,
			KeepReplacement: func(error) bool { return false },
		})
		done <- result
	}()
	select {
	case <-source.entered:
	case <-time.After(checkpointWait):
		t.Fatal("the copy never reached the gated file")
	}
	cancel()
	close(source.gate)
	select {
	case result := <-done:
		if !result.ResumeReset {
			t.Fatal("a rolled-back replacement did not report ResumeReset")
		}
	case <-time.After(checkpointWait):
		t.Fatal("the cancelled copy did not settle")
	}
	if got, _ := dest.file("/dest/src/a.txt"); string(got) != "old-a.txt" {
		t.Fatalf("cancel left destination a.txt = %q, want the original", got)
	}
	for _, full := range dest.entriesAll() {
		if strings.Contains(full, ".netcatty-") {
			t.Errorf("a staging or backup file survived the cancel: %s", full)
		}
	}
}
