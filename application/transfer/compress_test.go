package transfer

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// compressedRequest is one folder upload with the default stop policy.
func compressedRequest(source, dest FileSystem) CopyRequest {
	return CopyRequest{
		Source:     source,
		Dest:       dest,
		Sources:    []string{"/docs"},
		DestDir:    "/home",
		Kind:       domainsftp.OpUpload,
		Conflict:   ConflictStop,
		Compressed: true,
	}
}

func TestCompressedCopyUploadsOneArchive(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	source.addFile("/docs/sub", "data.bin", "0123456789", 0o600)
	source.addDir("/docs/empty")
	dest := newFakeFS("dest")
	var reports []Progress

	result, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/docs"}, DestDir: "/home",
		Kind: domainsftp.OpUpload, Conflict: ConflictStop, Compressed: true,
		Progress: func(progress Progress) { reports = append(reports, progress) },
	})
	if err != nil {
		t.Fatalf("compressed copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 1 {
		t.Fatalf("archive extractions = %d, want 1", calls)
	}
	if created := dest.created(); created != 0 {
		t.Fatalf("per-file creates = %d, want 0: the compressed path must not copy file by file", created)
	}
	for _, check := range []struct{ path, want string }{
		{"/home/docs/readme.txt", "hello"},
		{"/home/docs/sub/data.bin", "0123456789"},
	} {
		if data, ok := dest.file(check.path); !ok || string(data) != check.want {
			t.Fatalf("%s = %q, %v; want %q", check.path, data, ok, check.want)
		}
	}
	if info, err := dest.Stat(context.Background(), "/home/docs/empty"); err != nil || !info.IsDir() {
		t.Fatalf("empty directory not extracted: %v %+v", err, info)
	}
	if result.Bytes != 15 {
		t.Fatalf("bytes = %d, want 15", result.Bytes)
	}
	wantWritten := []string{
		"/home/docs", "/home/docs/empty", "/home/docs/readme.txt",
		"/home/docs/sub", "/home/docs/sub/data.bin",
	}
	for _, want := range wantWritten {
		found := false
		for _, written := range result.Written {
			if written == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Written %v does not contain %s", result.Written, want)
		}
	}
	if len(reports) == 0 {
		t.Fatal("no progress reports")
	}
	final := reports[len(reports)-1]
	if !final.Done() || final.FilesDone != 5 || final.BytesDone != 15 {
		t.Fatalf("final progress = %+v, want 5 files and 15 bytes", final)
	}
	// The staging directory must not survive the promotion.
	for _, entry := range dest.entries("/home") {
		if entry.Name != "docs" {
			t.Fatalf("leftover entry %q in the destination", entry.Name)
		}
	}
}

