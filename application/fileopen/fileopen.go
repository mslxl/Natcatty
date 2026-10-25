// Package fileopen opens a file with the opener the user configured for its
// extension: the app's internal editor, the operating system's associated
// application, or a chosen external application. It is the application seam
// between the SFTP surface and the Settings associations, and it owns the
// temporary materialization remote files need before a local tool can read
// them.
package fileopen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// Preferences is the settings snapshot this service resolves associations
// from.
type Preferences interface {
	Settings() domainpreferences.Settings
}

// Launcher hands a local file to the operating system or to one chosen
// application. The adapter implements it; tests provide a fake.
type Launcher interface {
	OpenSystem(ctx context.Context, path string) error
	OpenApplication(ctx context.Context, application, path string) error
}

// File is what the internal editor is asked to open. A remote file has
// already been materialized, so Path is always local. Session and Save let
// the editor write its buffer back and close the session when it goes away.
type File struct {
	Session EditID
	Name    string
	Path    string
	Remote  bool
	Size    int64
	// Save writes the editor buffer back to the file this session opened.
	Save SaveFunc
}

// Editor is the internal-editor port. Ticket 38 builds the editor surface and
// supplies it; a service without one reports that the internal opener is not
// available instead of opening nothing.
type Editor interface {
	OpenFile(ctx context.Context, file File) error
}

// Request is one file to open. A local file supplies LocalPath. A remote file
// supplies Remote and a Source; the service materializes it under its temp
// root before opening.
type Request struct {
	Name      string
	LocalPath string
	Size      int64
	Remote    bool
	Source    func(context.Context) (io.ReadCloser, error)
	// Target is the write-back channel for a remote file. Without it a remote
	// file still opens, but the internal editor cannot save it back and an
	// external application's changes are not watched.
	Target RemoteTarget
}

// Result describes the opener that ran and where it read the file. Session
// names the editing session the open created, if any; closing it sweeps the
// temporary copy.
type Result struct {
	Name      string
	Kind      domainpreferences.OpenerKind
	Remote    bool
	LocalPath string
	Session   EditID
}

// ErrorKind classifies an open failure so the surface can localize it
// without showing transport or OS text.
type ErrorKind string

const (
	// ErrorSource means the file itself could not be identified or read.
	ErrorSource ErrorKind = "open-source"
	// ErrorDownload means a remote file could not be materialized.
	ErrorDownload ErrorKind = "open-download"
	// ErrorEditorUnavailable means no internal editor is wired in this build.
	ErrorEditorUnavailable ErrorKind = "open-editor-unavailable"
	// ErrorEditorFailed means the internal editor was handed the file but
	// could not open it.
	ErrorEditorFailed ErrorKind = "open-editor-failed"
	// ErrorLaunch means the system or external application could not start.
	ErrorLaunch ErrorKind = "open-launch"
	// ErrorCancelled means the surface or window went away first.
	ErrorCancelled ErrorKind = "open-cancelled"
	// ErrorTooLarge means the internal editor refuses a file above its bound.
	ErrorTooLarge ErrorKind = "open-too-large"
	// ErrorConflict means the remote file changed while it was being edited;
	// nothing was written.
	ErrorConflict ErrorKind = "edit-conflict"
	// ErrorSave means an editor buffer could not be written back.
	ErrorSave ErrorKind = "edit-save"
)

// Error carries a classified open or save failure and its cause.
type Error struct {
	Kind ErrorKind
	Err  error
	// Conflict describes the remote version a save refused to replace.
	Conflict *Conflict
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return string(e.Kind) + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf reports the classification of an open failure.
func KindOf(err error) (ErrorKind, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target.Kind, true
	}
	return "", false
}

func failure(kind ErrorKind, err error) error { return &Error{Kind: kind, Err: err} }

// Service resolves associations and opens files. It is safe for concurrent
// use; every call reads a fresh settings snapshot.
type Service struct {
	prefs    Preferences
	launcher Launcher
	editorMu sync.RWMutex
	editor   Editor
	tempRoot string

	// Sessions and their state. The lock also guards the observer and the
	// watch intervals because a watcher goroutine reads them.
	mu            sync.Mutex
	sessions      map[EditID]*session
	nextSession   atomic.Uint64
	observer      Observer
	watchInterval time.Duration
	watchDebounce time.Duration
}

