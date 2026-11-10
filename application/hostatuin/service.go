// Package hostatuin detects a Host's existing Atuin installation and imports
// its history read-only. Detection and the copy use a separate SFTP channel
// before the interactive terminal opens, so the terminal channel is never
// consumed. The package never runs the Atuin binary, never executes a remote
// command, and never writes to the Host: it only stats, reads, and copies the
// database files and a few non-executed installation hints.
//
// Detection requires install evidence that Atuin is installed: a readable
// config file, a known binary path, or an ATUIN_HOME/XDG_DATA_HOME assignment
// in a shell profile. A regular candidate history database is a presence
// candidate rather than evidence on its own: a clean check that finds a
// leftover history.db without install evidence is conclusive absence, so a Host
// that removed Atuin regains the normal capture hook instead of losing history
// forever. A clean check that finds installation evidence but no
// database is also conclusive absence; a database created later is picked up on
// the next Session. Profile hints only see assignments saved in the usual
// profile files; an ATUIN_HOME exported only in an interactive shell is not
// visible, and an Atuin binary outside the known paths is not detected. Those
// Hosts fall back to the capture hook rather than risk reading an arbitrary
// file. Anything the copy cannot prove consistent is rejected and retried on a
// later Session.
//
// Before any evidence, a check that cannot complete is treated as absence: a
// false Present would permanently disable journaled history for a Host that
// does not run Atuin. Ordinary shell profile failures are not evidence at all;
// an oversized or unreadable generic profile is simply skipped. Once detection
// has any evidence, every later failure is inconclusive rather than absent: a
// non-not-exist Stat or Open error, a timeout, a copy or parse failure, or an
// unknown schema keeps Present true and returns an error. The Session then
// keeps the Netcatty capture hook skipped, captures commands transiently for
// suggestions, shows the import-failed notice, and retries on a later Session.
// Present false therefore means a conclusive absence, not merely a check that
// failed.
//
// A conclusive-absence check relies on the SFTP server reporting a missing path
// as SSH_FX_NO_SUCH_FILE, which pkg/sftp maps to fs.ErrNotExist. A
// non-conforming server that answers a missing path with a generic failure
// makes the check inconclusive: before any evidence that is treated as absence
// under the pre-evidence policy above, and after evidence it keeps the Host
// Present.
//
// One accepted residual window remains: if the read-only channel cannot be
// opened at all, or its home directory cannot be read, before any evidence is
// seen, the probe reports absence. A Host without SFTP is common, and treating
// every channel failure as Atuin would disable persistent history for every
// saved SSH Host without a read-only channel. The cost is that an intermittent
// channel failure on a Host that also runs Atuin can journal a command that a
// later successful probe imports again from the Host's own Atuin. The
// alternative - suppressing persistent capture for every Host whose channel is
// unavailable - is worse.
package hostatuin

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// DefaultTimeout bounds one detection/import attempt so Session open does not
// wait forever on a slow Host. A timed-out attempt retries on a later Session.
const DefaultTimeout = 15 * time.Second

// DefaultMaxTotalBytes caps the bytes one attempt copies from a Host. Real
// Atuin databases are far smaller; the cap keeps a misreported or hostile file
// from filling the local disk.
const DefaultMaxTotalBytes int64 = 1 << 30

// closeGrace bounds how long a timed-out attempt waits for a blocked remote
// read to notice its closed channel. The result channel is buffered, so a read
// that never returns cannot block the next Session; closing a real SFTP client
// unblocks its pending requests.
const closeGrace = time.Second

var (
	// ErrInconsistentCopy reports a copy whose source changed while it was
	// read, or whose database, WAL, and SHM files do not describe the same
	// instant. The Host is untouched and the import checkpoint does not
	// advance.
	ErrInconsistentCopy = errors.New("hostatuin: inconsistent Atuin copy")
	// ErrCopyTooLarge reports a remote file that exceeds the per-attempt copy
	// budget. It is rejected before any of it is written locally.
	ErrCopyTooLarge = errors.New("hostatuin: Atuin copy exceeds the size limit")
	// ErrProbeUnavailable reports that detection could not complete and the Host
	// may or may not run Atuin. The probe still reports the Host Present so the
	// Session captures transiently instead of journaling a command a later
	// import could repeat.
	ErrProbeUnavailable = errors.New("hostatuin: Atuin detection unavailable")
)

// HostRef is the Host context an import needs. It is deliberately small: the
// remote source opens the Host's connection by identity, and the origin
// fallback uses the same captured values as a normal command.
type HostRef struct {
	ID       model.HostID
	Hostname string
	Username string
	Label    string
}

// FileInfo is the metadata the importer needs about one remote file. Symlink
// reports that the path resolved through a link; the importer never follows
// one for a database or hint file.
type FileInfo struct {
	Size    int64
	ModTime time.Time
	Regular bool
	Symlink bool
}

