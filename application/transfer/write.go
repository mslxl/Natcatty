package transfer

import (
	"context"
	"errors"
	"fmt"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// WriteFile replaces one existing file with content. It is the single-file
// replacement transaction of ADR-0012: the old destination is moved to the
// backup sibling, the bytes are staged in a `.netcatty-upload-*.part` sibling,
// verified, given the destination's mode, and renamed into place, and only then
// is the backup removed. Any failure before the promotion puts the backup back,
// so the remote file is never left half-written or missing while the transport
// still works. An interrupted attempt's stale backup is reconciled by the same
// transaction before it starts.
//
// A destination that does not exist is created. A directory is refused: a
// save-back never deletes a subtree to make room for a file.
func (s *Service) WriteFile(ctx context.Context, files FileSystem, path string, content []byte) error {
	if files == nil {
		return failure(KindInternal, domainsftp.OpUpload, path, errors.New("sftp: no filesystem"))
	}
	if err := ctx.Err(); err != nil {
		return &Error{Kind: KindCancelled, Op: domainsftp.OpUpload, Path: path, Cause: err}
	}

	// The destination is inspected before the backup moves it aside, so a
	// directory is refused rather than renamed out of the way.
	existing, statErr := files.Stat(ctx, path)
	switch {
	case statErr == nil:
		if existing.IsDir() {
			return failure(KindIsDir, domainsftp.OpUpload, path, nil)
		}
	case KindOf(statErr) == KindNotFound:
		// A new file is written with the creation mode the adapter chooses.
	default:
		return wrap(domainsftp.OpUpload, path, statErr)
	}

	backup, err := takeBackup(ctx, files, domainsftp.OpUpload, path, false)
	if err != nil {
		return err
	}
	// abort restores the original after any failure before the promotion. It
	// runs even for a cancelled context: the file being replaced must not stay
	// absent because the caller walked away.
	abort := func(cause error) error {
		if backup.path == "" {
			return cause
		}
		return restoreBackup(files, domainsftp.OpUpload, path, backup.path, cause)
	}

	temporary := files.Join(files.Parent(path), stageName("", files.Base(path), path))
	writer, err := files.Create(ctx, temporary)
	if err != nil {
		return abort(wrap(domainsftp.OpUpload, temporary, err))
	}
	discard := func(cause error) error {
		_ = writer.Close()
		_ = files.Remove(context.WithoutCancel(ctx), temporary)
		return abort(cause)
	}
	if _, err := writer.Write(content); err != nil {
		return discard(wrap(domainsftp.OpUpload, temporary, err))
	}
	if err := writer.Close(); err != nil {
		return discard(wrap(domainsftp.OpUpload, temporary, err))
	}
	if backup.modeKnown {
		if err := files.Chmod(ctx, temporary, backup.mode&domainsftp.MaxMode); err != nil {
			return discard(wrap(domainsftp.OpUpload, temporary, err))
		}
	}
	staged, err := files.Stat(ctx, temporary)
	if err != nil {
		return discard(wrap(domainsftp.OpUpload, temporary, err))
	}
	if staged.IsDir() {
		return discard(failure(KindTypeMismatch, domainsftp.OpUpload, path, nil))
	}
	if staged.Size != int64(len(content)) {
		return discard(failure(KindInternal, domainsftp.OpUpload, path,
			fmt.Errorf("sftp: staged %s has %d bytes, expected %d",
				files.Base(temporary), staged.Size, len(content))))
	}
	if backup.modeKnown && staged.Mode&domainsftp.MaxMode != backup.mode&domainsftp.MaxMode {
		return discard(failure(KindInternal, domainsftp.OpUpload, path,
			fmt.Errorf("sftp: staged %s has mode %04o, expected %04o",
				files.Base(temporary), staged.Mode, backup.mode&domainsftp.MaxMode)))
	}
	if err := files.Rename(ctx, temporary, path); err != nil {
		_ = files.Remove(context.WithoutCancel(ctx), temporary)
		return abort(wrap(domainsftp.OpUpload, path, err))
	}
	if backup.path != "" {
		// The promoted file is durable; the old one is no longer needed. A
		// cleanup failure is reported instead of hidden: the promoted
		// destination stays and the backup stays recoverable.
		if err := removeBackup(ctx, files, domainsftp.OpUpload, backup.path); err != nil {
			return err
		}
	}
	return nil
}
