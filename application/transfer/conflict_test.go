package transfer

import (
	"context"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// setModTime gives a fake node the modification time the skip comparison
// reads. The fake filesystem otherwise stamps created files at the epoch.
func setModTime(files *fakeFS, path string, at time.Time) {
	files.mu.Lock()
	defer files.mu.Unlock()
	if node := files.nodes[path]; node != nil {
		node.modTime = at
	}
}

func copyOne(t *testing.T, source, dest *fakeFS, name string, policy ConflictPolicy, options func(*CopyRequest)) CopyResult {
	t.Helper()
	request := CopyRequest{
		Source: source, Dest: dest,
		Sources:  []string{"/" + name},
		DestDir:  "/",
		Conflict: policy,
	}
	if options != nil {
		options(&request)
	}
	result, err := NewService(nil, nil).Copy(context.Background(), request)
	if err != nil {
		t.Fatalf("Copy with policy %q: %v", policy, err)
	}
	return result
}

func TestCopyStopPolicyLeavesTheDestination(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/a.txt"}, DestDir: "/",
		Conflict: ConflictStop,
	})
	if KindOf(err) != KindExists {
		t.Fatalf("error = %v (kind %q), want exists", err, KindOf(err))
	}
	if got, _ := dest.file("/a.txt"); string(got) != "old" {
		t.Fatalf("destination = %q", got)
	}
}

func TestCopySkipPolicyReportsTheUntouchedDestination(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)

	result := copyOne(t, source, dest, "a.txt", ConflictSkip, nil)
	if got, _ := dest.file("/a.txt"); string(got) != "old" {
		t.Fatalf("destination = %q", got)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "/a.txt" {
		t.Fatalf("Skipped = %v", result.Skipped)
	}
	if len(result.Written) != 0 {
		t.Fatalf("Written = %v", result.Written)
	}
}

func TestCopyReplacePolicyPromotesTheNewContent(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o640)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)

	result := copyOne(t, source, dest, "a.txt", ConflictReplace, nil)
	if got, _ := dest.file("/a.txt"); string(got) != "new" {
		t.Fatalf("destination = %q", got)
	}
	if len(result.Written) != 1 || result.Written[0] != "/a.txt" {
		t.Fatalf("Written = %v", result.Written)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("Skipped = %v", result.Skipped)
	}
	backup := backupName(dest, "/a.txt")
	if dest.has("/" + backup) {
		t.Fatalf("replacement left %q behind", backup)
	}
}

func TestCopyDuplicatePolicyUsesCopySuffixes(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)

	copyOne(t, source, dest, "a.txt", ConflictDuplicate, nil)
	if got, _ := dest.file("/a.txt"); string(got) != "old" {
		t.Fatalf("original = %q", got)
	}
	if got, ok := dest.file("/a (copy).txt"); !ok || string(got) != "new" {
		t.Fatalf("first duplicate = %q, exists = %v", got, ok)
	}

	copyOne(t, source, dest, "a.txt", ConflictDuplicate, nil)
	if got, ok := dest.file("/a (copy 2).txt"); !ok || string(got) != "new" {
		t.Fatalf("second duplicate = %q, exists = %v", got, ok)
	}
}

func TestCopyDuplicatePolicyKeepsSiblingsFromOneBatchDistinct(t *testing.T) {
	left := newFakeFS("left")
	left.addFile("/", "a.txt", "left", 0o644)
	right := newFakeFS("right")
	right.addFile("/", "a.txt", "right", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)

	// Production binds this to the queue's path reservation. The shared map
	// reproduces its contract: a name another top-level source already chose in
	// this batch is taken even though no file exists at it yet.
	taken := map[string]bool{"/a.txt": true}
	var reserved []string
	shared := func(name string, _ bool) bool {
		if taken[name] {
			return false
		}
		taken[name] = true
		reserved = append(reserved, name)
		return true
	}
	options := func(request *CopyRequest) { request.ReserveName = shared }
	copyOne(t, left, dest, "a.txt", ConflictDuplicate, options)
	copyOne(t, right, dest, "a.txt", ConflictDuplicate, options)

	if got, ok := dest.file("/a (copy).txt"); !ok || string(got) != "left" {
		t.Fatalf("first duplicate = %q, exists = %v", got, ok)
	}
	if got, ok := dest.file("/a (copy 2).txt"); !ok || string(got) != "right" {
		t.Fatalf("second duplicate = %q, exists = %v", got, ok)
	}
	if len(reserved) != 2 {
		t.Fatalf("reserved = %v", reserved)
	}
}

