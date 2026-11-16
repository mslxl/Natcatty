package state

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// The compose directory picker browses a Session's filesystem from the working
// directory the shell reported with OSC 7. Listing goes through the Session's
// existing programmatic shell execution (Session.Command), so the picker never
// opens a second connection and never executes a command on its own; the only
// command it runs is a read-only directory listing.

// ComposeDirectoryEntry is one entry of a listed directory.
type ComposeDirectoryEntry struct {
	Name      string
	Directory bool
}

// ComposeDirectoryListing is one listed directory. Path is the resolved
// absolute directory, which may differ from the requested path for ".." and
// symlinks. An empty requested path lists the Session user's home directory.
type ComposeDirectoryListing struct {
	Path    string
	Entries []ComposeDirectoryEntry
}

// ComposeDirectoryLister lists a directory on a Session. It is the seam tests
// replace with a fake; the default lists through the Session's existing
// command path.
type ComposeDirectoryLister func(ctx context.Context, session *appsession.Session, path string) (ComposeDirectoryListing, error)

// ComposeDirectorySession reports the working directory a Session last
// reported and whether the Session is still connected. The default reads the
// Session snapshot.
type ComposeDirectorySession func(id model.SessionID) (cwd string, live bool)

// ComposeDirectorySource supplies the picker's seams. A nil field uses the
// live Session.
type ComposeDirectorySource struct {
	List    ComposeDirectoryLister
	Session ComposeDirectorySession
}

// WithComposeDirectory replaces the picker's listing and Session-state seams.
func WithComposeDirectory(source ComposeDirectorySource) Option {
	return func(s *State) { s.composeDirectory = source }
}

// composeDirectoryTimeout bounds one directory listing. It shares the
// Session's command path, whose default budget is a minute; a listing that
// exceeds this window is reported instead of holding the picker open.
const composeDirectoryTimeout = 15 * time.Second

// The listing protocol prefixes the resolved path and each entry type so the
// parser never guesses at a file name.
const (
	composeDirectoryPathPrefix      = "__D__"
	composeDirectoryEntryPrefix     = "__d__"
	composeDirectoryFilePrefix      = "__f__"
	composeDirectoryListingMaxBytes = 1 << 20
)

// errComposeDirectoryUnsupported reports a Session whose shell cannot list a
// directory through the picker (raw network, serial, and cmd Sessions).
var errComposeDirectoryUnsupported = errors.New("compose directory: this Session cannot list directories")

// errComposeDirectoryNoPath reports a listing whose resolved path was missing.
var errComposeDirectoryNoPath = errors.New("compose directory: the listing did not report its path")

// errComposeDirectoryListingFailed reports a listing command that exited with
// a status. Its output is the shell's error, not a directory.
var errComposeDirectoryListingFailed = errors.New("compose directory: the listing command failed")

// OpenComposeDirectory shows the picker for the focused Session.
func (s *State) OpenComposeDirectory() bool {
	id, ok := s.FocusedSessionID()
	if !ok {
		return false
	}
	return s.OpenComposeDirectoryFor(id)
}

// OpenComposeDirectoryFor shows the picker for one Session. It starts at the
// tracked working directory, or at the user's home directory when the Session
// has not reported one.
func (s *State) OpenComposeDirectoryFor(id model.SessionID) bool {
	cwd, live := s.composeDirectorySessionInfo(id)
	if !live {
		return false
	}
	shell := ""
	if session, ok := s.SessionByID(id); ok {
		shell = session.ShellKind()
	}
	s.inputMu.Lock()
	s.closeComposeDirectoryLocked()
	s.composeDirectoryOpen = true
	s.composeDirectorySession = id
	s.composeDirectoryShell = shell
	s.composeDirectoryTracked = cwd
	s.composeDirectoryPath = cwd
	s.composeDirectoryTyped = cwd
	s.composeDirectoryUnknown = strings.TrimSpace(cwd) == ""
	s.inputMu.Unlock()
	s.requestComposeDirectory(cwd)
	return true
}

// RefreshComposeDirectory follows the Session: a changed tracked working
// directory re-roots the picker, and a Session that ended closes it. The view
// calls it while the picker is open.
func (s *State) RefreshComposeDirectory() {
	s.inputMu.Lock()
	if !s.composeDirectoryOpen {
		s.inputMu.Unlock()
		return
	}
	id := s.composeDirectorySession
	s.inputMu.Unlock()

	cwd, live := s.composeDirectorySessionInfo(id)
	if !live {
		s.CloseComposeDirectory()
		return
	}

	s.inputMu.Lock()
	if !s.composeDirectoryOpen || s.composeDirectorySession != id {
		s.inputMu.Unlock()
		return
	}
	if cwd == s.composeDirectoryTracked {
		s.inputMu.Unlock()
		return
	}
	// The shell's working directory is the picker's root, so a changed OSC 7
	// directory re-roots even after the user browsed elsewhere.
	s.composeDirectoryTracked = cwd
	s.composeDirectoryUnknown = strings.TrimSpace(cwd) == ""
	s.composeDirectoryTyped = cwd
	s.inputMu.Unlock()
	s.requestComposeDirectory(cwd)
	s.RequestRedraw()
}

