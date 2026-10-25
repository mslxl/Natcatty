// Package editor owns the internal text editor surface: the open document,
// its buffer, save-back, the remote-change conflict prompt, and the prompts
// for unsaved changes and failed external uploads. It is mutated only on the
// UI thread; goroutines append outcomes to a locked queue that Flush applies.
package editor

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	appfileopen "github.com/mslxl/natcatty/application/fileopen"
)

// Service is the application editing seam. The app supplies the real file
// service; tests supply a fake.
type Service interface {
	Save(ctx context.Context, id appfileopen.EditID, content []byte, overwrite bool) error
	Close(id appfileopen.EditID)
	ResolveExternal(ctx context.Context, id appfileopen.EditID, overwrite bool) error
	RetryExternal(ctx context.Context, id appfileopen.EditID) error
}

// State is the editor surface's state. A document is open at most one at a
// time, matching the original modal editor.
type State struct {
	service Service
	redraw  func()

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	queue    []func()
	occupied bool

	doc      *document
	conflict *conflictPrompt
	confirm  *confirmPrompt
	failure  *failurePrompt
}

type document struct {
	id         appfileopen.EditID
	name       string
	content    string
	baseline   string
	save       appfileopen.SaveFunc
	saving     bool
	saveFailed bool
	saved      bool
}

type conflictPrompt struct {
	id       appfileopen.EditID
	name     string
	internal bool
}

type confirmPrompt struct {
	id   appfileopen.EditID
	name string
}

type failurePrompt struct {
	id   appfileopen.EditID
	name string
}

// Option customizes a State.
type Option func(*State)

// WithRedrawRequest sets the function that wakes the main window after a
// goroutine finishes.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// NewState builds the editor state.
func NewState(service Service, options ...Option) *State {
	ctx, cancel := context.WithCancel(context.Background())
	state := &State{service: service, ctx: ctx, cancel: cancel}
	for _, option := range options {
		option(state)
	}
	return state
}

// Shutdown cancels in-flight saves. The app closes editor documents before
// the file service sweeps their temporary copies.
func (s *State) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
}

// OpenFile implements fileopen.Editor. It reads the file off the UI thread and
// queues the document. An open while another document is active is refused so
// an unsaved buffer is never replaced silently.
func (s *State) OpenFile(_ context.Context, file appfileopen.File) error {
	s.mu.Lock()
	if s.occupied {
		s.mu.Unlock()
		return errors.New("the editor already has a file open")
	}
	s.occupied = true
	s.mu.Unlock()

	content, err := readLimited(file.Path, appfileopen.MaxEditorBytes)
	if err != nil {
		s.mu.Lock()
		s.occupied = false
		s.mu.Unlock()
		return err
	}
	s.post(func() {
		s.doc = &document{
			id: file.Session, name: file.Name,
			content: string(content), baseline: string(content), save: file.Save,
		}
		s.conflict = nil
		s.confirm = nil
	})
	return nil
}

// Flush applies outcomes queued by goroutines. The view calls it every frame.
func (s *State) Flush() {
	s.mu.Lock()
	queue := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, fn := range queue {
		fn()
	}
}

func (s *State) post(fn func()) {
	s.mu.Lock()
	s.queue = append(s.queue, fn)
	s.mu.Unlock()
	if s.redraw != nil {
		s.redraw()
	}
}

// Active reports whether a document is open.
func (s *State) Active() bool { return s.doc != nil }

// Name returns the open document's file name.
func (s *State) Name() string {
	if s.doc == nil {
		return ""
	}
	return s.doc.name
}

// Content returns the editable buffer. The view binds a TextArea to it; it is
// only valid on the UI thread.
func (s *State) Content() *string {
	if s.doc == nil {
		return nil
	}
	return &s.doc.content
}

// Dirty reports whether the buffer differs from the saved baseline.
func (s *State) Dirty() bool {
	return s.doc != nil && s.doc.content != s.doc.baseline
}

// Saving reports whether a save is in flight.
func (s *State) Saving() bool { return s.doc != nil && s.doc.saving }

// SaveError reports whether the last save failed.
func (s *State) SaveError() bool { return s.doc != nil && s.doc.saveFailed }

// Saved reports whether the last save succeeded and nothing changed since.
func (s *State) Saved() bool {
	return s.doc != nil && s.doc.saved && s.doc.content == s.doc.baseline
}

// ConflictPending reports whether the remote-change prompt is open.
func (s *State) ConflictPending() bool { return s.conflict != nil }

// ConflictName returns the file the conflict prompt names.
func (s *State) ConflictName() string {
	if s.conflict == nil {
		return ""
	}
	return s.conflict.name
}

// ConfirmPending reports whether the unsaved-changes prompt is open.
func (s *State) ConfirmPending() bool { return s.confirm != nil }

// FailurePending reports whether the upload-failure prompt is open.
func (s *State) FailurePending() bool { return s.failure != nil }

