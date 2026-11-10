package plugin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Local file access modes accepted by files.open.
const (
	FileModeRead      = "read"
	FileModeWrite     = "write"
	FileModeReadWrite = "read-write"
)

// FilePreopen is one directory the host has granted to the plugin. Path is
// the canonical absolute directory; Read and Write report the granted access
// classes. A plugin can reach nothing outside the union of its preopens.
type FilePreopen struct {
	Path  string `json:"path"`
	Read  bool   `json:"read"`
	Write bool   `json:"write"`
}

// FileEntry describes one local directory entry or file.
type FileEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Mode     uint32    `json:"mode"`
	Modified time.Time `json:"modified"`
}

// filePreopen keeps the exact grant scopes beside the canonical root. The
// canonical path is used for filesystem containment; the original scope is
// used for the default-deny authority check, because a grant is compared
// against the scope the user consented to.
type filePreopen struct {
	FilePreopen
	readScope  domainplugin.Scope
	writeScope domainplugin.Scope
}

// localFileHandle is one open local file issued to a guest. The stored path is
// already canonical and contained by root.
type localFileHandle struct {
	host  *HostServices
	file  *os.File
	path  string
	root  string
	read  bool
	write bool
	scope domainplugin.Scope
}

// fileLockRegistry is the host-wide advisory lock table. Locks are keyed by
// canonical path, so two handles reached through different preopens or through
// a symlinked spelling still contend on the same file.
type fileLockRegistry struct {
	mu    sync.Mutex
	paths map[string]*pathLock
}

type pathLock struct {
	exclusive *localFileHandle
	shared    map[*localFileHandle]bool
}

func newFileLockRegistry() *fileLockRegistry {
	return &fileLockRegistry{paths: make(map[string]*pathLock)}
}

// lock acquires a shared or exclusive advisory lock for f. Re-locking by the
// same handle is idempotent; an incompatible holder yields a conflict.
func (r *fileLockRegistry) lock(f *localFileHandle, exclusive bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.paths[f.path]
	if entry == nil {
		entry = &pathLock{shared: make(map[*localFileHandle]bool)}
		r.paths[f.path] = entry
	}
	if exclusive {
		if entry.exclusive != nil && entry.exclusive != f {
			return workspaceConflict("file %s is already locked exclusively", f.path)
		}
		for holder := range entry.shared {
			if holder != f {
				return workspaceConflict("file %s has a shared lock", f.path)
			}
		}
		entry.exclusive = f
		delete(entry.shared, f)
		return nil
	}
	if entry.exclusive != nil && entry.exclusive != f {
		return workspaceConflict("file %s is already locked exclusively", f.path)
	}
	// Downgrading an exclusive lock to shared is allowed.
	entry.exclusive = nil
	entry.shared[f] = true
	return nil
}

// unlock releases every lock f holds on its path.
func (r *fileLockRegistry) unlock(f *localFileHandle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseLocked(f)
}

// release drops every lock held by f across every path. It runs when the
// handle closes or its capability epoch is revoked.
func (r *fileLockRegistry) release(f *localFileHandle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseLocked(f)
}

func (r *fileLockRegistry) releaseLocked(f *localFileHandle) {
	entry := r.paths[f.path]
	if entry == nil {
		return
	}
	if entry.exclusive == f {
		entry.exclusive = nil
	}
	delete(entry.shared, f)
	if entry.exclusive == nil && len(entry.shared) == 0 {
		delete(r.paths, f.path)
	}
}

// close closes the file, releases its locks, and is idempotent.
func (f *localFileHandle) close() {
	if f == nil {
		return
	}
	if f.file != nil {
		_ = f.file.Close()
	}
	if f.host != nil && f.host.fileLocks != nil {
		f.host.fileLocks.release(f)
	}
}

// FilePreopens lists the canonical directories this session may reach in
// deterministic order. It never discloses an ungranted path.
func (s *ServiceSession) FilePreopens() ([]FilePreopen, error) {
	preopens := s.effectivePreopens()
	out := make([]FilePreopen, 0, len(preopens))
	for _, preopen := range preopens {
		out = append(out, preopen.FilePreopen)
	}
	s.recordSuccess(ServiceFiles, "preopens", "", domainplugin.Scope{})
	return out, nil
}

