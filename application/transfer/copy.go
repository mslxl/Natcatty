package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Walk limits keep a malformed or hostile tree from growing an operation
// without bound. They match the directory-checkpoint v2 limits.
const (
	// MaxWalkDepth is how many directory levels a copy descends.
	MaxWalkDepth = 32
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
	// ConflictDuplicate writes beside the destination with a "(copy)" name.
	ConflictDuplicate ConflictPolicy = "duplicate"
	// ConflictMerge writes into an existing directory, keeping entries that
	// exist only at the destination.
	ConflictMerge ConflictPolicy = "merge"
	// ConflictAsk asks the caller for one of the other policies. A caller with
	// no resolver treats it as stop.
	ConflictAsk ConflictPolicy = "ask"
)

// Valid reports whether the policy is one the service knows.
func (p ConflictPolicy) Valid() bool {
	switch p {
	case ConflictStop, ConflictSkip, ConflictReplace, ConflictDuplicate,
		ConflictMerge, ConflictAsk:
		return true
	}
	return false
}

// ConflictResolution is the answer to one destination conflict.
type ConflictResolution struct {
	Action ConflictPolicy
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
	// Checkpoint is the durable directory resume record. It is nil for a
	// request with no directories. CheckpointBytes is the honest prefix the
	// checkpoint covers, which excludes a partially written current file.
	Checkpoint      *domainsftp.DirectoryCheckpoint
	CheckpointBytes int64
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
	// StageKey names the durable owner of this request (the queue passes its
	// Task ID). A non-empty key makes every staging file name deterministic
	// from the key and the target, so a resumed request reuses the staging
	// file an interrupted process left instead of orphaning it. An empty key
	// keeps the original random suffix for callers with no durable identity.
	StageKey string
	// Checkpoint resumes a directory request from the completed prefix it
	// names. The engine validates it against a fresh traversal before
	// skipping anything.
	Checkpoint *domainsftp.DirectoryCheckpoint
	// KnownTargets pins the destination root chosen by an earlier attempt, so
	// a resumed duplicate or replacement continues in the same tree.
	KnownTargets map[string]string
	// ReplaceTargets and SkipTargets carry earlier per-source conflict answers
	// through a resume. They override the request-wide policy for those
	// sources.
	ReplaceTargets map[string]bool
	SkipTargets    map[string]bool
	// FollowSymlinkDirs descends into symlinked directories. A download does
	// this by default, matching the original; uploads and copies do not.
	FollowSymlinkDirs bool
	// SkipUnchanged leaves a file alone when its destination already has the
	// same size and whole-second modification time. It applies while a
	// directory is merged and never during a replacement.
	SkipUnchanged bool
	// OnConflict answers a ConflictAsk policy. A nil resolver means stop. The
	// queue uses it to park the row as attention until the user chooses.
	OnConflict func(domainsftp.Conflict) ConflictResolution
	// ReserveName claims one destination path for this request. It reports
	// false when another live Transfer already owns the path or a subtree that
	// contains it. The queue binds it to path reservations; the engine still
	// checks it so two requests cannot both pick the same duplicate name.
	ReserveName func(path string, directory bool) bool
	// OnTarget reports each resolved top-level destination before work starts,
	// so the queue can persist it for a later resume.
	OnTarget func(source, target string)
	// KeepReplacement reports whether a failed replacement should keep the
	// partially written destination and its backup for a later resume. A pause
	// and a lost transport return true; a cancel or ordinary failure return
	// false, which restores the original before Copy returns.
	KeepReplacement func(err error) bool
	// budget bounds the traversal. Tests pass a small budget; production uses
	// the original limits.
	budget *domainsftp.DirectoryTraversalBudget
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
	// ResumeReset reports that an interrupted replacement was rolled back to
	// its original, so any checkpoint for this request no longer describes the
	// destination and must not be used to skip work.
	ResumeReset bool
}

// kindOf resolves an empty request kind the same way the queue does.
func kindOf(request CopyRequest) domainsftp.OperationKind {
	if request.Kind.Valid() {
		return request.Kind
	}
	if request.Move {
		return domainsftp.OpMove
	}
	return domainsftp.OpCopy
}

// plannedEntry is one deterministic traversal step. Its identity hashes the
// source and target paths, size, and modification time so a changed source
// invalidates a checkpoint prefix instead of skipping a stale file.
type plannedEntry struct {
	SourcePath string
	TargetPath string
	Directory  bool
	Size       int64
	Mode       uint32
	Modified   time.Time
	Symlink    bool
	Identity   string
}

// plannedSource is one top-level source and the half-open range of plan
// entries it produced.
type plannedSource struct {
	Source      string
	Destination string
	Skip        bool
	// Replace says the source's whole destination tree was moved aside, so no
	// destination file can be unchanged.
	Replace bool
	First   int
	Count   int
}