// RemoteFiles is the read-only remote file view the importer uses. It exposes
// no write or execute operation, so the Host cannot be modified by this path.
type RemoteFiles interface {
	// Root returns the user's home directory.
	Root(context.Context) (string, error)
	// Stat reads one entry. A missing or unreadable file is an error.
	Stat(context.Context, string) (FileInfo, error)
	// Open starts reading one file.
	Open(context.Context, string) (io.ReadCloser, error)
	// Close closes the channel and must unblock pending Root, Stat, and read
	// calls. It is safe to call more than once.
	Close() error
}

// RemoteSource opens the separate read-only channel for a Host. The app
// implements it with the SFTP file protocol. A channel that cannot be opened
// before any install evidence is seen is treated as absence: most saved SSH
// Hosts do not provide SFTP, and treating every channel failure as Atuin would
// disable persistent history for all of them. The package doc describes the
// accepted duplicate window this leaves.
type RemoteSource interface {
	OpenReadOnly(context.Context, model.HostID) (RemoteFiles, error)
}

// Database reads a copied Atuin history database. The app implements it with
// adapters/atuinimport.
type Database interface {
	ReadHistory(context.Context, string) ([]domainhistory.AtuinRow, error)
}

// Recorder writes imported rows and the one-time notice state. It returns the
// number of newly written rows; the app implements it with the history
// service's one read-only import port.
type Recorder interface {
	ImportHostAtuin(context.Context, string, []domainhistory.AtuinRow, string) (int, error)
	MarkHostAtuinNoticed(context.Context, string) (bool, error)
}

// ProbeResult reports one detection attempt. Present means the Host runs
// Atuin, so the Netcatty capture hook is skipped and the Session captures
// commands transiently. It stays true when detection or the import could not
// complete after evidence was seen: a Stat/Open failure, a timeout, an
// inconsistent copy, or an unknown schema. Present false means the check found
// no usable evidence, so the normal capture hook runs; a pre-evidence check
// failure is treated the same way and is the documented residual duplicate
// window.
type ProbeResult struct {
	Present  bool
	Imported int
}

// ImportResult reports one background import after a Session ends.
type ImportResult struct {
	Present  bool
	Imported int
}

// Option customizes a Service.
type Option func(*Service)

// WithRemote supplies the read-only remote file source.
func WithRemote(remote RemoteSource) Option {
	return func(s *Service) { s.remote = remote }
}

// WithDatabase supplies the copied-database reader.
func WithDatabase(database Database) Option {
	return func(s *Service) { s.database = database }
}

// WithRecorder supplies the journal writer.
func WithRecorder(recorder Recorder) Option {
	return func(s *Service) { s.recorder = recorder }
}

// WithTempDir replaces the private temporary-directory factory.
func WithTempDir(create func() (string, error)) Option {
	return func(s *Service) {
		if create != nil {
			s.makeTempDir = create
		}
	}
}

// WithTimeout replaces the per-attempt bound.
func WithTimeout(timeout time.Duration) Option {
	return func(s *Service) {
		if timeout > 0 {
			s.timeout = timeout
		}
	}
}

// WithMaxTotalBytes replaces the maximum bytes one attempt copies from a Host.
func WithMaxTotalBytes(limit int64) Option {
	return func(s *Service) {
		if limit > 0 {
			s.maxTotalBytes = limit
		}
	}
}

// Service detects and imports one Host's existing Atuin history. It is safe
// for concurrent use: each attempt owns its remote channel and temp directory.
type Service struct {
	remote        RemoteSource
	database      Database
	recorder      Recorder
	makeTempDir   func() (string, error)
	timeout       time.Duration
	maxTotalBytes int64
}

