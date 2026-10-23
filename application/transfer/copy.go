package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Walk limits keep a malformed or hostile tree from growing an operation
// without bound. They match the directory-checkpoint v2 limits.
const (
	// MaxWalkDepth is how many directory levels a copy descends.
	MaxWalkDepth = 32
	// MaxWalkEntries is how many entries one operation may visit.
	MaxWalkEntries = 200_000
	// copyBufferSize is the streaming chunk size; it matches the original's
	// request size.
	copyBufferSize = 32 * 1024
	// progressStep is how many bytes pass between progress callbacks, so a
	// large file does not flood the UI thread.
	progressStep = 256 * 1024
)

// ConflictPolicy decides what a copy does when the destination exists.
type ConflictPolicy string

const (
	// ConflictStop refuses the copy; the UI offers a replacement decision.
	ConflictStop ConflictPolicy = "stop"
	// ConflictSkip leaves the existing destination and reports it skipped.
	ConflictSkip ConflictPolicy = "skip"
	// ConflictReplace removes the destination first.
	ConflictReplace ConflictPolicy = "replace"
	// ConflictDuplicate writes beside the destination with a "(copy N)" name.
	ConflictDuplicate ConflictPolicy = "duplicate"
)

// Valid reports whether the policy is one the service knows.
func (p ConflictPolicy) Valid() bool {
	switch p {
	case ConflictStop, ConflictSkip, ConflictReplace, ConflictDuplicate:
		return true
	}
	return false
}

// Progress is one snapshot of a running operation. Byte counts cover the
// whole request; Current names the entry being written.
type Progress struct {
	Kind       domainsftp.OperationKind
	Current    string
	FilesDone  int
	FilesTotal int
	BytesDone  int64
	BytesTotal int64
}

// Done reports whether every planned file has been written.
func (p Progress) Done() bool {
	return p.FilesTotal > 0 && p.FilesDone >= p.FilesTotal
}

// CopyRequest is one copy or move between two panes. Source and Dest may be
// the same FileSystem (same-pane paste) or different ones (upload, download,
// or remote-to-remote through this process).
type CopyRequest struct {
	Source  FileSystem
	Dest    FileSystem
	Sources []string
	DestDir string
	// Move removes each source after it was copied successfully.
	Move bool
	// Kind labels progress as upload or download; empty means copy.
	Kind     domainsftp.OperationKind
	Conflict ConflictPolicy
	Progress func(Progress)
}

// CopyResult reports what one request did.
type CopyResult struct {
	// Written lists destination paths that were created or replaced.
	Written []string
	// Skipped lists destination paths left untouched by ConflictSkip or a
	// symlinked directory.
	Skipped []string
	// Bytes is the number of file bytes copied.
	Bytes int64
}

