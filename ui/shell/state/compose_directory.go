package state

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
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

// The listing protocol is one ASCII frame on the wire:
//
//	~<nonce>~B<8 hex byte length><hex payload>~<nonce>~Z
//
// The nonce is unique per request. The payload is the NUL-framed stream (a P
// record with the resolved path, then a D or F record per entry), so the
// terminal never carries a NUL, a newline, or a raw file name: everything but
// the fixed sentinel is hex. A name cannot spoof a record because it is decoded
// only after the frame length has been checked. The sentinel cannot occur in
// the payload because '~' is not a hex digit.
const (
	composeDirectoryPathRecord      = 'P'
	composeDirectoryEntryRecord     = 'D'
	composeDirectoryFileRecord      = 'F'
	composeDirectoryListingMaxBytes = 1 << 20
	composeDirectoryFrameBegin      = "B"
	composeDirectoryFrameEnd        = "Z"
)

// errComposeDirectoryUnsupported reports a Session whose shell cannot list a
// directory through the picker (raw network, serial, and cmd Sessions).
var errComposeDirectoryUnsupported = errors.New("compose directory: this Session cannot list directories")

// errComposeDirectoryNoPath reports a listing whose resolved path was missing.
var errComposeDirectoryNoPath = errors.New("compose directory: the listing did not report its path")

// errComposeDirectoryListingFailed reports a listing command that exited with
// a status. Its output is the shell's error, not a directory.
var errComposeDirectoryListingFailed = errors.New("compose directory: the listing command failed")

// errComposeDirectoryListingTooLarge reports a listing whose output filled the
// capture cap. The cap keeps the newest bytes, so the leading path record is
// gone; parsing the tail would show part of a directory as if it were whole.
var errComposeDirectoryListingTooLarge = errors.New("compose directory: the listing is too large")

// errComposeDirectoryCorrupt reports a frame whose length or hex is damaged,
// as a ConPTY wrap can leave it if the healer cannot identify the artifact.
// Guessing would show part of a listing as if it were whole.
var errComposeDirectoryCorrupt = errors.New("compose directory: the listing frame is damaged")

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
	// The UI thread owns composeDirectoryTyped; a finished listing only left a
	// pending copy. Apply it here, before the view binds the editor.
	s.applyComposeDirectoryPendingLocked()
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
	s.composeDirectoryPendingTyped = ""
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

// ComposeDirectoryTyped returns the path editor for binding. Only the UI
// thread may read or write the returned string; a background listing hands its
// result over through RefreshComposeDirectory instead of touching it.
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
	// Drain a finished listing so Insert sees the resolved path even when the
	// click arrives before the next Refresh.
	s.applyComposeDirectoryPendingLocked()
	path := strings.TrimSpace(s.composeDirectoryTyped)
	if path == "" {
		// The editor was cleared; confirm the directory being browsed.
		// A path still equal to the request is the user's own choice: while a
		// listing is in flight, or after it failed, composeDirectoryPath is
		// the previous directory and must not stand in for it.
		path = s.composeDirectoryPath
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
	// A resolved path left by the previous listing belongs to the directory the
	// user has just left. Applying it after this request starts would overwrite
	// the new path, which still equals the request the apply tests for.
	s.composeDirectoryPendingTyped = ""
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
		// The TextInput binds composeDirectoryTyped directly, so this goroutine
		// must not write it: the UI thread owns it. Leave the resolved path for
		// applyComposeDirectoryPendingLocked, which runs on the UI thread and
		// decides whether the user's typed text wins.
		s.composeDirectoryPendingTyped = listing.Path
		s.composeDirectoryEntries = append([]ComposeDirectoryEntry(nil), listing.Entries...)
	}
	s.inputMu.Unlock()
	s.RequestRedraw()
}

