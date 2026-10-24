// Package scpfixture is an in-process SCP-only remote used by adapter and
// application tests. It implements the SSH exec surface the SCP backend uses:
// a small POSIX command set for browsing and management, the OpenSSH scp
// -t/-f wire protocol, the SCP probe, and the sudo sftp-server handshake. It
// never starts an sshd or a shell, so tests stay deterministic.
package scpfixture

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// NodeType is one fixture entry's type.
type NodeType string

const (
	File      NodeType = "file"
	Directory NodeType = "directory"
	Symlink   NodeType = "symlink"
)

// Node is one fixture entry.
type Node struct {
	Type   NodeType
	Data   []byte
	Mode   uint32
	Owner  string
	Group  string
	Target string
	Mod    time.Time
}

// Option customizes a Server.
type Option func(*Server)

// WithHome sets the remote home directory.
func WithHome(home string) Option {
	return func(s *Server) { s.home = home }
}

// WithoutSCP makes command -v scp fail, so the probe reports a server with no
// SCP binary.
func WithoutSCP() Option {
	return func(s *Server) { s.scpAvailable = false }
}

// WithSudoPassword registers the sudo password and makes the standard
// sftp-server path available.
func WithSudoPassword(password string) Option {
	return func(s *Server) {
		s.sudoPassword = password
		s.sudoServerPath = "/usr/lib/openssh/sftp-server"
	}
}

// WithSudoServerPath chooses which sftp-server path exists. An empty path
// simulates a server without sftp-server installed.
func WithSudoServerPath(serverPath string) Option {
	return func(s *Server) { s.sudoServerPath = serverPath }
}

// Server is one in-process remote. It is safe for concurrent use.
type Server struct {
	home           string
	scpAvailable   bool
	sudoPassword   string
	sudoServerPath string

	mu       sync.Mutex
	nodes    map[string]*Node
	commands []string
}

// New starts an empty fixture rooted at "/" with a home directory and SCP.
func New(options ...Option) *Server {
	server := &Server{
		home:         "/home/tester",
		scpAvailable: true,
		nodes:        map[string]*Node{"/": {Type: Directory, Mode: 0o755, Owner: "root"}},
	}
	server.ensureParents(server.home)
	server.nodes[server.home] = &Node{Type: Directory, Mode: 0o755, Owner: "tester"}
	for _, option := range options {
		option(server)
	}
	return server
}

var _ appconnection.Executor = (*Server)(nil)

// Home returns the fixture home directory.
func (s *Server) Home() string { return s.home }

// WriteFile creates or replaces a regular file.
func (s *Server) WriteFile(remotePath, data string, mode uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureParentsLocked(remotePath)
	s.nodes[path.Clean(remotePath)] = &Node{
		Type: File, Data: []byte(data), Mode: mode, Owner: "tester",
		Mod: time.Unix(1700000000, 0).UTC(),
	}
}

// MkdirAll creates a directory and its ancestors.
func (s *Server) MkdirAll(remotePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureParentsLocked(path.Join(remotePath, "x"))
}

// Symlink creates a symbolic link.
func (s *Server) Symlink(remotePath, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureParentsLocked(remotePath)
	s.nodes[path.Clean(remotePath)] = &Node{
		Type: Symlink, Target: target, Mode: 0o777, Owner: "tester",
		Mod: time.Unix(1700000000, 0).UTC(),
	}
}

// ReadFile returns a file's content.
func (s *Server) ReadFile(remotePath string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[path.Clean(remotePath)]
	if !ok || node.Type != File {
		return "", false
	}
	return string(node.Data), true
}

// Has reports whether a path exists.
func (s *Server) Has(remotePath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.nodes[path.Clean(remotePath)]
	return ok
}

// Commands returns every command the fixture has executed.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// Exec runs one non-interactive shell command.
func (s *Server) Exec(ctx context.Context, command string) (appconnection.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.ExecResult{}, err
	}
	s.record(command)
	stdout, stderr, code := s.runCommand(command)
	return appconnection.ExecResult{
		Stdout: stdout, Stderr: stderr, ExitCode: code, HasExitCode: true,
	}, nil
}