// effectivePreopens merges all files.read and files.write grants into
// canonical directory roots. Two grants that spell the same directory through
// different symlinks merge into one root with both access classes.
func (s *ServiceSession) effectivePreopens() []filePreopen {
	if s == nil || s.host == nil || s.host.authority == nil {
		return nil
	}
	merged := make(map[string]*filePreopen)
	for _, grant := range s.host.authority.Grants(s.subject) {
		if grant.Pending {
			continue
		}
		if grant.Permission != domainplugin.PermissionFilesRead &&
			grant.Permission != domainplugin.PermissionFilesWrite {
			continue
		}
		if grant.Scope.Kind != domainplugin.ScopeFiles || grant.Scope.Path == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(grant.Scope.Path)
		if err != nil {
			// A grant whose directory disappeared or became unreachable is not
			// usable; it must not expose its parent or a partial path.
			continue
		}
		resolved = filepath.Clean(resolved)
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			continue
		}
		// A grant rooted at or below a host-reserved directory can never
		// resolve a usable path, so it is not offered as a preopen.
		if s.host.reservedPath(resolved) {
			continue
		}
		entry := merged[resolved]
		if entry == nil {
			entry = &filePreopen{FilePreopen: FilePreopen{Path: resolved}}
			merged[resolved] = entry
		}
		if grant.Permission == domainplugin.PermissionFilesRead {
			entry.Read = true
			entry.readScope = grant.Scope
		} else {
			entry.Write = true
			entry.writeScope = grant.Scope
		}
	}
	out := make([]filePreopen, 0, len(merged))
	for _, entry := range merged {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// FileOpen resolves a path through the granted preopens and returns a handle
// for range reads and writes. A relative path resolves against each preopen in
// deterministic order; an absolute path must already lie inside one.
func (s *ServiceSession) FileOpen(path, mode string) (Handle, error) {
	needRead, needWrite, err := fileMode(mode)
	if err != nil {
		s.record(ServiceFiles, "open", "", domainplugin.Scope{}, ServiceCodeInvalid)
		return 0, serviceError(ServiceFiles, "open", "", domainplugin.Scope{}, err)
	}
	preopen, resolved, err := s.resolveFilePath(path, needRead, needWrite)
	if err != nil {
		return 0, s.recordFailure(ServiceFiles, "open", filePermission(needRead, needWrite),
			fileScope(preopen, needRead), err)
	}
	permission, scope := filePermission(needRead, needWrite), fileScope(preopen, needRead)
	if needRead {
		if err := s.check(ServiceFiles, "open", domainplugin.PermissionFilesRead, preopen.readScope); err != nil {
			return 0, err
		}
	}
	if needWrite {
		if err := s.check(ServiceFiles, "open", domainplugin.PermissionFilesWrite, preopen.writeScope); err != nil {
			return 0, err
		}
	}
	flags := os.O_RDONLY
	switch {
	case needRead && needWrite:
		flags = os.O_RDWR | os.O_CREATE
	case needWrite:
		flags = os.O_WRONLY | os.O_CREATE
	}
	file, err := os.OpenFile(resolved, flags, 0o600)
	if err != nil {
		return 0, s.recordFailure(ServiceFiles, "open", permission, scope, mapFileError(resolved, err))
	}
	s.mu.Lock()
	if s.files == nil {
		s.files = make(map[Handle]*localFileHandle)
	}
	if len(s.files) >= s.host.limits.FileHandles {
		s.mu.Unlock()
		_ = file.Close()
		return 0, s.recordFailure(ServiceFiles, "open", permission, scope,
			workspaceLimit("at most %d open files per plugin instance", s.host.limits.FileHandles))
	}
	if s.handles == nil {
		s.mu.Unlock()
		_ = file.Close()
		return 0, s.recordFailure(ServiceFiles, "open", permission, scope,
			fmt.Errorf("%w: handle table is not bound", ErrServiceUnavailable))
	}
	entry := &localFileHandle{
		host: s.host, file: file, path: resolved, root: preopen.Path,
		read: needRead, write: needWrite, scope: scope,
	}
	handle, err := s.handles.Issue(entry)
	if err != nil {
		s.mu.Unlock()
		_ = file.Close()
		return 0, s.recordFailure(ServiceFiles, "open", permission, scope, err)
	}
	s.files[handle] = entry
	s.mu.Unlock()
	s.recordSuccess(ServiceFiles, "open", permission, scope)
	return handle, nil
}

// FileRead returns at most length bytes at offset. eof reports whether the
// returned range reached the end of the file. length is capped by
// ServiceLimits.FileChunkBytes so one call cannot copy an unbounded buffer.
func (s *ServiceSession) FileRead(handle Handle, offset int64, length int) ([]byte, bool, error) {
	entry, err := s.openFile(handle, true)
	if err != nil {
		return nil, false, s.recordFailure(ServiceFiles, "read",
			domainplugin.PermissionFilesRead, domainplugin.Scope{}, err)
	}
	if offset < 0 {
		return nil, false, s.recordFailure(ServiceFiles, "read",
			domainplugin.PermissionFilesRead, entry.scope, workspaceInvalid("read offset must not be negative"))
	}
	if length < 0 || length > s.host.limits.FileChunkBytes {
		return nil, false, s.recordFailure(ServiceFiles, "read",
			domainplugin.PermissionFilesRead, entry.scope,
			workspaceLimit("read length must be between 0 and %d bytes", s.host.limits.FileChunkBytes))
	}
	if err := s.check(ServiceFiles, "read", domainplugin.PermissionFilesRead, entry.scope); err != nil {
		return nil, false, err
	}
	buffer := make([]byte, length)
	n, readErr := entry.file.ReadAt(buffer, offset)
	eof := errors.Is(readErr, io.EOF)
	if readErr != nil && !eof {
		return nil, false, s.recordFailure(ServiceFiles, "read",
			domainplugin.PermissionFilesRead, entry.scope, mapFileError(entry.path, readErr))
	}
	s.recordSuccess(ServiceFiles, "read", domainplugin.PermissionFilesRead, entry.scope)
	return buffer[:n], eof, nil
}

// FileWrite writes data at offset and returns the bytes written. One call is
// capped by ServiceLimits.FileChunkBytes.
func (s *ServiceSession) FileWrite(handle Handle, offset int64, data []byte) (int, error) {
	entry, err := s.openFile(handle, false)
	if err != nil {
		return 0, s.recordFailure(ServiceFiles, "write",
			domainplugin.PermissionFilesWrite, domainplugin.Scope{}, err)
	}
	if offset < 0 {
		return 0, s.recordFailure(ServiceFiles, "write",
			domainplugin.PermissionFilesWrite, entry.scope, workspaceInvalid("write offset must not be negative"))
	}
	if len(data) > s.host.limits.FileChunkBytes {
		return 0, s.recordFailure(ServiceFiles, "write",
			domainplugin.PermissionFilesWrite, entry.scope,
			workspaceLimit("write length must not exceed %d bytes", s.host.limits.FileChunkBytes))
	}
	if err := s.check(ServiceFiles, "write", domainplugin.PermissionFilesWrite, entry.scope); err != nil {
		return 0, err
	}
	n, writeErr := entry.file.WriteAt(data, offset)
	if writeErr != nil {
		return n, s.recordFailure(ServiceFiles, "write",
			domainplugin.PermissionFilesWrite, entry.scope, mapFileError(entry.path, writeErr))
	}
	s.recordSuccess(ServiceFiles, "write", domainplugin.PermissionFilesWrite, entry.scope)
	return n, nil
}

// FileTruncate sets the open file's length. It requires files.write.
func (s *ServiceSession) FileTruncate(handle Handle, size int64) error {
	entry, err := s.openFile(handle, false)
	if err != nil {
		return s.recordFailure(ServiceFiles, "truncate",
			domainplugin.PermissionFilesWrite, domainplugin.Scope{}, err)
	}
	if size < 0 {
		return s.recordFailure(ServiceFiles, "truncate",
			domainplugin.PermissionFilesWrite, entry.scope, workspaceInvalid("size must not be negative"))
	}
	if err := s.check(ServiceFiles, "truncate", domainplugin.PermissionFilesWrite, entry.scope); err != nil {
		return err
	}
	if err := entry.file.Truncate(size); err != nil {
		return s.recordFailure(ServiceFiles, "truncate",
			domainplugin.PermissionFilesWrite, entry.scope, mapFileError(entry.path, err))
	}
	s.recordSuccess(ServiceFiles, "truncate", domainplugin.PermissionFilesWrite, entry.scope)
	return nil
}

// FileSync flushes the open file to stable storage. It requires files.write.
func (s *ServiceSession) FileSync(handle Handle) error {
	entry, err := s.openFile(handle, false)
	if err != nil {
		return s.recordFailure(ServiceFiles, "sync",
			domainplugin.PermissionFilesWrite, domainplugin.Scope{}, err)
	}
	if err := s.check(ServiceFiles, "sync", domainplugin.PermissionFilesWrite, entry.scope); err != nil {
		return err
	}
	if err := entry.file.Sync(); err != nil {
		return s.recordFailure(ServiceFiles, "sync",
			domainplugin.PermissionFilesWrite, entry.scope, mapFileError(entry.path, err))
	}
	s.recordSuccess(ServiceFiles, "sync", domainplugin.PermissionFilesWrite, entry.scope)
	return nil
}

// FileStat reports one path's metadata. It requires files.read and stays
// inside the granted preopens.
func (s *ServiceSession) FileStat(path string) (FileEntry, error) {
	preopen, resolved, err := s.resolveFilePath(path, true, false)
	if err != nil {
		return FileEntry{}, s.recordFailure(ServiceFiles, "stat",
			domainplugin.PermissionFilesRead, fileScope(preopen, true), err)
	}
	scope := preopen.readScope
	if err := s.check(ServiceFiles, "stat", domainplugin.PermissionFilesRead, scope); err != nil {
		return FileEntry{}, err
	}
	entry, err := statFileEntry(resolved)
	if err != nil {
		return FileEntry{}, s.recordFailure(ServiceFiles, "stat",
			domainplugin.PermissionFilesRead, scope, err)
	}
	s.recordSuccess(ServiceFiles, "stat", domainplugin.PermissionFilesRead, scope)
	return entry, nil
}

// FileList returns the entries of one directory. It requires files.read and
// is capped by ServiceLimits.DirectoryEntries.
func (s *ServiceSession) FileList(path string) ([]FileEntry, error) {
	preopen, resolved, err := s.resolveFilePath(path, true, false)
	if err != nil {
		return nil, s.recordFailure(ServiceFiles, "list",
			domainplugin.PermissionFilesRead, fileScope(preopen, true), err)
	}
	scope := preopen.readScope
	if err := s.check(ServiceFiles, "list", domainplugin.PermissionFilesRead, scope); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, s.recordFailure(ServiceFiles, "list",
			domainplugin.PermissionFilesRead, scope, mapFileError(resolved, err))
	}
	if len(entries) > s.host.limits.DirectoryEntries {
		return nil, s.recordFailure(ServiceFiles, "list",
			domainplugin.PermissionFilesRead, scope,
			workspaceLimit("directory has more than %d entries", s.host.limits.DirectoryEntries))
	}
	out := make([]FileEntry, 0, len(entries))
	for _, dirEntry := range entries {
		entryPath := filepath.Join(resolved, dirEntry.Name())
		if s.host.reservedPath(entryPath) {
			continue
		}
		info, infoErr := dirEntry.Info()
		if infoErr != nil {
			return nil, s.recordFailure(ServiceFiles, "list",
				domainplugin.PermissionFilesRead, scope, mapFileError(resolved, infoErr))
		}
		out = append(out, fileEntryFromInfo(entryPath, info))
	}
	s.recordSuccess(ServiceFiles, "list", domainplugin.PermissionFilesRead, scope)
	return out, nil
}

