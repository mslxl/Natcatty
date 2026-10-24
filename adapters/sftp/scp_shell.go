package sftp

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// scpError is one SCP shell or protocol failure. The Code field is a stable
// token, matching the original's error classification.
type scpError struct {
	Code    string
	Message string
	// ExitCode and Stderr are retained for a failed remote command so a
	// caller can tell an empty response from a rejected one.
	ExitCode int
	Stderr   string
}

func (e *scpError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func scpFailure(code, message string) *scpError {
	return &scpError{Code: code, Message: message}
}

// shellQuote single-quotes one POSIX shell argument. A single quote becomes
// the standard '\” sequence. NUL is refused; an empty value is allowed only
// when the caller says so.
func shellQuote(value string, allowEmpty bool) (string, error) {
	if strings.ContainsRune(value, '\x00') {
		return "", scpFailure("SCP_SHELL_ERROR", "shell argument must not contain NUL")
	}
	if value == "" && !allowEmpty {
		return "", scpFailure("SCP_SHELL_ERROR", "shell argument must not be empty")
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'", nil
}

// assertSafeRemotePath rejects NUL and line breaks before a path reaches a
// remote shell. Quoting protects every other character.
func assertSafeRemotePath(remotePath string) error {
	if remotePath == "" {
		return scpFailure("SCP_SHELL_ERROR", "remote path is required")
	}
	if strings.ContainsRune(remotePath, '\x00') ||
		strings.ContainsAny(remotePath, "\r\n") {
		return scpFailure("SCP_SHELL_ERROR", "remote path must not contain NUL or newlines")
	}
	return nil
}

func quotePath(remotePath string) (string, error) {
	if err := assertSafeRemotePath(remotePath); err != nil {
		return "", err
	}
	return shellQuote(remotePath, false)
}

// buildListCommand emits one record per entry:
//
//	T|MODE|SIZE|MTIME|B64NAME|OWNER
//
// where T is f/d/l, MODE is the ls permission text, MTIME is Unix seconds,
// and B64NAME is the base64 basename so spaces and unicode survive. The loop
// is POSIX/BusyBox oriented and never opens a file (a FIFO would hang).
func buildListCommand(dir string) (string, error) {
	q, err := quotePath(dir)
	if err != nil {
		return "", err
	}
	loop := []string{
		"for f in * .[!.]* ..?*; do",
		`  { [ -e "$f" ] || [ -L "$f" ]; } || continue`,
		`  [ "$f" = "." ] && continue`,
		`  [ "$f" = ".." ] && continue`,
		`  if [ -L "$f" ]; then t=l`,
		`  elif [ -d "$f" ]; then t=d`,
		`  else t=f; fi`,
		`  lsline=$(ls -ld -- "$f" 2>/dev/null)`,
		`  mode=$(printf "%s\n" "$lsline" | awk '{print $1}')`,
		`  owner=$(printf "%s\n" "$lsline" | awk '{print $3}')`,
		`  size=$(stat -c %s -- "$f" 2>/dev/null || stat -f %z -- "$f" 2>/dev/null || echo 0)`,
		`  [ -z "$size" ] && size=0`,
		`  mtime=$(date -r "$f" +%s 2>/dev/null || stat -c %Y -- "$f" 2>/dev/null || stat -f %m -- "$f" 2>/dev/null || echo 0)`,
		`  if command -v base64 >/dev/null 2>&1; then b64=$(printf "%s" "$f" | base64 2>/dev/null | tr -d "\r\n")`,
		`  else b64=; fi`,
		`  printf "%s|%s|%s|%s|%s|%s\n" "$t" "${mode:-?}" "$size" "$mtime" "$b64" "${owner:-}"`,
		"done",
	}
	return "cd " + q + " || exit 1; " + strings.Join(loop, "\n"), nil
}

// buildListCommandLs emits a classic ls -la listing for the fallback parser.
func buildListCommandLs(dir string) (string, error) {
	q, err := quotePath(dir)
	if err != nil {
		return "", err
	}
	return "LC_ALL=C ls -la -- " + q + " 2>/dev/null || LC_ALL=C ls -la " + q, nil
}

// buildStatCommand emits:
//
//	T|MODE|SIZE|MTIME|ABS
//
// A missing path prints ENOENT on stderr and exits 2 so a caller can tell it
// from an empty exec response.
func buildStatCommand(remotePath string) (string, error) {
	q, err := quotePath(remotePath)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"p=" + q,
		`if [ ! -e "$p" ] && [ ! -L "$p" ]; then echo "ENOENT" >&2; exit 2; fi`,
		`if [ -L "$p" ]; then t=l`,
		`elif [ -d "$p" ]; then t=d`,
		`else t=f; fi`,
		`mode=$(ls -ld -- "$p" 2>/dev/null | awk '{print $1}')`,
		`size=$(stat -c %s -- "$p" 2>/dev/null || stat -f %z -- "$p" 2>/dev/null || echo "?")`,
		`mtime=$(date -r "$p" +%s 2>/dev/null || stat -c %Y -- "$p" 2>/dev/null || stat -f %m -- "$p" 2>/dev/null || echo 0)`,
		`abs=$(cd "$(dirname -- "$p")" 2>/dev/null && printf "%s/%s\n" "$(pwd -P 2>/dev/null || pwd)" "$(basename -- "$p")" || printf "%s\n" "$p")`,
		`printf "%s|%s|%s|%s|%s\n" "$t" "${mode:-?}" "${size:-?}" "$mtime" "$abs"`,
	}, "; "), nil
}