// Option customizes a Service.
type Option func(*Service)

// WithEditor supplies the internal editor. Without it the internal opener is
// reported as unavailable.
func WithEditor(editor Editor) Option {
	return func(s *Service) { s.editor = editor }
}

// WithTempRoot places materialized remote files under root. The default is
// the operating system's temp directory.
func WithTempRoot(root string) Option {
	return func(s *Service) { s.tempRoot = root }
}

// WithWatchObserver receives external-edit results. The observer runs on a
// watcher goroutine, so a UI observer must post its work to the UI thread.
func WithWatchObserver(observer Observer) Option {
	return func(s *Service) { s.observer = observer }
}

// WithWatchInterval sets how often an external edit's local copy is polled.
// The default is one second, matching the original.
func WithWatchInterval(interval time.Duration) Option {
	return func(s *Service) { s.watchInterval = interval }
}

// WithWatchDebounce sets how long an external edit must settle before it is
// uploaded. The default is 500 ms, matching the original.
func WithWatchDebounce(debounce time.Duration) Option {
	return func(s *Service) { s.watchDebounce = debounce }
}

// SetEditor supplies the internal editor after construction. The composition
// root uses it when the editor needs the service back; a service built with
// WithEditor needs no call.
func (s *Service) SetEditor(editor Editor) {
	s.editorMu.Lock()
	s.editor = editor
	s.editorMu.Unlock()
}

// SetObserver supplies the external-edit observer after construction.
func (s *Service) SetObserver(observer Observer) {
	s.mu.Lock()
	s.observer = observer
	s.mu.Unlock()
}