// FileRename moves a file or directory inside the granted preopens. Both
// paths must resolve under write-granted roots.
func (s *ServiceSession) FileRename(from, to string) error {
	fromPreopen, fromResolved, err := s.resolveFilePath(from, false, true)
	if err != nil {
		return s.recordFailure(ServiceFiles, "rename",
			domainplugin.PermissionFilesWrite, fileScope(fromPreopen, false), err)
	}
	toPreopen, toResolved, err := s.resolveFilePath(to, false, true)
	if err != nil {
		return s.recordFailure(ServiceFiles, "rename",
			domainplugin.PermissionFilesWrite, fileScope(toPreopen, false), err)
	}
	if s.host.containsReserved(fromResolved) || s.host.containsReserved(toResolved) {
		return s.recordFailure(ServiceFiles, "rename",
			domainplugin.PermissionFilesWrite, fileScope(fromPreopen, false),
			workspaceDenied("path contains a host-reserved directory"))
	}
	scope := fromPreopen.writeScope
	if err := s.check(ServiceFiles, "rename", domainplugin.PermissionFilesWrite, fromPreopen.writeScope); err != nil {
		return err
	}
	if !fromPreopen.writeScope.Equal(toPreopen.writeScope) {
		if err := s.check(ServiceFiles, "rename", domainplugin.PermissionFilesWrite, toPreopen.writeScope); err != nil {
			return err
		}
	}
	if err := os.Rename(fromResolved, toResolved); err != nil {
		return s.recordFailure(ServiceFiles, "rename",
			domainplugin.PermissionFilesWrite, scope, mapFileError(fromResolved, err))
	}
	s.recordSuccess(ServiceFiles, "rename", domainplugin.PermissionFilesWrite, scope)
	return nil
}