// copyPlan is the whole request's deterministic traversal plus its resume
// seed. Building it once replaces the old counting walk, so totals, order,
// and checkpoint identities cannot disagree.
type copyPlan struct {
	sources         []plannedSource
	entries         []plannedEntry
	filesTotal      int
	bytesTotal      int64
	hasDirs         bool
	resumedEntries  int
	checkpoint      *domainsftp.DirectoryCheckpoint
	checkpointBytes int64
}

// Copy runs a copy or move request. It checks ctx between every chunk and
// directory entry, removes a partially written file when the operation is
// cancelled, and never leaves a half-written file under the destination name:
// data is staged in a temporary sibling and renamed into place.
//
// A directory request also records a bounded checkpoint after each completed
// entry. A later call with that checkpoint skips the completed prefix after
// verifying the source still matches.
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

	plan, err := buildCopyPlan(ctx, request, policy)
	if err != nil {
		return result, err
	}
	progress := newPlanProgress(kind, plan)
	report := func(current string) {
		if request.Progress == nil {
			return
		}
		progress.Current = current
		snapshot := progress
		if plan.checkpoint != nil {
			checkpoint := *plan.checkpoint
			snapshot.Checkpoint = &checkpoint
			snapshot.CheckpointBytes = plan.checkpointBytes
		}
		request.Progress(snapshot)
	}
	report("")

	var accumulator *domainsftp.DirectoryManifestAccumulator
	if plan.hasDirs && plan.checkpoint != nil {
		accumulator, err = domainsftp.NewDirectoryManifestAccumulator(*plan.checkpoint)
		if err != nil {
			// The checkpoint was validated before the plan, so this cannot
			// happen; starting over is safer than failing the request.
			empty := domainsftp.CreateEmptyDirectoryCheckpoint()
			plan.checkpoint = &empty
			plan.resumedEntries = 0
			plan.checkpointBytes = 0
			progress = newPlanProgress(kind, plan)
			accumulator, _ = domainsftp.NewDirectoryManifestAccumulator(empty)
		}
	}

	for _, source := range plan.sources {
		if source.Skip {
			result.Skipped = append(result.Skipped, source.Destination)
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, &Error{Kind: KindCancelled, Op: kind, Path: source.Source, Cause: err}
		}
		// A resumed source continues inside the tree it already wrote. Only a
		// source that has not been touched yet gets the replacement backup; a
		// resumed one reuses the backup a pause or lost transport retained.
		resumedSource := plan.resumedEntries > source.First
		replacingDirectory := plan.entries[source.First].Directory
		backup := replacementBackup{}
		if source.Replace {
			if resumedSource {
				backup, err = retainedBackup(ctx, request.Dest, kind, source.Destination)
			} else {
				backup, err = takeBackup(ctx, request.Dest, kind, source.Destination, replacingDirectory)
			}
			if err != nil {
				return result, err
			}
		}
		// abort keeps a paused or interrupted replacement for a later resume.
		// A hard cancel or ordinary failure puts the original back and tells
		// the queue to drop its checkpoint.
		abort := func(cause error) (CopyResult, error) {
			if backup.path == "" || (request.KeepReplacement != nil && request.KeepReplacement(cause)) {
				return result, cause
			}
			result.ResumeReset = true
			return result, restoreBackup(request.Dest, kind, source.Destination, backup.path, cause)
		}
		markDone := func(entry plannedEntry) error {
			progress.FilesDone++
			if accumulator != nil {
				plan.checkpoint.CoveredEntries++
				plan.checkpoint.CompletedEntries++
				if err := accumulator.Append(entry.Identity); err != nil {
					return failure(KindInternal, kind, entry.SourcePath, err)
				}
				plan.checkpoint.ManifestHash = accumulator.Digest()
				if !entry.Directory {
					plan.checkpointBytes += entry.Size
				}
			}
			report(request.Source.Base(entry.SourcePath))
			return nil
		}
		canSkipUnchanged := request.SkipUnchanged && !source.Replace
		for entryIndex := source.First; entryIndex < source.First+source.Count; entryIndex++ {
			entry := plan.entries[entryIndex]
			if entryIndex < plan.resumedEntries {
				continue
			}
			if err := ctx.Err(); err != nil {
				return abort(&Error{Kind: KindCancelled, Op: kind, Path: entry.SourcePath, Cause: err})
			}
			if entry.Directory {
				if err := request.Dest.MkdirAll(ctx, entry.TargetPath); err != nil {
					return abort(wrap(kind, entry.TargetPath, err))
				}
				if entry.Mode != 0 {
					_ = request.Dest.Chmod(ctx, entry.TargetPath, entry.Mode&domainsftp.MaxMode)
				}
				result.Written = append(result.Written, entry.TargetPath)
			} else if canSkipUnchanged && !entry.Symlink && unchangedAtDestination(ctx, request.Dest, entry) {
				result.Skipped = append(result.Skipped, entry.TargetPath)
			} else {
				info := domainsftp.Entry{
					Name: request.Source.Base(entry.SourcePath),
					Path: entry.SourcePath,
					Size: entry.Size,
					Mode: entry.Mode,
				}
				// A replaced file keeps the destination's own mode, the way
				// the original's stage/verify/promote transaction does. A file
				// inside a replaced tree keeps its source mode, and can be
				// checked only where the destination actually carries POSIX
				// modes (Windows local files expose just the read-only bit).
				// The staged copy is checked against both values before it may
				// take the destination name.
				verify := stagedVerification{Size: entry.Size}
				if source.Replace && !entry.Directory {
					switch {
					case entryIndex == source.First && backup.modeKnown:
						verify.Mode = backup.mode
						verify.ModeKnown = true
					case entry.Mode != 0 && request.Dest.Style() == StylePOSIX:
						verify.Mode = entry.Mode
						verify.ModeKnown = true
					}
				}
				bytes, copyErr := copyFile(ctx, request.Source, request.Dest, fileCopy{
					info:        info,
					destination: entry.TargetPath,
					stageKey:    request.StageKey,
					verify:      verify,
				}, &progress, report)
				if copyErr != nil {
					return abort(copyErr)
				}
				result.Written = append(result.Written, entry.TargetPath)
				result.Bytes += bytes
			}
			if err := markDone(entry); err != nil {
				return result, err
			}
		}
		if backup.path != "" {
			// The new tree is in place and verified; the old one is no longer
			// needed. A cleanup failure is reported instead of hidden: the
			// promoted destination stays and the backup stays recoverable,
			// which is how the original reports a stuck post-commit cleanup.
			if err := removeBackup(ctx, request.Dest, kind, backup.path); err != nil {
				return result, err
			}
		}
		if request.Move {
			info, err := request.Source.Stat(ctx, source.Source)
			if err != nil {
				return result, wrap(kind, source.Source, err)
			}
			if err := removeSource(ctx, request.Source, source.Source, info); err != nil {
				return result, wrap(kind, source.Source, err)
			}
		}
	}
	return result, nil
}