// OpenExec starts a streaming command: the scp protocol, the sudo handshake,
// or an unsupported command.
func (s *Server) OpenExec(ctx context.Context, command string) (appconnection.ExecStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.record(command)
	stream := newFixtureStream()
	var handler streamHandler
	switch {
	case strings.HasPrefix(command, "scp -t -- "):
		dir, ok := firstQuotedAfter(command, "scp -t -- ")
		if !ok {
			handler = errorHandler("fixture: malformed scp sink command")
		} else {
			handler = func(r *bufio.Reader, out, _ io.Writer) int { return s.serveSink(r, out, dir) }
		}
	case strings.HasPrefix(command, "scp -f -- "):
		remotePath, ok := firstQuotedAfter(command, "scp -f -- ")
		if !ok {
			handler = errorHandler("fixture: malformed scp source command")
		} else {
			handler = func(r *bufio.Reader, out, _ io.Writer) int { return s.serveSource(r, out, remotePath) }
		}
	case strings.Contains(command, "sudo -S -p") && strings.Contains(command, "SFTPREADY"):
		if s.sudoServerPath == "" || !strings.Contains(command, s.sudoServerPath) {
			handler = func(_ *bufio.Reader, _, stderr io.Writer) int {
				_, _ = io.WriteString(stderr, "bash: line 1: exec: sftp-server: not found\n")
				return 127
			}
		} else {
			handler = s.serveSudo
		}
	default:
		handler = errorHandler("fixture: unsupported streaming command: " + command)
	}
	stream.run(ctx, handler)
	return stream, nil
}

func (s *Server) record(command string) {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	s.mu.Unlock()
}

// runCommand interprets the command builders the SCP backend emits. Anything
// else fails loudly so a changed builder shows up as a fixture error rather
// than a false pass.
func (s *Server) runCommand(command string) (string, string, int) {
	switch {
	case command == `printf "%s\n" "$HOME"`:
		return s.home + "\n", "", 0
	case strings.Contains(command, "command -v scp"):
		if !s.scpAvailable {
			return "SCP_MISSING\n", "", 127
		}
		return s.home + "\nSCP_OK\n", "", 0
	case strings.HasPrefix(command, "cd ") && strings.Contains(command, "for f in *"):
		dir, ok := firstQuotedAfter(command, "cd ")
		if !ok {
			return "", "fixture: malformed list command", 1
		}
		return s.listStructured(dir)
	case strings.HasPrefix(command, "LC_ALL=C ls -la"):
		dir, ok := firstQuotedAfter(command, "-- ")
		if !ok {
			return "", "fixture: malformed ls command", 1
		}
		return s.listLs(dir)
	case strings.HasPrefix(command, "p=") && strings.Contains(command, "echo broken"):
		remotePath, ok := firstQuotedAfter(command, "p=")
		if !ok {
			return "", "fixture: malformed symlink command", 1
		}
		return s.linkTarget(remotePath)
	case strings.HasPrefix(command, "p=") && strings.Contains(command, `printf "%s|%s|%s|%s|%s\n"`):
		remotePath, ok := firstQuotedAfter(command, "p=")
		if !ok {
			return "", "fixture: malformed stat command", 1
		}
		return s.stat(remotePath)
	case strings.HasPrefix(command, "mkdir "):
		recursive := strings.HasPrefix(command, "mkdir -p -- ")
		remotePath, ok := lastQuoted(command)
		if !ok {
			return "", "fixture: malformed mkdir command", 1
		}
		return s.mkdir(remotePath, recursive)
	case strings.HasPrefix(command, "rm -rf -- "):
		remotePath, ok := firstQuotedAfter(command, "rm -rf -- ")
		if !ok {
			return "", "fixture: malformed remove command", 1
		}
		s.remove(remotePath, true)
		return "", "", 0
	case strings.HasPrefix(command, "rm -f -- "):
		remotePath, ok := firstQuotedAfter(command, "rm -f -- ")
		if !ok {
			return "", "fixture: malformed remove command", 1
		}
		if s.remove(remotePath, false) {
			return "", "", 0
		}
		return "", "rmdir: failed to remove '" + remotePath + "': Directory not empty", 1
	case strings.HasPrefix(command, "mv -- "):
		tokens := shellTokens(command)
		if len(tokens) != 4 {
			return "", "fixture: malformed mv command", 1
		}
		return s.rename(tokens[2], tokens[3])
	case strings.HasPrefix(command, "chmod "):
		tokens := shellTokens(command)
		if len(tokens) != 4 {
			return "", "fixture: malformed chmod command", 1
		}
		value, err := strconv.ParseUint(tokens[1], 8, 32)
		if err != nil {
			return "", "fixture: malformed mode", 1
		}
		return s.chmod(tokens[3], uint32(value))
	case strings.Contains(command, "test -x"):
		if s.sudoServerPath == "" {
			return "", "", 1
		}
		return s.sudoServerPath + "\n", "", 0
	case strings.HasPrefix(command, "realpath -- "), strings.HasPrefix(command, "readlink -f -- "):
		remotePath, ok := firstQuotedAfter(command, "-- ")
		if !ok {
			return "", "fixture: malformed realpath command", 1
		}
		if !s.Has(remotePath) {
			return "", "fixture: no such file", 1
		}
		return path.Clean(remotePath) + "\n", "", 0
	}
	return "", "fixture: unsupported command: " + command, 127
}