// FileUnlink removes one file. It requires files.write and refuses
// directories; removing a tree is not part of the file service.
func (s *ServiceSession) FileUnlink(path string) error {
	preopen, resolved, err := s.resolveFilePath(path, false, true)
	if err != nil {
		return s.recordFailure(ServiceFiles, "unlink",
			domainplugin.PermissionFilesWrite, fileScope(preopen, false), err)
	}
	scope := preopen.writeScope
	if err := s.check(ServiceFiles, "unlink", domainplugin.PermissionFilesWrite, scope); err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return s.recordFailure(ServiceFiles, "unlink",
			domainplugin.PermissionFilesWrite, scope, mapFileError(resolved, err))
	}
	if info.IsDir() {
		return s.recordFailure(ServiceFiles, "unlink",
			domainplugin.PermissionFilesWrite, scope, workspaceInvalid("unlink requires a file"))
	}
	if err := os.Remove(resolved); err != nil {
		return s.recordFailure(ServiceFiles, "unlink",
			domainplugin.PermissionFilesWrite, scope, mapFileError(resolved, err))
	}
	s.recordSuccess(ServiceFiles, "unlink", domainplugin.PermissionFilesWrite, scope)
	return nil
}

// FileClose releases one open handle and every advisory lock it holds.
func (s *ServiceSession) FileClose(handle Handle) error {
	if s == nil {
		return serviceError(ServiceFiles, "close", "", domainplugin.Scope{},
			fmt.Errorf("%w: service session is nil", ErrServiceUnavailable))
	}
	s.mu.Lock()
	entry, ok := s.files[handle]
	var handles *HandleTable
	if ok {
		delete(s.files, handle)
		handles = s.handles
	}
	s.mu.Unlock()
	if !ok {
		return s.recordFailure(ServiceFiles, "close", "", domainplugin.Scope{},
			workspaceInvalid("handle is not an open file"))
	}
	if handles != nil {
		_ = handles.Release(handle)
	}
	entry.close()
	s.recordSuccess(ServiceFiles, "close", "", domainplugin.Scope{})
	return nil
}