// CloseComposeDirectory hides the picker and cancels any listing in flight.
func (s *State) CloseComposeDirectory() {
	s.inputMu.Lock()
	s.closeComposeDirectoryLocked()
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// ToggleComposeDirectory shows or hides the picker.
func (s *State) ToggleComposeDirectory() {
	if s.ComposeDirectoryOpen() {
		s.CloseComposeDirectory()
		return
	}
	s.OpenComposeDirectory()
}

// closeComposeDirectoryLocked stops the picker. The caller holds inputMu. The
// sequence bump makes a listing that is already in flight stale even if a new
// picker opens before it finishes.
func (s *State) closeComposeDirectoryLocked() {
	if s.composeDirectoryCancel != nil {
		s.composeDirectoryCancel()
		s.composeDirectoryCancel = nil
	}
	s.composeDirectorySeq++
	s.composeDirectoryOpen = false
	s.composeDirectoryLoading = false
	s.composeDirectoryFailed = false
	s.composeDirectoryUnsupported = false
	s.composeDirectoryEntries = nil
	s.composeDirectoryUnknown = false
	s.composeDirectoryRequested = ""
	s.composeDirectorySession = ""
}

// ComposeDirectoryOpen reports whether the picker is showing.
func (s *State) ComposeDirectoryOpen() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeDirectoryOpen
}

// ComposeDirectoryPath returns the directory being browsed.
func (s *State) ComposeDirectoryPath() string {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeDirectoryPath
}

// ComposeDirectoryTyped returns the path editor for binding.
func (s *State) ComposeDirectoryTyped() *string {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return &s.composeDirectoryTyped
}

// ComposeDirectoryEntries returns a copy of the listed entries.
func (s *State) ComposeDirectoryEntries() []ComposeDirectoryEntry {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return append([]ComposeDirectoryEntry(nil), s.composeDirectoryEntries...)
}

// ComposeDirectoryLoading reports a listing in flight.
func (s *State) ComposeDirectoryLoading() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeDirectoryLoading
}

// ComposeDirectoryFailed reports that the last listing failed. The picker
// keeps the last successful path, so the user can correct the typed path.
func (s *State) ComposeDirectoryFailed() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeDirectoryFailed
}

// ComposeDirectoryUnsupported reports that the Session's shell cannot list
// directories at all. The view explains that instead of offering a listing.
func (s *State) ComposeDirectoryUnsupported() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeDirectoryUnsupported
}

// ComposeDirectoryUnknownCWD reports that the picker started at home because
// the shell had not reported a working directory. The view says so explicitly.
func (s *State) ComposeDirectoryUnknownCWD() bool {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.composeDirectoryUnknown
}

// NavigateComposeDirectory lists one directory entry inside the current
// directory. The UI calls it only for entries marked Directory.
func (s *State) NavigateComposeDirectory(name string) {
	s.inputMu.Lock()
	if !s.composeDirectoryOpen || name == "" {
		s.inputMu.Unlock()
		return
	}
	path := joinComposeDirectory(s.composeDirectoryPath, name)
	s.composeDirectoryTyped = path
	s.composeDirectoryUnknown = false
	s.inputMu.Unlock()
	s.requestComposeDirectory(path)
}

// ComposeDirectoryUp lists the parent of the directory being browsed.
func (s *State) ComposeDirectoryUp() {
	s.inputMu.Lock()
	if !s.composeDirectoryOpen {
		s.inputMu.Unlock()
		return
	}
	parent := parentComposeDirectory(s.composeDirectoryPath)
	if parent == "" || parent == s.composeDirectoryPath {
		s.inputMu.Unlock()
		return
	}
	s.composeDirectoryTyped = parent
	s.composeDirectoryUnknown = false
	s.inputMu.Unlock()
	s.requestComposeDirectory(parent)
}

// ConfirmComposeDirectory inserts the chosen path into the compose draft and
// closes the picker. It inserts text only and never sends or executes
// anything. A path typed since the last listing is inserted as typed; a path
// that was listed is inserted as the resolved absolute directory, so browsing
// with ".." still confirms the right location.
func (s *State) ConfirmComposeDirectory() bool {
	s.inputMu.Lock()
	if !s.composeDirectoryOpen {
		s.inputMu.Unlock()
		return false
	}
	path := strings.TrimSpace(s.composeDirectoryTyped)
	if path == "" || path == s.composeDirectoryRequested {
		path = s.composeDirectoryPath
	}
	if path == "" {
		path = s.composeDirectoryTyped
	}
	shell := s.composeDirectoryShell
	ok := s.confirmComposeDirectoryLocked(path, shell)
	s.inputMu.Unlock()
	if ok {
		s.RequestRedraw()
	}
	return ok
}

