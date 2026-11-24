// Phone Session Log wiring. The Tools surface owns the presentation; this file
// supplies the platform facts it needs: the app's Documents directory holds a
// Session Logs folder, the saved logs are listed from that folder, and an
// export goes through the system save/export picker on a phone (MyGo's
// Dialog.SaveCopy), which cancels without touching the file.
//
// The recording itself is the shared application Session logger. phoneSessionHandle
// maps the surface's start/stop onto Session.StartLogging, so the log is fed by
// the Session's own read loop - the same terminal output stream the screen reads
// - and the Session's normal teardown flushes and closes it.

package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egoist/mygo"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// sessionLogFolder is the folder under the app's Documents directory. Logs are
// named by the shared logger as <label>/<timestamp>.<ext>, where the label
// carries the Session's stable Tab and Pane identity.
const sessionLogFolder = "Session Logs"

// errPhoneSessionLogs is the stated failure when this build has no Session Log
// workspace. It never replaces the terminal: the tool page shows it inline.
var errPhoneSessionLogs = errors.New("app: Session logs are not available in this build")

// phoneSessionLogService is the phone's Session Log workspace over the app's
// Documents directory. The seams are fields so a test can point the service at
// a temporary directory and a fake export picker.
type phoneSessionLogService struct {
	documents func() (string, error)
	saveCopy  func(path, name string) (string, error)
}

// newPhoneSessionLogService builds the production service. Documents is the
// same platform path the phone's Browser downloads use; SaveCopy is MyGo's
// system export picker, which answers a content URI on Android and the Files
// picker on iOS, or "" when the user cancels.
func newPhoneSessionLogService() *phoneSessionLogService {
	return &phoneSessionLogService{
		documents: func() (string, error) { return mygo.App.Path(mygo.PathDocuments) },
		saveCopy:  mygo.Dialog.SaveCopy,
	}
}

// sessionLogs returns this runtime's service, building the production one on
// first use. Tests replace the field before the surface is drawn.
func (r *phoneSessionRuntime) sessionLogs() *phoneSessionLogService {
	if r == nil {
		return nil
	}
	if r.logs == nil {
		r.logs = newPhoneSessionLogService()
	}
	return r.logs
}

// SessionLogDefaults supplies the directory and format for a new log. Text is
// the readable rendering the desktop also defaults to; the shared logger adds
// the safe Session label and the start timestamp.
func (r *phoneSessionRuntime) SessionLogDefaults() (phoneshell.SessionLogOptions, error) {
	return r.sessionLogs().Defaults()
}

// SessionLogFiles lists every saved log under the Session Logs folder, newest
// first, so a log the app never exported remains reachable after its Session
// ended or after an app restart.
func (r *phoneSessionRuntime) SessionLogFiles() ([]phoneshell.SessionLogFile, error) {
	return r.sessionLogs().Files()
}

// ExportSessionLog hands one saved log to the system export picker. A
// cancelled picker returns "", nil and leaves the file in place.
func (r *phoneSessionRuntime) ExportSessionLog(path, name string) (string, error) {
	return r.sessionLogs().Export(path, name)
}

// root is the Session Logs directory under Documents.
func (s *phoneSessionLogService) root() (string, error) {
	if s == nil || s.documents == nil {
		return "", errPhoneSessionLogs
	}
	documents, err := s.documents()
	if err != nil {
		return "", fmt.Errorf("locate Documents: %w", err)
	}
	return filepath.Join(documents, sessionLogFolder), nil
}

// Defaults creates the Session Logs folder and returns the options a log
// starts with. The folder is created here rather than at first write so the
// export entry lists an empty directory instead of reporting a missing one.
func (s *phoneSessionLogService) Defaults() (phoneshell.SessionLogOptions, error) {
	root, err := s.root()
	if err != nil {
		return phoneshell.SessionLogOptions{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return phoneshell.SessionLogOptions{}, fmt.Errorf("create %s: %w", root, err)
	}
	return phoneshell.SessionLogOptions{
		Directory: root,
		Format:    domainterminal.LogFormatText,
	}, nil
}

// Files walks the Session Logs folder for the shared logger's files. A missing
// folder is an empty list, not an error: no Session has logged yet. Any other
// walk failure is reported so the page can state it inline.
func (s *phoneSessionLogService) Files() ([]phoneshell.SessionLogFile, error) {
	root, err := s.root()
	if err != nil {
		return nil, err
	}
	files := make([]phoneshell.SessionLogFile, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".log", ".txt", ".html":
		default:
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		files = append(files, phoneshell.SessionLogFile{
			Path:     path,
			Name:     entry.Name(),
			Session:  filepath.Base(filepath.Dir(path)),
			Modified: info.ModTime(),
			Size:     info.Size(),
		})
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Modified.Equal(files[j].Modified) {
			return files[i].Name > files[j].Name
		}
		return files[i].Modified.After(files[j].Modified)
	})
	return files, nil
}