// Copy runs a copy or move request. It checks ctx between every chunk and
// directory entry, removes a partially written file when the operation is
// cancelled, and never leaves a half-written file under the destination name:
// data is staged in a temporary sibling and renamed into place.
func (s *Service) Copy(ctx context.Context, request CopyRequest) (CopyResult, error) {
	var result CopyResult
	if request.Source == nil || request.Dest == nil {
		return result, failure(KindInternal, kindOf(request), request.DestDir, errors.New("sftp: missing filesystem"))
	}
	if len(request.Sources) == 0 || request.DestDir == "" {
		return result, failure(KindInternal, kindOf(request), request.DestDir, errors.New("sftp: empty copy request"))
	}
	policy := request.Conflict
	if policy == "" {
		policy = ConflictStop
	}
	if !policy.Valid() {
		return result, failure(KindInternal, kindOf(request), request.DestDir, errors.New("sftp: unknown conflict policy"))
	}

	kind := kindOf(request)
	// The engine refuses a self-copy even when a caller forgot to guard it.
	// Copying a folder over or into its own tree would discover the new
	// entries and recurse until the walk bound.
	for _, source := range request.Sources {
		if PathWithin(request.Source, source, request.Dest, request.DestDir) {
			return result, failure(KindSelfCopy, kind, source, nil)
		}
	}
	counter := &walkCounter{}
	plan := Progress{Kind: kind}
	for _, source := range request.Sources {
		info, err := request.Source.Stat(ctx, source)
		if err != nil {
			return result, wrap(kind, source, err)
		}
		if info.IsDir() {
			bytes, entries, err := countTree(ctx, request.Source, source, counter)
			if err != nil {
				return result, err
			}
			plan.FilesTotal += entries
			plan.BytesTotal += bytes
		} else {
			plan.FilesTotal++
			plan.BytesTotal += info.Size
		}
	}
	report := func(current string) {
		if request.Progress != nil {
			// Keep the last name in the plan too: the final snapshot after the
			// request must still say which file finished, not drop to a bare
			// operation kind.
			plan.Current = current
			request.Progress(plan)
		}
	}
	report("")

	for _, source := range request.Sources {
		if err := ctx.Err(); err != nil {
			return result, &Error{Kind: KindCancelled, Op: kind, Path: source, Cause: err}
		}
		destination, skip, err := resolveDestination(ctx, request, policy, source)
		if err != nil {
			return result, err
		}
		if skip {
			result.Skipped = append(result.Skipped, destination)
			continue
		}
		info, err := request.Source.Stat(ctx, source)
		if err != nil {
			return result, wrap(kind, source, err)
		}
		backup := ""
		if policy == ConflictReplace {
			backup, err = makeBackup(ctx, request.Dest, kind, destination)
			if err != nil {
				return result, err
			}
		}
		written, bytes, err := copyEntry(ctx, request, kind, counter, info, destination, &plan, report)
		if err != nil {
			if backup != "" {
				return result, restoreBackup(request.Dest, kind, destination, backup, err)
			}
			return result, err
		}
		if backup != "" {
			// The new tree is in place; the old one is no longer needed. A
			// stale backup is harmless and removed by the next replacement.
			_ = request.Dest.RemoveAll(context.WithoutCancel(ctx), backup)
		}
		result.Written = append(result.Written, written...)
		result.Bytes += bytes
		report(request.Source.Base(source))
		if request.Move {
			if err := removeSource(ctx, request.Source, source, info); err != nil {
				return result, wrap(kind, source, err)
			}
		}
	}
	if request.Progress != nil {
		request.Progress(plan)
	}
	return result, nil
}

func kindOf(request CopyRequest) domainsftp.OperationKind {
	if request.Kind.Valid() {
		return request.Kind
	}
	if request.Move {
		return domainsftp.OpMove
	}
	return domainsftp.OpCopy
}

// resolveDestination returns the destination path, or skip when the
// destination exists and the policy says to leave it alone.
func resolveDestination(ctx context.Context, request CopyRequest,
	policy ConflictPolicy, source string) (string, bool, error) {
	destination := request.Dest.Join(request.DestDir, request.Source.Base(source))
	if policy == ConflictDuplicate {
		taken, err := namesIn(ctx, request.Dest, request.DestDir)
		if err != nil {
			return "", false, err
		}
		destination = domainsftp.CopyName(request.Dest.Base(destination), func(name string) bool {
			return taken[name]
		})
		destination = request.Dest.Join(request.DestDir, destination)
		return destination, false, nil
	}
	if _, err := request.Dest.Stat(ctx, destination); err != nil {
		if KindOf(err) == KindNotFound {
			return destination, false, nil
		}
		return "", false, wrap(kindOf(request), destination, err)
	}
	switch policy {
	case ConflictSkip:
		return destination, true, nil
	case ConflictStop:
		return "", false, failure(KindExists, kindOf(request), destination, nil)
	default:
		return destination, false, nil
	}
}

// namesIn lists a directory's entry names for duplicate naming. A missing
// directory has no taken names, so the first copy proceeds and its own error
// reports the real problem.
func namesIn(ctx context.Context, files FileSystem, dir string) (map[string]bool, error) {
	entries, err := files.List(ctx, dir)
	if err != nil {
		if KindOf(err) == KindNotFound {
			return map[string]bool{}, nil
		}
		return nil, wrap(domainsftp.OpCopy, dir, err)
	}
	taken := make(map[string]bool, len(entries))
	for _, entry := range entries {
		taken[entry.Name] = true
	}
	return taken, nil
}

// copyEntry copies one entry, returning every destination path written.
func copyEntry(ctx context.Context, request CopyRequest, kind domainsftp.OperationKind,
	counter *walkCounter, info domainsftp.Entry, destination string,
	plan *Progress, report func(string)) ([]string, int64, error) {
	return copyEntryDepth(ctx, request, kind, counter, info, destination, 0, plan, report)
}