// newPlanProgress seeds a snapshot from the plan's resumed prefix.
func newPlanProgress(kind domainsftp.OperationKind, plan *copyPlan) Progress {
	progress := Progress{
		Kind:       kind,
		FilesTotal: plan.filesTotal,
		BytesTotal: plan.bytesTotal,
		FilesDone:  plan.resumedEntries,
	}
	for index := 0; index < plan.resumedEntries && index < len(plan.entries); index++ {
		if !plan.entries[index].Directory {
			progress.BytesDone += plan.entries[index].Size
		}
	}
	if plan.checkpoint != nil {
		checkpoint := *plan.checkpoint
		progress.Checkpoint = &checkpoint
		progress.CheckpointBytes = plan.checkpointBytes
	}
	return progress
}

// buildCopyPlan resolves every source destination and walks each directory
// once, in the deterministic order a checkpoint depends on.
func buildCopyPlan(ctx context.Context, request CopyRequest, policy ConflictPolicy) (*copyPlan, error) {
	kind := kindOf(request)
	plan := &copyPlan{}
	budget := request.budget
	if budget == nil {
		budget = domainsftp.NewDirectoryTraversalBudget(0, 0)
	}
	followSymlinks := request.FollowSymlinkDirs || kind == domainsftp.OpDownload
	// Duplicate names chosen earlier in this request are not on disk yet, so
	// the plan remembers them; the queue's reservation covers other requests.
	plannedNames := map[string]bool{}
	// How many later sources conflict with the same destination type, so an
	// apply-to-all prompt states a true count instead of every source left.
	// The same scan hands back the source stats it read.
	scanned := scanRequestConflicts(ctx, request, policy)
	for sourceIndex, source := range request.Sources {
		if err := ctx.Err(); err != nil {
			return nil, &Error{Kind: KindCancelled, Op: kind, Path: source, Cause: err}
		}
		var observation scannedSource
		if sourceIndex < len(scanned) {
			observation = scanned[sourceIndex]
		}
		info := observation.info
		var err error
		if !observation.read {
			info, err = request.Source.Stat(ctx, source)
			if err != nil {
				return nil, wrap(kind, source, err)
			}
		}
		destination := ""
		skip := false
		resolved := policy
		if request.KnownTargets != nil {
			destination = request.KnownTargets[source]
		}
		if destination != "" {
			// A resumed source keeps the answers it already received: skipping
			// it again, or continuing a replacement's partial tree.
			skip = request.SkipTargets[source]
			if request.ReplaceTargets[source] {
				resolved = ConflictReplace
			}
			// A pinned target was chosen by an earlier attempt. Claim it again
			// so the paused row's subtree stays reserved across the resume.
			if !skip && request.ReserveName != nil && !request.ReserveName(destination, info.IsDir()) {
				return nil, failure(KindReserved, kind, destination, nil)
			}
		}
		if destination == "" {
			destination, skip, resolved, err = resolveDestination(ctx, request, policy, source,
				info, observation.remaining, plannedNames)
			if err != nil {
				return nil, err
			}
		}
		planned := plannedSource{Source: source, Destination: destination, Skip: skip,
			Replace: resolved == ConflictReplace, First: len(plan.entries)}
		if !skip {
			if request.OnTarget != nil {
				request.OnTarget(source, destination)
			}
			if info.IsDir() {
				plan.hasDirs = true
				if err := planDirectory(ctx, request.Source, request.Dest, kind, budget, info, destination,
					0, 0, map[string]struct{}{}, followSymlinks, plan); err != nil {
					return nil, err
				}
			} else {
				plan.entries = append(plan.entries, plannedFile(request.Source, info, destination))
				plan.filesTotal++
				plan.bytesTotal += info.Size
			}
		}
		planned.Count = len(plan.entries) - planned.First
		plan.sources = append(plan.sources, planned)
	}
	plan.resumedEntries = seedCheckpoint(plan, request.Checkpoint)
	return plan, nil
}

