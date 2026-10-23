// Package sftp adapts the transfer application service to real filesystems:
// the local OS filesystem and a remote SFTP subsystem. The local adapter uses
// os; the remote adapter speaks pkg/sftp over a stream the SSH adapter opened.
package sftp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Local opens the OS filesystem for the local pane.
type Local struct {
	home func() (string, error)
}

// NewLocal returns the local opener. The app wires it once.
func NewLocal() *Local {
	return &Local{home: os.UserHomeDir}
}

// OpenLocal opens the user's home directory.
func (l *Local) OpenLocal(ctx context.Context) (apptransfer.FileSystem, error) {
	home := ""
	if l != nil && l.home != nil {
		if resolved, err := l.home(); err == nil && resolved != "" {
			home = resolved
		}
	}
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	return &localFiles{home: home}, nil
}

// localFiles is one local pane's filesystem over os. Its path notation
// follows the host platform.
type localFiles struct {
	home string
}

var _ apptransfer.FileSystem = (*localFiles)(nil)

// Root returns the start directory: the user's home, or the current
// directory when the home could not be resolved.
func (f *localFiles) Root(context.Context) (string, error) {
	if f.home != "" {
		return f.home, nil
	}
	return os.Getwd()
}

// List reads one directory. Symlinks are resolved so the entry's Kind is
// navigable; a dangling link keeps KindLink.
func (f *localFiles) List(ctx context.Context, dir string) ([]domainsftp.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]domainsftp.Entry, 0, len(items))
	for _, item := range items {
		path := filepath.Join(dir, item.Name())
		entry, err := statEntry(path, item.Name())
		if err != nil {
			// A file removed between ReadDir and stat is omitted rather than
			// failing the whole listing.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		entry.Hidden = entry.Hidden || hiddenAttribute(path)
		entries = append(entries, entry)
	}
	return entries, nil
}

// Stat reads one entry, following symlinks.
func (f *localFiles) Stat(ctx context.Context, path string) (domainsftp.Entry, error) {
	if err := ctx.Err(); err != nil {
		return domainsftp.Entry{}, err
	}
	entry, err := statEntry(path, filepath.Base(path))
	if err != nil {
		return domainsftp.Entry{}, err
	}
	entry.Hidden = entry.Hidden || hiddenAttribute(path)
	return entry, nil
}

// statEntry inspects one path. Lstat detects the link, Stat resolves it.
func statEntry(path, name string) (domainsftp.Entry, error) {
	link, linkErr := os.Lstat(path)
	if linkErr != nil {
		return domainsftp.Entry{}, linkErr
	}
	symlink := link.Mode()&os.ModeSymlink != 0
	target := ""
	if symlink {
		target, _ = os.Readlink(path)
	}
	info, err := os.Stat(path)
	if err != nil {
		if symlink {
			return domainsftp.Entry{
				Name:       name,
				Path:       path,
				Kind:       domainsftp.KindLink,
				Symlink:    true,
				LinkTarget: target,
				Mode:       modeBits(link.Mode()),
				ModTime:    link.ModTime(),
			}, nil
		}
		return domainsftp.Entry{}, err
	}
	return domainsftp.Entry{
		Name:       name,
		Path:       path,
		Kind:       kindOf(info.Mode()),
		Symlink:    symlink,
		LinkTarget: target,
		Size:       info.Size(),
		Mode:       modeBits(info.Mode()),
		ModTime:    info.ModTime(),
	}, nil
}

// kindOf maps an os.FileMode to the browser's entry kind.
func kindOf(mode os.FileMode) domainsftp.Kind {
	switch {
	case mode.IsDir():
		return domainsftp.KindDir
	case mode.IsRegular():
		return domainsftp.KindFile
	case mode&os.ModeSymlink != 0:
		return domainsftp.KindLink
	default:
		return domainsftp.KindOther
	}
}

// modeBits converts the portable permission bits onto the wire layout the
// browser displays.
func modeBits(mode os.FileMode) uint32 {
	bits := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= domainsftp.ModeSetuid
	}
	if mode&os.ModeSetgid != 0 {
		bits |= domainsftp.ModeSetgid
	}
	if mode&os.ModeSticky != 0 {
		bits |= domainsftp.ModeSticky
	}
	return bits
}

// Mkdir creates one directory.
func (f *localFiles) Mkdir(ctx context.Context, path string) error {
	return os.Mkdir(path, 0o755)
}

// MkdirAll creates a directory tree.
func (f *localFiles) MkdirAll(ctx context.Context, path string) error {
	return os.MkdirAll(path, 0o755)
}

// Open starts reading a file.
func (f *localFiles) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return os.Open(path)
}

// Create starts writing a file, creating or truncating it.
func (f *localFiles) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}

// Remove removes a file or empty directory.
func (f *localFiles) Remove(ctx context.Context, path string) error {
	return os.Remove(path)
}

// RemoveAll removes a directory tree.
func (f *localFiles) RemoveAll(ctx context.Context, path string) error {
	return os.RemoveAll(path)
}

// Rename moves an entry within its filesystem.
func (f *localFiles) Rename(ctx context.Context, oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

// Chmod replaces permission bits. Windows only carries the read-only bit, so
// the OS layer applies what it can and the call still succeeds.
func (f *localFiles) Chmod(ctx context.Context, path string, mode uint32) error {
	perm := os.FileMode(mode & 0o777)
	if mode&domainsftp.ModeSetuid != 0 {
		perm |= os.ModeSetuid
	}
	if mode&domainsftp.ModeSetgid != 0 {
		perm |= os.ModeSetgid
	}
	if mode&domainsftp.ModeSticky != 0 {
		perm |= os.ModeSticky
	}
	return os.Chmod(path, perm)
}

// Join joins path components in the platform's notation.
func (f *localFiles) Join(parts ...string) string { return filepath.Join(parts...) }

// Parent returns the containing directory.
func (f *localFiles) Parent(path string) string { return filepath.Dir(path) }

// Base returns the last component.
func (f *localFiles) Base(path string) string { return filepath.Base(path) }

// Style reports the platform's notation.
func (f *localFiles) Style() apptransfer.PathStyle {
	if runtime.GOOS == "windows" {
		return apptransfer.StyleWindows
	}
	return apptransfer.StylePOSIX
}

// Close releases the local filesystem; it holds no handles.
func (f *localFiles) Close() error { return nil }