func TestCompressedCopyFallsBackWhenExtractionFails(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	source.addFile("/docs/sub", "data.bin", "0123456789", 0o600)
	dest := newFakeFS("dest")
	dest.extractFail = true

	result, err := NewService(nil, nil).Copy(context.Background(), compressedRequest(source, dest))
	if err != nil {
		t.Fatalf("fallback copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 1 {
		t.Fatalf("archive extractions = %d, want 1 before falling back", calls)
	}
	if created := dest.created(); created != 2 {
		t.Fatalf("per-file creates = %d, want 2: the fallback must copy every file", created)
	}
	if dest.has("/home/docs/partial.tmp") {
		t.Fatal("the failed extraction's partial file survived the fallback")
	}
	for _, entry := range dest.entries("/home") {
		if entry.Name != "docs" {
			t.Fatalf("leftover staging entry %q after fallback", entry.Name)
		}
	}
	if data, ok := dest.file("/home/docs/sub/data.bin"); !ok || string(data) != "0123456789" {
		t.Fatalf("fallback content = %q, %v", data, ok)
	}
	if result.Bytes != 15 {
		t.Fatalf("bytes = %d, want 15", result.Bytes)
	}
}

// TestCompressedCopyFallsBackWhenTheExtractionIsShort proves the read-back
// before promotion refuses an extracted tree that does not match the plan and
// publishes only the ordinary per-file copy.
func TestCompressedCopyFallsBackWhenTheExtractionIsShort(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	source.addFile("/docs/sub", "data.bin", "0123456789", 0o600)
	dest := newFakeFS("dest")
	dest.extractShort = true

	result, err := NewService(nil, nil).Copy(context.Background(), compressedRequest(source, dest))
	if err != nil {
		t.Fatalf("fallback copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 1 {
		t.Fatalf("archive extractions = %d, want 1 before falling back", calls)
	}
	if created := dest.created(); created != 2 {
		t.Fatalf("per-file creates = %d, want 2: a short extraction must not be promoted", created)
	}
	for path, want := range map[string]string{
		"/home/docs/readme.txt":   "hello",
		"/home/docs/sub/data.bin": "0123456789",
	} {
		if data, ok := dest.file(path); !ok || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", path, data, ok, want)
		}
	}
	for _, entry := range dest.entries("/home") {
		if entry.Name != "docs" {
			t.Fatalf("leftover staging entry %q after the refused extraction", entry.Name)
		}
	}
	if result.Bytes != 15 {
		t.Fatalf("bytes = %d, want 15", result.Bytes)
	}
}

// TestPackArchiveRefusesAFileThatGrew proves the packer reads past the planned
// size, so a source that grew after the plan is rejected instead of publishing
// a silent prefix.
func TestPackArchiveRefusesAFileThatGrew(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "grew.txt", "0123456789", 0o644)
	var out bytes.Buffer

	err := packArchive(context.Background(), source, []archiveEntry{{
		source: "/docs/grew.txt", name: "docs/grew.txt", size: 4, mode: 0o644,
	}}, &out, func(archiveEntry) {})
	if err == nil || !strings.Contains(err.Error(), "grew") {
		t.Fatalf("error = %v, want a grown-source refusal", err)
	}
}

// TestPackArchiveRefusesAnEmptyFileThatGrew covers the same growth rule for a
// file the plan observed as empty, which CopyN alone would archive as empty.
func TestPackArchiveRefusesAnEmptyFileThatGrew(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "empty.txt", "now has content", 0o644)
	var out bytes.Buffer

	err := packArchive(context.Background(), source, []archiveEntry{{
		source: "/docs/empty.txt", name: "docs/empty.txt", size: 0, mode: 0o644,
	}}, &out, func(archiveEntry) {})
	if err == nil || !strings.Contains(err.Error(), "grew") {
		t.Fatalf("error = %v, want a grown-source refusal", err)
	}
}

func TestCompressedCopyFallsBackWithoutExtractor(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	dest := newFakeFS("dest")

	request := compressedRequest(source, noArchiveFS{FileSystem: dest})
	if _, err := NewService(nil, nil).Copy(context.Background(), request); err != nil {
		t.Fatalf("fallback copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("archive extractions = %d, want 0 without a capability", calls)
	}
	if created := dest.created(); created != 1 {
		t.Fatalf("per-file creates = %d, want 1", created)
	}
	if data, ok := dest.file("/home/docs/readme.txt"); !ok || string(data) != "hello" {
		t.Fatalf("content = %q, %v", data, ok)
	}
}

func TestCompressedCopyFallsBackOnExistingTarget(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/home/docs", "old.txt", "old", 0o644)

	_, err := NewService(nil, nil).Copy(context.Background(), compressedRequest(source, dest))
	if KindOf(err) != KindExists {
		t.Fatalf("error = %v, want an exists failure", err)
	}
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("archive extractions = %d, want 0: a live target is a conflict", calls)
	}
	if data, _ := dest.file("/home/docs/old.txt"); string(data) != "old" {
		t.Fatalf("existing destination changed to %q", data)
	}
}

