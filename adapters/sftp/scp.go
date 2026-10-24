package sftp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"

	appconnection "github.com/mslxl/natcatty/application/connection"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// SCP is one remote pane's SCP-mode filesystem. Browsing and management run
// shell commands over the SSH exec capability; file transfers speak the
// OpenSSH scp -t/-f protocol on their own exec channels.
type SCP struct {
	exec    appconnection.Executor
	release func(error) error
	backing string

	mu     sync.Mutex
	closed bool
}

// SCPOption customizes an SCP filesystem.
type SCPOption func(*SCP)

// WithSCPBacking names the Host connection behind the filesystem.
func WithSCPBacking(backing string) SCPOption {
	return func(s *SCP) {
		if backing != "" {
			s.backing = backing
		}
	}
}

// NewSCP wraps one authenticated exec capability. release is called exactly
// once from Close; the app uses it to release or discard the Connection
// lease.
func NewSCP(exec appconnection.Executor, release func(error) error, options ...SCPOption) (*SCP, error) {
	if exec == nil {
		return nil, scpFailure("SCP_SHELL_ERROR", "SCP mode requires an SSH exec channel")
	}
	files := &SCP{exec: exec, release: release}
	for _, option := range options {
		option(files)
	}
	return files, nil
}

var _ apptransfer.FileSystem = (*SCP)(nil)

// ErrSCPMissing reports a remote that answered commands but has no scp
// binary, so SCP mode cannot work even though the shell does.
var ErrSCPMissing = errors.New("sftp: scp is not installed on the remote host")

// scpProbeCommand verifies a working shell and the scp binary in one round
// trip. A missing shell leaves stdout empty; a missing scp exits 127.
const scpProbeCommand = `printf "%s
" "$HOME"; if command -v scp >/dev/null 2>&1 || which scp >/dev/null 2>&1; then echo SCP_OK; else echo SCP_MISSING; exit 127; fi`

// ProbeSCP checks that the remote can run the shell commands and the scp
// binary SCP mode needs. The caller bounds it with its own timeout.
func ProbeSCP(ctx context.Context, exec appconnection.Executor) error {
	if exec == nil {
		return ErrSCPMissing
	}
	result, err := exec.Exec(ctx, scpProbeCommand)
	if err != nil {
		return err
	}
	if !strings.Contains(result.Stdout, "SCP_OK") {
		return ErrSCPMissing
	}
	return nil
}

// Root returns the remote home directory.
func (s *SCP) Root(ctx context.Context) (string, error) {
	result, err := s.exec.Exec(ctx, buildHomeCommand())
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(result.Stdout)
	if home == "" || !result.HasExitCode || result.ExitCode != 0 {
		return "", scpFailure("SCP_SHELL_ERROR", "could not determine the remote home directory")
	}
	return home, nil
}

