package transfer

import (
	"context"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

func TestCopyUploadsATreeWithContentAndMode(t *testing.T) {
	local := newFakeFS("local")
	local.addDir("/src")
	local.addDir("/src/sub")
	local.addFile("/src", "a.txt", "alpha", 0o640)
	local.addFile("/src/sub", "b.txt", "beta", 0o600)
	remote := newFakeFS("remote")
	remote.addDir("/dest")

	service := NewService(nil, nil)
	result, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote,
		Sources: []string{"/src"},
		DestDir: "/dest",
		Kind:    domainsftp.OpUpload,
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got, ok := remote.file("/dest/src/a.txt"); !ok || string(got) != "alpha" {
		t.Fatalf("a.txt = %q, exists = %v", got, ok)
	}
	if got, ok := remote.file("/dest/src/sub/b.txt"); !ok || string(got) != "beta" {
		t.Fatalf("b.txt = %q, exists = %v", got, ok)
	}
	entry, err := remote.Stat(context.Background(), "/dest/src/a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry.Mode != 0o640 {
		t.Fatalf("mode = %04o, want 0640", entry.Mode)
	}
	if len(result.Written) != 4 {
		t.Fatalf("Written = %v", result.Written)
	}
	if result.Bytes != int64(len("alpha")+len("beta")) {
		t.Fatalf("Bytes = %d", result.Bytes)
	}
}

func TestCopyReportsProgressAndFinishesEveryFile(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a", "12345", 0o644)
	local.addFile("/", "b", "67890", 0o644)
	remote := newFakeFS("remote")

	var mu sync.Mutex
	var last Progress
	service := NewService(nil, nil)
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a", "/b"}, DestDir: "/",
		Progress: func(state Progress) {
			mu.Lock()
			last = state
			mu.Unlock()
		},
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if last.FilesTotal != 2 || last.FilesDone != 2 {
		t.Fatalf("progress = %+v", last)
	}
	if last.BytesDone != 10 || last.BytesTotal != 10 {
		t.Fatalf("bytes = %d/%d", last.BytesDone, last.BytesTotal)
	}
}

func TestCopyStopsOnAnExistingDestination(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "new", 0o644)
	remote := newFakeFS("remote")
	remote.addFile("/", "a.txt", "old", 0o644)

	service := NewService(nil, nil)
	_, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a.txt"}, DestDir: "/",
	})
	if KindOf(err) != KindExists {
		t.Fatalf("err = %v, kind = %q", err, KindOf(err))
	}
	if got, _ := remote.file("/a.txt"); string(got) != "old" {
		t.Fatalf("destination changed to %q", got)
	}
}

func TestCopyReplacesWhenAsked(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "new", 0o644)
	remote := newFakeFS("remote")
	remote.addFile("/", "a.txt", "old", 0o644)

	service := NewService(nil, nil)
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a.txt"}, DestDir: "/",
		Conflict: ConflictReplace,
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got, _ := remote.file("/a.txt"); string(got) != "new" {
		t.Fatalf("destination = %q", got)
	}
}

func TestCopySkipsWhenAsked(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "new", 0o644)
	remote := newFakeFS("remote")
	remote.addFile("/", "a.txt", "old", 0o644)

	service := NewService(nil, nil)
	result, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a.txt"}, DestDir: "/",
		Conflict: ConflictSkip,
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if len(result.Skipped) != 1 || len(result.Written) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got, _ := remote.file("/a.txt"); string(got) != "old" {
		t.Fatalf("destination = %q", got)
	}
}

func TestCopyDuplicatesWithCopyName(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "report.txt", "new", 0o644)
	remote := newFakeFS("remote")
	remote.addFile("/", "report.txt", "old", 0o644)
	remote.addFile("/", "report (copy 1).txt", "older", 0o644)

	service := NewService(nil, nil)
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/report.txt"}, DestDir: "/",
		Conflict: ConflictDuplicate,
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got, ok := remote.file("/report (copy 2).txt"); !ok || string(got) != "new" {
		t.Fatalf("duplicate = %q, exists = %v", got, ok)
	}
	if got, _ := remote.file("/report.txt"); string(got) != "old" {
		t.Fatalf("original changed to %q", got)
	}
}

