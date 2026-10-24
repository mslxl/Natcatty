package transfer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// extractionPrefix marks the staging directory a compressed upload extracts
// into. The directory is a sibling of the final targets, so promoting one
// target is a rename inside the destination directory.
const extractionPrefix = ".netcatty-extract-"

// archiveRoot names one top-level source and where it will land.
type archiveRoot struct {
	source string
	target string
}

// archiveEntry is one directory or file in the archive, in traversal order.
// Name is POSIX-relative to the extraction directory and never absolute.
type archiveEntry struct {
	source  string
	name    string
	target  string
	dir     bool
	size    int64
	mode    uint32
	modTime time.Time
}

// archivePlan is the preflight a compressed upload must pass before it may
// touch the destination: every entry is a regular file or directory, every
// target is free, and every name is one safe path component.
type archivePlan struct {
	roots   []archiveRoot
	entries []archiveEntry
	files   int
	bytes   int64
}

// copyCompressed attempts one compressed folder upload. handled reports that
// this attempt decides the request: either it succeeded, or the caller must
// return err. A false handled value means nothing was left behind and the
// ordinary copy plan must run instead.
func (s *Service) copyCompressed(ctx context.Context, request CopyRequest) (CopyResult, bool, error) {
	kind := kindOf(request)
	plan, ok := collectArchive(ctx, request)
	if !ok {
		return CopyResult{}, false, nil
	}
	// A caller that cancelled while the tree was being inspected falls back to
	// the ordinary path, which reports the cancellation immediately.
	if err := ctx.Err(); err != nil {
		return CopyResult{}, false, nil
	}

	for _, root := range plan.roots {
		if request.ReserveName != nil && !request.ReserveName(root.target, true) {
			return CopyResult{}, true, failure(KindReserved, kind, root.target, nil)
		}
	}
	for _, root := range plan.roots {
		if request.OnTarget != nil {
			request.OnTarget(root.source, root.target)
		}
	}
	if err := request.Dest.MkdirAll(ctx, request.DestDir); err != nil {
		return CopyResult{}, true, wrap(kind, request.DestDir, err)
	}
	staging := request.Dest.Join(request.DestDir, extractionPrefix+randomSuffix())
	if err := request.Dest.Mkdir(ctx, staging); err != nil {
		return CopyResult{}, true, wrap(kind, staging, err)
	}

	result := CopyResult{Bytes: plan.bytes}
	for _, entry := range plan.entries {
		result.Written = append(result.Written, entry.target)
	}
	progress := newArchiveProgress(kind, plan)
	report := func() {
		if request.Progress != nil {
			request.Progress(progress)
		}
	}
	report()

	extractor, _ := request.Dest.(ArchiveExtractor)
	pipeReader, pipeWriter := io.Pipe()
	packed := make(chan error, 1)
	go func() {
		err := packArchive(ctx, request.Source, plan.entries, pipeWriter, func(entry archiveEntry) {
			progress.Current = entry.name
			progress.FilesDone++
			if !entry.dir {
				progress.BytesDone += entry.size
			}
			report()
		})
		_ = pipeWriter.CloseWithError(err)
		packed <- err
	}()
	extractErr := extractor.ExtractTar(ctx, staging, pipeReader)
	// A failed extractor can stop reading before the packer finishes; closing
	// the reader releases the writer instead of leaving it blocked.
	_ = pipeReader.CloseWithError(extractionStreamError(extractErr))
	packErr := <-packed

	if cause := ctx.Err(); cause != nil {
		cleanupExtraction(request.Dest, staging, nil)
		return CopyResult{}, true, &Error{Kind: KindCancelled, Op: kind, Path: request.Sources[0], Cause: cause}
	}
	if extractErr != nil || packErr != nil {
		cleanupErr := cleanupExtraction(request.Dest, staging, nil)
		if cleanupErr != nil {
			return CopyResult{}, true, extractionFailure(kind, staging, extractErr, packErr, cleanupErr)
		}
		// Nothing reached a live destination, so the ordinary copy can run.
		return CopyResult{}, false, nil
	}

	// The overwrite contract requires every staged file to be read back before
	// promotion. A tar that dropped, padded, or misnamed content must not be
	// published; the staging directory is removed and the ordinary path runs.
	if err := verifyExtraction(ctx, kind, request.Dest, staging, plan.entries); err != nil {
		cleanupErr := cleanupExtraction(request.Dest, staging, nil)
		if cleanupErr != nil {
			return CopyResult{}, true, extractionFailure(kind, staging, err, nil, cleanupErr)
		}
		return CopyResult{}, false, nil
	}

	promoted := make([]string, 0, len(plan.roots))
	for _, root := range plan.roots {
		staged := request.Dest.Join(staging, request.Dest.Base(root.target))
		info, err := request.Dest.Stat(ctx, staged)
		if err != nil || !info.IsDir() {
			cause := err
			if cause == nil {
				cause = failure(KindTypeMismatch, kind, staged, nil)
			}
			cleanupErr := cleanupExtraction(request.Dest, staging, promoted)
			if cleanupErr != nil {
				return CopyResult{}, true, extractionFailure(kind, staging, cause, nil, cleanupErr)
			}
			return CopyResult{}, false, nil
		}
		if err := request.Dest.Rename(ctx, staged, root.target); err != nil {
			cleanupErr := cleanupExtraction(request.Dest, staging, promoted)
			if cleanupErr != nil {
				return CopyResult{}, true, extractionFailure(kind, root.target, err, nil, cleanupErr)
			}
			return CopyResult{}, false, nil
		}
		promoted = append(promoted, root.target)
	}
	// The promotion is complete. The staging directory is empty now; a failure
	// to remove it does not affect the data, so it must not fail the upload.
	_ = request.Dest.RemoveAll(context.WithoutCancel(ctx), staging)

	for _, root := range plan.roots {
		info, err := request.Dest.Stat(ctx, root.target)
		if err != nil || !info.IsDir() {
			if err == nil {
				err = failure(KindTypeMismatch, kind, root.target, nil)
			}
			return CopyResult{}, true, wrap(kind, root.target, err)
		}
	}
	progress.FilesDone = progress.FilesTotal
	progress.BytesDone = progress.BytesTotal
	progress.Current = ""
	report()
	return result, true, nil
}

