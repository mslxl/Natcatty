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
// already been materialized, so Path is always local.
type File struct {
	Name   string
	Path   string
	Remote bool
	Size   int64
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
}

// Result describes the opener that ran and where it read the file.
type Result struct {
	Name      string
	Kind      domainpreferences.OpenerKind
	Remote    bool
	LocalPath string
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
)

// Error carries a classified open failure and its cause.
type Error struct {
	Kind ErrorKind
	Err  error
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
	editor   Editor
	tempRoot string
}

// Option customizes a Service.
type Option func(*Service)

// WithEditor supplies the internal editor. Without it the internal opener is
// reported as unavailable.
func WithEditor(editor Editor) Option {
	return func(s *Service) { s.editor = editor }
}

// WithTempRoot places materialized remote files under root. The default is
// the operating system's temp directory. Ticket 38 owns sweeping leftovers.
func WithTempRoot(root string) Option {
	return func(s *Service) { s.tempRoot = root }
}

// New builds the service.
func New(prefs Preferences, launcher Launcher, options ...Option) *Service {
	service := &Service{prefs: prefs, launcher: launcher, tempRoot: os.TempDir()}
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
	if opener.Kind == domainpreferences.OpenerInternal && s.editor == nil {
		// Check before downloading: a remote file is not worth fetching when
		// nothing can display it.
		return Result{}, failure(ErrorEditorUnavailable, errors.New("no internal editor is wired"))
	}

	localPath := req.LocalPath
	var tempDir string
	if req.Remote {
		dir, materialized, err := s.materialize(ctx, req, name)
		if err != nil {
			return Result{}, err
		}
		tempDir, localPath = dir, materialized
	} else if strings.TrimSpace(localPath) == "" {
		return Result{}, failure(ErrorSource, errors.New("local file has no path"))
	}
	// Nothing will read a materialized file after a failure, so the directory
	// this call created is removed. A successful open keeps it: the launched
	// application may still be reading, and ticket 38 sweeps leftovers.
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
	switch opener.Kind {
	case domainpreferences.OpenerInternal:
		file := File{Name: name, Path: localPath, Remote: req.Remote, Size: req.Size}
		if err := s.editor.OpenFile(ctx, file); err != nil {
			discard()
			return Result{}, failure(ErrorEditorFailed, err)
		}
	case domainpreferences.OpenerSystem:
		if err := s.launcher.OpenSystem(ctx, localPath); err != nil {
			discard()
			return Result{}, failure(ErrorLaunch, err)
		}
	case domainpreferences.OpenerExternal:
		if err := s.launcher.OpenApplication(ctx, opener.Application.Path, localPath); err != nil {
			discard()
			return Result{}, failure(ErrorLaunch, err)
		}
	default:
		discard()
		return Result{}, failure(ErrorSource, fmt.Errorf("unknown opener %q", opener.Kind))
	}
	return result, nil
}

// materialize downloads a remote file into its own temp directory and returns
// the directory and local path. The directory is intentionally left in place
// after a successful open: the launched application may still be reading it. A
// failed download removes it; the caller removes it for a later failure.
func (s *Service) materialize(ctx context.Context, req Request, name string) (string, string, error) {
	if req.Source == nil {
		return "", "", failure(ErrorSource, errors.New("remote file has no source"))
	}
	source, err := req.Source(ctx)
	if err != nil {
		return "", "", failure(ErrorDownload, err)
	}
	defer source.Close()

	dir, err := os.MkdirTemp(s.tempRoot, "natcatty-open-*")
	if err != nil {
		return "", "", failure(ErrorDownload, err)
	}
	path := filepath.Join(dir, safeName(name))
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", failure(ErrorDownload, err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.RemoveAll(dir)
		return "", "", failure(ErrorDownload, copyErr)
	}
	return dir, path, nil
}

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