// New returns a Service with the platform defaults.
func New(opts ...Option) *Service {
	service := &Service{
		makeTempDir: func() (string, error) {
			return os.MkdirTemp("", "netcatty-atuin-")
		},
		timeout:       DefaultTimeout,
		maxTotalBytes: DefaultMaxTotalBytes,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Enabled reports whether the service has the remote and database ports it
// needs.
func (s *Service) Enabled() bool {
	return s != nil && s.remote != nil && s.database != nil
}

// Probe detects a Host's Atuin history and imports it. It runs before the
// interactive terminal opens. A clean check that finds no Atuin evidence, or a
// separate channel that cannot be opened before any evidence, reports Present
// false so the normal capture hook is used. Once any evidence is seen, Present
// stays true through every later failure - a Stat/Open error, a timeout, an
// inconsistent copy, a parse failure, or an unknown schema - so the Session
// captures transiently and a later Session retries.
func (s *Service) Probe(ctx context.Context, ref HostRef) (ProbeResult, error) {
	if !s.Enabled() || ref.ID == "" {
		return ProbeResult{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	snapshot, imported, err := s.attempt(ctx, ref)
	if !snapshot.Present {
		return ProbeResult{}, nil
	}
	return ProbeResult{Present: true, Imported: imported}, err
}

// MarkNoticed records the Host's one-time notice and reports whether this call
// set it. The Session calls it after opening successfully, so a failed terminal
// start does not consume the notice, and two concurrent opens publish it once.
func (s *Service) MarkNoticed(ctx context.Context, ref HostRef) bool {
	if s == nil || s.recorder == nil || ref.ID == "" {
		return false
	}
	first, err := s.recorder.MarkHostAtuinNoticed(ctx, string(ref.ID))
	return err == nil && first
}

// Import copies and imports a Host's Atuin history without publishing a
// notice. The Session calls it after its terminal closes to pick up rows Atuin
// wrote during the Session.
func (s *Service) Import(ctx context.Context, ref HostRef) (ImportResult, error) {
	if !s.Enabled() || ref.ID == "" {
		return ImportResult{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	snapshot, imported, err := s.attempt(ctx, ref)
	if !snapshot.Present {
		return ImportResult{}, nil
	}
	return ImportResult{Present: true, Imported: imported}, err
}

// ImportAfterClose starts the post-Session import without blocking teardown.
// A failure is silent: the next Session open retries from the same checkpoint.
func (s *Service) ImportAfterClose(ref HostRef) {
	if !s.Enabled() || ref.ID == "" {
		return
	}
	go func() {
		// The Session context is already canceled at teardown, so the import
		// owns a fresh bounded context.
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		defer cancel()
		_, _ = s.Import(ctx, ref)
	}()
}

// snapshot is one read of the Host's Atuin files.
type snapshot struct {
	Present bool
	rows    []domainhistory.AtuinRow
}

// attempt reads the Host's Atuin database and imports it, returning the number
// of newly written rows. Absence and failure stay distinct: a failure after
// detection keeps Present true so the hook stays skipped and retries later.
func (s *Service) attempt(ctx context.Context, ref HostRef) (snapshot, int, error) {
	snapshot, err := s.read(ctx, ref)
	if !snapshot.Present || err != nil || len(snapshot.rows) == 0 || s.recorder == nil {
		return snapshot, 0, err
	}
	imported, err := s.recorder.ImportHostAtuin(ctx, string(ref.ID), snapshot.rows, hostOrigin(ref))
	if err != nil {
		return snapshot, 0, err
	}
	return snapshot, imported, nil
}

const (
	historyName = "history.db"
	walName     = "history.db-wal"
	shmName     = "history.db-shm"

	// maxHintBytes bounds every config or profile read used for detection.
	// These files only provide hints; a larger file is ignored rather than
	// copied into memory.
	maxHintBytes = 64 << 10
)

// candidateDataDirs are Atuin's default data-directory layouts, newest first.
// ATUIN_HOME and XDG_DATA_HOME discovered in a shell profile are tried before
// these defaults; a DB path in config.toml takes precedence over both.
var candidateDataDirs = []string{
	".atuin/data",
	".local/share/atuin",
	"Library/Application Support/atuin",
	"AppData/Roaming/atuin",
	".atuin",
}

// candidateConfigFiles are Atuin's default config locations relative to home.
var candidateConfigFiles = []string{
	".config/atuin/config.toml",
	".atuin/config.toml",
	"Library/Application Support/atuin/config.toml",
	"AppData/Roaming/atuin/config.toml",
}

// candidateBinaries are common installation markers. They are only stat'ed,
// never executed. Symlinks are allowed here because package managers commonly
// install the command as one.
var candidateBinaries = []string{
	".atuin/bin/atuin",
	".cargo/bin/atuin",
	".local/bin/atuin",
	".nix-profile/bin/atuin",
	"AppData/Local/Programs/atuin/atuin.exe",
	"scoop/shims/atuin.exe",
	"AppData/Local/Microsoft/WinGet/Links/atuin.exe",
	"/run/current-system/sw/bin/atuin",
	"/usr/local/bin/atuin",
	"/usr/bin/atuin",
	"/opt/homebrew/bin/atuin",
	"/opt/local/bin/atuin",
}

// candidateProfiles are read only for ATUIN_HOME and XDG_DATA_HOME hints.
var candidateProfiles = []string{
	".profile",
	".bash_profile",
	".bashrc",
	".zshrc",
	".zshenv",
	".config/fish/config.fish",
}

// remoteHandle publishes the files of an attempt to the watchdog that closes
// them on a deadline. The mutex makes attach and close mutually exclusive, so
// either the watchdog closes the opened files or the attempt learns that the
// deadline passed and closes them itself.
type remoteHandle struct {
	mu    sync.Mutex
	files RemoteFiles
	done  bool
}

// attach publishes the opened files. It reports false once the attempt has
// timed out, in which case the caller must close the files.
func (h *remoteHandle) attach(files RemoteFiles) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return false
	}
	h.files = files
	return true
}

// close closes the files once. Later calls do nothing.
func (h *remoteHandle) close() error {
	h.mu.Lock()
	files := h.files
	h.files = nil
	h.done = true
	h.mu.Unlock()
	if files != nil {
		return files.Close()
	}
	return nil
}

type readOutcome struct {
	snapshot snapshot
	err      error
}

// read runs one attempt on its own goroutine so the deadline can close the
// channel. The SFTP operations the attempt uses (Getwd, Lstat, Open, and file
// reads) do not observe a context, so the close is what actually bounds them.
// The result channel is buffered: a read that never returns still cannot block
// a later Session or leak a send.
func (s *Service) read(ctx context.Context, ref HostRef) (snapshot, error) {
	handle := &remoteHandle{}
	// found is set as soon as any evidence is seen, including a regular
	// candidate database. A deadline can fire after evidence but before
	// detectionCandidates clears the flag on a conclusive-absence return;
	// reporting Present in that window is the conservative choice because
	// evidence was already seen, and it can only cost transient capture, never a
	// journaled duplicate.
	found := &atomic.Bool{}
	done := make(chan readOutcome, 1)
	go func() {
		snap, err := s.readRemote(ctx, ref, handle, found)
		done <- readOutcome{snapshot: snap, err: err}
	}()

	select {
	case outcome := <-done:
		_ = handle.close()
		return outcome.snapshot, outcome.err
	case <-ctx.Done():
		// Closing the SFTP client is the only way to interrupt a blocked
		// remote call. Close in its own goroutine so even a transport whose
		// Close waits on the network cannot hold the Session past the grace
		// period. Then wait briefly for the attempt to unwind and remove its
		// temporary copy; a real SFTP close unblocks immediately.
		go func() { _ = handle.close() }()
		select {
		case outcome := <-done:
			return outcome.snapshot, outcome.err
		case <-time.After(closeGrace):
		}
		return snapshot{Present: found.Load()}, ctx.Err()
	}
}

// readRemote opens the channel, decides whether the Host runs Atuin, and reads
// the first candidate database that parses as an Atuin history. A detection
// error is reported as Present true: after any evidence the Host may be running
// Atuin, so the Session must capture transiently instead of journaling a command
// a later import could repeat. Before any evidence, a check that cannot complete
// - a channel that does not open, a home directory that cannot be read, or a
// Stat/Open failure on an Atuin-specific path - is reported as absence. The
// channel and home-directory cases are the documented residual duplicate
// window: an intermittent channel failure on a Host that runs Atuin can journal
// a row that a later import repeats, but treating every channel failure as
// Atuin would disable persistent history for every saved SSH Host without SFTP.
//
// An unknown schema lets the remaining candidates be tried, and when none
// parses the Host is still reported present with ErrUnknownAtuinSchema rather
// than falling back to the Netcatty hook. A candidate that cannot be stat'ed
// during this pass is likewise inconclusive and keeps the Host present.
func (s *Service) readRemote(ctx context.Context, ref HostRef, handle *remoteHandle,
	found *atomic.Bool) (snapshot, error) {
	files, err := s.remote.OpenReadOnly(ctx, ref.ID)
	if err != nil {
		// Accepted residual window: a Host without SFTP is common, so a
		// channel that never opens stays absent. See the package doc.
		return snapshot{}, nil
	}
	if !handle.attach(files) {
		// The deadline passed while the channel was opening; do not leave it
		// behind.
		_ = files.Close()
		return snapshot{}, nil
	}
	defer handle.close()

	root, err := files.Root(ctx)
	if err != nil || root == "" {
		// Same accepted window as an unopenable channel: absence before any
		// install evidence is conclusive.
		return snapshot{}, nil
	}

	candidates, installed, err := s.detectionCandidates(ctx, files, root, found)
	if err != nil {
		// Detection could not finish after (or while looking for) evidence.
		// Keep the Host present so the hook stays skipped and a later Session
		// retries.
		return snapshot{Present: true}, errors.Join(ErrProbeUnavailable, err)
	}
	if !installed {
		return snapshot{}, nil
	}
	unknownSchema := false
	var statErr error
	for _, candidate := range candidates {
		info, err := files.Stat(ctx, candidate)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				// The check failed rather than proving absence; keep the
				// other candidates and remember that this pass was
				// inconclusive.
				statErr = errors.Join(statErr, err)
			}
			continue
		}
		if !info.Regular || info.Symlink {
			continue
		}
		dir, err := s.copy(ctx, files, candidate)
		if err != nil {
			if unknownSchema {
				// An earlier candidate had an unknown schema; keep the
				// fail-closed reason alongside this read failure.
				return snapshot{Present: true},
					errors.Join(domainhistory.ErrUnknownAtuinSchema, err)
			}
			return snapshot{Present: true}, err
		}
		rows, parseErr := s.readCopied(ctx, dir)
		if parseErr != nil {
			if errors.Is(parseErr, domainhistory.ErrUnknownAtuinSchema) {
				// This is not an Atuin database this build understands. Try
				// the next location. If none parses, the Host still runs
				// Atuin, so report it present and fail closed below: the
				// Netcatty hook must not run next to the Host's own Atuin.
				unknownSchema = true
				continue
			}
			if unknownSchema {
				return snapshot{Present: true},
					errors.Join(domainhistory.ErrUnknownAtuinSchema, parseErr)
			}
			return snapshot{Present: true}, parseErr
		}
		return snapshot{Present: true, rows: rows}, nil
	}
	if unknownSchema {
		// At least one regular candidate database existed but none was a
		// schema this build understands. Keeping the Host present skips the
		// Netcatty hook and leaves the checkpoint for a later retry.
		return snapshot{Present: true}, errors.Join(domainhistory.ErrUnknownAtuinSchema, statErr)
	}
	if statErr != nil {
		// Detection saw a regular candidate but this pass could not check it.
		return snapshot{Present: true}, errors.Join(ErrProbeUnavailable, statErr)
	}
	// Detection confirmed a regular candidate, so it can only have vanished
	// without an error; that is still not proof the Host stopped running
	// Atuin, so fail closed and retry.
	return snapshot{Present: true},
		fmt.Errorf("%w: the candidate database disappeared between detection and copy", ErrProbeUnavailable)
}

// detectionCandidates returns the database paths to try, in order, whether the
// Host shows non-executing evidence that Atuin is installed, and an error when
// the check could not complete. Install evidence is a readable Atuin config
// file, an ATUIN_HOME/XDG_DATA_HOME assignment in a shell profile, or a known
// binary path. A regular candidate database is a presence candidate, not
// install evidence on its own: once one is found, a later inconclusive check
// fails closed, but a clean check with no config, profile hint, or binary marker
// treats the database as a stale leftover and returns absence, so an
// uninstalled Atuin cannot suppress the capture hook forever. A clean check
// that finds install evidence but no database is likewise conclusive absence.
// Binary markers are checked last because they are only needed for a Host whose
// database exists but whose config and profiles are silent.
//
// Ordinary shell profile failures are not evidence: an oversized, unreadable,
// or otherwise failing generic profile is skipped silently, because a false
// Present would permanently disable journaled history for a Host that does not
// run Atuin. Other inconclusive checks - a non-not-exist Stat error on an Atuin
// config file or a candidate database - are accumulated. Before any evidence,
// such an error is still treated as conclusive absence and the found flag is
// cleared. After evidence (found is true) it is returned so the caller keeps
// the Host present instead of journaling a row a later import could repeat. A
// timeout that lands after evidence but before the conclusive-absence clear
// still reports Present; that conservative false-Present window is accepted
// because evidence was already seen.
func (s *Service) detectionCandidates(ctx context.Context, files RemoteFiles,
	root string, found *atomic.Bool) ([]string, bool, error) {
	installed := false
	explicit := ""
	var checkErr error
	for _, rel := range candidateConfigFiles {
		data, ok, err := readSmallFile(ctx, files, homePath(root, rel), maxHintBytes)
		if err != nil {
			checkErr = errors.Join(checkErr, err)
			continue
		}
		if !ok {
			continue
		}
		installed = true
		found.Store(true)
		if explicit == "" {
			explicit = configDatabasePath(data, root)
		}
	}

	var candidates []string
	if explicit != "" {
		// Atuin's db_path overrides its default data directory, so profile
		// hints cannot change where the database lives.
		candidates = append(candidates, explicit, path.Join(explicit, historyName))
	} else {
		hints, ok := profileHints(ctx, files, root)
		if ok {
			installed = true
			found.Store(true)
			candidates = append(candidates, hints...)
		}
	}
	for _, rel := range candidateDataDirs {
		candidates = append(candidates, path.Join(root, rel, historyName))
	}
	candidates = uniqueStrings(candidates)

	present := false
	for _, candidate := range candidates {
		info, err := files.Stat(ctx, candidate)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				// The check failed rather than proving absence; try the
				// remaining candidates and remember the inconclusive pass.
				checkErr = errors.Join(checkErr, err)
			}
			continue
		}
		if info.Regular && !info.Symlink {
			present = true
			found.Store(true)
			break
		}
	}
	if !present {
		if checkErr != nil && found.Load() {
			return nil, false, checkErr
		}
		// No regular database. A clean check is conclusive absence whether or
		// not install evidence was seen; after evidence, an inconclusive check
		// stays fail-closed instead. Without evidence a check that could not
		// complete is absence too, because a false Present would permanently
		// disable journaled history for a Host that does not run Atuin. Clear
		// the flag before returning so a timeout racing this return does not
		// resurrect an absence as Present.
		found.Store(false)
		return nil, false, nil
	}
	if !installed {
		for _, rel := range candidateBinaries {
			info, err := files.Stat(ctx, homePath(root, rel))
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					checkErr = errors.Join(checkErr, err)
				}
				continue
			}
			if info.Regular {
				installed = true
				found.Store(true)
				break
			}
		}
	}
	if !installed {
		if checkErr != nil && found.Load() {
			return nil, false, checkErr
		}
		// A regular database exists, but no config, profile hint, or binary
		// marks Atuin as installed: a leftover database without installation
		// evidence is conclusive absence. After evidence, an inconclusive
		// check stays fail-closed instead.
		found.Store(false)
		return nil, false, nil
	}
	return candidates, true, nil
}

