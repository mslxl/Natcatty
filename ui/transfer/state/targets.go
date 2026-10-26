package state

import (
	"context"
	"errors"
	"io"
	"sync"

	appfileopen "github.com/mslxl/natcatty/application/fileopen"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
)

// ErrPaneClosed refuses a save through a Tab the user closed. It is a typed
// refusal, never a write through a dead connection.
var ErrPaneClosed = errors.New("the remote pane is closed")

// errTransportUnavailable means the Tab has no current transport yet. A save
// target only exists for an open pane, so this is a transient dial failure
// rather than a closed Tab.
var errTransportUnavailable = errors.New("the remote pane has no live connection")

// remoteLink is one remote Tab's live transport cell. The Tab and every
// editing session opened from it share the cell instead of snapshotting a
// handle, so a transport re-acquired by a Retry or by the GUI's Host
// reconnect serves the next save. dial is the app's Connection Manager path
// (saved credentials, keys, known-host trust, jump chain, and proxy); adopt
// hands a re-acquired transport to the UI thread so the pane lists through it.
//
// dialMu serializes dials; mu guards the handle and its generation. The
// generation lets a session tell "the transport I failed on" from "a
// transport the pane already re-acquired".
type remoteLink struct {
	dial  func(context.Context) (apptransfer.FileSystem, error)
	adopt func(apptransfer.FileSystem)

	mu     sync.Mutex
	fs     apptransfer.FileSystem
	gen    uint64
	closed bool

	dialMu sync.Mutex
}

// current returns the live transport and its generation.
func (l *remoteLink) current() (apptransfer.FileSystem, uint64, error) {
	if l == nil {
		return nil, 0, ErrPaneClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, 0, ErrPaneClosed
	}
	if l.fs == nil {
		return nil, 0, errTransportUnavailable
	}
	return l.fs, l.gen, nil
}

// acquire dials a fresh transport for the Tab and installs it. When adopt is
// set the transport is also handed to the UI thread. A dial that fails leaves
// the previous handle and generation untouched, so the next attempt can still
// replace it.
func (l *remoteLink) acquire(ctx context.Context, adopt bool) (apptransfer.FileSystem, error) {
	if l == nil {
		return nil, ErrPaneClosed
	}
	l.dialMu.Lock()
	defer l.dialMu.Unlock()
	return l.dialLocked(ctx, adopt)
}

// dialLocked dials and installs a transport. dialMu must be held, so dials
// never overlap and a concurrent reconnect sees the generation the last one
// installed.
func (l *remoteLink) dialLocked(ctx context.Context, adopt bool) (apptransfer.FileSystem, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrPaneClosed
	}
	dial, notify := l.dial, l.adopt
	l.mu.Unlock()
	if dial == nil {
		return nil, errTransportUnavailable
	}
	files, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = files.Close()
		return nil, ErrPaneClosed
	}
	previous := l.fs
	l.fs = files
	l.gen++
	l.mu.Unlock()
	if previous != nil && previous != files {
		_ = previous.Close()
	}
	if adopt && notify != nil {
		notify(files)
	}
	return files, nil
}

// reconnect acquires a transport the caller has not used yet. When the link's
// generation moved since seenGen, another path (the GUI's Host reconnect)
// already installed a live transport and the caller follows it without a
// second dial. Otherwise a fresh connection replaces the failed one. The new
// transport is adopted by the Tab, so its listing recovers too.
func (l *remoteLink) reconnect(ctx context.Context, seenGen uint64) (apptransfer.FileSystem, error) {
	if l == nil {
		return nil, ErrPaneClosed
	}
	l.dialMu.Lock()
	defer l.dialMu.Unlock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrPaneClosed
	}
	if l.fs != nil && l.gen != seenGen {
		files := l.fs
		l.mu.Unlock()
		return files, nil
	}
	l.mu.Unlock()
	return l.dialLocked(ctx, true)
}

// close releases the link and its transport. It is safe to call more than
// once; a save that races it is refused with ErrPaneClosed.
func (l *remoteLink) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	files := l.fs
	l.fs = nil
	l.mu.Unlock()
	if files != nil {
		_ = files.Close()
	}
}

// paneRemoteTarget binds an open remote file to its Tab's live transport.
// Saves use the shared stage/verify/promote write from the transfer service,
// so an editor save carries the ADR-0012 guarantees.
//
// The link, not a captured filesystem, is held: a Retry re-acquires a
// transport through the application's Connection Manager, and a GUI Host
// reconnect installs one in the same cell, so the session follows it without
// a new Tab. A closed Tab reports ErrPaneClosed instead of writing through a
// dead connection.
type paneRemoteTarget struct {
	service *apptransfer.Service
	link    *remoteLink
	path    string

	mu  sync.Mutex
	gen uint64
}

// files returns the Tab's live transport and remembers which generation this
// session last used, so a Retry can tell a dead handle from one the pane has
// already replaced.
func (t *paneRemoteTarget) files() (apptransfer.FileSystem, error) {
	files, gen, err := t.link.current()
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.gen = gen
	t.mu.Unlock()
	return files, nil
}

// Path returns the remote file a save writes.
func (t *paneRemoteTarget) Path() string { return t.path }

// Stat reports the version currently on the server.
func (t *paneRemoteTarget) Stat(ctx context.Context) (appfileopen.RemoteStat, error) {
	files, err := t.files()
	if err != nil {
		return appfileopen.RemoteStat{}, err
	}
	entry, err := files.Stat(ctx, t.path)
	if err != nil {
		return appfileopen.RemoteStat{}, err
	}
	return appfileopen.RemoteStat{Size: entry.Size, Modified: entry.ModTime}, nil
}

// Read opens the current remote content for the conflict content check.
func (t *paneRemoteTarget) Read(ctx context.Context) (io.ReadCloser, error) {
	files, err := t.files()
	if err != nil {
		return nil, err
	}
	return files.Open(ctx, t.path)
}

// Write replaces the remote file through the shared atomic transaction.
func (t *paneRemoteTarget) Write(ctx context.Context, content []byte) error {
	if t.service == nil {
		return errors.New("the transfer service is not available")
	}
	files, err := t.files()
	if err != nil {
		return err
	}
	return t.service.WriteFile(ctx, files, t.path, content)
}

// Reconnect implements fileopen.Reconnector. It acquires a fresh transport
// for the Tab's Host through the application's Connection Manager and keeps
// the same file path, unless the pane already installed one after the failed
// attempt. A closed Tab is refused.
func (t *paneRemoteTarget) Reconnect(ctx context.Context) (appfileopen.RemoteTarget, error) {
	if t.link == nil {
		return nil, ErrPaneClosed
	}
	t.mu.Lock()
	seen := t.gen
	t.mu.Unlock()
	if _, err := t.link.reconnect(ctx, seen); err != nil {
		return nil, err
	}
	return t, nil
}

// TransportLost implements fileopen.TransportLoss with the transfer
// service's classification, so an ordinary remote refusal is reported as it
// is and only a lost connection is re-acquired.
func (t *paneRemoteTarget) TransportLost(cause error) bool {
	return apptransfer.KindOf(cause) == apptransfer.KindConnection
}

var (
	_ appfileopen.Reconnector   = (*paneRemoteTarget)(nil)
	_ appfileopen.TransportLoss = (*paneRemoteTarget)(nil)
)