func TestCopyCancelledBeforeStartWritesNothing(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "data", 0o644)
	remote := newFakeFS("remote")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service := NewService(nil, nil)
	_, err := service.Copy(ctx, CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a.txt"}, DestDir: "/",
	})
	if KindOf(err) != KindCancelled {
		t.Fatalf("err = %v, kind = %q", err, KindOf(err))
	}
	if remote.has("/a.txt") {
		t.Fatal("a cancelled copy wrote the destination")
	}
}

func TestCopyCancelledMidFileLeavesNoStagedFile(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "big.bin", strings.Repeat("x", 512*1024), 0o644)
	local.block = make(chan struct{})
	local.openGate = make(chan struct{}, 1)
	remote := newFakeFS("remote")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewService(nil, nil)
	done := make(chan error, 1)
	go func() {
		_, err := service.Copy(ctx, CopyRequest{
			Source: local, Dest: remote, Sources: []string{"/big.bin"}, DestDir: "/",
		})
		done <- err
	}()
	select {
	case <-local.openGate:
	case <-time.After(5 * time.Second):
		t.Fatal("the copy never opened the source")
	}
	cancel()
	close(local.block)
	select {
	case err := <-done:
		if KindOf(err) != KindCancelled {
			t.Fatalf("err = %v, kind = %q", err, KindOf(err))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the copy did not stop after cancellation")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	for full := range remote.nodes {
		if full == "/" {
			continue
		}
		t.Fatalf("cancelled copy left %q", full)
	}
}

func TestMoveRemovesTheSourceAfterCopying(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "data", 0o644)
	remote := newFakeFS("remote")

	service := NewService(nil, nil)
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a.txt"}, DestDir: "/", Move: true,
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if local.has("/a.txt") {
		t.Fatal("move left the source in place")
	}
	if !remote.has("/a.txt") {
		t.Fatal("move did not write the destination")
	}
}

func TestRemoteToRemoteCopiesDirectly(t *testing.T) {
	first := newFakeFS("first")
	first.addFile("/", "a.txt", "data", 0o644)
	second := newFakeFS("second")

	service := NewService(nil, nil)
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: first, Dest: second, Sources: []string{"/a.txt"}, DestDir: "/",
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got, _ := second.file("/a.txt"); string(got) != "data" {
		t.Fatalf("destination = %q", got)
	}
}

func TestDeleteRefusesANonEmptyDirectoryWithoutRecursion(t *testing.T) {
	files := newFakeFS("remote")
	files.addDir("/dir")
	files.addFile("/dir", "a.txt", "data", 0o644)

	service := NewService(nil, nil)
	err := service.Delete(context.Background(), files, []string{"/dir"}, false)
	if KindOf(err) != KindNotEmpty {
		t.Fatalf("err = %v, kind = %q", err, KindOf(err))
	}
	if !files.has("/dir/a.txt") {
		t.Fatal("the refused delete removed the tree")
	}
	if err := service.Delete(context.Background(), files, []string{"/dir"}, true); err != nil {
		t.Fatalf("recursive delete: %v", err)
	}
	if files.has("/dir") {
		t.Fatal("recursive delete left the directory")
	}
}

func TestRenameRefusesAnExistingDestination(t *testing.T) {
	files := newFakeFS("local")
	files.addFile("/", "a.txt", "a", 0o644)
	files.addFile("/", "b.txt", "b", 0o644)

	service := NewService(nil, nil)
	err := service.Rename(context.Background(), files, "/a.txt", "b.txt")
	if KindOf(err) != KindExists {
		t.Fatalf("err = %v, kind = %q", err, KindOf(err))
	}
	if err := service.Rename(context.Background(), files, "/a.txt", "c.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got, _ := files.file("/c.txt"); string(got) != "a" {
		t.Fatalf("renamed content = %q", got)
	}
}

func TestMkdirAndCreateFileValidateNames(t *testing.T) {
	files := newFakeFS("local")
	service := NewService(nil, nil)
	if _, err := service.Mkdir(context.Background(), files, "/", "bad/name"); KindOf(err) != KindInvalidName {
		t.Fatalf("Mkdir err = %v, kind = %q", err, KindOf(err))
	}
	path, err := service.Mkdir(context.Background(), files, "/", "docs")
	if err != nil || !files.has(path) {
		t.Fatalf("Mkdir = %q, %v", path, err)
	}
	filePath, err := service.CreateFile(context.Background(), files, path, "notes.txt")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if got, ok := files.file(filePath); !ok || len(got) != 0 {
		t.Fatalf("created file = %q, exists = %v", got, ok)
	}
}

