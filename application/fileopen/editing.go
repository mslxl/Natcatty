package fileopen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxEditorBytes is the largest file the internal editor opens. The original
// refused anything above 10 MB before reading it; the same bound keeps a
// mistaken open from pulling an arbitrary remote object into memory.
const MaxEditorBytes int64 = 10 << 20

// EditID identifies one editing session: a file handed to the internal
// editor, or a remote copy watched for an external application's saves.
type EditID string

// RemoteStat is one observable version of a remote file. Digest is the
// SHA-256 of its content, so a same-size edit inside one modification-time
// tick is still detected as a change.
type RemoteStat struct {
	Size     int64
	Modified time.Time
	Digest   string
}

// RemoteTarget is the write-back channel for a remote file. The SFTP surface
// supplies it because it owns the connection and the atomic write path.
type RemoteTarget interface {
	// Path returns the remote path saves go back to.
	Path() string
	// Stat reports the remote version currently on the server.
	Stat(ctx context.Context) (RemoteStat, error)
	// Read opens the current remote content; Save uses it to compare the
	// content digest when size and modification time are unchanged.
	Read(ctx context.Context) (io.ReadCloser, error)
	// Write replaces the remote file. The surface stages and verifies the
	// bytes and promotes them with a rename (ADR-0012).
	Write(ctx context.Context, content []byte) error
}

// Conflict describes a remote file that changed after the editing session
// read it. Nothing is written until the user chooses to overwrite.
type Conflict struct {
	Session  EditID
	Name     string
	Path     string
	Baseline RemoteStat
	Current  RemoteStat
}

// WatchEventKind is one external-edit watch outcome.
type WatchEventKind string

const (
	// WatchSynced reports that the edited local copy reached the remote.
	WatchSynced WatchEventKind = "synced"
	// WatchConflict reports that the remote changed and nothing was written.
	WatchConflict WatchEventKind = "conflict"
	// WatchFailed reports that an upload failed; the local copy is kept.
	WatchFailed WatchEventKind = "failed"
)

// WatchEvent is one external-edit watch outcome. It is delivered from the
// watcher goroutine; a UI observer must post it to its own thread.
type WatchEvent struct {
	Kind     WatchEventKind
	Session  EditID
	Name     string
	Err      error
	Conflict *Conflict
}

// Observer receives external-edit watch events.
type Observer func(WatchEvent)

// SaveFunc writes an editor buffer back to its file. overwrite skips the
// remote-change check after the user approved replacing the remote version.
type SaveFunc func(ctx context.Context, content []byte, overwrite bool) error

// session is one tracked file. baseline and pendingConflict are read and
// written under Service.mu; the watch goroutine may outlive an open call.
type session struct {
	id        EditID
	name      string
	remote    bool
	localPath string
	tempDir   string
	target    RemoteTarget

	baseline        RemoteStat
	pendingConflict bool

	watchCancel context.CancelFunc
	watchDone   chan struct{}
}

// sessionState is the copy a caller works with outside the lock.
type sessionState struct {
	id              EditID
	name            string
	remote          bool
	localPath       string
	tempDir         string
	target          RemoteTarget
	baseline        RemoteStat
	pendingConflict bool
}

func (s *Service) session(id EditID) (sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return sessionState{}, false
	}
	return sessionState{
		id:              sess.id,
		name:            sess.name,
		remote:          sess.remote,
		localPath:       sess.localPath,
		tempDir:         sess.tempDir,
		target:          sess.target,
		baseline:        sess.baseline,
		pendingConflict: sess.pendingConflict,
	}, true
}

func (s *Service) newSession(name, localPath, tempDir string, remote bool,
	target RemoteTarget, baseline RemoteStat) EditID {
	id := EditID(fmt.Sprintf("edit-%d", s.nextSession.Add(1)))
	s.mu.Lock()
	s.sessions[id] = &session{
		id: id, name: name, remote: remote, localPath: localPath,
		tempDir: tempDir, target: target, baseline: baseline,
	}
	s.mu.Unlock()
	return id
}

// Save writes content back to the session's file. A local session writes the
// local file atomically. A remote session first compares the current remote
// version with the one the editor read; a change returns ErrorConflict and
// writes nothing. overwrite is only true after the user approved replacing
// the changed remote file.
//
// A failed write leaves the session and its local copy intact, so the editor
// buffer and an external application's file both survive a lost connection.
func (s *Service) Save(ctx context.Context, id EditID, content []byte, overwrite bool) error {
	if err := ctx.Err(); err != nil {
		return failure(ErrorCancelled, err)
	}
	sess, ok := s.session(id)
	if !ok {
		return failure(ErrorSave, errors.New("the editing session is closed"))
	}
	if !sess.remote {
		if err := writeLocalFile(sess.localPath, content); err != nil {
			return failure(ErrorSave, err)
		}
		return nil
	}
	if sess.target == nil {
		return failure(ErrorSave, errors.New("the remote file cannot be written back"))
	}
	if !overwrite {
		current, err := remoteVersion(ctx, sess.target)
		if err != nil {
			return failure(ErrorSave, err)
		}
		if !sameRemoteVersion(sess.baseline, current) {
			return s.conflictFailure(sess, current)
		}
	}
	if err := sess.target.Write(ctx, content); err != nil {
		return failure(ErrorSave, err)
	}
	s.recordSaved(ctx, sess, content)
	return nil
}