func TestCopyMergePolicyKeepsDestinationOnlyContent(t *testing.T) {
	source := newFakeFS("source")
	source.addDir("/src")
	source.addFile("/src", "a.txt", "new", 0o644)
	source.addFile("/src", "c.txt", "added", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/src")
	dest.addFile("/src", "a.txt", "old", 0o644)
	dest.addFile("/src", "b.txt", "kept", 0o644)

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/",
		Conflict: ConflictMerge,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got, _ := dest.file("/src/a.txt"); string(got) != "new" {
		t.Fatalf("colliding file = %q", got)
	}
	if got, _ := dest.file("/src/b.txt"); string(got) != "kept" {
		t.Fatalf("destination-only file = %q", got)
	}
	if got, _ := dest.file("/src/c.txt"); string(got) != "added" {
		t.Fatalf("new file = %q", got)
	}
	if dest.has("/" + backupName(dest, "/src")) {
		t.Fatal("merge created a replacement backup")
	}
}

func TestCopyAskPolicyReportsTheConflictAndAppliesTheAnswer(t *testing.T) {
	second := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name   string
		answer ConflictPolicy
		dest   string
		check  func(t *testing.T, dest *fakeFS)
	}{
		{
			name: "skip", answer: ConflictSkip, dest: "old",
			check: func(t *testing.T, dest *fakeFS) {
				if got, _ := dest.file("/a.txt"); string(got) != "old" {
					t.Fatalf("destination = %q", got)
				}
			},
		},
		{
			name: "replace", answer: ConflictReplace, dest: "old",
			check: func(t *testing.T, dest *fakeFS) {
				if got, _ := dest.file("/a.txt"); string(got) != "new" {
					t.Fatalf("destination = %q", got)
				}
			},
		},
		{
			name: "duplicate", answer: ConflictDuplicate, dest: "old",
			check: func(t *testing.T, dest *fakeFS) {
				if got, _ := dest.file("/a (copy).txt"); string(got) != "new" {
					t.Fatalf("duplicate = %q", got)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source := newFakeFS("source")
			source.addFile("/", "a.txt", "new", 0o644)
			setModTime(source, "/a.txt", second)
			dest := newFakeFS("dest")
			dest.addFile("/", "a.txt", test.dest, 0o644)
			setModTime(dest, "/a.txt", second.Add(-time.Hour))
			var seen domainsftp.Conflict
			_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
				Source: source, Dest: dest, Sources: []string{"/a.txt"}, DestDir: "/",
				Conflict: ConflictAsk,
				OnConflict: func(conflict domainsftp.Conflict) ConflictResolution {
					seen = conflict
					return ConflictResolution{Action: test.answer}
				},
			})
			if err != nil {
				t.Fatalf("ask copy: %v", err)
			}
			if seen.Name != "a.txt" || seen.IsDirectory || seen.ExistingKind != domainsftp.ExistingFile {
				t.Fatalf("conflict = %+v", seen)
			}
			if seen.ExistingSize != int64(len(test.dest)) || seen.NewSize != int64(len("new")) {
				t.Fatalf("sizes = %d/%d", seen.ExistingSize, seen.NewSize)
			}
			if !seen.ExistingModified.Equal(second.Add(-time.Hour)) || !seen.NewModified.Equal(second) {
				t.Fatalf("mtimes = %v/%v", seen.ExistingModified, seen.NewModified)
			}
			test.check(t, dest)
		})
	}
}

func TestCopyAskPolicyWithoutAnAnswerStops(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old", 0o644)

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/a.txt"}, DestDir: "/",
		Conflict: ConflictAsk,
	})
	if KindOf(err) != KindExists {
		t.Fatalf("error = %v (kind %q), want exists", err, KindOf(err))
	}
}

func TestCopyRejectsTypeMismatchedReplaceAndMerge(t *testing.T) {
	cases := []struct {
		name   string
		source *fakeFS
		dest   *fakeFS
		policy ConflictPolicy
	}{
		{
			name: "directory over file",
			source: func() *fakeFS {
				files := newFakeFS("source")
				files.addDir("/src")
				return files
			}(),
			dest: func() *fakeFS {
				files := newFakeFS("dest")
				files.addFile("/", "src", "file", 0o644)
				return files
			}(),
			policy: ConflictReplace,
		},
		{
			name: "file over directory",
			source: func() *fakeFS {
				files := newFakeFS("source")
				files.addFile("/", "src", "file", 0o644)
				return files
			}(),
			dest: func() *fakeFS {
				files := newFakeFS("dest")
				files.addDir("/src")
				return files
			}(),
			policy: ConflictReplace,
		},
		{
			name: "merge into a file",
			source: func() *fakeFS {
				files := newFakeFS("source")
				files.addDir("/src")
				return files
			}(),
			dest: func() *fakeFS {
				files := newFakeFS("dest")
				files.addFile("/", "src", "file", 0o644)
				return files
			}(),
			policy: ConflictMerge,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
				Source: test.source, Dest: test.dest, Sources: []string{"/src"}, DestDir: "/",
				Conflict: test.policy,
			})
			if KindOf(err) != KindTypeMismatch {
				t.Fatalf("error = %v (kind %q), want type-mismatch", err, KindOf(err))
			}
		})
	}
}

