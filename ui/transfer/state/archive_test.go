package state

import (
	"context"
	"sync"
	"testing"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
)

// archiveFS adds the archive capability to the fixture's fake disk and records
// the request, so the prompt's choices can be asserted. A successful request
// writes the archive into the same tree, which is what the refreshed listing
// reads.
type archiveFS struct {
	*fakeFS

	mu       sync.Mutex
	requests []apptransfer.ArchiveRequest
	err      error
}

func (a *archiveFS) CreateArchive(_ context.Context, request apptransfer.ArchiveRequest) error {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	err := a.err
	a.mu.Unlock()
	if err != nil {
		return err
	}
	a.disk.put(request.Directory+"/"+request.Name, "archive", 0o600)
	return nil
}

func (a *archiveFS) requestCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func (a *archiveFS) lastRequest() apptransfer.ArchiveRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests[len(a.requests)-1]
}

// TestArchiveSelectionCreatesTheArchiveAndRefreshes holds the acceptance path:
// the prompt starts from the folder's name, the format choice follows through
// to the command's request, and the finished operation reloads the folder.
func TestArchiveSelectionCreatesTheArchiveAndRefreshes(t *testing.T) {
	f := newFixture(t)
	tab := f.state.Tab(Left)
	archive := &archiveFS{fakeFS: tab.FS.(*fakeFS)}
	tab.FS = archive
	if !CanArchive(tab) {
		t.Fatal("a filesystem with CreateArchive is not offered the action")
	}

	selectOne(t, f.state, Left, "folder")
	f.state.RequestArchive(Left)
	dialog := f.state.Dialog()
	if dialog == nil || dialog.Kind != DialogArchive {
		t.Fatalf("the create-archive prompt did not open: %+v", dialog)
	}
	if dialog.Draft != "folder.tar.gz" {
		t.Fatalf("the default name is %q", dialog.Draft)
	}
	if dialog.ArchiveFormat != apptransfer.ArchiveTarGz {
		t.Fatalf("the default format is %q", dialog.ArchiveFormat)
	}

	f.state.SetDialogArchiveFormat(apptransfer.ArchiveZip)
	if dialog := f.state.Dialog(); dialog.Draft != "folder.zip" {
		t.Fatalf("switching to zip left the name %q", dialog.Draft)
	}
	f.state.SetDialogArchiveFormat(apptransfer.ArchiveTarGz)
	if dialog := f.state.Dialog(); dialog.Draft != "folder.tar.gz" {
		t.Fatalf("switching back left the name %q", dialog.Draft)
	}

	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if got := archive.requestCount(); got != 1 {
		t.Fatalf("CreateArchive ran %d times", got)
	}
	request := archive.lastRequest()
	if request.Directory != "/home/me" || request.Name != "folder.tar.gz" ||
		request.Format != apptransfer.ArchiveTarGz || len(request.Paths) != 1 ||
		request.Paths[0] != "/home/me/folder" {
		t.Fatalf("request = %+v", request)
	}
	if notice := f.state.Notice(); notice != "operation-done" {
		t.Fatalf("notice = %q", notice)
	}
	settle(t, f.state, "the refreshed listing to show the archive", func() bool {
		return contains(visibleNames(tab), "folder.tar.gz")
	})
	if data, ok := f.local.read("/home/me/folder.tar.gz"); !ok || data != "archive" {
		t.Fatalf("the archive is %q, %v", data, ok)
	}
}

// TestArchiveSelectionRejectsANameWithoutItsExtension keeps the prompt open
// and runs nothing when the typed name does not match the chosen format.
func TestArchiveSelectionRejectsANameWithoutItsExtension(t *testing.T) {
	f := newFixture(t)
	tab := f.state.Tab(Left)
	archive := &archiveFS{fakeFS: tab.FS.(*fakeFS)}
	tab.FS = archive
	selectOne(t, f.state, Left, "folder")
	f.state.RequestArchive(Left)
	f.state.SetDialogDraft("folder.zip")
	f.state.SubmitDialog()

	dialog := f.state.Dialog()
	if dialog == nil || dialog.Err != "invalid-archive-name" {
		t.Fatalf("the invalid name did not keep the prompt open: %+v", dialog)
	}
	if archive.requestCount() != 0 {
		t.Fatal("an invalid name reached the filesystem")
	}
}

// TestArchiveSelectionReportsAReadOnlyPane mirrors Duplicate's availability
// rule: a read-only filesystem cannot write the archive.
func TestArchiveSelectionReportsAReadOnlyPane(t *testing.T) {
	f := newFixture(t)
	tab := f.state.Tab(Left)
	tab.FS = readOnlyFiles{FileSystem: &archiveFS{fakeFS: tab.FS.(*fakeFS)}}
	selectOne(t, f.state, Left, "folder")
	if CanArchive(tab) {
		t.Fatal("a read-only pane reports Create Archive as available")
	}
	f.state.RequestArchive(Left)
	if notice := f.state.Notice(); notice != "pane-unavailable" {
		t.Fatalf("notice = %q", notice)
	}
	if f.state.Dialog() != nil {
		t.Fatal("a read-only pane opened the prompt")
	}
}

// TestArchiveFailureKeepsTheToolMissingNotice is the typed-failure path: the
// classified kind reaches the status bar and no partial archive is written.
func TestArchiveFailureKeepsTheToolMissingNotice(t *testing.T) {
	f := newFixture(t)
	tab := f.state.Tab(Left)
	archive := &archiveFS{
		fakeFS: tab.FS.(*fakeFS),
		err:    &apptransfer.Error{Kind: apptransfer.KindToolMissing, Cause: apptransfer.ErrArchiveToolMissing},
	}
	tab.FS = archive
	selectOne(t, f.state, Left, "folder")
	f.state.RequestArchive(Left)
	f.state.SubmitDialog()
	waitIdle(t, f.state)

	if notice := f.state.Notice(); notice != "error:tool-missing" {
		t.Fatalf("notice = %q", notice)
	}
	if f.local.has("/home/me/folder.tar.gz") {
		t.Fatal("a failed archive left a partial file")
	}
}