// buildSymlinkTargetCommand classifies a symlink target so a directory link
// stays navigable and a dangling link stays a link.
func buildSymlinkTargetCommand(remotePath string) (string, error) {
	q, err := quotePath(remotePath)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"p=" + q,
		`if [ ! -e "$p" ] && [ ! -L "$p" ]; then echo broken; exit 0; fi`,
		`if [ -d "$p" ]; then echo directory; else echo file; fi`,
	}, "; "), nil
}

func buildMkdirCommand(remotePath string, recursive bool) (string, error) {
	q, err := quotePath(remotePath)
	if err != nil {
		return "", err
	}
	if recursive {
		return "mkdir -p -- " + q, nil
	}
	return "mkdir -- " + q, nil
}

func buildDeleteCommand(remotePath string, recursive bool) (string, error) {
	q, err := quotePath(remotePath)
	if err != nil {
		return "", err
	}
	if recursive {
		return "rm -rf -- " + q, nil
	}
	return "rm -f -- " + q + " 2>/dev/null || rmdir -- " + q, nil
}

func buildRenameCommand(oldPath, newPath string) (string, error) {
	a, err := quotePath(oldPath)
	if err != nil {
		return "", err
	}
	b, err := quotePath(newPath)
	if err != nil {
		return "", err
	}
	return "mv -- " + a + " " + b, nil
}

func buildChmodCommand(remotePath string, mode uint32) (string, error) {
	q, err := quotePath(remotePath)
	if err != nil {
		return "", err
	}
	modeText := formatModeOctal(mode)
	return "chmod " + modeText + " -- " + q, nil
}

func buildHomeCommand() string { return `printf "%s\n" "$HOME"` }

// buildScpSinkCommand is `scp -t` for a destination directory.
func buildScpSinkCommand(dir string) (string, error) {
	q, err := quotePath(dir)
	if err != nil {
		return "", err
	}
	return "scp -t -- " + q, nil
}

// buildScpSourceCommand is `scp -f` for one remote file.
func buildScpSourceCommand(remotePath string) (string, error) {
	q, err := quotePath(remotePath)
	if err != nil {
		return "", err
	}
	return "scp -f -- " + q, nil
}

// scpRecord is one parsed listing row.
type scpRecord struct {
	Name    string
	Type    string
	Mode    uint32
	Size    int64
	ModTime time.Time
	Owner   string
}

// parseListRecords parses buildListCommand output. Malformed lines are
// skipped so one odd remote filename does not empty the whole listing.
func parseListRecords(stdout string) []scpRecord {
	lines := strings.Split(stdout, "\n")
	records := make([]scpRecord, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		name, err := decodeListName(parts[4])
		if err != nil || name == "" || name == "." || name == ".." {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		seconds, _ := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64)
		record := scpRecord{
			Name:    name,
			Type:    listType(parts[0]),
			Mode:    lsModeToBits(parts[1]),
			Size:    size,
			ModTime: time.Unix(seconds, 0),
		}
		if len(parts) > 5 {
			record.Owner = strings.TrimSpace(parts[5])
			if record.Owner == "?" || record.Owner == "UNKNOWN" {
				record.Owner = ""
			}
		}
		records = append(records, record)
	}
	return records
}