func TestChmodRejectsBitsAboveTheContract(t *testing.T) {
	files := newFakeFS("local")
	files.addFile("/", "a.txt", "a", 0o644)
	service := NewService(nil, nil)
	if err := service.Chmod(context.Background(), files, "/a.txt", 0o17777); KindOf(err) != KindInvalidMode {
		t.Fatalf("err = %v, kind = %q", err, KindOf(err))
	}
	if err := service.Chmod(context.Background(), files, "/a.txt", 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	entry, _ := files.Stat(context.Background(), "/a.txt")
	if entry.Mode != 0o600 {
		t.Fatalf("mode = %04o", entry.Mode)
	}
}

func TestListPrependsTheParentEntryUnlessAtARoot(t *testing.T) {
	files := newFakeFS("remote")
	files.addDir("/dir")
	files.addFile("/dir", "a.txt", "data", 0o644)
	service := NewService(nil, nil)
	entries, err := service.List(context.Background(), files, "/dir")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != ".." || entries[0].Path != "/" {
		t.Fatalf("entries = %+v", entries)
	}
	root, err := service.List(context.Background(), files, "/")
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	for _, entry := range root {
		if entry.Name == ".." {
			t.Fatal("the root listing has a parent entry")
		}
	}
}

func TestOpenReportsUnavailablePanes(t *testing.T) {
	service := NewService(nil, nil)
	if service.LocalAvailable() || service.RemoteAvailable() {
		t.Fatal("an empty service claims a pane kind")
	}
	if _, err := service.OpenLocal(context.Background()); KindOf(err) != KindUnsupported {
		t.Fatalf("OpenLocal err = %v, kind = %q", err, KindOf(err))
	}
	if _, err := service.OpenRemote(context.Background(), "host", "box"); KindOf(err) != KindUnsupported {
		t.Fatalf("OpenRemote err = %v, kind = %q", err, KindOf(err))
	}
}

func TestOpenClosesTheTransportOnCopyFailure(t *testing.T) {
	// A failed create surfaces as a permission error, not an internal one.
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "data", 0o644)
	remote := newFakeFS("remote")
	remote.failCreate = true
	service := NewService(nil, nil)
	_, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a.txt"}, DestDir: "/denied",
	})
	if KindOf(err) != KindPermission {
		t.Fatalf("err = %v, kind = %q", err, KindOf(err))
	}
}

// failPromoteFS makes the final staging-to-destination rename fail while
// allowing the backup and restore renames, so a replacement can be observed
// half-way through.
type failPromoteFS struct {
	*fakeFS
	destination string
}

func (f failPromoteFS) Rename(ctx context.Context, oldPath, newPath string) error {
	if newPath == f.destination && strings.Contains(oldPath, ".netcatty-upload-") {
		return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrPermission}
	}
	return f.fakeFS.Rename(ctx, oldPath, newPath)
}

// entries lists every path in the fake tree, so a test can prove that a
// staging or backup file was removed.
func (f *fakeFS) entriesAll() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.nodes))
	for full := range f.nodes {
		out = append(out, full)
	}
	return out
}

func TestReplaceKeepsTheOriginalWhenPromotionFails(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "new", 0o644)
	remote := newFakeFS("remote")
	remote.addFile("/", "a.txt", "old", 0o644)
	dest := failPromoteFS{fakeFS: remote, destination: "/a.txt"}

	service := NewService(nil, nil)
	_, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: dest, Sources: []string{"/a.txt"}, DestDir: "/",
		Conflict: ConflictReplace,
	})
	if err == nil {
		t.Fatal("the failed promotion was reported as success")
	}
	if got, _ := remote.file("/a.txt"); string(got) != "old" {
		t.Fatalf("the original was lost: destination = %q", got)
	}
	for _, full := range remote.entriesAll() {
		if strings.Contains(full, ".netcatty-") {
			t.Errorf("a staging file survived: %s", full)
		}
	}
}