// listStructured emits the T|MODE|SIZE|MTIME|B64NAME|OWNER records.
func (s *Server) listStructured(dir string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir = path.Clean(dir)
	node, ok := s.nodes[dir]
	if !ok || node.Type != Directory {
		return "", dir + ": No such file or directory", 1
	}
	var out strings.Builder
	for _, full := range s.childrenLocked(dir) {
		child := s.nodes[full]
		name := path.Base(full)
		encoded := base64.StdEncoding.EncodeToString([]byte(name))
		kind := "f"
		size := int64(len(child.Data))
		switch child.Type {
		case Directory:
			kind, size = "d", 0
		case Symlink:
			kind, size = "l", 0
		}
		fmt.Fprintf(&out, "%s|%s|%d|%d|%s|%s\n",
			kind, lsMode(child), size, child.Mod.Unix(), encoded, child.Owner)
	}
	return out.String(), "", 0
}

// listLs emits a classic ls -la listing for the fallback parser.
func (s *Server) listLs(dir string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir = path.Clean(dir)
	if node, ok := s.nodes[dir]; !ok || node.Type != Directory {
		return "", "ls: cannot access '" + dir + "': No such file or directory", 2
	}
	var out strings.Builder
	total := 0
	for _, full := range s.childrenLocked(dir) {
		child := s.nodes[full]
		name := path.Base(full)
		if child.Type == Symlink {
			name += " -> " + child.Target
		}
		fmt.Fprintf(&out, "%s 1 %s %s %d Jan  1 1970 %s\n",
			lsMode(child), child.Owner, groupOr(child), len(child.Data), name)
		total++
	}
	return fmt.Sprintf("total %d\n%s", total, out.String()), "", 0
}

// childrenLocked returns one directory's children in name order. The caller
// holds the mutex.
func (s *Server) childrenLocked(dir string) []string {
	var children []string
	for full := range s.nodes {
		if full != dir && path.Dir(full) == dir {
			children = append(children, full)
		}
	}
	sort.Strings(children)
	return children
}

// stat emits one T|MODE|SIZE|MTIME|ABS record.
func (s *Server) stat(remotePath string) (string, string, int) {
	s.mu.Lock()
	node, ok := s.nodes[path.Clean(remotePath)]
	s.mu.Unlock()
	if !ok {
		return "", "ENOENT\n", 2
	}
	kind := "f"
	switch node.Type {
	case Directory:
		kind = "d"
	case Symlink:
		kind = "l"
	}
	return fmt.Sprintf("%s|%s|%d|%d|%s\n", kind, lsMode(node), len(node.Data),
		node.Mod.Unix(), path.Clean(remotePath)), "", 0
}

func (s *Server) linkTarget(remotePath string) (string, string, int) {
	s.mu.Lock()
	_, ok := s.nodes[path.Clean(remotePath)]
	s.mu.Unlock()
	if !ok {
		return "broken\n", "", 0
	}
	resolved, ok := s.resolve(remotePath)
	if !ok {
		return "broken\n", "", 0
	}
	if resolved.Type == Directory {
		return "directory\n", "", 0
	}
	return "file\n", "", 0
}

func (s *Server) mkdir(remotePath string, recursive bool) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := path.Clean(remotePath)
	if existing, exists := s.nodes[clean]; exists {
		if recursive && existing.Type == Directory {
			return "", "", 0
		}
		return "", "mkdir: cannot create directory '" + clean + "': File exists", 1
	}
	if _, exists := s.nodes[path.Dir(clean)]; !exists {
		if !recursive {
			return "", "mkdir: cannot create directory '" + clean + "': No such file or directory", 1
		}
		s.ensureParentsLocked(clean)
	}
	s.nodes[clean] = &Node{Type: Directory, Mode: 0o755, Owner: "tester", Mod: time.Unix(1700000000, 0).UTC()}
	return "", "", 0
}

