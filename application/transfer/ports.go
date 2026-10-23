// Package transfer is the file-browser application service. It opens local and
// remote filesystems through consumer-owned ports, lists directories, and
// runs copy, move, delete, rename, create, and permission operations with
// progress and cancellation. It owns no UI and no transport.
package transfer

import (
	"context"
	"io"

	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// FileSystem is one pane's directory transport. A local adapter and a remote
// SFTP adapter both implement it, so the copy engine treats a local-to-remote
// upload like any other two-ended copy. Implementations are safe for one
// operation at a time; the browser serializes calls per pane.
type FileSystem interface {
	// Root returns the directory a new pane starts in: the user's home, or
	// the closest readable ancestor.
	Root(ctx context.Context) (string, error)
	// List returns one directory's entries, including the parent entry ".."
	// when the directory is not a filesystem root. The adapter resolves
	// symlinks so Entry.Kind is navigable.
	List(ctx context.Context, dir string) ([]domainsftp.Entry, error)
	// Stat returns one entry, following symlinks.
	Stat(ctx context.Context, path string) (domainsftp.Entry, error)
	// Mkdir creates one directory; an existing directory is an error so the
	// caller can report "already exists".
	Mkdir(ctx context.Context, path string) error
	// MkdirAll creates a directory and any missing ancestors.
	MkdirAll(ctx context.Context, path string) error
	// Open starts reading a file.
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// Create starts writing a new file, truncating an existing one.
	Create(ctx context.Context, path string) (io.WriteCloser, error)
	// Remove removes a file or an empty directory.
	Remove(ctx context.Context, path string) error
	// RemoveAll removes a file or a directory tree.
	RemoveAll(ctx context.Context, path string) error
	// Rename moves one entry in place.
	Rename(ctx context.Context, oldPath, newPath string) error
	// Chmod replaces the permission bits.
	Chmod(ctx context.Context, path string, mode uint32) error
	// Join joins path components in this filesystem's notation.
	Join(parts ...string) string
	// Parent returns the containing directory, or path at a root.
	Parent(path string) string
	// Base returns the last path component.
	Base(path string) string
	// Style reports the notation Join and Parent use.
	Style() PathStyle
	// Backing returns a stable identifier for the store this filesystem
	// reads and writes. Two filesystems must share a Backing exactly when
	// they address the same tree: two local panes on one device, or two
	// connections to one Host. Different Hosts keep different Backings even
	// when their paths look identical.
	Backing() string
	// Canonical returns path in the form used to compare locations within a
	// Backing. A local filesystem resolves symlinks and returns an absolute
	// path; a remote one cleans its POSIX path.
	Canonical(path string) string
	// Close releases the transport; it is safe to call more than once.
	Close() error
}

// PathStyle is the notation a FileSystem uses.
type PathStyle string

const (
	// StylePOSIX is "/" separated, with "/" as the root.
	StylePOSIX PathStyle = "posix"
	// StyleWindows is drive-letter notation with "\" separators.
	StyleWindows PathStyle = "windows"
)

// LocalOpener opens a local filesystem for a pane. The app wires the OS
// adapter; tests wire a temporary directory or a fake.
type LocalOpener interface {
	OpenLocal(ctx context.Context) (FileSystem, error)
}

// RemoteOpener opens an SFTP filesystem for a saved Host. The app binds it to
// the connection manager, which supplies the SSH transport and its lease;
// tests bind a fake.
type RemoteOpener interface {
	OpenRemote(ctx context.Context, hostID model.HostID, label string) (FileSystem, error)
}

// HostTarget is one saved Host a remote pane can open. It is a display record
// plus the identity the RemoteOpener needs.
type HostTarget struct {
	ID       model.HostID
	Label    string
	Hostname string
	Username string
	Port     int
}

// HostSource lists the saved Hosts for the pane's host picker. The app backs
// it with the Vault; tests back it with a fixed list.
type HostSource interface {
	Hosts(ctx context.Context) ([]HostTarget, error)
}

// BookmarkRepository persists bookmark lists. The original keeps local-pane
// bookmarks on the device and shares global bookmarks across panes and
// windows; this build persists both sets on the device. A nil repository
// keeps bookmarks in memory.
type BookmarkRepository interface {
	LoadBookmarks(ctx context.Context) ([]domainsftp.Bookmark, error)
	SaveBookmarks(ctx context.Context, items []domainsftp.Bookmark) error
	LoadLocalBookmarks(ctx context.Context) ([]domainsftp.Bookmark, error)
	SaveLocalBookmarks(ctx context.Context, items []domainsftp.Bookmark) error
}