func TestReplaceRemovesTheBackupAfterSuccess(t *testing.T) {
	local := newFakeFS("local")
	local.addFile("/", "a.txt", "new", 0o644)
	remote := newFakeFS("remote")
	remote.addFile("/", "a.txt", "old", 0o644)

	service := NewService(nil, nil)
	if _, err := service.Copy(context.Background(), CopyRequest{
		Source: local, Dest: remote, Sources: []string{"/a.txt"}, DestDir: "/",
		Conflict: ConflictReplace,
	}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got, _ := remote.file("/a.txt"); string(got) != "new" {
		t.Fatalf("destination = %q", got)
	}
	for _, full := range remote.entriesAll() {
		if strings.Contains(full, ".netcatty-") {
			t.Errorf("a staging file survived: %s", full)
		}
	}
}

func TestStagingNamesFollowTheRestartContract(t *testing.T) {
	// A caller with no durable identity keeps the original random suffix.
	random := regexp.MustCompile(`^\.netcatty-upload-[0-9a-f]{8}-report\.txt\.part$`)
	if name := stageName("", "report.txt", "/dir/report.txt"); !random.MatchString(name) {
		t.Fatalf("stage name without a durable key = %q", name)
	}
	// A queued Transfer names its staging file from its durable Task ID and
	// target, so a resume in another process computes the same path.
	durable := regexp.MustCompile(`^\.netcatty-upload-[0-9a-f]{16}-report\.txt\.part$`)
	resumed := stageName("task-1", "report.txt", "/dir/report.txt")
	if !durable.MatchString(resumed) {
		t.Fatalf("stage name with a durable key = %q", resumed)
	}
	if again := stageName("task-1", "report.txt", "/dir/report.txt"); again != resumed {
		t.Fatalf("the same task and target produced %q then %q", resumed, again)
	}
	if otherTask := stageName("task-2", "report.txt", "/dir/report.txt"); otherTask == resumed {
		t.Fatalf("two tasks share the staging name %q", resumed)
	}
	if otherTarget := stageName("task-1", "report.txt", "/elsewhere/report.txt"); otherTarget == resumed {
		t.Fatalf("two targets share the staging name %q", resumed)
	}
	backup := regexp.MustCompile(`^\.netcatty-backup-[0-9a-f]{16}-report\.txt\.bak$`)
	if name := backupName(newFakeFS("dest"), "/dir/report.txt"); !backup.MatchString(name) {
		t.Fatalf("backup name = %q", name)
	}
}

func TestGlobalBookmarksRoundTripThroughTheRepository(t *testing.T) {
	repository := &fakeBookmarks{}
	service := NewService(nil, nil, WithBookmarks(repository))
	set, err := service.GlobalBookmarks(context.Background())
	if err != nil {
		t.Fatalf("GlobalBookmarks: %v", err)
	}
	set.Add(domainsftp.Bookmark{Label: "Home", Path: "/home"})
	if err := service.SaveGlobalBookmarks(context.Background(), set); err != nil {
		t.Fatalf("SaveGlobalBookmarks: %v", err)
	}
	reloaded, err := service.GlobalBookmarks(context.Background())
	if err != nil {
		t.Fatalf("GlobalBookmarks: %v", err)
	}
	if !reloaded.Contains(domainsftp.Bookmark{Path: "/home"}) {
		t.Fatal("the bookmark was not reloaded")
	}
}

type fakeBookmarks struct {
	items []domainsftp.Bookmark
	local []domainsftp.Bookmark
}

func (f *fakeBookmarks) LoadBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	return append([]domainsftp.Bookmark(nil), f.items...), nil
}

func (f *fakeBookmarks) SaveBookmarks(_ context.Context, items []domainsftp.Bookmark) error {
	f.items = append([]domainsftp.Bookmark(nil), items...)
	return nil
}

func (f *fakeBookmarks) LoadLocalBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	return append([]domainsftp.Bookmark(nil), f.local...), nil
}

func (f *fakeBookmarks) SaveLocalBookmarks(_ context.Context, items []domainsftp.Bookmark) error {
	f.local = append([]domainsftp.Bookmark(nil), items...)
	return nil
}