func copyEntryDepth(ctx context.Context, request CopyRequest, kind domainsftp.OperationKind,
	counter *walkCounter, info domainsftp.Entry, destination string, depth int,
	plan *Progress, report func(string)) ([]string, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, &Error{Kind: KindCancelled, Op: kind, Path: destination, Cause: err}
	}
	if depth > MaxWalkDepth {
		return nil, 0, failure(KindTooLarge, kind, destination, nil)
	}
	if info.IsDir() {
		if info.Symlink {
			// Following a symlinked directory while copying can loop or
			// duplicate a tree; the original browser does not descend either.
			return nil, 0, nil
		}
		if err := request.Dest.MkdirAll(ctx, destination); err != nil {
			return nil, 0, wrap(kind, destination, err)
		}
		plan.FilesDone++
		if info.Mode != 0 {
			_ = request.Dest.Chmod(ctx, destination, info.Mode&domainsftp.MaxMode)
		}
		entries, err := request.Source.List(ctx, info.Path)
		if err != nil {
			return nil, 0, wrap(kind, info.Path, err)
		}
		var written []string
		var bytes int64
		written = append(written, destination)
		for _, child := range entries {
			if child.Name == ".." {
				continue
			}
			if err := ctx.Err(); err != nil {
				return written, bytes, &Error{Kind: KindCancelled, Op: kind, Path: child.Path, Cause: err}
			}
			counter.entries++
			if counter.entries > MaxWalkEntries {
				return written, bytes, failure(KindTooLarge, kind, child.Path, nil)
			}
			childDestination := request.Dest.Join(destination, child.Name)
			childWritten, childBytes, err := copyEntryDepth(ctx, request, kind, counter,
				child, childDestination, depth+1, plan, report)
			if err != nil {
				return written, bytes, err
			}
			written = append(written, childWritten...)
			bytes += childBytes
		}
		return written, bytes, nil
	}
	bytes, err := copyFile(ctx, request.Source, request.Dest, info, destination, plan, report)
	if err != nil {
		return nil, 0, err
	}
	plan.FilesDone++
	return []string{destination}, bytes, nil
}

// copyFile streams one file into a temporary sibling and renames it into
// place. A cancelled or failed copy removes the temporary file, so a
// half-written file never carries the destination name.
func copyFile(ctx context.Context, source, dest FileSystem, info domainsftp.Entry,
	destination string, plan *Progress, report func(string)) (int64, error) {
	reader, err := source.Open(ctx, info.Path)
	if err != nil {
		return 0, wrap(domainsftp.OpCopy, info.Path, err)
	}
	defer reader.Close()

	temporary := dest.Join(dest.Parent(destination), stageName(dest.Base(destination)))
	writer, err := dest.Create(ctx, temporary)
	if err != nil {
		return 0, wrap(domainsftp.OpCopy, temporary, err)
	}
	written := int64(0)
	buffer := make([]byte, copyBufferSize)
	sinceProgress := int64(0)
	fail := func(cause error) (int64, error) {
		_ = writer.Close()
		_ = dest.Remove(context.WithoutCancel(ctx), temporary)
		return written, cause
	}
	for {
		if err := ctx.Err(); err != nil {
			return fail(&Error{Kind: KindCancelled, Op: domainsftp.OpCopy, Path: info.Path, Cause: err})
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, err := writer.Write(buffer[:n]); err != nil {
				return fail(wrap(domainsftp.OpCopy, destination, err))
			}
			written += int64(n)
			plan.BytesDone += int64(n)
			sinceProgress += int64(n)
			if sinceProgress >= progressStep {
				sinceProgress = 0
				report(info.Name)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fail(wrap(domainsftp.OpCopy, info.Path, readErr))
		}
	}
	if err := writer.Close(); err != nil {
		return fail(wrap(domainsftp.OpCopy, temporary, err))
	}
	if info.Mode != 0 {
		_ = dest.Chmod(ctx, temporary, info.Mode&domainsftp.MaxMode)
	}
	if err := dest.Rename(ctx, temporary, destination); err != nil {
		_ = dest.Remove(context.WithoutCancel(ctx), temporary)
		return written, wrap(domainsftp.OpCopy, destination, err)
	}
	return written, nil
}

// stageName is the staging file's name beside the destination. ADR-0012
// fixes the prefix and suffix; cleanup and resume depend on them.
func stageName(name string) string {
	return fmt.Sprintf(".netcatty-upload-%s-%s.part", randomSuffix(), name)
}

// backupName is the sibling that holds the old destination during a
// replacement. The hash keeps two same-named destinations in different
// directories apart while the base name keeps the file recognizable.
func backupName(dest FileSystem, destination string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(destination))
	return fmt.Sprintf(".netcatty-backup-%016x-%s.bak", hash.Sum64(), dest.Base(destination))
}