// seedCheckpoint validates a saved checkpoint against the plan and returns the
// number of already-completed entries to skip. A checkpoint that does not
// match the freshly traversed prefix is discarded, so a changed source is
// copied again rather than skipped.
func seedCheckpoint(plan *copyPlan, saved *domainsftp.DirectoryCheckpoint) int {
	if !plan.hasDirs {
		plan.checkpoint = nil
		return 0
	}
	empty := domainsftp.CreateEmptyDirectoryCheckpoint()
	plan.checkpoint = &empty
	if saved == nil {
		return 0
	}
	identities := make([]string, len(plan.entries))
	for index, entry := range plan.entries {
		identities[index] = entry.Identity
	}
	checkpoint := *saved
	if checkpoint.Version == 1 {
		migrated, ok := domainsftp.MigrateDirectoryCheckpoint(checkpoint, identities)
		if !ok {
			return 0
		}
		checkpoint = migrated
	}
	if !checkpoint.Valid() || checkpoint.CompletedEntries != checkpoint.CoveredEntries ||
		!domainsftp.VerifyDirectoryCheckpoint(checkpoint, identities) {
		return 0
	}
	plan.checkpoint = &checkpoint
	plan.checkpointBytes = 0
	for index := 0; index < checkpoint.CompletedEntries; index++ {
		if !plan.entries[index].Directory {
			plan.checkpointBytes += plan.entries[index].Size
		}
	}
	return checkpoint.CompletedEntries
}

// plannedFile builds a regular-file plan entry.
func plannedFile(files FileSystem, info domainsftp.Entry, destination string) plannedEntry {
	return plannedEntry{
		SourcePath: info.Path,
		TargetPath: destination,
		Size:       info.Size,
		Mode:       info.Mode,
		Modified:   info.ModTime,
		Symlink:    info.Symlink,
		Identity: domainsftp.CreateDirectoryEntryIdentity(info.Path, destination,
			info.Size, info.ModTime.UnixMilli()),
	}
}

// planDirectory appends one directory and everything below it in traversal
// order: child directories first, then files, each by name.
func planDirectory(ctx context.Context, files, dest FileSystem, kind domainsftp.OperationKind,
	budget *domainsftp.DirectoryTraversalBudget, info domainsftp.Entry,
	destination string, depth, symlinkDepth int,
	ancestors map[string]struct{}, followSymlinks bool, plan *copyPlan) error {
	if err := ctx.Err(); err != nil {
		return &Error{Kind: KindCancelled, Op: kind, Path: info.Path, Cause: err}
	}
	if depth > MaxWalkDepth {
		return failure(KindTooLarge, kind, info.Path, nil)
	}
	claimed, err := budget.Visit(directoryCanonical(files, info), ancestors)
	if err != nil {
		return traversalFailure(kind, info.Path, err)
	}
	if claimed == "" {
		// A symlink ancestor cycle. Skipping it is safer than copying a tree
		// onto itself.
		return nil
	}
	defer budget.Release(claimed, ancestors)

	plan.entries = append(plan.entries, plannedEntry{
		SourcePath: info.Path,
		TargetPath: destination,
		Directory:  true,
		Mode:       info.Mode,
		Identity: domainsftp.CreateDirectoryEntryIdentity(info.Path, destination,
			0, info.ModTime.UnixMilli()),
	})
	plan.filesTotal++

	entries, err := files.List(ctx, info.Path)
	if err != nil {
		return wrap(kind, info.Path, err)
	}
	children := make([]domainsftp.Entry, 0, len(entries))
	for _, child := range entries {
		if child.Name == ".." {
			continue
		}
		children = append(children, child)
	}
	sort.SliceStable(children, func(left, right int) bool {
		if children[left].IsDir() != children[right].IsDir() {
			return children[left].IsDir()
		}
		return children[left].Name < children[right].Name
	})
	if err := budget.Account(len(children)); err != nil {
		return traversalFailure(kind, info.Path, err)
	}
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return &Error{Kind: KindCancelled, Op: kind, Path: child.Path, Cause: err}
		}
		childDestination := dest.Join(destination, child.Name)
		if !child.IsDir() {
			plan.entries = append(plan.entries, plannedFile(files, child, childDestination))
			plan.filesTotal++
			plan.bytesTotal += child.Size
			continue
		}
		nextDepth := depth + 1
		nextSymlinkDepth := symlinkDepth
		if child.Symlink {
			if !followSymlinks || !domainsftp.ShouldFollowSymlinkDirectory(symlinkDepth) {
				continue
			}
			nextSymlinkDepth++
		}
		if err := planDirectory(ctx, files, dest, kind, budget, child, childDestination,
			nextDepth, nextSymlinkDepth, ancestors, followSymlinks, plan); err != nil {
			return err
		}
	}
	return nil
}