// readSmallFile reads at most limit bytes of a regular file. Dotfiles are
// commonly symlinked, so a link is followed here; only the 64 KiB hint budget
// is ever read. A missing or non-regular file is conclusive absence for this
// hint: found is false with no error. A permission, connection, or read error,
// or a file larger than limit, is an inconclusive check and returns an error,
// because detection could not prove the hint absent.
func readSmallFile(ctx context.Context, files RemoteFiles, name string, limit int64) ([]byte, bool, error) {
	info, err := files.Stat(ctx, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("hostatuin: stat hint %s: %w", name, err)
	}
	if !info.Regular {
		return nil, false, nil
	}
	if info.Size > limit {
		return nil, false, fmt.Errorf("hostatuin: hint %s is larger than %d bytes", name, limit)
	}
	reader, err := files.Open(ctx, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("hostatuin: open hint %s: %w", name, err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, fmt.Errorf("hostatuin: read hint %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, false, fmt.Errorf("hostatuin: hint %s is larger than %d bytes", name, limit)
	}
	return data, true, nil
}

// configDatabasePath extracts a top-level db_path from an Atuin TOML config.
// Only the plain key = "value" form is understood; anything else is ignored
// and detection falls back to Atuin's default directories.
func configDatabasePath(data []byte, root string) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "db_path" {
			continue
		}
		if expanded, ok := expandProfilePath(shellValue(value), root); ok {
			return expanded
		}
		return ""
	}
	return ""
}