func TestCompressedCopyFallsBackForSymlinkTree(t *testing.T) {
	source := newFakeFS("source")
	source.add("/docs/link", &fakeNode{symlink: true, data: []byte("target")})
	dest := newFakeFS("dest")

	if _, err := NewService(nil, nil).Copy(context.Background(), compressedRequest(source, dest)); err != nil {
		t.Fatalf("fallback copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("archive extractions = %d, want 0 for a symlink tree", calls)
	}
	if created := dest.created(); created != 1 {
		t.Fatalf("per-file creates = %d, want 1", created)
	}
}

func TestCompressedCopyFallsBackForMixedSelection(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "one.txt", "one", 0o644)
	source.addFile("/docs", "two.txt", "two", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/home")

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/one.txt", "/docs"},
		DestDir: "/home", Kind: domainsftp.OpUpload, Conflict: ConflictStop, Compressed: true,
	})
	if err != nil {
		t.Fatalf("fallback copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("archive extractions = %d, want 0 for a mixed selection", calls)
	}
	for _, path := range []string{"/home/one.txt", "/home/docs/two.txt"} {
		if !dest.has(path) {
			t.Fatalf("fallback did not write %s", path)
		}
	}
}

func TestCompressedCopyFallsBackForMove(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	dest := newFakeFS("dest")

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/docs"}, DestDir: "/home",
		Kind: domainsftp.OpMove, Move: true, Conflict: ConflictStop, Compressed: true,
	})
	if err != nil {
		t.Fatalf("fallback move: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("archive extractions = %d, want 0 for a move", calls)
	}
	if source.has("/docs") {
		t.Fatal("the move left its source behind")
	}
	if !dest.has("/home/docs/readme.txt") {
		t.Fatal("the move did not write its destination")
	}
}

func TestCompressedCopyFallsBackWithResumeState(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	dest := newFakeFS("dest")

	request := compressedRequest(source, dest)
	request.KnownTargets = map[string]string{"/docs": "/home/docs"}
	if _, err := NewService(nil, nil).Copy(context.Background(), request); err != nil {
		t.Fatalf("resumed copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("archive extractions = %d, want 0 when a resume pins the target", calls)
	}
	if !dest.has("/home/docs/readme.txt") {
		t.Fatal("the resumed request did not copy its file")
	}
}

func TestQueueCompressedUploadUsesTheArchivePath(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	source.addFile("/docs/sub", "data.bin", "0123456789", 0o600)
	dest := newFakeFS("dest")

	queue := NewQueue(NewService(nil, nil), WithQueueConcurrency(1))
	startTestQueue(t, queue)
	id, err := queue.Enqueue(TaskSpec{
		Kind: domainsftp.OpUpload, Source: source, Dest: dest,
		Sources: []string{"/docs"}, DestDir: "/home", Conflict: ConflictStop,
		Compressed: true,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitQueueTask(t, queue, id, domainsftp.TaskCompleted)
	if calls := dest.archiveCalls(); calls != 1 {
		t.Fatalf("archive extractions = %d, want 1 through the queue", calls)
	}
	if created := dest.created(); created != 0 {
		t.Fatalf("per-file creates = %d, want 0 through the queue", created)
	}
	if data, ok := dest.file("/home/docs/sub/data.bin"); !ok || string(data) != "0123456789" {
		t.Fatalf("queued compressed content = %q, %v", data, ok)
	}
}

func TestCompressedCopyCancelLeavesNoTarget(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/docs", "readme.txt", "hello", 0o644)
	source.block = make(chan struct{})
	source.openGate = make(chan struct{}, 1)
	dest := newFakeFS("dest")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewService(nil, nil).Copy(ctx, compressedRequest(source, dest))
		done <- err
	}()

	select {
	case <-source.openGate:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("the compressed packer never opened a source file")
	}
	select {
	case err := <-done:
		if KindOf(err) != KindCancelled {
			t.Fatalf("error = %v, want cancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled compressed copy did not return")
	}
	if dest.has("/home/docs") {
		t.Fatal("a cancelled compressed copy left a destination target")
	}
	for _, entry := range dest.entries("/home") {
		if strings.Contains(entry.Name, "extract") {
			t.Fatalf("a cancelled compressed copy left staging %q", entry.Name)
		}
	}
}

func TestCompressedCopyRejectsUnsafeNames(t *testing.T) {
	source := newFakeFS("source")
	source.addDir("/docs")
	source.add("/docs/bad\\name", &fakeNode{data: []byte("x")})
	dest := newFakeFS("dest")

	if _, err := NewService(nil, nil).Copy(context.Background(), compressedRequest(source, dest)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("archive extractions = %d, want 0 for an unsafe entry name", calls)
	}
}