// makeBackup moves an existing destination aside before a replacement. A
// missing destination needs no backup. The move is a rename in the same
// directory, so it does not copy the tree.
func makeBackup(ctx context.Context, dest FileSystem, kind domainsftp.OperationKind,
	destination string) (string, error) {
	if _, err := dest.Stat(ctx, destination); err != nil {
		if KindOf(err) == KindNotFound {
			return "", nil
		}
		return "", wrap(kind, destination, err)
	}
	backup := dest.Join(dest.Parent(destination), backupName(dest, destination))
	// A backup left by an interrupted earlier replacement is stale; this
	// replacement owns the name now.
	_ = dest.RemoveAll(context.WithoutCancel(ctx), backup)
	if err := dest.Rename(ctx, destination, backup); err != nil {
		return "", wrap(kind, destination, err)
	}
	return backup, nil
}

// restoreBackup puts the old destination back after a failed replacement and
// removes the partially copied tree. It uses a background context: a
// cancelled operation must still restore what it was replacing.
func restoreBackup(dest FileSystem, kind domainsftp.OperationKind,
	destination, backup string, cause error) error {
	ctx := context.Background()
	_ = dest.RemoveAll(ctx, destination)
	if err := dest.Rename(ctx, backup, destination); err != nil {
		return &Error{
			Kind: KindInternal,
			Op:   kind,
			Path: destination,
			Cause: fmt.Errorf("sftp: restore %s: %v (original error: %v)",
				dest.Base(backup), err, cause),
		}
	}
	return cause
}

// removeSource removes a moved entry after a successful copy.
func removeSource(ctx context.Context, files FileSystem, path string, info domainsftp.Entry) error {
	if info.IsDir() {
		return files.RemoveAll(ctx, path)
	}
	return files.Remove(ctx, path)
}

// countTree counts the files and bytes below dir for progress totals. It
// obeys the same bounds as the copy itself.
func countTree(ctx context.Context, files FileSystem, dir string, counter *walkCounter) (int64, int, error) {
	return countTreeDepth(ctx, files, dir, 0, counter)
}

func countTreeDepth(ctx context.Context, files FileSystem, dir string, depth int,
	counter *walkCounter) (int64, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, &Error{Kind: KindCancelled, Op: domainsftp.OpCopy, Path: dir, Cause: err}
	}
	if depth > MaxWalkDepth {
		return 0, 0, failure(KindTooLarge, domainsftp.OpCopy, dir, nil)
	}
	entries, err := files.List(ctx, dir)
	if err != nil {
		return 0, 0, wrap(domainsftp.OpCopy, dir, err)
	}
	var bytes int64
	filesCount := 1
	for _, entry := range entries {
		if entry.Name == ".." {
			continue
		}
		counter.entries++
		if counter.entries > MaxWalkEntries {
			return 0, 0, failure(KindTooLarge, domainsftp.OpCopy, entry.Path, nil)
		}
		if entry.IsDir() {
			if entry.Symlink {
				continue
			}
			childBytes, childFiles, err := countTreeDepth(ctx, files, entry.Path, depth+1, counter)
			if err != nil {
				return 0, 0, err
			}
			bytes += childBytes
			filesCount += childFiles
			continue
		}
		bytes += entry.Size
		filesCount++
	}
	return bytes, filesCount, nil
}

// walkCounter bounds one request's visits across its sources.
type walkCounter struct{ entries int }

// randomSuffix returns eight hexadecimal characters for a staging name.
func randomSuffix() string {
	var data [4]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(data[:])
}