// profileHints returns database paths hinted by ATUIN_HOME or XDG_DATA_HOME
// assignments in the Host's shell profiles and whether any profile supplied
// one. Profiles are only hint sources: an oversized, unreadable, or otherwise
// failing generic profile is skipped silently rather than making the Atuin
// check inconclusive, because a false Present would permanently disable
// journaled history for a Host that does not run Atuin. Profiles are read only
// for those two variables: nothing is executed and the contents are never
// stored.
func profileHints(ctx context.Context, files RemoteFiles, root string) ([]string, bool) {
	var paths []string
	found := false
	for _, rel := range candidateProfiles {
		data, ok := readProfileHint(ctx, files, homePath(root, rel), maxHintBytes)
		if !ok {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			name, value, ok := profileAssignment(line)
			if !ok {
				continue
			}
			switch name {
			case "ATUIN_HOME":
				base, ok := expandProfilePath(value, root)
				if !ok {
					continue
				}
				found = true
				paths = append(paths, path.Join(base, historyName), path.Join(base, "data", historyName))
			case "XDG_DATA_HOME":
				base, ok := expandProfilePath(value, root)
				if !ok {
					continue
				}
				found = true
				dir := path.Join(base, "atuin")
				paths = append(paths, path.Join(dir, historyName), path.Join(dir, "data", historyName))
			}
		}
	}
	return paths, found
}