// collectArchive validates one request for compression and builds its archive
// plan. A false return means the request is not compressible safely; the
// ordinary copy plan then runs and reports any real failure.
func collectArchive(ctx context.Context, request CopyRequest) (*archivePlan, bool) {
	if request.Move || request.Checkpoint != nil ||
		len(request.KnownTargets) > 0 || len(request.ReplaceTargets) > 0 ||
		len(request.SkipTargets) > 0 {
		return nil, false
	}
	if _, ok := request.Dest.(ArchiveExtractor); !ok {
		return nil, false
	}
	budget := domainsftp.NewDirectoryTraversalBudget(0, 0)
	plan := &archivePlan{}
	chosen := map[string]bool{}
	for _, source := range request.Sources {
		if err := ctx.Err(); err != nil {
			return nil, false
		}
		info, err := request.Source.Stat(ctx, source)
		if err != nil || !info.IsDir() || info.Symlink {
			return nil, false
		}
		target := destinationFor(request, source)
		if _, err := request.Dest.Stat(ctx, target); err == nil || KindOf(err) != KindNotFound {
			return nil, false
		}
		name := request.Dest.Base(target)
		if err := domainsftp.ValidateName(name); err != nil {
			return nil, false
		}
		if chosen[name] {
			return nil, false
		}
		chosen[name] = true
		plan.roots = append(plan.roots, archiveRoot{source: source, target: target})
		plan.entries = append(plan.entries, archiveEntry{
			source: info.Path, name: name, target: target, dir: true,
			mode: info.Mode, modTime: info.ModTime,
		})
		if !collectArchiveDirectory(ctx, request.Source, budget, info, name, target, 0, plan) {
			return nil, false
		}
	}
	if len(plan.roots) == 0 {
		return nil, false
	}
	return plan, true
}

// collectArchiveDirectory appends one directory tree in the same deterministic
// order the copy plan uses: directories first, then files, each by name. Every
// entry a compressed extraction cannot reproduce exactly (a symlink, a special
// file, an unsafe name, an unreadable directory, or a tree beyond the walk
// bounds) refuses compression for the whole request.
func collectArchiveDirectory(ctx context.Context, source FileSystem,
	budget *domainsftp.DirectoryTraversalBudget, info domainsftp.Entry,
	name, target string, depth int, plan *archivePlan) bool {
	if depth > MaxWalkDepth || ctx.Err() != nil {
		return false
	}
	entries, err := source.List(ctx, info.Path)
	if err != nil {
		return false
	}
	children := make([]domainsftp.Entry, 0, len(entries))
	for _, child := range entries {
		if child.Name == ".." {
			continue
		}
		children = append(children, child)
	}
	if err := budget.Account(len(children)); err != nil {
		return false
	}
	sort.SliceStable(children, func(left, right int) bool {
		if children[left].IsDir() != children[right].IsDir() {
			return children[left].IsDir()
		}
		return children[left].Name < children[right].Name
	})
	for _, child := range children {
		if ctx.Err() != nil {
			return false
		}
		if child.Symlink || domainsftp.ValidateName(child.Name) != nil {
			return false
		}
		childName := name + "/" + child.Name
		childTarget := source.Join(target, child.Name)
		switch {
		case child.IsDir():
			plan.entries = append(plan.entries, archiveEntry{
				source: child.Path, name: childName, target: childTarget, dir: true,
				mode: child.Mode, modTime: child.ModTime,
			})
			if !collectArchiveDirectory(ctx, source, budget, child, childName, childTarget, depth+1, plan) {
				return false
			}
		case child.Kind == domainsftp.KindFile:
			plan.entries = append(plan.entries, archiveEntry{
				source: child.Path, name: childName, target: childTarget,
				size: child.Size, mode: child.Mode, modTime: child.ModTime,
			})
			plan.files++
			plan.bytes += child.Size
		default:
			return false
		}
	}
	return true
}