func TestCopyReplaceUnlinksASymlinkDestination(t *testing.T) {
	source := newFakeFS("source")
	source.addFile("/", "link", "new", 0o644)
	dest := newFakeFS("dest")
	dest.add("/link", &fakeNode{data: []byte("target"), mode: 0o644, symlink: true})

	if _, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/link"}, DestDir: "/",
		Conflict: ConflictReplace,
	}); err != nil {
		t.Fatalf("replace symlink: %v", err)
	}
	if got, _ := dest.file("/link"); string(got) != "new" {
		t.Fatalf("destination = %q", got)
	}
}

func TestCopySkipUnchangedComparesSizeAndWholeSecondMtime(t *testing.T) {
	second := time.Unix(1_700_000_000, 0)
	source := newFakeFS("source")
	source.addDir("/src")
	source.addFile("/src", "same.txt", "NEW", 0o644)
	setModTime(source, "/src/same.txt", second.Add(100*time.Millisecond))
	source.addFile("/src", "resized.txt", "NEWSIZE", 0o644)
	setModTime(source, "/src/resized.txt", second)
	source.addFile("/src", "restamped.txt", "NEW", 0o644)
	setModTime(source, "/src/restamped.txt", second.Add(time.Second))
	dest := newFakeFS("dest")
	dest.addDir("/src")
	dest.addFile("/src", "same.txt", "OLD", 0o644)
	setModTime(dest, "/src/same.txt", second.Add(900*time.Millisecond))
	dest.addFile("/src", "resized.txt", "OLD", 0o644)
	setModTime(dest, "/src/resized.txt", second)
	dest.addFile("/src", "restamped.txt", "OLD", 0o644)
	setModTime(dest, "/src/restamped.txt", second)

	result, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/",
		Conflict: ConflictMerge, SkipUnchanged: true,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got, _ := dest.file("/src/same.txt"); string(got) != "OLD" {
		t.Fatalf("same size/second file was rewritten: %q", got)
	}
	if got, _ := dest.file("/src/resized.txt"); string(got) != "NEWSIZE" {
		t.Fatalf("different size file was not copied: %q", got)
	}
	if got, _ := dest.file("/src/restamped.txt"); string(got) != "NEW" {
		t.Fatalf("different second file was not copied: %q", got)
	}
	skipped := strings.Join(result.Skipped, ",")
	if skipped != "/src/same.txt" {
		t.Fatalf("Skipped = %v", result.Skipped)
	}
	if result.Bytes != int64(len("NEWSIZE")+len("NEW")) {
		t.Fatalf("Bytes = %d", result.Bytes)
	}
}

func TestCopyReplaceDoesNotSkipUnchanged(t *testing.T) {
	second := time.Unix(1_700_000_000, 0)
	source := newFakeFS("source")
	source.addDir("/src")
	source.addFile("/src", "a.txt", "NEW", 0o644)
	setModTime(source, "/src/a.txt", second)
	dest := newFakeFS("dest")
	dest.addDir("/src")
	dest.addFile("/src", "a.txt", "OLD", 0o644)
	setModTime(dest, "/src/a.txt", second)

	if _, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/",
		Conflict: ConflictReplace, SkipUnchanged: true,
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, _ := dest.file("/src/a.txt"); string(got) != "NEW" {
		t.Fatalf("a replacement skipped its file: %q", got)
	}
}

func TestCopyMergeDoesNotDeleteADestinationDirectoryThatMeetsAFile(t *testing.T) {
	source := newFakeFS("source")
	source.addDir("/src")
	source.addFile("/src", "nested", "incoming file", 0o644)
	dest := newFakeFS("dest")
	dest.addDir("/src")
	dest.addDir("/src/nested")
	dest.addFile("/src/nested", "keep.txt", "destination data", 0o644)

	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: []string{"/src"}, DestDir: "/",
		Conflict: ConflictMerge,
	})
	if KindOf(err) != KindTypeMismatch {
		t.Fatalf("a file over a destination directory = %v (kind %q), want type-mismatch",
			err, KindOf(err))
	}
	if !dest.has("/src/nested/keep.txt") {
		t.Fatal("the merge deleted the destination directory's contents")
	}
	dest.mu.Lock()
	defer dest.mu.Unlock()
	for full := range dest.nodes {
		if IsStagingName(path.Base(full)) {
			t.Fatalf("the failed merge left the staging file %q", full)
		}
	}
}