// InsertComposeDirectoryEntry inserts one file entry's full path. The UI uses
// it for file rows; directory rows navigate instead.
func (s *State) InsertComposeDirectoryEntry(name string) bool {
	s.inputMu.Lock()
	if !s.composeDirectoryOpen || name == "" {
		s.inputMu.Unlock()
		return false
	}
	path := joinComposeDirectory(s.composeDirectoryPath, name)
	ok := s.confirmComposeDirectoryLocked(path, s.composeDirectoryShell)
	s.inputMu.Unlock()
	if ok {
		s.RequestRedraw()
	}
	return ok
}

// confirmComposeDirectoryLocked appends path to the draft and closes the
// picker. The caller holds inputMu.
func (s *State) confirmComposeDirectoryLocked(path, shell string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	s.composeDraft = appendComposeDirectoryPath(s.composeDraft, path, shell)
	s.closeComposeDirectoryLocked()
	return true
}

// requestComposeDirectory starts an asynchronous listing. A newer request
// cancels the previous one, so a slow listing can never replace a newer path.
func (s *State) requestComposeDirectory(path string) {
	s.inputMu.Lock()
	if !s.composeDirectoryOpen {
		s.inputMu.Unlock()
		return
	}
	if s.composeDirectoryCancel != nil {
		s.composeDirectoryCancel()
	}
	seq := s.composeDirectorySeq + 1
	s.composeDirectorySeq = seq
	ctx, cancel := context.WithCancel(context.Background())
	s.composeDirectoryCancel = cancel
	s.composeDirectoryLoading = true
	s.composeDirectoryFailed = false
	s.composeDirectoryUnsupported = false
	s.composeDirectoryRequested = path
	s.composeDirectoryEntries = nil
	id := s.composeDirectorySession
	s.inputMu.Unlock()

	// Resolve the Session on the UI thread; the listing goroutine never reads
	// the Tabs. A test lister ignores the pointer.
	var session *appsession.Session
	if s.composeDirectory.List == nil {
		session, _ = s.SessionByID(id)
	}
	s.RequestRedraw()
	go s.listComposeDirectory(ctx, seq, session, path)
}

func (s *State) listComposeDirectory(ctx context.Context, seq uint64, session *appsession.Session, path string) {
	listing, err := s.composeDirectoryLister()(ctx, session, path)
	s.inputMu.Lock()
	if seq != s.composeDirectorySeq || !s.composeDirectoryOpen || ctx.Err() != nil {
		s.inputMu.Unlock()
		return
	}
	s.composeDirectoryCancel = nil
	s.composeDirectoryLoading = false
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			// A superseded request; the newer one owns the visible state.
		case errors.Is(err, errComposeDirectoryUnsupported):
			s.composeDirectoryUnsupported = true
		default:
			s.composeDirectoryFailed = true
		}
	} else {
		s.composeDirectoryFailed = false
		s.composeDirectoryUnsupported = false
		s.composeDirectoryPath = listing.Path
		// Never overwrite text typed while the listing was in flight. The
		// editor keeps the user's input; the resolved directory is shown
		// separately beside it.
		if strings.TrimSpace(s.composeDirectoryTyped) == "" ||
			s.composeDirectoryTyped == s.composeDirectoryRequested {
			s.composeDirectoryTyped = listing.Path
		}
		s.composeDirectoryEntries = append([]ComposeDirectoryEntry(nil), listing.Entries...)
	}
	s.inputMu.Unlock()
	s.RequestRedraw()
}

func (s *State) composeDirectoryLister() ComposeDirectoryLister {
	if s.composeDirectory.List != nil {
		return s.composeDirectory.List
	}
	return listComposeDirectory
}

// composeDirectorySessionInfo reads the Session's tracked CWD and liveness.
// The test seam wins; otherwise the live Session's snapshot supplies both. Only
// an active transport counts as connected: a Session that ended, failed, or is
// reconnecting is not browsable, so the picker closes instead of waiting for a
// listing that cannot come.
func (s *State) composeDirectorySessionInfo(id model.SessionID) (string, bool) {
	if s.composeDirectory.Session != nil {
		return s.composeDirectory.Session(id)
	}
	session, ok := s.SessionByID(id)
	if !ok {
		return "", false
	}
	snapshot := session.Snapshot()
	return snapshot.CWD, snapshot.Status == appsession.StatusRunning &&
		snapshot.Connection == appsession.ConnectionStatusActive
}