// readProfileHint reads at most limit bytes of a generic shell profile. These
// profiles are only hint sources, so every read or size failure is swallowed:
// a profile that cannot be read supplies no hint instead of making the Atuin
// check inconclusive. A false Present would permanently disable journaled
// history for a Host that does not run Atuin. Dotfiles are commonly symlinked,
// so a link is followed here; the contents are never executed or stored.
func readProfileHint(ctx context.Context, files RemoteFiles, name string, limit int64) ([]byte, bool) {
	data, ok, err := readSmallFile(ctx, files, name, limit)
	if err != nil {
		return nil, false
	}
	return data, ok
}

// profileAssignment reads one plain shell assignment. Only `NAME=value` and
// `export NAME=value` are understood; any other shell syntax is ignored.
func profileAssignment(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	name, raw, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	value := shellValue(raw)
	if value == "" {
		return "", "", false
	}
	return name, value, true
}

// shellValue strips a shell value's quoting and any trailing inline comment.
// It does not evaluate expansions.
func shellValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw[0] == '"' || raw[0] == '\'' {
		quote := raw[0]
		if end := strings.IndexByte(raw[1:], quote); end >= 0 {
			return raw[1 : 1+end]
		}
		return ""
	}
	if index := strings.IndexAny(raw, " \t#"); index >= 0 {
		raw = raw[:index]
	}
	return raw
}

// expandProfilePath resolves the home forms Atuin documents: ~, $HOME, and
// ${HOME}, plus absolute paths. A value that depends on any other variable is
// ignored rather than guessed without running a shell.
func expandProfilePath(value, root string) (string, bool) {
	value = strings.TrimSpace(value)
	switch {
	case value == "~", value == "$HOME", value == "${HOME}":
		return root, true
	case strings.HasPrefix(value, "~/"):
		return path.Join(root, value[len("~/"):]), true
	case strings.HasPrefix(value, "$HOME/"):
		return path.Join(root, value[len("$HOME/"):]), true
	case strings.HasPrefix(value, "${HOME}/"):
		return path.Join(root, value[len("${HOME}/"):]), true
	case strings.HasPrefix(value, "/"):
		return path.Clean(value), true
	}
	return "", false
}

