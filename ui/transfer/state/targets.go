package state

import (
	"context"
	"errors"
	"io"

	appfileopen "github.com/mslxl/natcatty/application/fileopen"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
)

// paneRemoteTarget binds an open remote file to the pane that produced it.
// Saves use the same transport and the shared stage/verify/promote write from
// the transfer service, so an editor save carries the ADR-0012 guarantees.
//
// The Tab, not a captured filesystem, is held: a Tab's transport can be
// replaced when the pane reconnects, and a save must use the live one. A
// closed Tab reports a clear failure instead of writing through a dead
// connection.
type paneRemoteTarget struct {
	service *apptransfer.Service
	tab     *Tab
	path    string
}

// files returns the Tab's live transport.
func (t *paneRemoteTarget) files() (apptransfer.FileSystem, error) {
	if t.tab == nil || t.tab.closed || t.tab.FS == nil {
		return nil, errors.New("the remote pane is no longer connected")
	}
	return t.tab.FS, nil
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