// List reads one remote directory. The structured shell loop runs first; a
// remote whose shell refuses it falls back to parsing ls -la.
func (s *SCP) List(ctx context.Context, dir string) ([]domainsftp.Entry, error) {
	if dir == "" {
		dir = "."
	}
	command, err := buildListCommand(dir)
	if err != nil {
		return nil, err
	}
	records := []scpRecord(nil)
	result, commandErr := s.exec.Exec(ctx, command)
	if commandErr == nil && result.HasExitCode && result.ExitCode == 0 {
		records = parseListRecords(result.Stdout)
		if len(records) == 0 && strings.TrimSpace(result.Stdout) != "" {
			// Non-empty output that parsed to nothing is not an empty
			// directory; fall through to ls.
			records = nil
		}
	}
	if records == nil {
		fallback, buildErr := buildListCommandLs(dir)
		if buildErr != nil {
			return nil, buildErr
		}
		result, err = s.exec.Exec(ctx, fallback)
		if err != nil {
			return nil, err
		}
		if result.HasExitCode && result.ExitCode != 0 {
			return nil, &scpError{Code: "SCP_SHELL_ERROR", Message: strings.TrimSpace(result.Stderr),
				ExitCode: result.ExitCode, Stderr: strings.TrimSpace(result.Stderr)}
		}
		records = parseLsLaOutput(result.Stdout)
	}
	entries := make([]domainsftp.Entry, 0, len(records))
	for _, record := range records {
		entry := entryFromRecord(path.Join(dir, record.Name), record)
		if record.Type == "symlink" {
			entry.Symlink = true
			entry.Kind = s.symlinkKind(ctx, entry.Path)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// symlinkKind resolves a link target so a directory link stays navigable.
func (s *SCP) symlinkKind(ctx context.Context, remotePath string) domainsftp.Kind {
	command, err := buildSymlinkTargetCommand(remotePath)
	if err != nil {
		return domainsftp.KindLink
	}
	result, err := s.exec.Exec(ctx, command)
	if err != nil {
		return domainsftp.KindLink
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	switch strings.TrimSpace(lines[len(lines)-1]) {
	case "directory":
		return domainsftp.KindDir
	case "file":
		return domainsftp.KindFile
	default:
		return domainsftp.KindLink
	}
}

// Stat reads one remote entry, following symlinks.
func (s *SCP) Stat(ctx context.Context, remotePath string) (domainsftp.Entry, error) {
	command, err := buildStatCommand(remotePath)
	if err != nil {
		return domainsftp.Entry{}, err
	}
	result, err := s.exec.Exec(ctx, command)
	if err != nil {
		return domainsftp.Entry{}, err
	}
	record, err := parseStatRecord(result.Stdout, result.Stderr, result.ExitCode, result.HasExitCode)
	if err != nil {
		return domainsftp.Entry{}, err
	}
	entry := entryFromRecord(remotePath, record)
	entry.Name = path.Base(remotePath)
	if record.Type == "symlink" {
		entry.Symlink = true
		entry.Kind = s.symlinkKind(ctx, remotePath)
	}
	return entry, nil
}

// entryFromRecord converts one parsed shell record into a listing row.
func entryFromRecord(full string, record scpRecord) domainsftp.Entry {
	kind := domainsftp.KindFile
	switch record.Type {
	case "directory":
		kind = domainsftp.KindDir
	case "symlink":
		kind = domainsftp.KindFile
	}
	return domainsftp.Entry{
		Name:    path.Base(full),
		Path:    full,
		Kind:    kind,
		Size:    record.Size,
		Mode:    record.Mode,
		ModTime: record.ModTime,
		Owner:   record.Owner,
		Hidden:  domainsftp.HiddenName(path.Base(full)),
	}
}

// Mkdir creates one directory. An existing directory is an error so the
// caller can report it.
func (s *SCP) Mkdir(ctx context.Context, remotePath string) error {
	return s.run(ctx, domainsftp.OpMkdir, remotePath, func() (string, error) {
		return buildMkdirCommand(remotePath, false)
	})
}

// MkdirAll creates a directory and any missing ancestors.
func (s *SCP) MkdirAll(ctx context.Context, remotePath string) error {
	return s.run(ctx, domainsftp.OpMkdir, remotePath, func() (string, error) {
		return buildMkdirCommand(remotePath, true)
	})
}

// Open starts reading a remote file through scp -f.
func (s *SCP) Open(ctx context.Context, remotePath string) (io.ReadCloser, error) {
	command, err := buildScpSourceCommand(remotePath)
	if err != nil {
		return nil, scpOperationError(domainsftp.OpDownload, remotePath, err)
	}
	stream, err := s.exec.OpenExec(ctx, command)
	if err != nil {
		return nil, scpOperationError(domainsftp.OpDownload, remotePath, err)
	}
	pipeReader, pipeWriter := io.Pipe()
	readerCtx, cancel := context.WithCancel(ctx)
	go func() {
		cause := readScpSource(readerCtx, stream, pipeWriter)
		_ = pipeWriter.CloseWithError(cause)
		_ = stream.Close()
		cancel()
	}()
	return &scpReader{pipe: pipeReader, stream: stream, cancel: cancel}, nil
}

// readScpSource consumes an scp -f stream and writes the file body into out.
func readScpSource(ctx context.Context, stream appconnection.ExecStream, out *io.PipeWriter) error {
	reader := bufio.NewReader(stream)
	if err := writeAck(stream); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return scpFailure("SCP_PROTOCOL_ERROR", "SCP source closed without a file")
			}
			return err
		}
		control, err := parseControlLine(line)
		if err != nil {
			return err
		}
		switch control.Kind {
		case "time":
			if err := writeAck(stream); err != nil {
				return err
			}
		case "end":
			if err := writeAck(stream); err != nil {
				return err
			}
		case "directory":
			return scpFailure("SCP_PROTOCOL_ERROR", "SCP source returned a directory")
		case "file":
			if err := writeAck(stream); err != nil {
				return err
			}
			if _, err := io.CopyN(out, reader, control.Size); err != nil {
				return err
			}
			trailing, err := reader.ReadByte()
			if err != nil {
				return err
			}
			if trailing != 0 {
				return scpFailure("SCP_PROTOCOL_ERROR", "expected a trailing NUL after SCP file data")
			}
			return writeAck(stream)
		}
	}
}

// Create starts writing a new remote file. The SCP protocol needs the size in
// its control line, so a caller that does not know it streams through a
// temporary local file and uploads on Close. The copy engine uses CreateSized
// to stream directly.
func (s *SCP) Create(ctx context.Context, remotePath string) (io.WriteCloser, error) {
	temporary, err := os.CreateTemp("", "natcatty-scp-upload-*")
	if err != nil {
		return nil, scpOperationError(domainsftp.OpUpload, remotePath, err)
	}
	return &scpBufferedWriter{scp: s, ctx: ctx, path: remotePath, temporary: temporary}, nil
}

// CreateSized starts a streaming upload whose size is known up front.
func (s *SCP) CreateSized(ctx context.Context, remotePath string, size int64) (io.WriteCloser, error) {
	parent := path.Dir(remotePath)
	if parent != "." && parent != "/" {
		if err := s.MkdirAll(ctx, parent); err != nil {
			return nil, err
		}
	}
	command, err := buildScpSinkCommand(parent)
	if err != nil {
		return nil, scpOperationError(domainsftp.OpUpload, remotePath, err)
	}
	stream, err := s.exec.OpenExec(ctx, command)
	if err != nil {
		return nil, scpOperationError(domainsftp.OpUpload, remotePath, err)
	}
	reader := bufio.NewReader(stream)
	if err := readAck(reader); err != nil {
		_ = stream.Close()
		return nil, scpOperationError(domainsftp.OpUpload, remotePath, err)
	}
	line, err := buildFileControlLine(0o644, size, path.Base(remotePath))
	if err != nil {
		_ = stream.Close()
		return nil, scpOperationError(domainsftp.OpUpload, remotePath, err)
	}
	if _, err := stream.Write(line); err != nil {
		_ = stream.Close()
		return nil, scpOperationError(domainsftp.OpUpload, remotePath, err)
	}
	if err := readAck(reader); err != nil {
		_ = stream.Close()
		return nil, scpOperationError(domainsftp.OpUpload, remotePath, err)
	}
	writer := &scpWriter{
		stream: stream,
		reader: reader,
		path:   remotePath,
		size:   size,
		done:   make(chan struct{}),
	}
	go writer.watch(ctx)
	return writer, nil
}

// SCP streams uploads when the copy engine can hand it the source size.
var _ apptransfer.SizedCreator = (*SCP)(nil)

// Remove removes a file or an empty directory.
func (s *SCP) Remove(ctx context.Context, remotePath string) error {
	return s.run(ctx, domainsftp.OpDelete, remotePath, func() (string, error) {
		return buildDeleteCommand(remotePath, false)
	})
}

// RemoveAll removes a file or a directory tree.
func (s *SCP) RemoveAll(ctx context.Context, remotePath string) error {
	return s.run(ctx, domainsftp.OpDelete, remotePath, func() (string, error) {
		return buildDeleteCommand(remotePath, true)
	})
}

// Rename moves one entry.
func (s *SCP) Rename(ctx context.Context, oldPath, newPath string) error {
	return s.run(ctx, domainsftp.OpRename, newPath, func() (string, error) {
		return buildRenameCommand(oldPath, newPath)
	})
}

// Chmod replaces permission bits.
func (s *SCP) Chmod(ctx context.Context, remotePath string, mode uint32) error {
	return s.run(ctx, domainsftp.OpChmod, remotePath, func() (string, error) {
		return buildChmodCommand(remotePath, mode)
	})
}

// run executes one management command and classifies a non-zero status.
func (s *SCP) run(ctx context.Context, op domainsftp.OperationKind, remotePath string,
	build func() (string, error)) error {
	command, err := build()
	if err != nil {
		return scpOperationError(op, remotePath, err)
	}
	result, err := s.exec.Exec(ctx, command)
	if err != nil {
		return scpOperationError(op, remotePath, err)
	}
	if result.HasExitCode && result.ExitCode != 0 {
		stderr := strings.TrimSpace(result.Stderr)
		if stderr == "" {
			stderr = strings.TrimSpace(result.Stdout)
		}
		return scpOperationError(op, remotePath, &scpError{
			Code:     "SCP_SHELL_ERROR",
			Message:  fmt.Sprintf("%s exited %d: %s", op, result.ExitCode, stderr),
			ExitCode: result.ExitCode,
			Stderr:   stderr,
		})
	}
	return nil
}

// Join joins remote path components with "/".
func (s *SCP) Join(parts ...string) string { return path.Join(parts...) }

// Parent returns the containing remote directory.
func (s *SCP) Parent(p string) string { return path.Dir(p) }

// Base returns the last remote component.
func (s *SCP) Base(p string) string { return path.Base(p) }

// Style reports POSIX notation.
func (s *SCP) Style() apptransfer.PathStyle { return apptransfer.StylePOSIX }

// Backing identifies the Host connection behind this filesystem.
func (s *SCP) Backing() string { return s.backing }

// Canonical cleans a remote path.
func (s *SCP) Canonical(p string) string {
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// Close releases the underlying connection lease. It is safe to call more
// than once.
func (s *SCP) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.release != nil {
		return s.release(nil)
	}
	return nil
}

// scpErrorMatches classifies one SCP failure for apptransfer.KindOf and for
// callers using errors.Is against the standard filesystem sentinels.
func (e *scpError) Is(target error) bool {
	if e == nil {
		return false
	}
	message := strings.ToLower(e.Message + " " + e.Stderr)
	switch {
	case e.Code == "ENOENT",
		strings.Contains(message, "no such file"),
		strings.Contains(message, "not found"):
		return target == fs.ErrNotExist
	case strings.Contains(message, "permission denied"),
		strings.Contains(message, "operation not permitted"):
		return target == fs.ErrPermission
	case strings.Contains(message, "file exists"),
		strings.Contains(message, "directory not empty"):
		return target == fs.ErrExist
	case e.Code == "SCP_PROTOCOL_ERROR", e.Code == "SCP_FATAL":
		return target == io.ErrUnexpectedEOF
	}
	return false
}

// scpOperationError attaches a file operation and classification to err.
func scpOperationError(op domainsftp.OperationKind, remotePath string, err error) error {
	var typed *apptransfer.Error
	if errors.As(err, &typed) {
		return err
	}
	return &apptransfer.Error{
		Kind:  apptransfer.KindOf(err),
		Op:    op,
		Path:  remotePath,
		Cause: err,
	}
}

// writeAck sends the SCP ready/ok byte.
func writeAck(w io.Writer) error {
	_, err := w.Write([]byte{0})
	return err
}

// readAck consumes one SCP status byte. An error or fatal status is reported
// with its message.
func readAck(reader *bufio.Reader) error {
	code, err := reader.ReadByte()
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	if code != 1 && code != 2 {
		return scpFailure("SCP_PROTOCOL_ERROR",
			fmt.Sprintf("unexpected SCP status byte 0x%x", code))
	}
	message, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "SCP remote error"
	}
	codeName := "SCP_PROTOCOL_ERROR"
	if code == 2 {
		codeName = "SCP_FATAL"
	}
	return &scpError{Code: codeName, Message: message}
}

