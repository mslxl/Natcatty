// Package hostatuin detects a Host's existing Atuin installation and imports
// its history read-only. Detection and the copy use a separate SFTP channel
// before the interactive terminal opens, so the terminal channel is never
// consumed. The package never runs the Atuin binary, never executes a remote
// command, and never writes to the Host: it only stats and reads the copied
// database files.
//
// A Host whose Atuin history is readable is reported as Present even when the
// import fails. The Session then keeps the Netcatty capture hook skipped, and a
// later Session retries the copy without advancing the import checkpoint.
package hostatuin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// DefaultTimeout bounds one detection/import attempt so Session open does not
// wait forever on a slow Host. A timed-out attempt retries on a later Session.
const DefaultTimeout = 15 * time.Second

// ErrInconsistentCopy reports a copy whose source changed while it was read.
// The Host is untouched and the import checkpoint does not advance.
var ErrInconsistentCopy = errors.New("hostatuin: inconsistent Atuin copy")

// HostRef is the Host context an import needs. It is deliberately small: the
// remote source opens the Host's connection by identity, and the origin
// fallback uses the same captured values as a normal command.
type HostRef struct {
	ID       model.HostID
	Hostname string
	Username string
	Label    string
}

// FileInfo is the metadata the importer needs about one remote file.
type FileInfo struct {
	Size    int64
	ModTime time.Time
	Regular bool
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
	Close() error
}

// RemoteSource opens the separate read-only channel for a Host. The app
// implements it with the SFTP file protocol; a Host without SFTP is absent.
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

// ProbeResult reports one detection attempt. Present means the Host has a
// readable Atuin history database, so the Netcatty capture hook is skipped.
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

// WithPreference supplies the HistoryPreferHostAtuin setting. A nil predicate
// keeps the preference on.
func WithPreference(prefer func() bool) Option {
	return func(s *Service) { s.prefer = prefer }
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

// Service detects and imports one Host's existing Atuin history. It is safe
// for concurrent use: each attempt owns its remote channel and temp directory.
type Service struct {
	remote      RemoteSource
	database    Database
	recorder    Recorder
	prefer      func() bool
	makeTempDir func() (string, error)
	timeout     time.Duration
}

// New returns a Service with the platform defaults.
func New(opts ...Option) *Service {
	service := &Service{
		makeTempDir: func() (string, error) {
			return os.MkdirTemp("", "netcatty-atuin-")
		},
		timeout: DefaultTimeout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Enabled reports whether the Host-Atuin preference is on and the service has
// the ports it needs.
func (s *Service) Enabled() bool {
	if s == nil || s.remote == nil || s.database == nil {
		return false
	}
	return s.prefer == nil || s.prefer()
}

// Probe detects a Host's Atuin history and imports it. It runs before the
// interactive terminal opens. A Host not using Atuin, or one whose separate
// channel cannot be opened, reports Present false so the normal capture hook
// is used. Once a database is found, Present stays true even when the copy or
// import fails.
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
// wrote during the Session. A Host no longer using the preference is skipped.
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

// candidateDataDirs are Atuin's data-directory layouts, newest first. The
// resolved directory can also be overridden by ATUIN_HOME in the Host's
// environment, which a read-only file probe cannot see; those Hosts simply
// retry in a later Session when the default layout is used.
var candidateDataDirs = []string{
	".atuin/data",
	".local/share/atuin",
	"Library/Application Support/atuin",
	"AppData/Roaming/atuin",
	".atuin",
}

const (
	historyName = "history.db"
	walName     = "history.db-wal"
	shmName     = "history.db-shm"
)

// read locates, copies, and parses the Host's history database. A failure
// before the database is found is reported as absent; a failure after it is
// found keeps Present true so the capture hook stays skipped.
func (s *Service) read(ctx context.Context, ref HostRef) (snapshot, error) {
	files, err := s.remote.OpenReadOnly(ctx, ref.ID)
	if err != nil {
		return snapshot{}, nil
	}
	defer files.Close()

	root, err := files.Root(ctx)
	if err != nil || root == "" {
		return snapshot{}, nil
	}
	historyPath := ""
	for _, dir := range candidateDataDirs {
		candidate := path.Join(root, dir, historyName)
		info, err := files.Stat(ctx, candidate)
		if err != nil || !info.Regular {
			continue
		}
		historyPath = candidate
		break
	}
	if historyPath == "" {
		return snapshot{}, nil
	}

	dir, err := s.copy(ctx, files, path.Dir(historyPath))
	if err != nil {
		return snapshot{Present: true}, err
	}
	defer os.RemoveAll(dir)
	rows, err := s.database.ReadHistory(ctx, filepath.Join(dir, historyName))
	if err != nil {
		return snapshot{Present: true}, err
	}
	return snapshot{Present: true, rows: rows}, nil
}

// copy reads history.db and any WAL/SHM side files into a private local
// directory. Each source is stat'ed before and after the read; a changed size
// or modification time means the writer raced the copy, so the snapshot is
// rejected and retried later. Only Stat and Open are ever called on the Host.
func (s *Service) copy(ctx context.Context, files RemoteFiles, dir string) (string, error) {
	local, err := s.makeTempDir()
	if err != nil {
		return "", fmt.Errorf("hostatuin: create temporary directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(local) }
	names := []string{historyName, walName, shmName}
	for index, name := range names {
		copied, err := copyFile(ctx, files, path.Join(dir, name), local, name)
		if err != nil {
			cleanup()
			return "", err
		}
		if !copied && index == 0 {
			cleanup()
			return "", fmt.Errorf("%w: %s disappeared", ErrInconsistentCopy, name)
		}
	}
	return local, nil
}

// copyFile copies one optional remote file. It reports false when the file is
// absent, which is normal for the WAL and SHM side files.
func copyFile(ctx context.Context, files RemoteFiles, remotePath, localDir, name string) (bool, error) {
	before, err := files.Stat(ctx, remotePath)
	if err != nil || !before.Regular {
		return false, nil
	}
	reader, err := files.Open(ctx, remotePath)
	if err != nil {
		return false, fmt.Errorf("hostatuin: open %s: %w", name, err)
	}
	defer reader.Close()

	target := filepath.Join(localDir, name)
	writer, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("hostatuin: create %s: %w", name, err)
	}
	written, copyErr := io.Copy(writer, reader)
	closeErr := writer.Close()
	if copyErr != nil {
		return false, fmt.Errorf("hostatuin: copy %s: %w", name, copyErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("hostatuin: close %s: %w", name, closeErr)
	}
	after, err := files.Stat(ctx, remotePath)
	if err != nil {
		return false, fmt.Errorf("%w: stat %s after copy: %v", ErrInconsistentCopy, name, err)
	}
	if written != before.Size || after.Size != before.Size ||
		!after.ModTime.Equal(before.ModTime) {
		return false, fmt.Errorf("%w: %s changed during copy", ErrInconsistentCopy, name)
	}
	return true, nil
}

func hostOrigin(ref HostRef) string {
	return domainhistory.CaptureOrigin(ref.Hostname, ref.Username, ref.Label)
}