// directoryCanonical resolves a directory's real identity for cycle
// detection. A symlink is resolved through its link target so an alias back to
// an ancestor is refused.
func directoryCanonical(files FileSystem, info domainsftp.Entry) string {
	if info.Symlink && info.LinkTarget != "" {
		target := info.LinkTarget
		if !isAbsoluteTarget(files.Style(), target) {
			target = files.Join(files.Parent(info.Path), target)
		}
		return files.Canonical(target)
	}
	return files.Canonical(info.Path)
}

func isAbsoluteTarget(style PathStyle, target string) bool {
	if style == StyleWindows {
		return strings.HasPrefix(target, `\\`) || (len(target) >= 3 && target[1] == ':' &&
			(target[2] == '\\' || target[2] == '/'))
	}
	return strings.HasPrefix(target, "/")
}

// traversalFailure maps a budget error onto the operation's too-large kind.
func traversalFailure(kind domainsftp.OperationKind, path string, cause error) error {
	return failure(KindTooLarge, kind, path, cause)
}

// scannedSource is what the conflict scan learned about one top-level source:
// its stat, so the ordered plan does not read it again, the destination
// conflict type it meets (empty when it meets none), and how many later
// sources meet that same type.
type scannedSource struct {
	info      domainsftp.Entry
	read      bool
	typeKey   string
	remaining int
}

// scanRequestConflicts pre-reads the top-level sources and records, for each
// one, how many later sources in the same request meet the same destination
// conflict type. The apply-to-all prompt is built from that count, so its
// number names real conflicts instead of every source left. A destination is
// still stat'ed by the plan itself, keeping the write path's own fresh view.
// A source or destination that cannot be inspected is left out, because the
// ordered plan reports that failure in its turn and counting it ahead of time
// would change which error the user sees first.
func scanRequestConflicts(ctx context.Context, request CopyRequest,
	policy ConflictPolicy) []scannedSource {
	if policy != ConflictAsk || request.OnConflict == nil || len(request.Sources) < 2 {
		return nil
	}
	scanned := make([]scannedSource, len(request.Sources))
	for index, source := range request.Sources {
		if request.KnownTargets[source] != "" {
			// A resumed source keeps the answer it already received, so it
			// will not prompt again.
			continue
		}
		info, err := request.Source.Stat(ctx, source)
		if err != nil {
			continue
		}
		scanned[index].info = info
		scanned[index].read = true
		destination := destinationFor(request, source)
		existing, err := request.Dest.Stat(ctx, destination)
		if err != nil {
			continue
		}
		scanned[index].typeKey = domainsftp.ConflictTypeKey(info.IsDir(), domainsftp.ExistingKindOf(existing))
	}
	// Walk backwards so each source sees only the conflicts after it: an
	// answer covers the later same-type conflicts, not the earlier ones.
	seen := map[string]int{}
	for index := len(scanned) - 1; index >= 0; index-- {
		key := scanned[index].typeKey
		if key == "" {
			continue
		}
		scanned[index].remaining = seen[key]
		seen[key]++
	}
	return scanned
}

// destinationFor names where one top-level source lands in this request. The
// name comes from the source's own Base: a Windows source and a POSIX
// destination do not agree on separators. The plan and the conflict scan must
// derive the same path, so both call this.
func destinationFor(request CopyRequest, source string) string {
	return request.Dest.Join(request.DestDir, request.Source.Base(source))
}