// applyComposeDirectoryPendingLocked copies a finished listing's resolved path
// into the editor. The caller holds inputMu and runs on the UI thread, the only
// writer of composeDirectoryTyped. Text typed while the listing was in flight
// wins; the editor is left alone unless it is empty or still holds the request.
func (s *State) applyComposeDirectoryPendingLocked() {
	pending := s.composeDirectoryPendingTyped
	if pending == "" {
		return
	}
	s.composeDirectoryPendingTyped = ""
	if strings.TrimSpace(s.composeDirectoryTyped) == "" ||
		s.composeDirectoryTyped == s.composeDirectoryRequested {
		s.composeDirectoryTyped = pending
	}
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
	nonce := newRuntimeID("ncdir")
	var command string
	switch session.ShellKind() {
	case "powershell":
		command = powershellDirectoryCommand(path, nonce)
	case "raw", "cmd":
		return ComposeDirectoryListing{}, errComposeDirectoryUnsupported
	default:
		command = posixDirectoryCommand(path, nonce)
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
	if result.Truncated {
		// The capture kept the newest bytes, so the leading path record is
		// already gone. Report a failure instead of parsing a partial listing.
		return ComposeDirectoryListing{}, errComposeDirectoryListingTooLarge
	}
	return parseComposeDirectoryOutput(result.Stdout, nonce)
}

// posixDirectoryCommand lists a directory through a non-interactive POSIX
// shell. The command runs under the Session's own shell, which may be zsh,
// fish, nushell or xonsh: `sh -c` makes the script's globs, `cd` and `printf`
// POSIX regardless. Without it, zsh's NOMATCH aborts the unmatched `. [!.].*`
// globs and a non-POSIX shell does not expand them at all.
//
// The script builds the NUL-framed record stream locally and pipes it through
// od, so the terminal carries only hex: no NUL, newline or raw name reaches the
// line protocol. cd runs in the command's subshell, so the Session's own
// directory never changes. PWD is printed so a relative or symlinked request
// resolves to the directory actually listed.
func posixDirectoryCommand(requested, nonce string) string {
	directory := `"$HOME"`
	if requested != "" {
		directory = singleQuotePOSIX(requested)
	}
	begin := composeDirectoryMarker(nonce, composeDirectoryFrameBegin)
	end := composeDirectoryMarker(nonce, composeDirectoryFrameEnd)
	script := strings.Join([]string{
		"__nc_dir=" + directory,
		`case $__nc_dir in -*) __nc_dir=./$__nc_dir ;; esac`,
		`cd "$__nc_dir" || exit 1`,
		`__nc_hex=$({`,
		`printf 'P%s\0' "$PWD"`,
		`for __nc_entry in .[!.]* ..?* *; do`,
		`if [ -e "$__nc_entry" ] || [ -L "$__nc_entry" ]; then`,
		`if [ -d "$__nc_entry" ]; then __nc_type=D; else __nc_type=F; fi`,
		`printf '%s%s\0' "$__nc_type" "$__nc_entry"`,
		`fi`,
		`done`,
		`} | od -A n -v -t x1 | tr -d ' \n') || exit 1`,
		`printf '` + begin + `%08x%s` + end + `' "$((${#__nc_hex} / 2))" "$__nc_hex"`,
	}, "\n")
	return "sh -c " + singleQuotePOSIX(script)
}

// powershellDirectoryCommand lists a directory without changing the Session's
// location: Resolve-Path and Get-ChildItem both take a literal path. It builds
// the NUL-framed stream in memory and writes one hex frame, so Console's text
// stream carries no NUL and a name containing a newline or a marker prefix
// cannot be misread. The single write also keeps the frame short enough for
// ConPTY not to hard-wrap it.
func powershellDirectoryCommand(requested, nonce string) string {
	target := "$HOME"
	if requested != "" {
		target = "'" + strings.ReplaceAll(requested, "'", "''") + "'"
	}
	begin := composeDirectoryMarker(nonce, composeDirectoryFrameBegin)
	end := composeDirectoryMarker(nonce, composeDirectoryFrameEnd)
	return "$__ncPath = " + target + "; " +
		"$__ncResolved = (Resolve-Path -LiteralPath $__ncPath -ErrorAction Stop).ProviderPath; " +
		"$__ncText = [Text.StringBuilder]::new(); " +
		"[void]$__ncText.Append('P').Append($__ncResolved).Append([char]0); " +
		"Get-ChildItem -LiteralPath $__ncResolved -Force | ForEach-Object { " +
		"if ($_.PSIsContainer) { $__ncType = 'D' } else { $__ncType = 'F' }; " +
		"[void]$__ncText.Append($__ncType).Append($_.Name).Append([char]0) }; " +
		"$__ncBytes = [Text.Encoding]::UTF8.GetBytes($__ncText.ToString()); " +
		"$__ncHex = [BitConverter]::ToString($__ncBytes).Replace('-', '').ToLowerInvariant(); " +
		"[Console]::Out.Write('" + begin + "' + ('{0:x8}' -f $__ncBytes.Length) + $__ncHex + '" + end + "')"
}

// composeDirectoryMarker is one end of the frame's sentinel.
func composeDirectoryMarker(nonce, final string) string {
	return "~" + nonce + "~" + final
}

// healComposeDirectoryWraps removes ConPTY's hard-wrap artifact. When the
// console wraps a long line it writes CR, LF, a cursor-position CSI and a copy
// of the cell at the margin; the command collector has already removed the CR
// and the CSI, leaving LF and a duplicated character. The frame is ASCII hex
// and the scripts write no LF, so every LF here is that artifact. Dropping it
// and the duplicate beside it reconnects the hex; the frame's length check
// rejects anything the healer could not identify.
func healComposeDirectoryWraps(output string) string {
	if !strings.ContainsRune(output, '\n') && !strings.ContainsRune(output, '\r') {
		return output
	}
	healed := make([]byte, 0, len(output))
	for index := 0; index < len(output); index++ {
		switch output[index] {
		case '\r':
			// The collector removes these; tolerate a caller that did not.
		case '\n':
			if len(healed) > 0 && index+1 < len(output) && healed[len(healed)-1] == output[index+1] {
				index++
			}
		default:
			healed = append(healed, output[index])
		}
	}
	return string(healed)
}

// parseComposeDirectoryOutput reads the hex frame. The typed command's echo
// can contain the sentinel text, so a frame that does not validate is skipped
// and the next sentinel is tried; the real listing is the one with a matching
// length and end marker. Unknown or malformed records fail the listing rather
// than becoming a guessed path.
func parseComposeDirectoryOutput(output, nonce string) (ComposeDirectoryListing, error) {
	healed := healComposeDirectoryWraps(output)
	begin := composeDirectoryMarker(nonce, composeDirectoryFrameBegin)
	end := composeDirectoryMarker(nonce, composeDirectoryFrameEnd)
	lastErr := errComposeDirectoryNoPath
	for search := 0; ; {
		index := strings.Index(healed[search:], begin)
		if index < 0 {
			return ComposeDirectoryListing{}, lastErr
		}
		start := search + index + len(begin)
		endIndex := strings.Index(healed[start:], end)
		if endIndex < 0 {
			return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
		}
		listing, err := decodeComposeDirectoryFrame(healed[start : start+endIndex])
		if err == nil {
			return listing, nil
		}
		lastErr = err
		search = start
	}
}

// decodeComposeDirectoryFrame checks the length prefix and decodes the payload
// into the resolved path and entries.
func decodeComposeDirectoryFrame(body string) (ComposeDirectoryListing, error) {
	if len(body) < 8 {
		return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
	}
	declared, err := strconv.ParseUint(body[:8], 16, 32)
	if err != nil {
		return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
	}
	encoded := body[8:]
	if uint64(len(encoded)) != declared*2 {
		return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
	}
	listing := ComposeDirectoryListing{}
	seenPath := false
	for _, record := range strings.Split(string(raw), "\x00") {
		if record == "" {
			continue
		}
		switch record[0] {
		case composeDirectoryPathRecord:
			if seenPath || len(record) == 1 {
				return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
			}
			listing.Path = record[1:]
			seenPath = true
		case composeDirectoryEntryRecord:
			if !seenPath || len(record) == 1 {
				return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
			}
			listing.Entries = append(listing.Entries, ComposeDirectoryEntry{
				Name: record[1:], Directory: true,
			})
		case composeDirectoryFileRecord:
			if !seenPath || len(record) == 1 {
				return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
			}
			listing.Entries = append(listing.Entries, ComposeDirectoryEntry{
				Name: record[1:],
			})
		default:
			return ComposeDirectoryListing{}, errComposeDirectoryCorrupt
		}
	}
	if !seenPath || listing.Path == "" {
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
