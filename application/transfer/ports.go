// Package transfer is the file-browser application service. It opens local and
// remote filesystems through consumer-owned ports, lists directories, and
// runs copy, move, delete, rename, create, and permission operations with
// progress and cancellation. It owns no UI and no transport.
package transfer

import (
	"context"
	"io"
	"strconv"

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

// SizedCreator is an optional FileSystem capability for a destination that
// must learn the byte count before it starts writing. The SCP protocol puts
// the size in the file control line, so the copy engine streams straight to
// it when the source size is known instead of buffering the whole file.
type SizedCreator interface {
	CreateSized(ctx context.Context, path string, size int64) (io.WriteCloser, error)
}

// ReadOnlyFileSystem is an optional FileSystem capability for a backing that
// refuses every write. A pane over such a filesystem hides write actions
// instead of letting the user start an operation that cannot finish. A
// filesystem that does not implement it is assumed writable: adapters report
// read-only only when the transport knows it.
type ReadOnlyFileSystem interface {
	ReadOnly() bool
}

// ReadOnly reports whether files refuses writes. An absent filesystem, or one
// that declares itself read-only, cannot host a write.
func ReadOnly(files FileSystem) bool {
	if files == nil {
		return true
	}
	declared, ok := files.(ReadOnlyFileSystem)
	return ok && declared.ReadOnly()
}

// ArchiveExtractor is an optional FileSystem capability for a destination that
// can unpack a gzip-compressed tar stream into a directory with its own tooling
// (the remote adapters run tar over an SSH exec channel). It lets a folder
// upload travel as one stream instead of one round trip per file. The engine
// only uses it when the whole request can be extracted safely; otherwise it
// falls back to the per-file copy.
type ArchiveExtractor interface {
	ExtractTar(ctx context.Context, dir string, archive io.Reader) error
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

// RemoteTarget is one remote endpoint a pane can open. A saved Host carries
// its HostID; an unsaved Quick Connect target has an empty HostID and carries
// the endpoint and the one-shot password that opened its Session.
// RemoteJump is one jump Host in a target's ad-hoc chain, ordered first =
// closest to the client. A saved jump names a Vault Host; a jump with no
// HostID is temporary, carrying the endpoint the user typed and its one-shot
// credential. It mirrors the Session layer's jump without importing it, so
// this context keeps its architectural boundary. Everything in it is
// memory-only: a temporary jump's password is never persisted or logged.
type RemoteJump struct {
	HostID   model.HostID
	Label    string
	Hostname string
	Port     int
	Username string
	Password string
}

type RemoteTarget struct {
	HostID   model.HostID
	Label    string
	Hostname string
	Port     int
	Username string
	// Password is memory-only. It travels from the Session that already holds
	// it to the SSH handshake, and is never persisted or logged.
	Password string
	// Jumps is the ad-hoc jump chain the owning Session dialed, memory-only
	// like the password. Empty for a saved Host, whose chain is resolved
	// from the Vault.
	Jumps []RemoteJump
}

// Saved reports whether the target is a Vault Host.
func (t RemoteTarget) Saved() bool { return t.HostID != "" }

// EndpointKey identifies the target for Tab reuse. A saved Host is identified
// by its ID; an unsaved target by its endpoint and chain, not its password,
// so a second Quick Connect through the same hops follows the Tab already
// browsing it.
func (t RemoteTarget) EndpointKey() string {
	if t.Saved() {
		return "host:" + string(t.HostID)
	}
	key := "quick:" + t.Username + "@" + t.Hostname + ":" + strconv.Itoa(t.Port)
	for _, jump := range t.Jumps {
		hop := jump.Username + "@" + jump.Hostname + ":" + strconv.Itoa(jump.Port)
		if jump.HostID != "" {
			hop = "#" + string(jump.HostID)
		}
		key += ">via:" + hop
	}
	return key
}

// EndpointOpener is an optional RemoteOpener capability: an opener that can
// also dial a target with no Vault Host record. It is what lets the side panel
// browse a Quick Connect Session. An opener without it refuses such targets
// instead of pretending they are local.
type EndpointOpener interface {
	OpenEndpoint(ctx context.Context, target RemoteTarget) (FileSystem, error)
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