// resolveDestination returns the destination path, whether to skip it, and the
// policy that actually applies after an ask. A duplicate name is chosen from
// the names already on disk plus this request's chosen names plus the queue's
// reservations, so two live Transfers never write the same new name.
func resolveDestination(ctx context.Context, request CopyRequest,
	policy ConflictPolicy, source string, info domainsftp.Entry, remaining int,
	plannedNames map[string]bool) (string, bool, ConflictPolicy, error) {
	kind := kindOf(request)
	destination := destinationFor(request, source)
	name := request.Dest.Base(destination)
	reserve := func(path string, directory bool) error {
		if request.ReserveName != nil && !request.ReserveName(path, directory) {
			return failure(KindReserved, kind, path, nil)
		}
		return nil
	}
	allocateDuplicate := func() (string, error) {
		taken, err := namesIn(ctx, request.Dest, request.DestDir)
		if err != nil {
			return "", err
		}
		chosen := domainsftp.CopyName(name, func(candidate string) bool {
			if taken[candidate] {
				return true
			}
			full := request.Dest.Join(request.DestDir, candidate)
			if plannedNames[full] {
				return true
			}
			if request.ReserveName != nil && !request.ReserveName(full, info.IsDir()) {
				return true
			}
			return false
		})
		full := request.Dest.Join(request.DestDir, chosen)
		plannedNames[full] = true
		return full, nil
	}

	if policy == ConflictDuplicate {
		chosen, err := allocateDuplicate()
		if err != nil {
			return "", false, policy, err
		}
		return chosen, false, policy, nil
	}

	existing, err := request.Dest.Stat(ctx, destination)
	if err != nil {
		if KindOf(err) != KindNotFound {
			return "", false, policy, wrap(kind, destination, err)
		}
		if err := reserve(destination, info.IsDir()); err != nil {
			return "", false, policy, err
		}
		return destination, false, policy, nil
	}

	conflict := domainsftp.Conflict{
		SourcePath:       source,
		TargetPath:       destination,
		Name:             name,
		IsDirectory:      info.IsDir(),
		ExistingKind:     domainsftp.ExistingKindOf(existing),
		ExistingSize:     existing.Size,
		ExistingModified: existing.ModTime,
		NewSize:          info.Size,
		NewModified:      info.ModTime,
		Remaining:        remaining,
		// One answer covers the later conflicts of this type. Sources with a
		// free destination are not conflicts and are not counted.
		ApplyToAllCount: remaining,
	}

	action := policy
	if action == ConflictAsk {
		action = ConflictStop
		if request.OnConflict != nil {
			action = request.OnConflict(conflict).Action
		}
	}
	switch action {
	case ConflictStop:
		return "", false, action, failure(KindExists, kind, destination, nil)
	case ConflictSkip:
		return destination, true, action, nil
	case ConflictDuplicate:
		chosen, err := allocateDuplicate()
		if err != nil {
			return "", false, action, err
		}
		return chosen, false, action, nil
	case ConflictMerge:
		if !conflict.CanMerge() {
			return "", false, action, failure(KindTypeMismatch, kind, destination, nil)
		}
		if err := reserve(destination, true); err != nil {
			return "", false, action, err
		}
		return destination, false, action, nil
	case ConflictReplace:
		if !conflict.CanReplace() {
			return "", false, action, failure(KindTypeMismatch, kind, destination, nil)
		}
		if err := reserve(destination, conflict.IsDirectory); err != nil {
			return "", false, action, err
		}
		return destination, false, action, nil
	default:
		return "", false, action, failure(KindInternal, kind, destination,
			errors.New("sftp: unknown conflict action"))
	}
}

// unchangedAtDestination reports whether the destination file already matches
// the source by size and whole-second mtime. A failed stat is "not known to be
// unchanged": the copy then reports the real problem instead of silently
// skipping.
func unchangedAtDestination(ctx context.Context, dest FileSystem, entry plannedEntry) bool {
	existing, err := dest.Stat(ctx, entry.TargetPath)
	if err != nil || existing.IsDir() || existing.Symlink {
		return false
	}
	return domainsftp.UnchangedTransfer(entry.Size, entry.Modified, existing.Size, existing.ModTime)
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

// stagedVerification is what a staged file must prove before it may take the
// destination name. Size is the source size the plan observed; ModeKnown asks
// the transaction to preserve and verify the original destination's mode.
type stagedVerification struct {
	Size      int64
	Mode      uint32
	ModeKnown bool
}

// fileCopy names one file's staged write: the source entry, the destination it
// must take, the durable stage owner, and what the stage must prove.
type fileCopy struct {
	info        domainsftp.Entry
	destination string
	stageKey    string
	verify      stagedVerification
}

// copyFile streams one file into a temporary sibling, verifies the staged
// size (and the preserved mode for a replacement), and renames it into place.
// A cancelled, failed, or unverified copy removes the temporary file, so a
// half-written file never carries the destination name.
func copyFile(ctx context.Context, source, dest FileSystem, item fileCopy,
	plan *Progress, report func(string)) (int64, error) {
	reader, err := source.Open(ctx, item.info.Path)
	if err != nil {
		return 0, wrap(domainsftp.OpCopy, item.info.Path, err)
	}
	defer reader.Close()

	temporary := dest.Join(dest.Parent(item.destination),
		stageName(item.stageKey, dest.Base(item.destination), item.destination))
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
			return fail(&Error{Kind: KindCancelled, Op: domainsftp.OpCopy, Path: item.info.Path, Cause: err})
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, err := writer.Write(buffer[:n]); err != nil {
				return fail(wrap(domainsftp.OpCopy, item.destination, err))
			}
			written += int64(n)
			plan.BytesDone += int64(n)
			sinceProgress += int64(n)
			if sinceProgress >= progressStep {
				sinceProgress = 0
				report(item.info.Name)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fail(wrap(domainsftp.OpCopy, item.info.Path, readErr))
		}
	}
	if err := writer.Close(); err != nil {
		return fail(wrap(domainsftp.OpCopy, temporary, err))
	}
	// A replacement publishes the mode the destination already had, so an
	// executable final is not silently demoted to the umask default. A refused
	// chmod fails the promotion instead of publishing the wrong permissions.
	if item.verify.ModeKnown {
		if err := dest.Chmod(ctx, temporary, item.verify.Mode&domainsftp.MaxMode); err != nil {
			return fail(wrap(domainsftp.OpCopy, temporary, err))
		}
	} else if item.info.Mode != 0 {
		_ = dest.Chmod(ctx, temporary, item.info.Mode&domainsftp.MaxMode)
	}
	if err := verifyStaged(ctx, dest, temporary, item.destination, item.verify); err != nil {
		return fail(err)
	}
	if err := dest.Rename(ctx, temporary, item.destination); err != nil {
		err = renameOverOccupiedFile(ctx, dest, temporary, item.destination, err)
		if err != nil {
			_ = dest.Remove(context.WithoutCancel(ctx), temporary)
			return written, wrap(domainsftp.OpCopy, item.destination, err)
		}
	}
	return written, nil
}