// remove deletes a file, link, or directory. A non-recursive directory
// removal succeeds only when it is empty.
func (s *Server) remove(remotePath string, recursive bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := path.Clean(remotePath)
	node, ok := s.nodes[clean]
	if !ok {
		return true
	}
	if node.Type == Directory && !recursive {
		for full := range s.nodes {
			if full != clean && path.Dir(full) == clean {
				return false
			}
		}
	}
	for full := range s.nodes {
		if full == clean || strings.HasPrefix(full, clean+"/") {
			delete(s.nodes, full)
		}
	}
	return true
}

func (s *Server) rename(oldPath, newPath string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldClean := path.Clean(oldPath)
	if _, ok := s.nodes[oldClean]; !ok {
		return "", "mv: cannot stat '" + oldClean + "': No such file or directory", 1
	}
	newClean := path.Clean(newPath)
	if _, exists := s.nodes[newClean]; exists {
		return "", "mv: cannot move to '" + newClean + "': File exists", 1
	}
	for full, child := range s.nodes {
		if full == oldClean || strings.HasPrefix(full, oldClean+"/") {
			delete(s.nodes, full)
			s.nodes[newClean+strings.TrimPrefix(full, oldClean)] = child
		}
	}
	return "", "", 0
}

func (s *Server) chmod(remotePath string, mode uint32) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[path.Clean(remotePath)]
	if !ok {
		return "", "chmod: cannot access '" + remotePath + "': No such file or directory", 1
	}
	node.Mode = mode & 0o7777
	return "", "", 0
}

// resolve follows symlinks within the fixture.
func (s *Server) resolve(remotePath string) (*Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveLocked(remotePath, 0)
}

func (s *Server) resolveLocked(remotePath string, depth int) (*Node, bool) {
	if depth > 8 {
		return nil, false
	}
	node, ok := s.nodes[path.Clean(remotePath)]
	if !ok {
		return nil, false
	}
	if node.Type != Symlink {
		return node, true
	}
	target := node.Target
	if !strings.HasPrefix(target, "/") {
		target = path.Join(path.Dir(remotePath), target)
	}
	return s.resolveLocked(target, depth+1)
}

func (s *Server) ensureParents(remotePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureParentsLocked(remotePath)
}

func (s *Server) ensureParentsLocked(remotePath string) {
	clean := path.Clean(remotePath)
	for current := path.Dir(clean); ; current = path.Dir(current) {
		if _, ok := s.nodes[current]; ok {
			break
		}
		s.nodes[current] = &Node{Type: Directory, Mode: 0o755, Owner: "tester"}
		if current == "/" || current == "." {
			break
		}
	}
	s.nodes["/"] = &Node{Type: Directory, Mode: 0o755, Owner: "root"}
}

// serveSink implements one scp -t session.
func (s *Server) serveSink(r *bufio.Reader, w io.Writer, dir string) int {
	dir = path.Clean(dir)
	if !s.Has(dir) {
		_, _ = io.WriteString(w, "\x01scp: "+dir+": No such file or directory\n")
		return 1
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return 1
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return 1
	}
	control, err := parseControl(line)
	if err != nil || control.kind != "file" {
		_, _ = io.WriteString(w, "\x01scp: protocol error\n")
		return 1
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return 1
	}
	data := make([]byte, control.size)
	if _, err := io.ReadFull(r, data); err != nil {
		return 1
	}
	trailing, err := r.ReadByte()
	if err != nil || trailing != 0 {
		return 1
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return 1
	}
	s.mu.Lock()
	s.nodes[path.Join(dir, control.name)] = &Node{
		Type: File, Data: data, Mode: control.mode, Owner: "tester",
		Mod: time.Unix(1700000000, 0).UTC(),
	}
	s.mu.Unlock()
	return 0
}

// serveSource implements one scp -f session for a regular file.
func (s *Server) serveSource(r *bufio.Reader, w io.Writer, remotePath string) int {
	remotePath = path.Clean(remotePath)
	if _, err := r.ReadByte(); err != nil {
		return 1
	}
	s.mu.Lock()
	node, ok := s.nodes[remotePath]
	s.mu.Unlock()
	if !ok || node.Type != File {
		_, _ = io.WriteString(w, "\x01scp: "+remotePath+": No such file or directory\n")
		return 1
	}
	fmt.Fprintf(w, "C%04o %d %s\n", node.Mode&0o7777, len(node.Data), path.Base(remotePath))
	if _, err := readAck(r); err != nil {
		return 1
	}
	if _, err := w.Write(node.Data); err != nil {
		return 1
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return 1
	}
	if _, err := readAck(r); err != nil {
		return 1
	}
	return 0
}

