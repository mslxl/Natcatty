package editor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appfileopen "github.com/mslxl/natcatty/application/fileopen"
)

type saveCall struct {
	session   appfileopen.EditID
	content   string
	overwrite bool
}

type fakeService struct {
	mu       sync.Mutex
	saves    []saveCall
	closed   []appfileopen.EditID
	resolved []bool
	retries  int
	// saveErr is returned by Save unless overwrite is true and
	// conflictUntilOverwrite is set.
	saveErr                error
	conflictUntilOverwrite bool
	retryErr               error
}

func (f *fakeService) Save(_ context.Context, id appfileopen.EditID,
	content []byte, overwrite bool) error {
	f.mu.Lock()
	f.saves = append(f.saves, saveCall{session: id, content: string(content), overwrite: overwrite})
	err := f.saveErr
	conflict := f.conflictUntilOverwrite
	f.mu.Unlock()
	if conflict && !overwrite {
		return &appfileopen.Error{
			Kind: appfileopen.ErrorConflict,
			Err:  errors.New("changed"),
			Conflict: &appfileopen.Conflict{
				Session: id, Name: "notes.txt", Path: "/remote/notes.txt",
			},
		}
	}
	if err != nil && !overwrite {
		return err
	}
	return nil
}

func (f *fakeService) Close(id appfileopen.EditID) {
	f.mu.Lock()
	f.closed = append(f.closed, id)
	f.mu.Unlock()
}

func (f *fakeService) ResolveExternal(_ context.Context, _ appfileopen.EditID,
	overwrite bool) error {
	f.mu.Lock()
	f.resolved = append(f.resolved, overwrite)
	f.mu.Unlock()
	return nil
}

func (f *fakeService) RetryExternal(context.Context, appfileopen.EditID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retries++
	return f.retryErr
}

func (f *fakeService) lastSave() (saveCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saves) == 0 {
		return saveCall{}, false
	}
	return f.saves[len(f.saves)-1], true
}

func (f *fakeService) closedIDs() []appfileopen.EditID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appfileopen.EditID(nil), f.closed...)
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within the deadline", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func openFixture(t *testing.T, state *State, service *fakeService, name string) appfileopen.File {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := appfileopen.File{
		Session: "s1", Name: name, Path: path, Remote: true,
		Save: func(ctx context.Context, content []byte, overwrite bool) error {
			return service.Save(ctx, "s1", content, overwrite)
		},
	}
	if err := state.OpenFile(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	state.Flush()
	if !state.Active() {
		t.Fatal("the document did not open")
	}
	return file
}

func TestOpenLoadsTheDocumentAndSavesBack(t *testing.T) {
	service := &fakeService{}
	state := NewState(service)
	openFixture(t, state, service, "notes.txt")

	if got := *state.Content(); got != "alpha" {
		t.Fatalf("content = %q", got)
	}
	*state.Content() = "alpha edited"
	if !state.Dirty() {
		t.Fatal("an edited buffer is not dirty")
	}
	state.Save()
	waitFor(t, "the save", func() bool { state.Flush(); return !state.Saving() })
	if call, ok := service.lastSave(); !ok || call.content != "alpha edited" || call.overwrite {
		t.Fatalf("save call = %+v, %v", call, ok)
	}
	if state.Dirty() || !state.Saved() {
		t.Fatalf("after save dirty=%v saved=%v", state.Dirty(), state.Saved())
	}
}

func TestConflictPromptsAndOverwriteRetries(t *testing.T) {
	service := &fakeService{conflictUntilOverwrite: true}
	state := NewState(service)
	openFixture(t, state, service, "notes.txt")
	*state.Content() = "mine"
	state.Save()
	waitFor(t, "the conflict prompt", func() bool {
		state.Flush()
		return state.ConflictPending()
	})
	if state.ConflictName() != "notes.txt" {
		t.Fatalf("conflict names %q", state.ConflictName())
	}
	if !state.Dirty() {
		t.Fatal("the buffer was lost when the save conflicted")
	}
	state.Overwrite()
	waitFor(t, "the overwrite save", func() bool {
		state.Flush()
		return !state.ConflictPending() && !state.Saving()
	})
	call, ok := service.lastSave()
	if !ok || !call.overwrite || call.content != "mine" {
		t.Fatalf("overwrite call = %+v, %v", call, ok)
	}
	if state.Dirty() {
		t.Fatal("the approved overwrite did not refresh the baseline")
	}
}