func listType(token string) string {
	switch strings.TrimSpace(token) {
	case "d":
		return "directory"
	case "l":
		return "symlink"
	default:
		return "file"
	}
}

func decodeListName(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		// A remote without base64 emits an empty field; the row is unusable.
		return "", err
	}
	return string(raw), nil
}

// parseLsLaOutput parses the fallback ls -la shape:
//
//	-rw-r--r-- 1 owner group 12 Jan 1 00:00 name
func parseLsLaOutput(stdout string) []scpRecord {
	lines := strings.Split(stdout, "\n")
	records := make([]scpRecord, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 || len(fields[0]) < 10 {
			continue
		}
		mode := fields[0]
		kind := mode[0]
		size, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			continue
		}
		// Names may contain spaces; everything after the time column is the
		// name. ls splits a symlink's arrow off, so drop it for links.
		name := strings.Join(fields[8:], " ")
		if kind == 'l' {
			if arrow := strings.Index(name, " -> "); arrow >= 0 {
				name = name[:arrow]
			}
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "." || name == ".." {
			continue
		}
		records = append(records, scpRecord{
			Name:    name,
			Type:    listType(string(kind)),
			Mode:    lsModeToBits(mode),
			Size:    size,
			ModTime: time.Now(),
			Owner:   fields[2],
		})
	}
	return records
}

// parseStatRecord parses buildStatCommand output. A missing path is reported
// as ErrNotFound-compatible scpError code ENOENT.
func parseStatRecord(stdout, stderr string, exitCode int, hasExit bool) (scpRecord, error) {
	line := ""
	if trimmed := strings.TrimSpace(stdout); trimmed != "" {
		line = strings.Split(trimmed, "\n")[0]
	}
	missing := line == "ENOENT" || hasLine(stderr, "ENOENT")
	if missing && (!hasExit || exitCode == 2) {
		return scpRecord{}, scpFailure("ENOENT", "no such file")
	}
	if line == "" || line == "ENOENT" {
		return scpRecord{}, &scpError{
			Code:     "EMPTY_RESPONSE",
			Message:  "exec channel returned an empty response: " + strings.TrimSpace(stderr),
			ExitCode: exitCode,
			Stderr:   strings.TrimSpace(stderr),
		}
	}
	if hasExit && exitCode != 0 {
		return scpRecord{}, &scpError{
			Code:     "SCP_SHELL_ERROR",
			Message:  "stat command exited " + strconv.Itoa(exitCode) + ": " + strings.TrimSpace(stderr),
			ExitCode: exitCode,
			Stderr:   strings.TrimSpace(stderr),
		}
	}
	parts := strings.Split(line, "|")
	if len(parts) < 5 {
		return scpRecord{}, scpFailure("SCP_SHELL_ERROR", "malformed stat record: "+line)
	}
	record := scpRecord{
		Type: listType(parts[0]),
		Mode: lsModeToBits(parts[1]),
	}
	if size, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); err == nil {
		record.Size = size
	}
	if seconds, err := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64); err == nil {
		record.ModTime = time.Unix(seconds, 0)
	}
	record.Name = strings.TrimSpace(parts[4])
	return record, nil
}

func hasLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// lsModeToBits converts ls permission text (drwxr-xr-x) into permission bits
// including setuid, setgid, and sticky. Unknown text yields 0.
func lsModeToBits(modeText string) uint32 {
	text := strings.TrimSpace(modeText)
	if text == "" || text == "?" {
		return 0
	}
	if len(text) >= 10 {
		text = text[1:10]
	}
	if len(text) != 9 {
		return 0
	}
	bit := func(ch byte, read, write, exec uint32) uint32 {
		switch ch {
		case 'r':
			return read
		case 'w':
			return write
		case 'x', 's', 't':
			return exec
		}
		return 0
	}
	mode := bit(text[0], 0o400, 0, 0) |
		bit(text[1], 0, 0o200, 0) |
		bit(text[2], 0, 0, 0o100) |
		bit(text[3], 0o040, 0, 0) |
		bit(text[4], 0, 0o020, 0) |
		bit(text[5], 0, 0, 0o010) |
		bit(text[6], 0o004, 0, 0) |
		bit(text[7], 0, 0o002, 0) |
		bit(text[8], 0, 0, 0o001)
	if text[2] == 's' || text[2] == 'S' {
		mode |= 0o4000
	}
	if text[5] == 's' || text[5] == 'S' {
		mode |= 0o2000
	}
	if text[8] == 't' || text[8] == 'T' {
		mode |= 0o1000
	}
	return mode
}