// Export copies one log through the platform picker. The logger keeps writing
// while an active log is exported; a cancelled picker is the caller's cue to
// say the file is still saved, and it never deletes anything.
func (s *phoneSessionLogService) Export(path, name string) (string, error) {
	if s == nil || s.saveCopy == nil {
		return "", errPhoneSessionLogs
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("the log has no file to export")
	}
	if name == "" {
		name = filepath.Base(path)
	}
	return s.saveCopy(path, name)
}

// trackHandle remembers a Session this runtime opened so app shutdown can
// flush its log. The entry is dropped when the Session ends, whichever path
// ended it.
func (r *phoneSessionRuntime) trackHandle(handle *phoneSessionHandle) {
	if r == nil || handle == nil {
		return
	}
	r.handlesMu.Lock()
	if r.handles == nil {
		r.handles = make(map[*phoneSessionHandle]struct{})
	}
	r.handles[handle] = struct{}{}
	r.handlesMu.Unlock()
	go func() {
		<-handle.Done()
		r.handlesMu.Lock()
		delete(r.handles, handle)
		r.handlesMu.Unlock()
	}()
}

// stopSessionLogs finalizes every tracked Session's active log. It is safe for
// a Session with no log and for a Session whose teardown already stopped one.
func (r *phoneSessionRuntime) stopSessionLogs() {
	if r == nil {
		return
	}
	r.handlesMu.Lock()
	handles := make([]*phoneSessionHandle, 0, len(r.handles))
	for handle := range r.handles {
		handles = append(handles, handle)
	}
	r.handlesMu.Unlock()
	for _, handle := range handles {
		_ = handle.StopSessionLog()
	}
}

// SessionLogStatus maps the shared logger's state onto the surface's type.
func phoneSessionLogStatus(status appsession.LogStatus) phoneshell.SessionLogStatus {
	return phoneshell.SessionLogStatus{
		Active:  status.Active,
		Format:  status.Format,
		Path:    status.Path,
		Started: status.Started,
		Problem: status.Problem,
	}
}

// SessionLogStatus reports this Session's current log.
func (h *phoneSessionHandle) SessionLogStatus() phoneshell.SessionLogStatus {
	if h == nil || h.session == nil {
		return phoneshell.SessionLogStatus{}
	}
	return phoneSessionLogStatus(h.session.Logging())
}

// StartSessionLog installs the shared logger on this Session. The Session's
// read loop feeds it from the same transport stream the terminal reads, so the
// phone adds no second reader.
func (h *phoneSessionHandle) StartSessionLog(options phoneshell.SessionLogOptions) error {
	if h == nil || h.session == nil {
		return errors.New("this Session is no longer running")
	}
	return h.session.StartLogging(appsession.LogOptions{
		Directory:  options.Directory,
		Format:     options.Format,
		Timestamps: options.Timestamps,
		Label:      options.Label,
	})
}

// StopSessionLog flushes and closes this Session's log. A Session that ends on
// its own finalizes the log the same way through its normal teardown.
func (h *phoneSessionHandle) StopSessionLog() phoneshell.SessionLogStatus {
	if h == nil || h.session == nil {
		return phoneshell.SessionLogStatus{}
	}
	return phoneSessionLogStatus(h.session.StopLogging())
}

// The phone runtime is the Tools surface's Session Log workspace, and every
// Session handle can record itself.
var (
	_ phoneshell.SessionLogWorkspace = (*phoneSessionRuntime)(nil)
	_ phoneshell.SessionLogHandle    = (*phoneSessionHandle)(nil)
)