// Save writes the buffer back. The remote-change check runs in the
// application service; a conflict opens the overwrite prompt instead of
// replacing the remote file.
func (s *State) Save() {
	doc := s.doc
	if doc == nil || doc.saving || doc.save == nil {
		return
	}
	s.startSave(doc, doc.content, false)
}

func (s *State) startSave(doc *document, content string, overwrite bool) {
	doc.saving = true
	doc.saveFailed = false
	doc.saved = false
	id := doc.id
	save := doc.save
	ctx := s.ctx
	if s.redraw != nil {
		s.redraw()
	}
	go func() {
		err := save(ctx, []byte(content), overwrite)
		s.post(func() { s.finishSave(id, content, err) })
	}()
}

func (s *State) finishSave(id appfileopen.EditID, content string, err error) {
	if s.doc == nil || s.doc.id != id {
		return
	}
	s.doc.saving = false
	if err == nil {
		s.doc.baseline = content
		s.doc.saved = true
		return
	}
	if conflict, ok := appfileopen.ConflictOf(err); ok {
		s.conflict = &conflictPrompt{id: id, name: conflict.Name, internal: true}
		return
	}
	s.doc.saveFailed = true
}

// Overwrite answers the conflict prompt by replacing the remote file. An
// internal document saves its current buffer; an external edit uploads the
// local copy the application is watching.
func (s *State) Overwrite() {
	prompt := s.conflict
	if prompt == nil {
		return
	}
	s.conflict = nil
	if prompt.internal && s.doc != nil && s.doc.id == prompt.id {
		s.startSave(s.doc, s.doc.content, true)
		return
	}
	id := prompt.id
	name := prompt.name
	go func() {
		err := s.service.ResolveExternal(s.ctx, id, true)
		s.post(func() { s.finishExternal(id, name, err) })
	}()
}

// CancelConflict leaves the remote file alone. A later external save asks
// again rather than silently replacing it.
func (s *State) CancelConflict() {
	prompt := s.conflict
	s.conflict = nil
	if prompt == nil || prompt.internal {
		return
	}
	id := prompt.id
	go func() {
		_ = s.service.ResolveExternal(s.ctx, id, false)
	}()
}

func (s *State) finishExternal(id appfileopen.EditID, name string, err error) {
	if err == nil {
		if s.failure != nil && s.failure.id == id {
			s.failure = nil
		}
		return
	}
	if conflict, ok := appfileopen.ConflictOf(err); ok {
		s.conflict = &conflictPrompt{id: id, name: conflict.Name}
		return
	}
	s.failure = &failurePrompt{id: id, name: name}
}

// Notify receives application watch events. It runs on the watcher goroutine,
// so it only queues UI work.
func (s *State) Notify(event appfileopen.WatchEvent) {
	s.post(func() { s.applyWatchEvent(event) })
}

func (s *State) applyWatchEvent(event appfileopen.WatchEvent) {
	switch event.Kind {
	case appfileopen.WatchConflict:
		if s.conflict == nil {
			s.conflict = &conflictPrompt{id: event.Session, name: event.Name}
		}
	case appfileopen.WatchFailed:
		s.failure = &failurePrompt{id: event.Session, name: event.Name}
	case appfileopen.WatchSynced:
		if s.failure != nil && s.failure.id == event.Session {
			s.failure = nil
		}
	}
	if s.redraw != nil {
		s.redraw()
	}
}

// Retry retries an external upload after a lost connection. The local copy is
// still on disk, so the user's edits are not lost.
func (s *State) Retry() {
	if s.failure == nil {
		return
	}
	id := s.failure.id
	name := s.failure.name
	s.failure = nil
	go func() {
		err := s.service.RetryExternal(s.ctx, id)
		s.post(func() { s.finishExternal(id, name, err) })
	}()
}

// DismissFailure closes the upload-failure prompt. The local copy stays on
// disk; a later external save retries the upload.
func (s *State) DismissFailure() { s.failure = nil }

// Close closes the document. Unsaved changes open a confirmation instead of
// being discarded.
func (s *State) Close() {
	if s.doc == nil {
		return
	}
	if s.doc.content != s.doc.baseline {
		s.confirm = &confirmPrompt{id: s.doc.id, name: s.doc.name}
		return
	}
	s.closeDocument()
}

// Discard confirms closing an unsaved document. The session's temporary copy
// is removed.
func (s *State) Discard() {
	s.confirm = nil
	s.closeDocument()
}

// CancelClose leaves the document open.
func (s *State) CancelClose() { s.confirm = nil }

func (s *State) closeDocument() {
	if s.doc == nil {
		return
	}
	id := s.doc.id
	s.doc = nil
	s.conflict = nil
	s.confirm = nil
	s.mu.Lock()
	s.occupied = false
	s.mu.Unlock()
	if s.service != nil {
		s.service.Close(id)
	}
	if s.redraw != nil {
		s.redraw()
	}
}

// readLimited reads at most limit+1 bytes so an oversized file is refused
// without loading it whole.
func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("the file is too large for the internal editor")
	}
	return content, nil
}