// packArchive streams the planned tree as gzip tar. Header sizes come from the
// plan, so the archive is deterministic and a source file that changed size
// mid-upload fails instead of publishing a different tree.
func packArchive(ctx context.Context, source FileSystem, entries []archiveEntry,
	out io.Writer, done func(archiveEntry)) error {
	compressed := gzip.NewWriter(out)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := &tar.Header{
			Name:    entry.name,
			Mode:    int64(entry.mode & domainsftp.MaxMode),
			ModTime: entry.modTime,
			Format:  tar.FormatPAX,
		}
		if entry.dir {
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = entry.size
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if !entry.dir {
			reader, err := source.Open(ctx, entry.source)
			if err != nil {
				return err
			}
			sized := &contextReader{ctx: ctx, reader: reader}
			written, copyErr := io.CopyN(archive, sized, entry.size)
			if copyErr == nil {
				// CopyN stops at the planned size, so a file that grew would
				// otherwise publish a silent prefix. One extra byte proves the
				// source still ended where the plan observed it.
				var extra [1]byte
				if n, readErr := sized.Read(extra[:]); n > 0 ||
					(readErr != nil && !errors.Is(readErr, io.EOF)) {
					copyErr = fmt.Errorf("sftp: %s grew while it was archived", entry.name)
				}
			}
			closeErr := reader.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if written != entry.size {
				return fmt.Errorf("sftp: %s has %d bytes, expected %d",
					entry.name, written, entry.size)
			}
		}
		done(entry)
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return compressed.Close()
}

// verifyExtraction reads every staged entry back before promotion, as the
// overwrite contract requires: each file's size must match the plan and each
// directory must exist. The caller cleans the staging directory on an error,
// so a remote tar that cannot reproduce the plan never reaches a live target.
func verifyExtraction(ctx context.Context, kind domainsftp.OperationKind,
	dest FileSystem, staging string, entries []archiveEntry) error {
	for _, entry := range entries {
		info, err := dest.Stat(ctx, dest.Join(staging, entry.name))
		if err != nil {
			return err
		}
		if entry.dir {
			if !info.IsDir() {
				return failure(KindTypeMismatch, kind, entry.name, nil)
			}
			continue
		}
		if info.IsDir() || info.Size != entry.size {
			return failure(KindInternal, kind, entry.name, fmt.Errorf(
				"sftp: extracted %s has %d bytes, expected %d",
				entry.name, info.Size, entry.size))
		}
	}
	return nil
}

// contextReader stops a file read when the request is cancelled.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// newArchiveProgress seeds the same snapshot shape the per-file plan reports;
// the compressed plan counts directories as entries too, like the file plan.
func newArchiveProgress(kind domainsftp.OperationKind, plan *archivePlan) Progress {
	return Progress{Kind: kind, FilesTotal: len(plan.entries), BytesTotal: plan.bytes}
}

// cleanupExtraction removes the staging directory and any target already
// promoted out of it. A background context is used because a cancelled or
// failed upload must still clean up.
func cleanupExtraction(dest FileSystem, staging string, promoted []string) error {
	ctx := context.WithoutCancel(context.Background())
	var first error
	for _, target := range promoted {
		if err := dest.RemoveAll(ctx, target); err != nil && first == nil {
			first = err
		}
	}
	if err := dest.RemoveAll(ctx, staging); err != nil && first == nil {
		first = err
	}
	return first
}

// extractionFailure reports an extraction failure that could not be cleaned
// up. Falling back is unsafe while a partial tree remains, so the caller sees
// the real error instead of a second copy over unknown data.
func extractionFailure(kind domainsftp.OperationKind, path string,
	causes ...error) error {
	var parts []error
	for _, cause := range causes {
		if cause != nil {
			parts = append(parts, cause)
		}
	}
	return &Error{
		Kind: KindInternal,
		Op:   kind,
		Path: path,
		Cause: fmt.Errorf("sftp: compressed upload could not be cleaned up: %w",
			errors.Join(parts...)),
	}
}

// extractionStreamError turns an extractor result into the read error that
// releases the archive writer. A clean extractor already consumed end-of-file,
// so the error only matters when it stopped early.
func extractionStreamError(err error) error {
	if err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