// listComposeDirectory is the production lister. It runs one read-only
// listing through the Session's existing programmatic command path, so no
// second connection is opened. PowerShell and POSIX-like shells are supported;
// raw network, serial, and cmd Sessions report that they cannot list.
func listComposeDirectory(ctx context.Context, session *appsession.Session, path string) (ComposeDirectoryListing, error) {
	if session == nil {
		return ComposeDirectoryListing{}, appsession.ErrCommandClosed
	}
	var command string
	switch session.ShellKind() {
	case "powershell":
		command = powershellDirectoryCommand(path)
	case "raw", "cmd":
		return ComposeDirectoryListing{}, errComposeDirectoryUnsupported
	default:
		command = posixDirectoryCommand(path)
	}
	result, err := session.Command(ctx, command, appsession.CommandOptions{
		Timeout: composeDirectoryTimeout, MaxOutputBytes: composeDirectoryListingMaxBytes,
	})
	if err != nil {
		return ComposeDirectoryListing{}, err
	}
	if result.HasExitCode && result.ExitCode != 0 {
		return ComposeDirectoryListing{}, errComposeDirectoryListingFailed
	}
	return parseComposeDirectoryOutput(result.Stdout)
}

// posixDirectoryCommand lists a directory in a POSIX-like shell (bash, zsh,
// fish, and the unknown shell family SSH uses). cd runs in the wrapper's
// subshell, so the Session's own directory never changes. PWD is printed so a
// relative or symlinked request resolves to the directory actually listed.
func posixDirectoryCommand(requested string) string {
	directory := `"$HOME"`
	if requested != "" {
		directory = singleQuotePOSIX(requested)
	}
	return "cd -- " + directory + " && printf '" + composeDirectoryPathPrefix + "%s\\n' \"$PWD\" && " +
		"env LC_ALL=C ls -1ApL -- .\n"
}

// powershellDirectoryCommand lists a directory without changing the Session's
// location: Resolve-Path and Get-ChildItem both take a literal path.
func powershellDirectoryCommand(requested string) string {
	target := "$HOME"
	if requested != "" {
		target = "'" + strings.ReplaceAll(requested, "'", "''") + "'"
	}
	return "$__ncPath = " + target + "; " +
		"$__ncResolved = (Resolve-Path -LiteralPath $__ncPath -ErrorAction Stop).ProviderPath; " +
		"Write-Output ('" + composeDirectoryPathPrefix + "' + $__ncResolved); " +
		"Get-ChildItem -LiteralPath $__ncResolved -Force | ForEach-Object { " +
		"if ($_.PSIsContainer) { Write-Output ('" + composeDirectoryEntryPrefix + "' + $_.Name) } " +
		"else { Write-Output ('" + composeDirectoryFilePrefix + "' + $_.Name) } }\n"
}

// parseComposeDirectoryOutput reads the marker protocol and the POSIX ls
// output. Directories come first, then names, so the picker is stable across
// shells.
func parseComposeDirectoryOutput(output string) (ComposeDirectoryListing, error) {
	listing := ComposeDirectoryListing{}
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, composeDirectoryPathPrefix):
			listing.Path = strings.TrimPrefix(line, composeDirectoryPathPrefix)
		case strings.HasPrefix(line, composeDirectoryEntryPrefix):
			listing.Entries = append(listing.Entries, ComposeDirectoryEntry{
				Name: strings.TrimPrefix(line, composeDirectoryEntryPrefix), Directory: true,
			})
		case strings.HasPrefix(line, composeDirectoryFilePrefix):
			listing.Entries = append(listing.Entries, ComposeDirectoryEntry{
				Name: strings.TrimPrefix(line, composeDirectoryFilePrefix),
			})
		case line == "":
			// The listing command's own line ends and prompt residue are blank.
		default:
			// POSIX ls marks a directory with a trailing slash.
			directory := strings.HasSuffix(line, "/")
			listing.Entries = append(listing.Entries, ComposeDirectoryEntry{
				Name: strings.TrimSuffix(line, "/"), Directory: directory,
			})
		}
	}
	if listing.Path == "" {
		return ComposeDirectoryListing{}, errComposeDirectoryNoPath
	}
	sort.SliceStable(listing.Entries, func(i, j int) bool {
		left, right := listing.Entries[i], listing.Entries[j]
		if left.Directory != right.Directory {
			return left.Directory
		}
		lowerLeft, lowerRight := strings.ToLower(left.Name), strings.ToLower(right.Name)
		if lowerLeft != lowerRight {
			return lowerLeft < lowerRight
		}
		return left.Name < right.Name
	})
	return listing, nil
}

// appendComposeDirectoryPath adds a path to the draft as one shell word. It