// verifyStaged checks the staged file against the size the plan observed and,
// for a replacement, the mode the transaction preserved. A transport that
// reported a successful close but dropped bytes, or a server that ignored the
// mode change, must not be able to publish the result.
func verifyStaged(ctx context.Context, dest FileSystem, temporary, destination string,
	verify stagedVerification) error {
	staged, err := dest.Stat(ctx, temporary)
	if err != nil {
		return wrap(domainsftp.OpCopy, temporary, err)
	}
	if staged.IsDir() {
		return failure(KindTypeMismatch, domainsftp.OpCopy, destination, nil)
	}
	if staged.Size != verify.Size {
		return failure(KindInternal, domainsftp.OpCopy, destination,
			fmt.Errorf("sftp: staged %s has %d bytes, expected %d",
				dest.Base(temporary), staged.Size, verify.Size))
	}
	if verify.ModeKnown {
		want := verify.Mode & domainsftp.MaxMode
		if staged.Mode&domainsftp.MaxMode != want {
			return failure(KindInternal, domainsftp.OpCopy, destination,
				fmt.Errorf("sftp: staged %s has mode %04o, expected %04o",
					dest.Base(temporary), staged.Mode, want))
		}
	}
	return nil
}

// renameOverOccupiedFile retries a rename that reported the destination as
// occupied. A merge writes files into an existing tree, and a transport
// without posix-rename semantics reports the name instead of replacing it, so
// the old file is removed first.
//
// Only a non-directory entry may be removed. An incoming file that meets a
// destination directory is a type mismatch: deleting the directory would
// discard its whole subtree, which no conflict policy asked for.
func renameOverOccupiedFile(ctx context.Context, dest FileSystem, temporary, destination string,
	cause error) error {
	if KindOf(cause) != KindExists {
		return cause
	}
	existing, err := dest.Stat(ctx, destination)
	if err != nil {
		return cause
	}
	if existing.IsDir() && !existing.Symlink {
		return failure(KindTypeMismatch, domainsftp.OpCopy, destination, nil)
	}
	if err := dest.Remove(ctx, destination); err != nil {
		return cause
	}
	return dest.Rename(ctx, temporary, destination)
}

// stageName is the staging file's name beside the destination. ADR-0012
// fixes the prefix and suffix; cleanup and resume depend on them.
//
// A request with a durable StageKey hashes that key together with the target,
// so the same top-level Transfer writes the same staging path in every
// process. A resume reuses (and truncates) the interrupted process's staging
// file and the successful rename removes it, rather than leaving an orphan.
// The hash includes the task identity, so two concurrent Transfers that name
// the same destination still use different staging files and neither can
// delete the other's. A request without a durable owner keeps the original
// random eight-hex suffix.
func stageName(stageKey, name, target string) string {
	suffix := randomSuffix()
	if stageKey != "" {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(stageKey))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(target))
		suffix = fmt.Sprintf("%016x", hash.Sum64())
	}
	return stagingPrefix + suffix + "-" + name + stagingSuffix
}

// The staging prefix and suffix are the ADR-0012 contract. Cleanup and resume
// depend on them, so one predicate answers the question for every caller.
const (
	stagingPrefix = ".netcatty-upload-"
	stagingSuffix = ".part"
)

// IsStagingName reports whether a base name is one of this package's staging
// files. Callers use it to identify the contract without re-implementing it.
func IsStagingName(name string) bool {
	return strings.HasPrefix(name, stagingPrefix) && strings.HasSuffix(name, stagingSuffix)
}