// parseModeOctal parses an SCP control-line mode. The wire value is octal
// digits without a prefix.
func parseModeOctal(text string) (uint32, error) {
	value, err := strconv.ParseUint(text, 8, 32)
	if err != nil {
		return 0, scpFailure("SCP_PROTOCOL_ERROR", "malformed SCP mode: "+text)
	}
	return uint32(value & 0o7777), nil
}

// formatModeOctal renders a mode for an SCP control line as four octal digits.
func formatModeOctal(mode uint32) string {
	text := strconv.FormatUint(uint64(mode&0o7777), 8)
	for len(text) < 4 {
		text = "0" + text
	}
	return text
}

// sanitizeScpBasename rejects a name that is not one safe path component.
func sanitizeScpBasename(name string) (string, error) {
	if name == "" {
		return "", scpFailure("SCP_PROTOCOL_ERROR", "SCP entry name is required")
	}
	if strings.ContainsRune(name, '\x00') {
		return "", scpFailure("SCP_PROTOCOL_ERROR", "SCP entry name must not contain NUL")
	}
	if strings.Contains(name, "/") || name == "." || name == ".." {
		return "", scpFailure("SCP_PROTOCOL_ERROR", "SCP entry name must be a simple basename: "+name)
	}
	if strings.ContainsAny(name, "\r\n") {
		return "", scpFailure("SCP_PROTOCOL_ERROR", "SCP entry name must not contain newlines")
	}
	return name, nil
}

// buildFileControlLine builds "Cmmmm <size> <name>\n".
func buildFileControlLine(mode uint32, size int64, name string) ([]byte, error) {
	if size < 0 {
		return nil, scpFailure("SCP_PROTOCOL_ERROR", fmt.Sprintf("invalid SCP file size: %d", size))
	}
	base, err := sanitizeScpBasename(name)
	if err != nil {
		return nil, err
	}
	return []byte("C" + formatModeOctal(mode) + " " + strconv.FormatInt(size, 10) + " " + base + "\n"), nil
}

// scpControl is one parsed source-stream control line.
type scpControl struct {
	Kind string // file, directory, end, time
	Mode uint32
	Size int64
	Name string
}

// parseControlLine parses one SCP source control line without its newline.
func parseControlLine(line string) (scpControl, error) {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return scpControl{}, scpFailure("SCP_PROTOCOL_ERROR", "empty SCP control line")
	}
	if line == "E" {
		return scpControl{Kind: "end"}, nil
	}
	switch line[0] {
	case 'T':
		return scpControl{Kind: "time"}, nil
	case 'C', 'D':
	default:
		return scpControl{}, scpFailure("SCP_PROTOCOL_ERROR", "unknown SCP control line: "+clip(line))
	}
	// Cmmmm size name — the name may contain spaces.
	firstSpace := strings.IndexByte(line, ' ')
	if firstSpace < 2 {
		return scpControl{}, scpFailure("SCP_PROTOCOL_ERROR", "malformed SCP control line: "+clip(line))
	}
	mode, err := parseModeOctal(line[1:firstSpace])
	if err != nil {
		return scpControl{}, err
	}
	rest := line[firstSpace+1:]
	secondSpace := strings.IndexByte(rest, ' ')
	if secondSpace < 1 {
		return scpControl{}, scpFailure("SCP_PROTOCOL_ERROR", "malformed SCP control line: "+clip(line))
	}
	size, err := strconv.ParseInt(rest[:secondSpace], 10, 64)
	if err != nil || size < 0 {
		return scpControl{}, scpFailure("SCP_PROTOCOL_ERROR", "invalid SCP file size: "+rest[:secondSpace])
	}
	name := rest[secondSpace+1:]
	if _, err := sanitizeScpBasename(name); err != nil {
		return scpControl{}, err
	}
	kind := "file"
	if line[0] == 'D' {
		kind = "directory"
	}
	return scpControl{Kind: kind, Mode: mode, Size: size, Name: name}, nil
}

func clip(text string) string {
	if len(text) > 80 {
		return text[:80]
	}
	return text
}
