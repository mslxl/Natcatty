package plugin

import (
	"context"
	"path"
	"strings"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// The remote file surface is Host-scoped: the consent names one Host, and
// every operation repeats the exact Host scope check. The path is validated by
// the application and interpreted by the host-mediated adapter; the guest can
// never supply a local path or escape the granted Host.

// RemoteList lists one remote directory.
func (s *ServiceSession) RemoteList(host, remotePath string) ([]RemoteEntry, error) {
	scope := hostScope(host)
	if err := s.validRemotePath(remotePath); err != nil {
		return nil, s.recordFailure(ServiceFiles, "remote_list",
			domainplugin.PermissionFilesRemoteRead, scope, err)
	}
	if err := s.check(ServiceFiles, "remote_list", domainplugin.PermissionFilesRemoteRead, scope); err != nil {
		return nil, err
	}
	if s.host.remoteFiles == nil {
		return nil, s.recordFailure(ServiceFiles, "remote_list",
			domainplugin.PermissionFilesRemoteRead, scope, unavailableRemoteFiles())
	}
	entries, err := s.host.remoteFiles.List(context.Background(), host, remotePath)
	if err != nil {
		return nil, s.recordFailure(ServiceFiles, "remote_list",
			domainplugin.PermissionFilesRemoteRead, scope, err)
	}
	s.recordSuccess(ServiceFiles, "remote_list", domainplugin.PermissionFilesRemoteRead, scope)
	return entries, nil
}

// RemoteStat reports one remote entry.
func (s *ServiceSession) RemoteStat(host, remotePath string) (RemoteEntry, error) {
	scope := hostScope(host)
	if err := s.validRemotePath(remotePath); err != nil {
		return RemoteEntry{}, s.recordFailure(ServiceFiles, "remote_stat",
			domainplugin.PermissionFilesRemoteRead, scope, err)
	}
	if err := s.check(ServiceFiles, "remote_stat", domainplugin.PermissionFilesRemoteRead, scope); err != nil {
		return RemoteEntry{}, err
	}
	if s.host.remoteFiles == nil {
		return RemoteEntry{}, s.recordFailure(ServiceFiles, "remote_stat",
			domainplugin.PermissionFilesRemoteRead, scope, unavailableRemoteFiles())
	}
	entry, ok, err := s.host.remoteFiles.Stat(context.Background(), host, remotePath)
	if err != nil {
		return RemoteEntry{}, s.recordFailure(ServiceFiles, "remote_stat",
			domainplugin.PermissionFilesRemoteRead, scope, err)
	}
	if !ok {
		return RemoteEntry{}, s.recordFailure(ServiceFiles, "remote_stat",
			domainplugin.PermissionFilesRemoteRead, scope,
			workspaceNotFound("remote path does not exist"))
	}
	s.recordSuccess(ServiceFiles, "remote_stat", domainplugin.PermissionFilesRemoteRead, scope)
	return entry, nil
}

// RemoteRead returns at most length bytes of one remote file at offset.
func (s *ServiceSession) RemoteRead(host, remotePath string, offset int64, length int) ([]byte, bool, error) {
	scope := hostScope(host)
	if err := s.validRemotePath(remotePath); err != nil {
		return nil, false, s.recordFailure(ServiceFiles, "remote_read",
			domainplugin.PermissionFilesRemoteRead, scope, err)
	}
	if offset < 0 {
		return nil, false, s.recordFailure(ServiceFiles, "remote_read",
			domainplugin.PermissionFilesRemoteRead, scope, workspaceInvalid("read offset must not be negative"))
	}
	if length < 0 || length > s.host.limits.FileChunkBytes {
		return nil, false, s.recordFailure(ServiceFiles, "remote_read",
			domainplugin.PermissionFilesRemoteRead, scope,
			workspaceLimit("read length must be between 0 and %d bytes", s.host.limits.FileChunkBytes))
	}
	if err := s.check(ServiceFiles, "remote_read", domainplugin.PermissionFilesRemoteRead, scope); err != nil {
		return nil, false, err
	}
	if s.host.remoteFiles == nil {
		return nil, false, s.recordFailure(ServiceFiles, "remote_read",
			domainplugin.PermissionFilesRemoteRead, scope, unavailableRemoteFiles())
	}
	data, eof, err := s.host.remoteFiles.Read(context.Background(), host, remotePath, offset, length)
	if err != nil {
		return nil, false, s.recordFailure(ServiceFiles, "remote_read",
			domainplugin.PermissionFilesRemoteRead, scope, err)
	}
	s.recordSuccess(ServiceFiles, "remote_read", domainplugin.PermissionFilesRemoteRead, scope)
	return data, eof, nil
}

// RemoteWrite writes data to one remote file at offset.
func (s *ServiceSession) RemoteWrite(host, remotePath string, offset int64, data []byte) (int, error) {
	scope := hostScope(host)
	if err := s.validRemotePath(remotePath); err != nil {
		return 0, s.recordFailure(ServiceFiles, "remote_write",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	if offset < 0 {
		return 0, s.recordFailure(ServiceFiles, "remote_write",
			domainplugin.PermissionFilesRemoteWrite, scope, workspaceInvalid("write offset must not be negative"))
	}
	if len(data) > s.host.limits.FileChunkBytes {
		return 0, s.recordFailure(ServiceFiles, "remote_write",
			domainplugin.PermissionFilesRemoteWrite, scope,
			workspaceLimit("write length must not exceed %d bytes", s.host.limits.FileChunkBytes))
	}
	if err := s.check(ServiceFiles, "remote_write", domainplugin.PermissionFilesRemoteWrite, scope); err != nil {
		return 0, err
	}
	if s.host.remoteFiles == nil {
		return 0, s.recordFailure(ServiceFiles, "remote_write",
			domainplugin.PermissionFilesRemoteWrite, scope, unavailableRemoteFiles())
	}
	written, err := s.host.remoteFiles.Write(context.Background(), host, remotePath, offset, data)
	if err != nil {
		return written, s.recordFailure(ServiceFiles, "remote_write",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	s.recordSuccess(ServiceFiles, "remote_write", domainplugin.PermissionFilesRemoteWrite, scope)
	return written, nil
}

// RemoteRename moves one remote path inside the same granted Host.
func (s *ServiceSession) RemoteRename(host, from, to string) error {
	scope := hostScope(host)
	if err := s.validRemotePath(from); err != nil {
		return s.recordFailure(ServiceFiles, "remote_rename",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	if err := s.validRemotePath(to); err != nil {
		return s.recordFailure(ServiceFiles, "remote_rename",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	if err := s.check(ServiceFiles, "remote_rename", domainplugin.PermissionFilesRemoteWrite, scope); err != nil {
		return err
	}
	if s.host.remoteFiles == nil {
		return s.recordFailure(ServiceFiles, "remote_rename",
			domainplugin.PermissionFilesRemoteWrite, scope, unavailableRemoteFiles())
	}
	if err := s.host.remoteFiles.Rename(context.Background(), host, from, to); err != nil {
		return s.recordFailure(ServiceFiles, "remote_rename",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	s.recordSuccess(ServiceFiles, "remote_rename", domainplugin.PermissionFilesRemoteWrite, scope)
	return nil
}

// RemoteDelete removes one remote file or directory.
func (s *ServiceSession) RemoteDelete(host, remotePath string, recursive bool) error {
	scope := hostScope(host)
	if err := s.validRemotePath(remotePath); err != nil {
		return s.recordFailure(ServiceFiles, "remote_delete",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	if err := s.check(ServiceFiles, "remote_delete", domainplugin.PermissionFilesRemoteWrite, scope); err != nil {
		return err
	}
	if s.host.remoteFiles == nil {
		return s.recordFailure(ServiceFiles, "remote_delete",
			domainplugin.PermissionFilesRemoteWrite, scope, unavailableRemoteFiles())
	}
	if err := s.host.remoteFiles.Delete(context.Background(), host, remotePath, recursive); err != nil {
		return s.recordFailure(ServiceFiles, "remote_delete",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	s.recordSuccess(ServiceFiles, "remote_delete", domainplugin.PermissionFilesRemoteWrite, scope)
	return nil
}

// RemoteMkdir creates one remote directory.
func (s *ServiceSession) RemoteMkdir(host, remotePath string) error {
	scope := hostScope(host)
	if err := s.validRemotePath(remotePath); err != nil {
		return s.recordFailure(ServiceFiles, "remote_mkdir",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	if err := s.check(ServiceFiles, "remote_mkdir", domainplugin.PermissionFilesRemoteWrite, scope); err != nil {
		return err
	}
	if s.host.remoteFiles == nil {
		return s.recordFailure(ServiceFiles, "remote_mkdir",
			domainplugin.PermissionFilesRemoteWrite, scope, unavailableRemoteFiles())
	}
	if err := s.host.remoteFiles.Mkdir(context.Background(), host, remotePath); err != nil {
		return s.recordFailure(ServiceFiles, "remote_mkdir",
			domainplugin.PermissionFilesRemoteWrite, scope, err)
	}
	s.recordSuccess(ServiceFiles, "remote_mkdir", domainplugin.PermissionFilesRemoteWrite, scope)
	return nil
}

func hostScope(host string) domainplugin.Scope {
	return domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: host}
}

func unavailableRemoteFiles() error {
	return &workspaceFailure{Code: ServiceCodeFault,
		Problem: "remote file port is not configured", Cause: ErrServiceUnavailable}
}

// validRemotePath rejects empty, NUL, glob, and traversal paths. A remote
// adapter may reject more; the application rejects everything that could be
// reinterpreted as a different path by a host shell or SFTP server.
func (s *ServiceSession) validRemotePath(remotePath string) error {
	if strings.TrimSpace(remotePath) == "" {
		return workspaceInvalid("remote path must not be empty")
	}
	if strings.ContainsRune(remotePath, 0) {
		return workspaceInvalid("remote path contains a NUL byte")
	}
	if fileGlobPattern.MatchString(remotePath) {
		return workspaceInvalid("remote path contains a glob")
	}
	normalized := strings.ReplaceAll(remotePath, "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return workspaceInvalid("remote path may not contain %q", "..")
		}
	}
	return nil
}

// Remote path helpers. They use POSIX semantics because a remote Host may be
// of any OS while the local host is not necessarily POSIX.

func remoteParent(remotePath string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(strings.ReplaceAll(remotePath, "\\", "/"), "/"))
	if cleaned == "/" {
		return "/"
	}
	return path.Dir(cleaned)
}

func remoteBase(remotePath string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(strings.ReplaceAll(remotePath, "\\", "/"), "/"))
	if cleaned == "/" {
		return "/"
	}
	return path.Base(cleaned)
}

func remoteParents(remotePath string) []string {
	var parents []string
	for {
		parent := remoteParent(remotePath)
		if parent == remotePath || parent == "/" {
			break
		}
		parents = append(parents, parent)
		remotePath = parent
	}
	return parents
}

func isRemoteDescendant(name, root string) bool {
	cleanRoot := path.Clean(root)
	if cleanRoot == "/" {
		return strings.HasPrefix(name, "/")
	}
	return strings.HasPrefix(name, cleanRoot+"/")
}