// askConflictCounts copies sources into dest with the ask policy, answers
// Skip, and returns the apply-to-all count of each prompt in order.
func askConflictCounts(t *testing.T, source, dest FileSystem, sources []string) []int {
	t.Helper()
	var counts []int
	_, err := NewService(nil, nil).Copy(context.Background(), CopyRequest{
		Source: source, Dest: dest, Sources: sources, DestDir: "/",
		Conflict: ConflictAsk,
		OnConflict: func(conflict domainsftp.Conflict) ConflictResolution {
			counts = append(counts, conflict.ApplyToAllCount)
			return ConflictResolution{Action: ConflictSkip}
		},
	})
	if err != nil {
		t.Fatalf("ask copy: %v", err)
	}
	return counts
}

func TestCopyAskDoesNotCountANewSiblingAsAConflict(t *testing.T) {
	// 1-of-2: only a.txt exists, so an answer covers no later conflict and the
	// prompt must not claim a second one.
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old-a", 0o644)

	if got := askConflictCounts(t, source, dest, []string{"/a.txt", "/b.txt"}); !slices.Equal(got, []int{0}) {
		t.Fatalf("apply-to-all counts = %v, want [0]: b.txt is new, not a conflict", got)
	}
}

func TestCopyAskCountsOnlyTheLaterSameTypeConflicts(t *testing.T) {
	// 3-of-3: the first answer covers the two conflicts after it, the second
	// covers one, and the last covers none.
	source := newFakeFS("source")
	dest := newFakeFS("dest")
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		source.addFile("/", name, "new-"+name, 0o644)
		dest.addFile("/", name, "old-"+name, 0o644)
	}

	if got := askConflictCounts(t, source, dest,
		[]string{"/a.txt", "/b.txt", "/c.txt"}); !slices.Equal(got, []int{2, 1, 0}) {
		t.Fatalf("apply-to-all counts = %v, want [2 1 0]", got)
	}
}

func TestCopyAskCountsOneOfThreeWhenTheLastSourceIsNew(t *testing.T) {
	// 2-of-3: a.txt and b.txt conflict; c.txt is new. After the first answer
	// exactly one conflict remains.
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	source.addFile("/", "c.txt", "new-c", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old-a", 0o644)
	dest.addFile("/", "b.txt", "old-b", 0o644)

	if got := askConflictCounts(t, source, dest,
		[]string{"/a.txt", "/b.txt", "/c.txt"}); !slices.Equal(got, []int{1, 0}) {
		t.Fatalf("apply-to-all counts = %v, want [1 0]", got)
	}
}

func TestCopyAskDoesNotCountADifferentConflictType(t *testing.T) {
	// A file:file conflict is not covered by a directory:directory answer, so
	// only b.txt counts toward a.txt's prompt.
	source := newFakeFS("source")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	source.addDir("/folder")
	source.addFile("/folder", "inside.txt", "inside", 0o644)
	dest := newFakeFS("dest")
	dest.addFile("/", "a.txt", "old-a", 0o644)
	dest.addFile("/", "b.txt", "old-b", 0o644)
	dest.addDir("/folder")

	if got := askConflictCounts(t, source, dest,
		[]string{"/a.txt", "/b.txt", "/folder"}); !slices.Equal(got, []int{1, 0, 0}) {
		t.Fatalf("apply-to-all counts = %v, want [1 0 0]", got)
	}
}

// windowsSourceFS is the local pane's view of a fake store on Windows: paths
// carry drive-letter, backslash notation while the fake's nodes stay keyed by
// a POSIX path. Only the source side needs it; the destination stays POSIX.
type windowsSourceFS struct {
	*fakeFS
}

func (windowsSourceFS) Style() PathStyle { return StyleWindows }

// Base follows Windows separators on every platform, so this regression does
// not depend on the host the suite runs on.
func (windowsSourceFS) Base(p string) string {
	trimmed := strings.TrimRight(p, `\/`)
	if index := strings.LastIndexAny(trimmed, `\/`); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

func (w windowsSourceFS) Stat(ctx context.Context, p string) (domainsftp.Entry, error) {
	return w.fakeFS.Stat(ctx, path.Join("/", w.Base(p)))
}

func TestCopyAskCountsAWindowsSourceConflict(t *testing.T) {
	// The conflict scan must derive the destination name with the source's own
	// Base. A POSIX Base over a Windows path keeps the separators, so the
	// destination stat misses and the count silently drops to zero.
	source := newFakeFS("local")
	source.addFile("/", "a.txt", "new-a", 0o644)
	source.addFile("/", "b.txt", "new-b", 0o644)
	dest := newFakeFS("remote")
	dest.addFile("/", "a.txt", "old-a", 0o644)
	dest.addFile("/", "b.txt", "old-b", 0o644)

	got := askConflictCounts(t, windowsSourceFS{source}, dest,
		[]string{`C:\dir\a.txt`, `C:\dir\b.txt`})
	if !slices.Equal(got, []int{1, 0}) {
		t.Fatalf("apply-to-all counts = %v, want [1 0]", got)
	}
}