// New builds the service.
func New(prefs Preferences, launcher Launcher, options ...Option) *Service {
	service := &Service{
		prefs: prefs, launcher: launcher, tempRoot: os.TempDir(),
		sessions:      make(map[EditID]*session),
		watchInterval: time.Second,
		watchDebounce: 500 * time.Millisecond,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

// Open opens req with its configured association, falling back to the default
// opener for an unknown extension or a file with no extension.
func (s *Service) Open(ctx context.Context, req Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, failure(ErrorCancelled, err)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = baseName(req.LocalPath)
	}
	if name == "" {
		return Result{}, failure(ErrorSource, errors.New("file has no name"))
	}

	settings := s.prefs.Settings().SFTPEditor
	opener := settings.OpenerFor(name)
	s.editorMu.RLock()
	editor := s.editor
	s.editorMu.RUnlock()
	if opener.Kind == domainpreferences.OpenerInternal && editor == nil {
		// Check before downloading: a remote file is not worth fetching when
		// nothing can display it.
		return Result{}, failure(ErrorEditorUnavailable, errors.New("no internal editor is wired"))
	}
	if opener.Kind == domainpreferences.OpenerInternal && req.Size > MaxEditorBytes {
		// The listing already knows the size, so an oversized file is refused
		// before its bytes cross the connection.
		return Result{}, failure(ErrorTooLarge, errors.New("the file is too large for the internal editor"))
	}

	localPath := req.LocalPath
	var tempDir, contentDigest string
	contentSize := req.Size
	if req.Remote {
		dir, materialized, size, digest, err := s.materialize(ctx, req, name)
		if err != nil {
			return Result{}, err
		}
		tempDir, localPath, contentSize, contentDigest = dir, materialized, size, digest
		if opener.Kind == domainpreferences.OpenerInternal && contentSize > MaxEditorBytes {
			_ = os.RemoveAll(tempDir)
			return Result{}, failure(ErrorTooLarge, errors.New("the file is too large for the internal editor"))
		}
	} else if strings.TrimSpace(localPath) == "" {
		return Result{}, failure(ErrorSource, errors.New("local file has no path"))
	}
	// Nothing will read a materialized file after a failure, so the directory
	// this call created is removed. A successful open keeps it for the
	// launched application or the editor, and Close or Shutdown removes it.
	discard := func() {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
	}

	if err := ctx.Err(); err != nil {
		discard()
		return Result{}, failure(ErrorCancelled, err)
	}
	result := Result{Name: name, Kind: opener.Kind, Remote: req.Remote, LocalPath: localPath}

	// A remote file's baseline is the version the editor or application is
	// about to read. The digest comes from the bytes just downloaded, so a
	// later save can compare content even when the server reports the same
	// modification time.
	var baseline RemoteStat
	if req.Remote && req.Target != nil {
		info, err := req.Target.Stat(ctx)
		if err != nil {
			discard()
			return Result{}, failure(ErrorDownload, err)
		}
		baseline = RemoteStat{Size: contentSize, Modified: info.Modified, Digest: contentDigest}
	}
	if !req.Remote && opener.Kind == domainpreferences.OpenerInternal {
		if info, err := os.Stat(localPath); err == nil {
			contentSize = info.Size()
		}
	}

	switch opener.Kind {
	case domainpreferences.OpenerInternal:
		id := s.newSession(name, localPath, tempDir, req.Remote, req.Target, baseline)
		file := File{
			Session: id,
			Name:    name,
			Path:    localPath,
			Remote:  req.Remote,
			Size:    contentSize,
			Save: func(ctx context.Context, content []byte, overwrite bool) error {
				return s.Save(ctx, id, content, overwrite)
			},
		}
		if err := editor.OpenFile(ctx, file); err != nil {
			s.Close(id)
			return Result{}, failure(ErrorEditorFailed, err)
		}
		result.Session = id
	case domainpreferences.OpenerSystem:
		if err := s.launcher.OpenSystem(ctx, localPath); err != nil {
			discard()
			return Result{}, failure(ErrorLaunch, err)
		}
		result.Session = s.trackExternal(name, localPath, tempDir, req, baseline)
	case domainpreferences.OpenerExternal:
		if err := s.launcher.OpenApplication(ctx, opener.Application.Path, localPath); err != nil {
			discard()
			return Result{}, failure(ErrorLaunch, err)
		}
		result.Session = s.trackExternal(name, localPath, tempDir, req, baseline)
	default:
		discard()
		return Result{}, failure(ErrorSource, fmt.Errorf("unknown opener %q", opener.Kind))
	}
	return result, nil
}

// trackExternal remembers a remote copy opened by an external application and
// starts polling it for saves. A local file needs no session: it is the file
// the application edits.
func (s *Service) trackExternal(name, localPath, tempDir string, req Request,
	baseline RemoteStat) EditID {
	if !req.Remote || req.Target == nil {
		return ""
	}
	id := s.newSession(name, localPath, tempDir, true, req.Target, baseline)
	sess, _ := s.session(id)
	s.startWatch(sess)
	return id
}

// materialize downloads a remote file into its own temp directory and returns
// the directory, local path, byte count, and content digest. The directory is
// intentionally left in place after a successful open: the launched
// application or editor still reads it. A failed download removes it; the
// caller removes it for a later failure.
func (s *Service) materialize(ctx context.Context, req Request, name string) (string, string, int64, string, error) {
	if req.Source == nil {
		return "", "", 0, "", failure(ErrorSource, errors.New("remote file has no source"))
	}
	source, err := req.Source(ctx)
	if err != nil {
		return "", "", 0, "", failure(ErrorDownload, err)
	}
	defer source.Close()

	dir, err := os.MkdirTemp(s.tempRoot, materializePrefix+"*")
	if err != nil {
		return "", "", 0, "", failure(ErrorDownload, err)
	}
	path := filepath.Join(dir, safeName(name))
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", 0, "", failure(ErrorDownload, err)
	}
	copied, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.RemoveAll(dir)
		return "", "", 0, "", failure(ErrorDownload, copyErr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", 0, "", failure(ErrorDownload, err)
	}
	return dir, path, copied, digestBytes(content), nil
}

// materializePrefix marks the temporary directory a remote copy lives in. It
// is also the sweep predicate: a directory with this prefix that no live
// session owns is an orphan from a previous process.
const materializePrefix = "natcatty-open-"

func baseName(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// safeName reduces a remote name to one filesystem component. It applies the
// Windows-invalid characters on every platform so a download stays portable,
// and never returns a relative path segment.
func safeName(name string) string {
	base := baseName(name)
	base = strings.Map(func(r rune) rune {
		switch {
		case r < 32, r == ':', r == '*', r == '?', r == '"',
			r == '<', r == '>', r == '|':
			return '_'
		}
		return r
	}, base)
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." {
		return "download"
	}
	return base
}