// remoteVersion reads the current remote metadata and content digest. The
// read makes the check independent of the server's modification-time
// granularity.
func remoteVersion(ctx context.Context, target RemoteTarget) (RemoteStat, error) {
	info, err := target.Stat(ctx)
	if err != nil {
		return RemoteStat{}, err
	}
	reader, err := target.Read(ctx)
	if err != nil {
		return RemoteStat{}, err
	}
	defer reader.Close()
	digest, err := digestOf(reader)
	if err != nil {
		return RemoteStat{}, err
	}
	return RemoteStat{Size: info.Size, Modified: info.Modified, Digest: digest}, nil
}

// sameRemoteVersion reports whether a remote file is still the version a
// session read. A zero Modified time means the post-write stat was
// unavailable, so the size and content digest decide.
func sameRemoteVersion(baseline, current RemoteStat) bool {
	if baseline.Size != current.Size || baseline.Digest != current.Digest {
		return false
	}
	if baseline.Modified.IsZero() || current.Modified.IsZero() {
		return true
	}
	return baseline.Modified.Equal(current.Modified)
}

func (s *Service) conflictFailure(sess sessionState, current RemoteStat) error {
	conflict := &Conflict{
		Session:  sess.id,
		Name:     sess.name,
		Path:     sess.target.Path(),
		Baseline: sess.baseline,
		Current:  current,
	}
	return &Error{Kind: ErrorConflict, Err: errors.New("the remote file changed"), Conflict: conflict}
}

// recordSaved records the version just written. The digest and size come from
// the bytes the caller supplied; the modification time comes from a fresh
// stat. When that stat fails the modified time stays zero, and the next save
// compares size and content instead of failing.
func (s *Service) recordSaved(ctx context.Context, sess sessionState, content []byte) {
	next := RemoteStat{Size: int64(len(content)), Digest: digestBytes(content)}
	if info, err := sess.target.Stat(ctx); err == nil {
		next.Modified = info.Modified
	}
	s.mu.Lock()
	if live := s.sessions[sess.id]; live != nil {
		live.baseline = next
		live.pendingConflict = false
	}
	s.mu.Unlock()
}

// ConflictOf returns the conflict carried by a save failure.
func ConflictOf(err error) (*Conflict, bool) {
	var target *Error
	if errors.As(err, &target) && target.Conflict != nil {
		return target.Conflict, true
	}
	return nil, false
}

// ResolveExternal answers an external-edit conflict. overwrite uploads the
// current local copy; false leaves the remote untouched, and a later local
// change asks again rather than overwriting silently.
func (s *Service) ResolveExternal(ctx context.Context, id EditID, overwrite bool) error {
	sess, ok := s.session(id)
	if !ok {
		return failure(ErrorSave, errors.New("the editing session is closed"))
	}
	if !overwrite {
		s.clearConflict(id)
		return nil
	}
	content, err := os.ReadFile(sess.localPath)
	if err != nil {
		return failure(ErrorSave, err)
	}
	if err := s.Save(ctx, id, content, true); err != nil {
		return err
	}
	s.clearConflict(id)
	s.emit(WatchEvent{Kind: WatchSynced, Session: id, Name: sess.name})
	return nil
}

// RetryExternal retries an external upload after a failure. The local copy is
// still on disk, so a lost connection loses no edits.
func (s *Service) RetryExternal(ctx context.Context, id EditID) error {
	sess, ok := s.session(id)
	if !ok {
		return failure(ErrorSave, errors.New("the editing session is closed"))
	}
	content, err := os.ReadFile(sess.localPath)
	if err != nil {
		return failure(ErrorSave, err)
	}
	return s.syncExternal(ctx, sess, content)
}

func (s *Service) clearConflict(id EditID) {
	s.mu.Lock()
	if sess := s.sessions[id]; sess != nil {
		sess.pendingConflict = false
	}
	s.mu.Unlock()
}

func (s *Service) markConflict(id EditID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil || sess.pendingConflict {
		return false
	}
	sess.pendingConflict = true
	return true
}

func (s *Service) emit(event WatchEvent) {
	s.mu.Lock()
	observer := s.observer
	s.mu.Unlock()
	if observer != nil {
		observer(event)
	}
}

// Close stops watching a session and removes the temporary directory this
// service created for it. Closing an unknown or already closed session is a
// no-op.
func (s *Service) Close(id EditID) {
	s.mu.Lock()
	sess := s.sessions[id]
	if sess == nil {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, id)
	cancel, done := sess.watchCancel, sess.watchDone
	tempDir := sess.tempDir
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if tempDir != "" {
		_ = os.RemoveAll(tempDir)
	}
}

