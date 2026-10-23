package transfer

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Service opens panes and runs file operations. It is safe for concurrent
// use: each FileSystem serializes its own transport, and the copy engine
// holds no shared mutable state.
type Service struct {
	local     LocalOpener
	remote    RemoteOpener
	hosts     HostSource
	bookmarks BookmarkRepository
}

// Option customizes a Service.
type Option func(*Service)

// WithHosts gives the service the saved-Host source behind the remote pane's
// host picker.
func WithHosts(source HostSource) Option {
	return func(s *Service) { s.hosts = source }
}

// WithBookmarks gives the service the global bookmark repository.
func WithBookmarks(repository BookmarkRepository) Option {
	return func(s *Service) { s.bookmarks = repository }
}

// NewService builds a service over the local and remote openers. Either may
// be nil, which makes that pane kind unavailable rather than failing at open
// time.
func NewService(local LocalOpener, remote RemoteOpener, options ...Option) *Service {
	service := &Service{local: local, remote: remote}
	for _, option := range options {
		option(service)
	}
	return service
}

// LocalAvailable reports whether this build can open a local pane.
func (s *Service) LocalAvailable() bool { return s != nil && s.local != nil }

// RemoteAvailable reports whether this build can open a remote pane.
func (s *Service) RemoteAvailable() bool { return s != nil && s.remote != nil }

// OpenLocal opens a local filesystem rooted at the user's home.
func (s *Service) OpenLocal(ctx context.Context) (FileSystem, error) {
	if s == nil || s.local == nil {
		return nil, failure(KindUnsupported, domainsftp.OpCopy, "", errors.New("sftp: no local opener"))
	}
	files, err := s.local.OpenLocal(ctx)
	if err != nil {
		if _, ok := err.(*Error); ok {
			return nil, err
		}
		return nil, &Error{Kind: KindConnection, Cause: err}
	}
	return files, nil
}

// OpenRemote opens an SFTP filesystem for a saved Host.
func (s *Service) OpenRemote(ctx context.Context, hostID model.HostID, label string) (FileSystem, error) {
	if s == nil || s.remote == nil {
		return nil, failure(KindUnsupported, domainsftp.OpCopy, "", errors.New("sftp: no remote opener"))
	}
	files, err := s.remote.OpenRemote(ctx, hostID, label)
	if err != nil {
		if _, ok := err.(*Error); ok {
			return nil, err
		}
		return nil, &Error{Kind: KindConnection, Cause: err}
	}
	return files, nil
}

// Hosts lists the saved Hosts for the picker. A service without a source has
// no remote targets.
func (s *Service) Hosts(ctx context.Context) ([]HostTarget, error) {
	if s == nil || s.hosts == nil {
		return nil, nil
	}
	return s.hosts.Hosts(ctx)
}

// GlobalBookmarks loads the persisted global bookmarks. A service without a
// repository starts empty.
func (s *Service) GlobalBookmarks(ctx context.Context) (domainsftp.BookmarkSet, error) {
	if s == nil || s.bookmarks == nil {
		return domainsftp.BookmarkSet{}, nil
	}
	items, err := s.bookmarks.LoadBookmarks(ctx)
	if err != nil {
		return domainsftp.BookmarkSet{}, err
	}
	return domainsftp.NewBookmarkSet(items), nil
}

// SaveGlobalBookmarks persists the global bookmarks in display order.
func (s *Service) SaveGlobalBookmarks(ctx context.Context, set domainsftp.BookmarkSet) error {
	if s == nil || s.bookmarks == nil {
		return nil
	}
	return s.bookmarks.SaveBookmarks(ctx, set.Items())
}

// LocalBookmarks loads the persisted local-pane bookmarks. They are kept per
// device rather than shared through the global set.
func (s *Service) LocalBookmarks(ctx context.Context) (domainsftp.BookmarkSet, error) {
	if s == nil || s.bookmarks == nil {
		return domainsftp.BookmarkSet{}, nil
	}
	items, err := s.bookmarks.LoadLocalBookmarks(ctx)
	if err != nil {
		return domainsftp.BookmarkSet{}, err
	}
	return domainsftp.NewBookmarkSet(items), nil
}

// SaveLocalBookmarks persists the local-pane bookmarks in display order.
func (s *Service) SaveLocalBookmarks(ctx context.Context, set domainsftp.BookmarkSet) error {
	if s == nil || s.bookmarks == nil {
		return nil
	}
	return s.bookmarks.SaveLocalBookmarks(ctx, set.Items())
}