// scpReader is an open download. Closing it stops the remote channel.
type scpReader struct {
	pipe   *io.PipeReader
	stream appconnection.ExecStream
	cancel context.CancelFunc
	once   sync.Once
}

func (r *scpReader) Read(p []byte) (int, error) { return r.pipe.Read(p) }

func (r *scpReader) Close() error {
	r.once.Do(func() {
		r.cancel()
		_ = r.stream.Close()
		_ = r.pipe.Close()
	})
	return nil
}

// scpWriter is a streaming upload against one scp -t channel.
type scpWriter struct {
	stream appconnection.ExecStream
	reader *bufio.Reader
	path   string
	size   int64

	mu      sync.Mutex
	written int64
	closed  bool
	done    chan struct{}
	once    sync.Once
}

// watch aborts the channel when the caller's context ends.
func (w *scpWriter) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = w.stream.Close()
	case <-w.done:
	}
}

func (w *scpWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.written+int64(len(p)) > w.size {
		return 0, scpFailure("SCP_PROTOCOL_ERROR", "SCP upload exceeded its declared size")
	}
	n, err := w.stream.Write(p)
	w.written += int64(n)
	return n, err
}

func (w *scpWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	written := w.written
	w.mu.Unlock()
	// Keep the context watcher armed through the final ACK and remote exit:
	// a wedged peer must still let cancellation close the stream.
	defer w.once.Do(func() { close(w.done) })

	if written != w.size {
		_ = w.stream.Close()
		return scpOperationError(domainsftp.OpUpload, w.path, fmt.Errorf(
			"verified SCP upload ended early: expected %d bytes, got %d", w.size, written))
	}
	// The sender terminates the file body with a NUL, then waits for the
	// sink's acknowledgement of the completed file.
	if _, err := w.stream.Write([]byte{0}); err != nil {
		_ = w.stream.Close()
		return scpOperationError(domainsftp.OpUpload, w.path, err)
	}
	if err := readAck(w.reader); err != nil {
		_ = w.stream.Close()
		return scpOperationError(domainsftp.OpUpload, w.path, err)
	}
	// Wait for the remote scp process to exit so a caller that checks the
	// destination immediately after Close observes the completed file.
	if code, err := w.stream.ExitCode(); err != nil || code != 0 {
		_ = w.stream.Close()
		if err == nil {
			err = fmt.Errorf("remote scp exited %d", code)
		}
		return scpOperationError(domainsftp.OpUpload, w.path, err)
	}
	return w.stream.Close()
}