// homePath resolves a candidate against the Host's home directory. An absolute
// candidate is used as-is.
func homePath(root, value string) string {
	if strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}
	return path.Join(root, value)
}

// uniqueStrings removes empty and duplicate candidates without changing their
// order.
func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// readCopied parses the local copy and removes it. Removal is best-effort for
// the same reason as in copy.
func (s *Service) readCopied(ctx context.Context, dir string) ([]domainhistory.AtuinRow, error) {
	defer func() { _ = os.RemoveAll(dir) }()
	return s.database.ReadHistory(ctx, filepath.Join(dir, historyName))
}

// fileState is one file's identity at one instant. Absent means the file
// proved missing (fs.ErrNotExist), or that it is a directory or symlink; any
// other Stat failure is an error, including on an optional WAL/SHM side file,
// because an unchecked side file could hide committed rows.
type fileState struct {
	present bool
	size    int64
	modTime time.Time
}

func (f fileState) equal(other fileState) bool {
	return f.present == other.present && f.size == other.size &&
		f.modTime.Equal(other.modTime)
}

// statState reads one optional file's identity. A missing file is absent with
// no error; any other Stat failure is returned so the caller can reject the
// copy instead of silently skipping a side file. A directory or symlink also
// counts as absent: the importer never follows a link out of the Atuin
// directory.
func statState(ctx context.Context, files RemoteFiles, name string) (fileState, error) {
	info, err := files.Stat(ctx, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileState{}, nil
		}
		return fileState{}, err
	}
	if !info.Regular || info.Symlink {
		return fileState{}, nil
	}
	return fileState{present: true, size: info.Size, modTime: info.ModTime}, nil
}

// copy reads one database and its WAL/SHM side files into a private local
// directory. The local copy always uses history.db* names, which is what the
// read-only SQLite adapter expects; the remote database may have any name when
// config.toml overrides db_path. All three identities are captured before and
// after the whole copy; a change to any file means the three do not describe
// one instant, so the snapshot is rejected and retried later. A Stat failure
// other than fs.ErrNotExist on any of the three files, including the optional
// WAL and SHM side files, is also an inconsistent copy: skipping an unchecked
// side file could import a partial snapshot. The WAL and SHM headers must also
// agree. Only Stat and Open are ever called on the Host.
//
// Some SFTP servers report stat times at whole-second resolution, so a
// same-size rewrite within one second can evade the identity comparison. The
// WAL/SHM salt check and SQLite's quick_check are the remaining guards; when
// either fails the copy is rejected and retried on a later Session.
//
// Temp-directory removal is best-effort. The directory is created 0700 with
// 0600 files, and the local journal is already plaintext at rest, so a failed
// removal can only waste disk space, never expose the copy to another user.
func (s *Service) copy(ctx context.Context, files RemoteFiles, databasePath string) (string, error) {
	local, err := s.makeTempDir()
	if err != nil {
		return "", fmt.Errorf("hostatuin: create temporary directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(local) }
	limit := s.maxTotalBytes
	if limit <= 0 {
		limit = DefaultMaxTotalBytes
	}

	remoteNames := []string{databasePath, databasePath + "-wal", databasePath + "-shm"}
	localNames := []string{historyName, walName, shmName}
	before := make([]fileState, len(remoteNames))
	for index, name := range remoteNames {
		state, err := statState(ctx, files, name)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("%w: stat %s: %v", ErrInconsistentCopy, name, err)
		}
		before[index] = state
	}
	if !before[0].present {
		cleanup()
		return "", fmt.Errorf("%w: %s disappeared", ErrInconsistentCopy, databasePath)
	}

	total := int64(0)
	for index, name := range remoteNames {
		if !before[index].present {
			continue
		}
		remaining := limit - total
		if before[index].size < 0 || before[index].size > remaining {
			cleanup()
			return "", fmt.Errorf("%w: %s is %d bytes and %d bytes remain",
				ErrCopyTooLarge, name, before[index].size, remaining)
		}
		written, err := copyFile(ctx, files, name, local, localNames[index], remaining)
		if err != nil {
			cleanup()
			return "", err
		}
		total += written
	}

	// Per-file checks inside copyFile catch a file changing while it is read.
	// This full second pass catches a change that happened after a file's own
	// copy finished: the database, WAL, and SHM must still be one snapshot.
	for index, name := range remoteNames {
		state, err := statState(ctx, files, name)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("%w: stat %s after copy: %v", ErrInconsistentCopy, name, err)
		}
		if !state.equal(before[index]) {
			cleanup()
			return "", fmt.Errorf("%w: %s changed during copy", ErrInconsistentCopy, name)
		}
	}

	if err := verifyWALAndSHM(filepath.Join(local, walName), filepath.Join(local, shmName)); err != nil {
		cleanup()
		return "", err
	}
	return local, nil
}