// List returns one directory's entries, including a typed ".." parent entry
// unless dir is a filesystem root. Ordering and hidden-file filtering are the
// pane's rules, applied by the caller.
func (s *Service) List(ctx context.Context, files FileSystem, dir string) ([]domainsftp.Entry, error) {
	if files == nil {
		return nil, failure(KindInternal, domainsftp.OpCopy, dir, errors.New("sftp: no filesystem"))
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Kind: KindCancelled, Op: domainsftp.OpCopy, Path: dir, Cause: err}
	}
	entries, err := files.List(ctx, dir)
	if err != nil {
		return nil, wrap(domainsftp.OpCopy, dir, err)
	}
	parent := files.Parent(dir)
	if parent != dir {
		entries = append([]domainsftp.Entry{{
			Name: "..",
			Path: parent,
			Kind: domainsftp.KindDir,
		}}, entries...)
	}
	return entries, nil
}

// Delete removes entries. A non-empty directory requires recursive, which
// the UI only sets after an explicit confirmation.
func (s *Service) Delete(ctx context.Context, files FileSystem, paths []string, recursive bool) error {
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return &Error{Kind: KindCancelled, Op: domainsftp.OpDelete, Path: path, Cause: err}
		}
		info, err := files.Stat(ctx, path)
		if err != nil {
			return wrap(domainsftp.OpDelete, path, err)
		}
		if info.IsDir() {
			if !recursive {
				children, listErr := files.List(ctx, path)
				if listErr != nil {
					return wrap(domainsftp.OpDelete, path, listErr)
				}
				if len(children) > 0 {
					return failure(KindNotEmpty, domainsftp.OpDelete, path, nil)
				}
				err = files.Remove(ctx, path)
			} else {
				err = files.RemoveAll(ctx, path)
			}
		} else {
			err = files.Remove(ctx, path)
		}
		if err != nil {
			return wrap(domainsftp.OpDelete, path, err)
		}
	}
	return nil
}

// Rename renames one entry within its directory. The new name is one path
// component; an existing destination is refused.
func (s *Service) Rename(ctx context.Context, files FileSystem, path, newName string) error {
	if err := domainsftp.ValidateName(newName); err != nil {
		return &Error{Kind: KindInvalidName, Op: domainsftp.OpRename, Path: newName, Cause: err}
	}
	destination := files.Join(files.Parent(path), newName)
	if destination == path {
		return nil
	}
	if _, err := files.Stat(ctx, destination); err == nil {
		return failure(KindExists, domainsftp.OpRename, destination, nil)
	}
	if err := files.Rename(ctx, path, destination); err != nil {
		return wrap(domainsftp.OpRename, path, err)
	}
	return nil
}

// Mkdir creates one directory inside dir.
func (s *Service) Mkdir(ctx context.Context, files FileSystem, dir, name string) (string, error) {
	if err := domainsftp.ValidateName(name); err != nil {
		return "", &Error{Kind: KindInvalidName, Op: domainsftp.OpMkdir, Path: name, Cause: err}
	}
	path := files.Join(dir, name)
	if err := files.Mkdir(ctx, path); err != nil {
		return "", wrap(domainsftp.OpMkdir, path, err)
	}
	return path, nil
}

// CreateFile creates an empty file inside dir.
func (s *Service) CreateFile(ctx context.Context, files FileSystem, dir, name string) (string, error) {
	if err := domainsftp.ValidateName(name); err != nil {
		return "", &Error{Kind: KindInvalidName, Op: domainsftp.OpCreateFile, Path: name, Cause: err}
	}
	path := files.Join(dir, name)
	writer, err := files.Create(ctx, path)
	if err != nil {
		return "", wrap(domainsftp.OpCreateFile, path, err)
	}
	if err := writer.Close(); err != nil {
		return "", wrap(domainsftp.OpCreateFile, path, err)
	}
	return path, nil
}

// Chmod replaces one entry's permission bits.
func (s *Service) Chmod(ctx context.Context, files FileSystem, path string, mode uint32) error {
	if mode&^uint32(domainsftp.MaxMode) != 0 {
		return failure(KindInvalidMode, domainsftp.OpChmod, path, nil)
	}
	if err := files.Chmod(ctx, path, mode); err != nil {
		return wrap(domainsftp.OpChmod, path, err)
	}
	return nil
}
