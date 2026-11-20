package sftp

import (
	"context"
	"errors"
	"os"
	"strings"

	appconnection "github.com/mslxl/natcatty/application/connection"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Remote runs the archive command over an exec channel on the same Connection
// as its SFTP subsystem. It implements ArchiveCreator in every mode so a call
// can classify a missing transport, but CanArchive reports false when no
// executor is attached, which is how a single-channel Host stays unavailable.
var (
	_ apptransfer.ArchiveCreator    = (*Remote)(nil)
	_ apptransfer.ArchiveCapability = (*Remote)(nil)
)

// SCP already owns an exec capability for every operation, so archive
// creation needs no extra transport.
var (
	_ apptransfer.ArchiveCreator    = (*SCP)(nil)
	_ apptransfer.ArchiveCapability = (*SCP)(nil)
)

// CanArchive reports whether this pane has the exec channel the archive
// command needs. A single-channel Host leaves the executor off.
func (r *Remote) CanArchive() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed && r.exec != nil
}

// CanArchive reports whether SCP mode can run the archive command. NewSCP
// refuses a nil executor, so every live SCP pane has one.
func (s *SCP) CanArchive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.exec != nil
}

// CreateArchive builds the archive beside its sources on the remote host.
func (r *Remote) CreateArchive(ctx context.Context, request apptransfer.ArchiveRequest) error {
	if r == nil {
		return os.ErrClosed
	}
	r.mu.Lock()
	closed, exec := r.closed, r.exec
	r.mu.Unlock()
	if closed {
		return os.ErrClosed
	}
	return createArchive(ctx, exec, request)
}

// CreateArchive builds the archive beside its sources over SCP mode's shell.
func (s *SCP) CreateArchive(ctx context.Context, request apptransfer.ArchiveRequest) error {
	if s == nil {
		return os.ErrClosed
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return os.ErrClosed
	}
	return createArchive(ctx, s.exec, request)
}

// createArchive validates the request, runs the staged command, and maps its
// exit status and confirmation marker onto classified transfer errors. It
// never leaves a partial archive under the final name: the command publishes
// by hard link after the archiver finished.
func createArchive(ctx context.Context, exec appconnection.Executor, request apptransfer.ArchiveRequest) error {
	command, plan, err := apptransfer.BuildArchiveCommand(request)
	if err != nil {
		return err
	}
	if exec == nil {
		return &apptransfer.Error{
			Kind:  apptransfer.KindUnsupported,
			Op:    domainsftp.OpArchive,
			Path:  plan.Name,
			Cause: errors.New("sftp: the exec transport cannot create an archive"),
		}
	}
	result, err := exec.Exec(ctx, command)
	if err != nil {
		return &apptransfer.Error{
			Kind:  apptransfer.KindOf(err),
			Op:    domainsftp.OpArchive,
			Path:  plan.Name,
			Cause: err,
		}
	}
	if !result.HasExitCode {
		return &apptransfer.Error{
			Kind:  apptransfer.KindConnection,
			Op:    domainsftp.OpArchive,
			Path:  plan.Name,
			Cause: apptransfer.ErrArchiveUnconfirmed,
		}
	}
	if result.ExitCode != 0 {
		if result.ExitCode == 127 ||
			strings.Contains(result.Stderr, "is not installed on the remote host") {
			return &apptransfer.Error{
				Kind:  apptransfer.KindToolMissing,
				Op:    domainsftp.OpArchive,
				Path:  plan.Name,
				Cause: apptransfer.ErrArchiveToolMissing,
			}
		}
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = "the remote archive command failed"
		}
		return &apptransfer.Error{
			Kind:  apptransfer.KindInternal,
			Op:    domainsftp.OpArchive,
			Path:  plan.Name,
			Cause: errors.New(message),
		}
	}
	if err := apptransfer.ParseArchiveResult(result.Stdout); err != nil {
		kind := apptransfer.KindInternal
		if errors.Is(err, apptransfer.ErrArchiveExists) {
			kind = apptransfer.KindExists
		}
		return &apptransfer.Error{Kind: kind, Op: domainsftp.OpArchive, Path: plan.Name, Cause: err}
	}
	return nil
}