// serveSudo implements the marker handshake: prompt on stderr, password on
// stdin, then the ready marker on stdout.
func (s *Server) serveSudo(r *bufio.Reader, out, stderr io.Writer) int {
	if _, err := io.WriteString(stderr, "SUDOPASSWORD:"); err != nil {
		return 1
	}
	password, err := r.ReadString('\n')
	if err != nil {
		return 1
	}
	if strings.TrimRight(password, "\r\n") != s.sudoPassword {
		_, _ = io.WriteString(stderr, "Sorry, try again.\n")
		return 1
	}
	if _, err := io.WriteString(out, "SFTPREADY"); err != nil {
		return 1
	}
	// A real sftp-server would speak the subsystem protocol from here. The
	// fixture holds the channel open until the client closes it.
	_, _ = io.Copy(io.Discard, r)
	return 0
}

// Control is one parsed SCP control line.
type controlLine struct {
	kind string
	mode uint32
	size int
	name string
}

func parseControl(line string) (controlLine, error) {
	line = strings.TrimSuffix(line, "\n")
	if len(line) < 2 || (line[0] != 'C' && line[0] != 'D') {
		return controlLine{}, fmt.Errorf("bad control line")
	}
	space := strings.IndexByte(line, ' ')
	if space < 2 {
		return controlLine{}, fmt.Errorf("bad control line")
	}
	mode, err := strconv.ParseUint(line[1:space], 8, 32)
	if err != nil {
		return controlLine{}, err
	}
	rest := line[space+1:]
	second := strings.IndexByte(rest, ' ')
	if second < 1 {
		return controlLine{}, fmt.Errorf("bad control line")
	}
	size, err := strconv.Atoi(rest[:second])
	if err != nil || size < 0 {
		return controlLine{}, fmt.Errorf("bad size")
	}
	name := rest[second+1:]
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return controlLine{}, fmt.Errorf("bad name")
	}
	kind := "file"
	if line[0] == 'D' {
		kind = "directory"
	}
	return controlLine{kind: kind, mode: uint32(mode & 0o7777), size: size, name: name}, nil
}

func readAck(r *bufio.Reader) (byte, error) {
	code, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, fmt.Errorf("peer refused with status %d", code)
	}
	return code, nil
}

func lsMode(node *Node) string {
	text := "-rw-r--r--"
	switch node.Type {
	case Directory:
		text = "drwxr-xr-x"
	case Symlink:
		text = "lrwxrwxrwx"
	}
	bits := []byte(text)
	for i, mask := range []uint32{0o400, 0o200, 0o100, 0o040, 0o020, 0o010, 0o004, 0o002, 0o001} {
		if node.Mode&mask == 0 {
			bits[i+1] = '-'
		}
	}
	return string(bits)
}

func groupOr(node *Node) string {
	if node.Group != "" {
		return node.Group
	}
	return "staff"
}

// shellTokens splits the single-quoted shell command subset the backend
// emits. A quote sequence '\” is decoded back to one quote.
func shellTokens(command string) []string {
	var tokens []string
	var current strings.Builder
	inToken := false
	flush := func() {
		if inToken {
			tokens = append(tokens, current.String())
			current.Reset()
			inToken = false
		}
	}
	for i := 0; i < len(command); i++ {
		switch command[i] {
		case '\'':
			inToken = true
			for i++; i < len(command); i++ {
				if command[i] != '\'' {
					current.WriteByte(command[i])
					continue
				}
				// '\” inside a quoted run is an escaped quote.
				if i+3 < len(command) && command[i+1] == '\\' &&
					command[i+2] == '\'' && command[i+3] == '\'' {
					current.WriteByte('\'')
					i += 3
					continue
				}
				break
			}
		case ' ', '\t', ';':
			flush()
		default:
			inToken = true
			current.WriteByte(command[i])
		}
	}
	flush()
	return tokens
}

func firstQuotedAfter(command, prefix string) (string, bool) {
	index := strings.Index(command, prefix)
	if index < 0 {
		return "", false
	}
	tokens := shellTokens(command[index+len(prefix):])
	if len(tokens) == 0 {
		return "", false
	}
	return tokens[0], true
}

func lastQuoted(command string) (string, bool) {
	tokens := shellTokens(command)
	if len(tokens) == 0 {
		return "", false
	}
	return tokens[len(tokens)-1], true
}

func errorHandler(message string) streamHandler {
	return func(_ *bufio.Reader, out, _ io.Writer) int {
		_, _ = io.WriteString(out, "\x01"+message+"\n")
		return 1
	}
}

// Mode returns an entry's permission bits.
func (s *Server) Mode(remotePath string) (uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[path.Clean(remotePath)]
	if !ok {
		return 0, false
	}
	return node.Mode, true
}
