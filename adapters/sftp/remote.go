package sftp

import (
	"context"
	"io"
	"os"
	"path"
	"strconv"
	"sync"

	"github.com/pkg/sftp"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Remote is one remote pane's SFTP filesystem. It owns the protocol client
// and the SSH lease behind it: closing it releases a healthy transport back
// to the pool, or discards one that already failed.
type Remote struct {
	client  *sftp.Client
	release func(error) error
	mu      sync.Mutex
	closed  bool
}

// NewRemote wraps an open SFTP subsystem stream. release is called exactly
// once from Close with the client's close error; the app connector uses it to
// release or discard the Connection lease.
func NewRemote(stream io.ReadWriteCloser, release func(error) error) (*Remote, error) {
	client, err := sftp.NewClientPipe(stream, stream,
		sftp.MaxPacket(32*1024),
		sftp.MaxConcurrentRequestsPerFile(64),
		sftp.UseConcurrentReads(true),
		sftp.UseConcurrentWrites(true),
	)
	if err != nil {
		_ = stream.Close()
		if release != nil {
			_ = release(err)
		}
		return nil, err
	}
	return &Remote{client: client, release: release}, nil
}

var _ apptransfer.FileSystem = (*Remote)(nil)

// Root returns the session's working directory.
func (r *Remote) Root(ctx context.Context) (string, error) {
	if r == nil || r.client == nil {
		return "", os.ErrClosed
	}
	return r.client.Getwd()
}

// List reads one remote directory.
func (r *Remote) List(ctx context.Context, dir string) ([]domainsftp.Entry, error) {
	items, err := r.client.ReadDirContext(ctx, dir)
	if err != nil {
		return nil, err
	}
	entries := make([]domainsftp.Entry, 0, len(items))
	for _, item := range items {
		entry := r.entryFor(dir, item)
		entries = append(entries, entry)
	}
	return entries, nil
}

// Stat reads one remote entry, following symlinks.
func (r *Remote) Stat(ctx context.Context, path string) (domainsftp.Entry, error) {
	if err := ctx.Err(); err != nil {
		return domainsftp.Entry{}, err
	}
	link, err := r.client.Lstat(path)
	if err != nil {
		return domainsftp.Entry{}, err
	}
	symlink := link.Mode()&os.ModeSymlink != 0
	target := ""
	if symlink {
		target, _ = r.client.ReadLink(path)
	}
	info := link
	resolved, statErr := r.client.Stat(path)
	if statErr == nil {
		info = resolved
	} else if !symlink {
		return domainsftp.Entry{}, statErr
	}
	return r.entryFrom(path, info, symlink, target), nil
}

// entryFor builds a listing row and resolves a symlink's target kind.
func (r *Remote) entryFor(dir string, item os.FileInfo) domainsftp.Entry {
	full := path.Join(dir, item.Name())
	symlink := item.Mode()&os.ModeSymlink != 0
	target := ""
	if symlink {
		target, _ = r.client.ReadLink(full)
		if resolved, err := r.client.Stat(full); err == nil {
			item = resolved
		}
	}
	return r.entryFrom(full, item, symlink, target)
}

// entryFrom converts one os.FileInfo into a browser entry.
func (r *Remote) entryFrom(full string, info os.FileInfo, symlink bool, target string) domainsftp.Entry {
	name := path.Base(full)
	kind := kindOf(info.Mode())
	if symlink && info.Mode()&os.ModeSymlink != 0 {
		kind = domainsftp.KindLink
	}
	entry := domainsftp.Entry{
		Name:       name,
		Path:       full,
		Kind:       kind,
		Symlink:    symlink,
		LinkTarget: target,
		Size:       info.Size(),
		Mode:       modeBits(info.Mode()),
		ModTime:    info.ModTime(),
		Hidden:     domainsftp.HiddenName(name),
	}
	if typed, ok := info.Sys().(*sftp.FileStat); ok {
		entry.Owner = strconv.FormatUint(uint64(typed.UID), 10)
		entry.Group = strconv.FormatUint(uint64(typed.GID), 10)
	}
	return entry
}

// Mkdir creates one directory.
func (r *Remote) Mkdir(ctx context.Context, path string) error {
	return r.client.Mkdir(path)
}

// MkdirAll creates a directory tree.
func (r *Remote) MkdirAll(ctx context.Context, path string) error {
	return r.client.MkdirAll(path)
}

// Open starts reading a remote file.
func (r *Remote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return r.client.Open(path)
}

// Create starts writing a remote file.
func (r *Remote) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	return r.client.Create(path)
}

// Remove removes a file or empty directory.
func (r *Remote) Remove(ctx context.Context, path string) error {
	return r.client.Remove(path)
}

// RemoveAll removes a directory tree.
func (r *Remote) RemoveAll(ctx context.Context, path string) error {
	return r.client.RemoveAll(path)
}

// Rename moves an entry. posix-rename@openssh.com is preferred because it is
// atomic and may replace; a server without it falls back to the standard
// rename.
func (r *Remote) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := r.client.PosixRename(oldPath, newPath); err == nil {
		return nil
	}
	return r.client.Rename(oldPath, newPath)
}

// Chmod replaces permission bits.
func (r *Remote) Chmod(ctx context.Context, path string, mode uint32) error {
	return r.client.Chmod(path, fileMode(mode))
}

// fileMode converts wire permission bits onto os.FileMode.
func fileMode(mode uint32) os.FileMode {
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
	return perm
}

// Join joins remote path components with "/".
func (r *Remote) Join(parts ...string) string { return path.Join(parts...) }

// Parent returns the containing remote directory.
func (r *Remote) Parent(p string) string { return path.Dir(p) }

// Base returns the last remote component.
func (r *Remote) Base(p string) string { return path.Base(p) }

// Style reports POSIX notation.
func (r *Remote) Style() apptransfer.PathStyle { return apptransfer.StylePOSIX }

// Close closes the protocol client and then the SSH lease. It is safe to
// call more than once.
func (r *Remote) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	var err error
	if r.client != nil {
		err = r.client.Close()
	}
	if r.release != nil {
		_ = r.release(err)
	}
	return err
}