// FileLock acquires an advisory lock on an open file. A shared lock needs
// files.read; an exclusive lock needs files.write.
func (s *ServiceSession) FileLock(handle Handle, exclusive bool) error {
	entry, err := s.openFile(handle, !exclusive)
	if err != nil {
		return s.recordFailure(ServiceFiles, "lock",
			lockPermission(exclusive), domainplugin.Scope{}, err)
	}
	permission := lockPermission(exclusive)
	if err := s.check(ServiceFiles, "lock", permission, entry.scope); err != nil {
		return err
	}
	if err := s.host.fileLocks.lock(entry, exclusive); err != nil {
		return s.recordFailure(ServiceFiles, "lock", permission, entry.scope, err)
	}
	s.recordSuccess(ServiceFiles, "lock", permission, entry.scope)
	return nil
}

// FileUnlock releases every advisory lock held by the handle.
func (s *ServiceSession) FileUnlock(handle Handle) error {
	entry, err := s.fileHandle(handle)
	if err != nil {
		return s.recordFailure(ServiceFiles, "unlock", "", domainplugin.Scope{}, err)
	}
	s.host.fileLocks.unlock(entry)
	s.recordSuccess(ServiceFiles, "unlock", "", entry.scope)
	return nil
}

// fileHandle resolves an issued handle without checking its access class.
func (s *ServiceSession) fileHandle(handle Handle) (*localFileHandle, error) {
	if s == nil {
		return nil, workspaceInvalid("service session is closed")
	}
	s.mu.Lock()
	entry, ok := s.files[handle]
	s.mu.Unlock()
	if !ok {
		return nil, workspaceInvalid("handle is not an open file")
	}
	return entry, nil
}