// Shutdown closes every session, which removes active temporary directories,
// then sweeps any that a previous process left behind.
func (s *Service) Shutdown() {
	s.mu.Lock()
	ids := make([]EditID, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.Close(id)
	}
	_, _ = s.SweepTemp(context.Background())
}

// SweepTemp removes temporary directories this service previously created
// and no live session owns. The application calls it once at startup and the
// shutdown path calls it again after closing sessions.
func (s *Service) SweepTemp(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(s.tempRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	s.mu.Lock()
	active := make(map[string]bool, len(s.sessions))
	for _, sess := range s.sessions {
		if sess.tempDir != "" {
			active[filepath.Clean(sess.tempDir)] = true
		}
	}
	s.mu.Unlock()

	removed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), materializePrefix) {
			continue
		}
		path := filepath.Join(s.tempRoot, entry.Name())
		if active[filepath.Clean(path)] {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// syncExternal uploads one local copy when the remote is unchanged, and
// records a conflict instead of overwriting when it is not.
func (s *Service) syncExternal(ctx context.Context, sess sessionState, content []byte) error {
	err := s.Save(ctx, sess.id, content, false)
	if err == nil {
		s.emit(WatchEvent{Kind: WatchSynced, Session: sess.id, Name: sess.name})
		return nil
	}
	if conflict, ok := ConflictOf(err); ok {
		if s.markConflict(sess.id) {
			s.emit(WatchEvent{
				Kind: WatchConflict, Session: sess.id, Name: sess.name,
				Conflict: conflict,
			})
		}
		return err
	}
	s.emit(WatchEvent{Kind: WatchFailed, Session: sess.id, Name: sess.name, Err: err})
	return err
}

// startWatch polls the materialized local copy. It never runs for a local
// file, because the local file is the file the user edits.
func (s *Service) startWatch(sess sessionState) {
	// Capture the materialized copy before returning: it is the baseline the
	// external application is about to read, and Open must not return before
	// the first observation exists.
	observed, observedOK := localObservation(sess.localPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	live := s.sessions[sess.id]
	if live != nil {
		live.watchCancel = cancel
		live.watchDone = done
	}
	s.mu.Unlock()
	if live == nil {
		// The session closed between tracking it and starting its watcher.
		cancel()
		close(done)
		return
	}
	go func() {
		defer close(done)
		defer cancel()
		s.runWatch(ctx, sess, observed, observedOK)
	}()
}

func (s *Service) runWatch(ctx context.Context, sess sessionState,
	observed observation, observedOK bool) {
	interval := s.watchInterval
	if interval <= 0 {
		interval = time.Second
	}
	debounce := s.watchDebounce
	if debounce < 0 {
		debounce = 0
	}
	var changedAt time.Time
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			current, ok := localObservation(sess.localPath)
			if !ok {
				if observedOK {
					s.emit(WatchEvent{
						Kind: WatchFailed, Session: sess.id, Name: sess.name,
						Err: errors.New("the local copy was removed"),
					})
				}
				return
			}
			if !observedOK || current != observed {
				observed, observedOK = current, true
				changedAt = now
			}
			if changedAt.IsZero() || now.Sub(changedAt) < debounce {
				continue
			}
			changedAt = time.Time{}
			s.takeSettledChange(ctx, sess)
		}
	}
}

// takeSettledChange handles one debounced external save. A pending conflict
// takes precedence: the user must answer it, and a later local save must not
// take the remote version's place silently.
func (s *Service) takeSettledChange(ctx context.Context, sess sessionState) {
	if state, ok := s.session(sess.id); ok && state.pendingConflict {
		return
	}
	content, err := os.ReadFile(sess.localPath)
	if err != nil {
		s.emit(WatchEvent{Kind: WatchFailed, Session: sess.id, Name: sess.name, Err: err})
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	_ = s.syncExternal(ctx, sess, content)
}

// observation is a local file version. The modification time and size are
// what an external application's save changes; the original polled the same
// pair every second.
type observation struct {
	modified time.Time
	size     int64
}

func localObservation(path string) (observation, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return observation{}, false
	}
	return observation{modified: info.ModTime(), size: info.Size()}, true
}

// writeLocalFile replaces a local file atomically: the bytes are written to a
// temporary sibling in the same directory and renamed over the target. A
// rename that cannot replace on this platform removes the destination first.
// This is the private materialized copy, not the user's remote file: the
// remote file's replacement is the transfer service's backup transaction.
func writeLocalFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return errors.New("the path is a directory")
		}
		mode = info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(dir, ".natcatty-edit-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	discard := func(cause error) error {
		_ = temporary.Close()
		_ = os.Remove(name)
		return cause
	}
	if _, err := temporary.Write(content); err != nil {
		return discard(err)
	}
	if err := temporary.Close(); err != nil {
		return discard(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
			if removeErr := os.Remove(path); removeErr == nil {
				if renameErr := os.Rename(name, path); renameErr == nil {
					return nil
				}
			}
		}
		_ = os.Remove(name)
		return err
	}
	return nil
}

func digestBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func digestOf(reader io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
