package transfer

import (
	"context"
	"strings"
	"testing"
)

func TestWriteFileStagesAndReplacesTheFile(t *testing.T) {
	files := newFakeFS("remote")
	files.addFile("/work", "notes.txt", "old", 0o640)
	service := NewService(nil, nil)

	if err := service.WriteFile(context.Background(), files, "/work/notes.txt",
		[]byte("new content")); err != nil {
		t.Fatal(err)
	}
	content, ok := files.file("/work/notes.txt")
	if !ok || string(content) != "new content" {
		t.Fatalf("content = %q, %v", content, ok)
	}
	entry, err := files.Stat(context.Background(), "/work/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Mode&0o777 != 0o640 {
		t.Errorf("mode = %04o, want the destination's 0640", entry.Mode)
	}
	for _, name := range files.entries("/work") {
		if IsStagingName(name.Name) {
			t.Errorf("a staging file survived the promotion: %s", name.Name)
		}
		if isBackupName(name.Name) {
			t.Errorf("a backup survived the completed replacement: %s", name.Name)
		}
	}
}

func TestWriteFileCreatesAMissingDestination(t *testing.T) {
	files := newFakeFS("remote")
	files.addDir("/work")
	service := NewService(nil, nil)
	if err := service.WriteFile(context.Background(), files, "/work/new.txt",
		[]byte("created")); err != nil {
		t.Fatal(err)
	}
	if content, ok := files.file("/work/new.txt"); !ok || string(content) != "created" {
		t.Fatalf("content = %q, %v", content, ok)
	}
}

func TestWriteFileRefusesADirectoryAndLeavesItAlone(t *testing.T) {
	files := newFakeFS("remote")
	files.addDir("/work")
	files.addFile("/work/dir", "child.txt", "keep", 0o600)
	files.addDir("/work/dir2")
	service := NewService(nil, nil)
	err := service.WriteFile(context.Background(), files, "/work/dir2", []byte("x"))
	if KindOf(err) != KindIsDir {
		t.Fatalf("error = %v, kind = %q; want the is-a-directory kind", err, KindOf(err))
	}
	if _, ok := files.file("/work/dir/child.txt"); !ok {
		t.Error("refusing a directory save removed its subtree")
	}
}

func TestWriteFileFailureRemovesTheStagingFile(t *testing.T) {
	files := newFakeFS("remote")
	files.addFile("/work", "denied.txt", "old", 0o600)
	// A destination whose create is refused makes every stage fail.
	files.failCreate = true
	service := NewService(nil, nil)
	if err := service.WriteFile(context.Background(), files, "/work/denied.txt",
		[]byte("new")); err == nil {
		t.Fatal("a refused create still reported success")
	}
	if content, ok := files.file("/work/denied.txt"); !ok || string(content) != "old" {
		t.Fatalf("the destination changed after a failed write: %q, %v", content, ok)
	}
	for _, name := range files.entries("/work") {
		if IsStagingName(name.Name) {
			t.Errorf("a failed write left staging file %s", name.Name)
		}
	}
}

func TestWriteFileVerifiesTheStagedSize(t *testing.T) {
	files := newFakeFS("remote")
	files.addFile("/work", "notes.txt", "old", 0o600)
	// A transport whose staged file loses a byte must not publish it.
	files.truncateWrites = 1
	service := NewService(nil, nil)
	err := service.WriteFile(context.Background(), files, "/work/notes.txt", []byte("new"))
	if err == nil {
		t.Fatal("a short staged write was promoted")
	}
	if !strings.Contains(err.Error(), "expected") {
		t.Errorf("error = %v, want the staged-size verification failure", err)
	}
	if content, ok := files.file("/work/notes.txt"); !ok || string(content) != "old" {
		t.Fatalf("the destination changed after a short write: %q, %v", content, ok)
	}
}

// A failed promotion restores the backup the transaction moved aside, so the
// remote file is never left missing. The stage and the backup are both cleaned
// up: the failure is reported, not hidden behind stray contract files.
func TestWriteFileRestoresTheBackupWhenThePromotionFails(t *testing.T) {
	files := newFakeFS("remote")
	files.addFile("/work", "notes.txt", "old", 0o640)
	files.failStageRename = true
	service := NewService(nil, nil)
	err := service.WriteFile(context.Background(), files, "/work/notes.txt", []byte("new"))
	if err == nil {
		t.Fatal("a refused promotion reported success")
	}
	if content, ok := files.file("/work/notes.txt"); !ok || string(content) != "old" {
		t.Fatalf("the original was not restored: %q, %v", content, ok)
	}
	entry, statErr := files.Stat(context.Background(), "/work/notes.txt")
	if statErr != nil {
		t.Fatal(statErr)
	}
	if entry.Mode&0o777 != 0o640 {
		t.Errorf("restored mode = %04o, want 0640", entry.Mode)
	}
	for _, name := range files.entries("/work") {
		if IsStagingName(name.Name) || isBackupName(name.Name) {
			t.Errorf("a failed replacement left %s behind", name.Name)
		}
	}
}

// An attempt interrupted after moving the destination aside leaves only the
// backup. The next save reconciles it before starting, so the original is
// never lost.
func TestWriteFileRecoversADestinationThatOnlyExistsAsABackup(t *testing.T) {
	files := newFakeFS("remote")
	files.addDir("/work")
	files.addFile("/work", "notes.txt", "old", 0o600)
	backup := backupPath(files, "/work/notes.txt")
	if err := files.Rename(context.Background(), "/work/notes.txt", backup); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, nil)
	if err := service.WriteFile(context.Background(), files, "/work/notes.txt",
		[]byte("new")); err != nil {
		t.Fatal(err)
	}
	if content, ok := files.file("/work/notes.txt"); !ok || string(content) != "new" {
		t.Fatalf("content = %q, %v", content, ok)
	}
	for _, name := range files.entries("/work") {
		if IsStagingName(name.Name) || isBackupName(name.Name) {
			t.Errorf("the reconciled replacement left %s behind", name.Name)
		}
	}
}

func isBackupName(name string) bool {
	return strings.HasPrefix(name, ".netcatty-backup-") && strings.HasSuffix(name, ".bak")
}