// backupName is the sibling that holds the old destination during a
// replacement. The hash keeps two same-named destinations in different
// directories apart while the base name keeps the file recognizable.
func backupName(dest FileSystem, destination string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(destination))
	return fmt.Sprintf(".netcatty-backup-%016x-%s.bak", hash.Sum64(), dest.Base(destination))
}

// replacementBackup is the original destination moved aside for a
// replacement. Mode is the mode the promoted file must keep, and it is known
// only when this attempt moved a live destination (a resumed replacement
// continues a tree whose mode the earlier attempt already applied).
type replacementBackup struct {
	path      string
	mode      uint32
	modeKnown bool
}

// backupPath is the sibling that holds the old destination during a
// replacement.
func backupPath(dest FileSystem, destination string) string {
	return dest.Join(dest.Parent(destination), backupName(dest, destination))
}

// takeBackup moves an existing destination aside before a replacement and
// reports the mode the promoted entry must keep. A missing destination needs
// no backup; the move is a rename in the same directory, so it does not copy
// the tree.
//
// A backup left by an interrupted earlier attempt is reconciled first. For a
// file, a live destination means the backup is stale and is removed, while a
// missing destination means the backup is the only known-good copy and is
// restored before this transaction creates its own. A directory replacement
// cannot make that call: an attempt that died before its durable checkpoint
// created the new root but wrote none of the original into it, so a live root
// beside a backup may be a partial tree. The backup is always the known-good
// copy there, so it is restored over whatever root is present.
func takeBackup(ctx context.Context, dest FileSystem, kind domainsftp.OperationKind,
	destination string, directory bool) (replacementBackup, error) {
	backup := backupPath(dest, destination)
	backupInfo, backupErr := dest.Stat(ctx, backup)
	hasBackup := backupErr == nil
	if backupErr != nil && KindOf(backupErr) != KindNotFound {
		return replacementBackup{}, wrap(kind, backup, backupErr)
	}
	targetInfo, targetErr := dest.Stat(ctx, destination)
	hasTarget := targetErr == nil
	if targetErr != nil && KindOf(targetErr) != KindNotFound {
		return replacementBackup{}, wrap(kind, destination, targetErr)
	}
	if hasBackup {
		if hasTarget && directory {
			if err := dest.RemoveAll(context.WithoutCancel(ctx), destination); err != nil {
				return replacementBackup{}, wrap(kind, destination, err)
			}
			hasTarget = false
		}
		if hasTarget {
			// The destination is live, so this backup belongs to an earlier
			// committed replacement and is stale.
			if err := dest.RemoveAll(context.WithoutCancel(ctx), backup); err != nil {
				return replacementBackup{}, wrap(kind, backup, err)
			}
		} else {
			// An interrupted promotion left the backup as the only known-good
			// copy; put it back before moving anything else.
			if err := dest.Rename(context.WithoutCancel(ctx), backup, destination); err != nil {
				return replacementBackup{}, wrap(kind, destination, err)
			}
			targetInfo = backupInfo
			hasTarget = true
		}
	}
	if !hasTarget {
		return replacementBackup{}, nil
	}
	if err := dest.Rename(ctx, destination, backup); err != nil {
		return replacementBackup{}, wrap(kind, destination, err)
	}
	// A transport that reports no mode cannot prove one, so the promotion
	// verifies only what the plan observed.
	return replacementBackup{path: backup, mode: targetInfo.Mode, modeKnown: targetInfo.Mode != 0}, nil
}

// retainedBackup names the backup a paused or interrupted replacement kept.
// A resumed source continues in the partial new tree, then removes this
// backup once the replacement completes. Its mode was already applied by the
// attempt that wrote the partial tree, so this attempt does not re-apply it.
func retainedBackup(ctx context.Context, dest FileSystem, kind domainsftp.OperationKind,
	destination string) (replacementBackup, error) {
	backup := backupPath(dest, destination)
	if _, err := dest.Stat(ctx, backup); err != nil {
		if KindOf(err) == KindNotFound {
			return replacementBackup{}, nil
		}
		return replacementBackup{}, wrap(kind, backup, err)
	}
	return replacementBackup{path: backup}, nil
}

// removeBackup deletes the backup after a committed replacement. A failure is
// reported rather than ignored: the promoted destination stays, the backup
// stays recoverable, and the caller knows the transaction did not finish its
// cleanup. A late cancel cannot skip it, because publication already happened.
func removeBackup(ctx context.Context, dest FileSystem, kind domainsftp.OperationKind,
	backup string) error {
	if err := dest.RemoveAll(context.WithoutCancel(ctx), backup); err != nil {
		return &Error{
			Kind: KindInternal,
			Op:   kind,
			Path: backup,
			Cause: fmt.Errorf("sftp: remove backup %s: %w",
				dest.Base(backup), err),
		}
	}
	return nil
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

// randomSuffix returns eight hexadecimal characters for a staging name.
func randomSuffix() string {
	var data [4]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(data[:])
}