func TestCloseWithUnsavedChangesAsksBeforeDiscarding(t *testing.T) {
	service := &fakeService{}
	state := NewState(service)
	openFixture(t, state, service, "notes.txt")
	*state.Content() = "changed"
	state.Close()
	if !state.ConfirmPending() {
		t.Fatal("closing a dirty document did not ask")
	}
	if !state.Active() || len(service.closedIDs()) != 0 {
		t.Fatalf("the document closed before the user answered: active=%v closed=%v",
			state.Active(), service.closedIDs())
	}
	state.CancelClose()
	if !state.Active() || state.ConfirmPending() {
		t.Fatal("cancel did not keep the document open")
	}
	state.Close()
	state.Discard()
	if state.Active() || len(service.closedIDs()) != 1 || service.closedIDs()[0] != "s1" {
		t.Fatalf("discard did not close the session: %v", service.closedIDs())
	}
}

func TestSaveFailureKeepsTheBufferAndReports(t *testing.T) {
	service := &fakeService{saveErr: &appfileopen.Error{
		Kind: appfileopen.ErrorSave, Err: errors.New("connection reset"),
	}}
	state := NewState(service)
	openFixture(t, state, service, "notes.txt")
	*state.Content() = "still mine"
	state.Save()
	waitFor(t, "the failed save", func() bool {
		state.Flush()
		return !state.Saving()
	})
	if !state.SaveError() {
		t.Fatal("the failed save was not reported")
	}
	if !state.Active() || *state.Content() != "still mine" {
		t.Fatal("the buffer was lost after a failed save")
	}
}

func TestExternalConflictEventOpensThePrompt(t *testing.T) {
	service := &fakeService{}
	state := NewState(service)
	state.Notify(appfileopen.WatchEvent{
		Kind: appfileopen.WatchConflict, Session: "s2", Name: "remote.txt",
		Conflict: &appfileopen.Conflict{Session: "s2", Name: "remote.txt"},
	})
	state.Flush()
	if !state.ConflictPending() || state.ConflictName() != "remote.txt" {
		t.Fatalf("conflict pending=%v name=%q", state.ConflictPending(), state.ConflictName())
	}
	if state.Active() {
		t.Fatal("an external conflict opened an internal document")
	}
	state.Overwrite()
	waitFor(t, "the external overwrite", func() bool {
		state.Flush()
		service.mu.Lock()
		defer service.mu.Unlock()
		return len(service.resolved) == 1
	})
	service.mu.Lock()
	resolved := service.resolved[0]
	service.mu.Unlock()
	if !resolved {
		t.Fatal("the external overwrite was not approved")
	}
	if state.ConflictPending() {
		t.Fatal("the conflict prompt stayed open after approval")
	}
}

func TestExternalFailureRetries(t *testing.T) {
	service := &fakeService{}
	state := NewState(service)
	state.Notify(appfileopen.WatchEvent{
		Kind: appfileopen.WatchFailed, Session: "s2", Name: "remote.txt",
	})
	state.Flush()
	if !state.FailurePending() {
		t.Fatal("the upload failure was not reported")
	}
	state.Retry()
	waitFor(t, "the retry", func() bool {
		state.Flush()
		service.mu.Lock()
		defer service.mu.Unlock()
		return service.retries == 1 && !state.FailurePending()
	})
}

func TestSecondOpenIsRefusedWhileADocumentIsActive(t *testing.T) {
	service := &fakeService{}
	state := NewState(service)
	openFixture(t, state, service, "notes.txt")
	dir := t.TempDir()
	path := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(path, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := state.OpenFile(context.Background(), appfileopen.File{
		Session: "s2", Name: "other.txt", Path: path,
		Save: func(context.Context, []byte, bool) error { return nil },
	})
	if err == nil {
		t.Fatal("a second document replaced the first")
	}
	state.Flush()
	if *state.Content() != "alpha" {
		t.Fatal("the first document's buffer changed")
	}
}

func TestOversizedFileIsRefused(t *testing.T) {
	service := &fakeService{}
	state := NewState(service)
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(appfileopen.MaxEditorBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	err = state.OpenFile(context.Background(), appfileopen.File{
		Session: "s1", Name: "huge.txt", Path: path,
		Save: func(context.Context, []byte, bool) error { return nil },
	})
	if err == nil {
		t.Fatal("an oversized file was opened")
	}
	state.Flush()
	if state.Active() {
		t.Fatal("an oversized file left a document open")
	}
}