// openFile resolves an issued handle and checks the handle's access class.
func (s *ServiceSession) openFile(handle Handle, read bool) (*localFileHandle, error) {
	entry, err := s.fileHandle(handle)
	if err != nil {
		return nil, err
	}
	if read && !entry.read {
		return nil, workspaceDenied("the file handle is not open for reading")
	}
	if !read && !entry.write {
		return nil, workspaceDenied("the file handle is not open for writing")
	}
	return entry, nil
}

func fileMode(mode string) (read, write bool, err error) {
	switch mode {
	case FileModeRead:
		return true, false, nil
	case FileModeWrite:
		return false, true, nil
	case FileModeReadWrite:
		return true, true, nil
	default:
		return false, false, workspaceInvalid("file mode must be read, write, or read-write")
	}
}

func filePermission(read, write bool) domainplugin.Permission {
	if write {
		return domainplugin.PermissionFilesWrite
	}
	if read {
		return domainplugin.PermissionFilesRead
	}
	return ""
}

// fileScope returns the write scope when a write is requested, otherwise the
// read scope. It is used for audit rows and error envelopes.
func fileScope(preopen filePreopen, read bool) domainplugin.Scope {
	if !read && !preopen.writeScope.IsZero() {
		return preopen.writeScope
	}
	return preopen.readScope
}

func lockPermission(exclusive bool) domainplugin.Permission {
	if exclusive {
		return domainplugin.PermissionFilesWrite
	}
	return domainplugin.PermissionFilesRead
}

var fileGlobPattern = regexp.MustCompile(`[*?\[\]{}]`)