// copyFile copies one present remote file, enforcing the remaining byte
// budget both from its stat size and while it streams. It reports the bytes
// written; a file that changes under the read is an inconsistent copy.
func copyFile(ctx context.Context, files RemoteFiles, remotePath, localDir, name string,
	limit int64) (int64, error) {
	before, err := files.Stat(ctx, remotePath)
	if err != nil || !before.Regular || before.Symlink {
		return 0, fmt.Errorf("%w: %s disappeared", ErrInconsistentCopy, name)
	}
	if before.Size < 0 || before.Size > limit {
		return 0, fmt.Errorf("%w: %s is %d bytes and %d bytes remain",
			ErrCopyTooLarge, name, before.Size, limit)
	}
	reader, err := files.Open(ctx, remotePath)
	if err != nil {
		return 0, fmt.Errorf("%w: open %s: %v", ErrInconsistentCopy, name, err)
	}
	defer reader.Close()

	target := filepath.Join(localDir, name)
	writer, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("hostatuin: create %s: %w", name, err)
	}
	readLimit := limit
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	written, copyErr := io.Copy(writer, io.LimitReader(reader, readLimit))
	closeErr := writer.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("%w: copy %s: %v", ErrInconsistentCopy, name, copyErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("hostatuin: close %s: %w", name, closeErr)
	}
	if written > limit {
		return 0, fmt.Errorf("%w: %s grew past the remaining %d bytes",
			ErrCopyTooLarge, name, limit)
	}
	after, err := files.Stat(ctx, remotePath)
	if err != nil {
		return 0, fmt.Errorf("%w: stat %s after copy: %v", ErrInconsistentCopy, name, err)
	}
	if written != before.Size || after.Size != before.Size ||
		!after.ModTime.Equal(before.ModTime) {
		return 0, fmt.Errorf("%w: %s changed during copy", ErrInconsistentCopy, name)
	}
	return written, nil
}

const (
	walHeaderSize = 32
	shmHeaderSize = 48
	walMagic1     = 0x377f0682
	walMagic2     = 0x377f0683
)

// verifyWALAndSHM checks that copied WAL and SHM side files describe the same
// WAL generation. The WAL header is big-endian; the wal-index header uses the
// Host's byte order, which the client cannot observe, so both byte orders are
// accepted. An empty or header-only WAL and an uninitialized SHM are ignored:
// SQLite rebuilds them and neither holds committed rows only.
func verifyWALAndSHM(walPath, shmPath string) error {
	wal, walSize, err := readLocalPrefix(walPath, walHeaderSize)
	if err != nil {
		return fmt.Errorf("%w: read WAL header: %v", ErrInconsistentCopy, err)
	}
	if walSize == 0 {
		return nil
	}
	if walSize < walHeaderSize {
		return fmt.Errorf("%w: WAL header is truncated", ErrInconsistentCopy)
	}
	magic := binary.BigEndian.Uint32(wal[0:4])
	if magic != walMagic1 && magic != walMagic2 {
		return fmt.Errorf("%w: unknown WAL header", ErrInconsistentCopy)
	}
	if walSize <= walHeaderSize {
		// No committed frames, so there is no generation to compare.
		return nil
	}

	shm, shmSize, err := readLocalPrefix(shmPath, shmHeaderSize)
	if err != nil {
		return fmt.Errorf("%w: read SHM header: %v", ErrInconsistentCopy, err)
	}
	if shmSize == 0 {
		return nil
	}
	if shmSize < shmHeaderSize {
		return fmt.Errorf("%w: SHM header is truncated", ErrInconsistentCopy)
	}
	if shm[12] == 0 {
		// The wal-index is uninitialized; SQLite rebuilds it from the WAL.
		return nil
	}
	if !saltsAgree(wal[16:24], shm[32:40]) {
		return fmt.Errorf("%w: WAL and SHM headers disagree", ErrInconsistentCopy)
	}
	return nil
}

// saltsAgree compares the two WAL salts with the wal-index salts in either
// host byte order.
func saltsAgree(wal, shm []byte) bool {
	if bytes.Equal(wal[0:4], shm[0:4]) && bytes.Equal(wal[4:8], shm[4:8]) {
		return true
	}
	return bytes.Equal(wal[0:4], reverse4(shm[0:4])) &&
		bytes.Equal(wal[4:8], reverse4(shm[4:8]))
}

func reverse4(value []byte) []byte {
	return []byte{value[3], value[2], value[1], value[0]}
}

// readLocalPrefix reads at most size bytes and reports the file's full size.
// A missing file is not an error: optional side files may be absent.
func readLocalPrefix(name string, size int) ([]byte, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	buffer := make([]byte, size)
	n, err := io.ReadFull(file, buffer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, 0, err
	}
	return buffer[:n], info.Size(), nil
}

func hostOrigin(ref HostRef) string {
	return domainhistory.CaptureOrigin(ref.Hostname, ref.Username, ref.Label)
}