// scpBufferedWriter buffers an upload of unknown size in a local temporary
// file and sends it through the SCP sink on Close.
type scpBufferedWriter struct {
	scp       *SCP
	ctx       context.Context
	path      string
	temporary *os.File
	once      sync.Once
}

func (w *scpBufferedWriter) Write(p []byte) (int, error) { return w.temporary.Write(p) }

func (w *scpBufferedWriter) Close() error {
	var cause error
	w.once.Do(func() {
		name := w.temporary.Name()
		defer os.Remove(name)
		if err := w.temporary.Close(); err != nil {
			cause = scpOperationError(domainsftp.OpUpload, w.path, err)
			return
		}
		source, err := os.Open(name)
		if err != nil {
			cause = scpOperationError(domainsftp.OpUpload, w.path, err)
			return
		}
		defer source.Close()
		info, err := source.Stat()
		if err != nil {
			cause = scpOperationError(domainsftp.OpUpload, w.path, err)
			return
		}
		target, err := w.scp.CreateSized(w.ctx, w.path, info.Size())
		if err != nil {
			cause = err
			return
		}
		if _, err := io.Copy(target, source); err != nil {
			_ = target.Close()
			cause = scpOperationError(domainsftp.OpUpload, w.path, err)
			return
		}
		cause = target.Close()
	})
	return cause
}