// resolveFilePath validates syntax, selects a granted preopen, and returns the
// canonical path. Path syntax errors are invalid; a path outside every
// preopen, or a symlink that escapes one, is denied.
func (s *ServiceSession) resolveFilePath(requested string, needRead, needWrite bool) (filePreopen, string, error) {
	if strings.TrimSpace(requested) == "" {
		return filePreopen{}, "", workspaceInvalid("path must not be empty")
	}
	if strings.ContainsRune(requested, 0) {
		return filePreopen{}, "", workspaceInvalid("path contains a NUL byte")
	}
	if fileGlobPattern.MatchString(requested) {
		return filePreopen{}, "", workspaceInvalid("path contains a glob")
	}
	for _, segment := range strings.Split(strings.ReplaceAll(requested, "\\", "/"), "/") {
		if segment == ".." {
			return filePreopen{}, "", workspaceInvalid("path may not contain %q", "..")
		}
	}
	preopens := s.effectivePreopens()
	candidates := make([]filePreopen, 0, len(preopens))
	for _, preopen := range preopens {
		if needRead && !preopen.Read {
			continue
		}
		if needWrite && !preopen.Write {
			continue
		}
		candidates = append(candidates, preopen)
	}
	if len(candidates) == 0 {
		return filePreopen{}, "", workspaceDenied("no files preopen grants the requested access")
	}
	if filepath.IsAbs(requested) {
		cleaned := filepath.Clean(requested)
		for _, preopen := range candidates {
			if !pathWithin(preopen.Path, cleaned) {
				continue
			}
			resolved, err := resolveFileSymlinks(cleaned)
			if err != nil {
				return preopen, "", err
			}
			if !pathWithin(preopen.Path, resolved) {
				return preopen, "", workspaceDenied("path escapes the granted preopen")
			}
			if s.host.reservedPath(resolved) {
				return preopen, "", workspaceDenied("path is reserved by the host")
			}
			return preopen, resolved, nil
		}
		return filePreopen{}, "", workspaceDenied("path is outside every granted preopen")
	}
	for _, preopen := range candidates {
		resolved, err := resolveFileSymlinks(filepath.Join(preopen.Path, requested))
		if err != nil {
			return preopen, "", err
		}
		if !pathWithin(preopen.Path, resolved) {
			return preopen, "", workspaceDenied("path escapes the granted preopen")
		}
		if s.host.reservedPath(resolved) {
			return preopen, "", workspaceDenied("path is reserved by the host")
		}
		return preopen, resolved, nil
	}
	return filePreopen{}, "", workspaceDenied("path is outside every granted preopen")
}

// resolveFileSymlinks canonicalizes the longest existing ancestor and appends
// the missing components. A dangling symlink is refused: opening through it
// would follow a target outside the preopen once the target appears.
func resolveFileSymlinks(path string) (string, error) {
	clean := filepath.Clean(path)
	current := clean
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		info, lstatErr := os.Lstat(current)
		if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", workspaceDenied("path contains an unresolved symlink")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", workspaceInvalid("path cannot be resolved")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// pathWithin reports whether path is root or a descendant of root. Both
// arguments are absolute and cleaned.
// canonicalReservedPaths resolves the host-reserved directories once at
// construction. A missing directory is still reserved: the host creates the
// audit directory lazily, and a plugin must not win that race.
func canonicalReservedPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		absolute, err := filepath.Abs(raw)
		if err != nil {
			continue
		}
		resolved, err := resolveFileSymlinks(absolute)
		if err != nil {
			resolved = filepath.Clean(absolute)
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
	}
	sort.Strings(out)
	return out
}

// reservedPath reports whether target is the host-reserved directory or lies
// inside it. No file operation may reach such a path, even through a grant.
func (h *HostServices) reservedPath(target string) bool {
	if h == nil {
		return false
	}
	for _, root := range h.reserved {
		if pathWithin(root, target) {
			return true
		}
	}
	return false
}

// containsReserved reports whether target is the host-reserved directory or
// one of its ancestors. Renaming an ancestor would move the reserved subtree,
// so that operation is denied even though the target itself is reachable.
func (h *HostServices) containsReserved(target string) bool {
	if h == nil {
		return false
	}
	for _, root := range h.reserved {
		if pathWithin(target, root) {
			return true
		}
	}
	return false
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func statFileEntry(path string) (FileEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileEntry{}, mapFileError(path, err)
	}
	return fileEntryFromInfo(path, info), nil
}

func fileEntryFromInfo(path string, info os.FileInfo) FileEntry {
	return FileEntry{
		Name:     info.Name(),
		Path:     path,
		Dir:      info.IsDir(),
		Size:     info.Size(),
		Mode:     uint32(info.Mode().Perm()),
		Modified: info.ModTime().UTC(),
	}
}

// mapFileError maps a filesystem failure to the closed service codes without
// disclosing host paths in the message.
func mapFileError(path string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return workspaceNotFound("path does not exist")
	case errors.Is(err, os.ErrExist):
		return workspaceConflict("path already exists")
	case errors.Is(err, os.ErrPermission):
		return workspaceDenied("host denied access to the path")
	default:
		return &workspaceFailure{Code: ServiceCodeFault,
			Problem: "filesystem operation failed", Cause: err}
	}
}
