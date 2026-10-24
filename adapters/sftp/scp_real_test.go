package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// TestSCPUploadAgainstRealOpenSSHSink drives the real SCP writer against a
// local OpenSSH `scp -t` process speaking the protocol over stdin/stdout. A
// real sink waits for end-of-transfer after the file-body ACK; this is the
// path that hung when scpWriter.Close called ExitCode without half-closing.
// The test needs an OpenSSH scp binary and is skipped without one.
func TestSCPUploadAgainstRealOpenSSHSink(t *testing.T) {
	binary, err := exec.LookPath("scp")
	if err != nil {
		t.Skip("no OpenSSH scp binary on PATH")
	}
	// OpenSSH rejects a local Windows path passed after "--" on some builds,
	// so keep the mapped destination free of spaces and drive it directly.
	root := t.TempDir()
	executor := &scpProcessExecutor{binary: binary, root: root}
	files, err := NewSCP(executor, nil)
	if err != nil {
		t.Fatalf("NewSCP: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	payload := bytes.Repeat([]byte("real-scp-"), 1<<16)
	writer, err := files.CreateSized(ctx, "/home/tester/real.bin", int64(len(payload)))
	if err != nil {
		t.Fatalf("start the real scp -t sink: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := closeWithin(t, "real scp upload Close", writer.Close); err != nil {
		t.Fatalf("Close against real scp -t: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "real.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("real sink wrote %d bytes (err=%v), want %d", len(got), err, len(payload))
	}
}

// scpProcessExecutor interprets the small command set the SCP adapter emits
// onto the local machine. It maps the adapter's POSIX home to a temp directory
// so a real scp -t process can receive the upload.
type scpProcessExecutor struct {
	binary string
	root   string
}

var _ appconnection.Executor = (*scpProcessExecutor)(nil)

func (e *scpProcessExecutor) localPath(remote string) string {
	clean := path.Clean(remote)
	clean = strings.TrimPrefix(clean, "/home/tester")
	return filepath.Join(e.root, filepath.FromSlash(clean))
}

func (e *scpProcessExecutor) Exec(ctx context.Context, command string) (appconnection.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.ExecResult{}, err
	}
	switch {
	case strings.HasPrefix(command, "mkdir -p -- "):
		target, ok := quotedAfter(command, "mkdir -p -- ")
		if !ok {
			return appconnection.ExecResult{}, errors.New("malformed mkdir command")
		}
		if err := os.MkdirAll(e.localPath(target), 0o755); err != nil {
			return appconnection.ExecResult{}, err
		}
		return appconnection.ExecResult{HasExitCode: true}, nil
	}
	return appconnection.ExecResult{}, errors.New("unsupported local command: " + command)
}

func (e *scpProcessExecutor) OpenExec(ctx context.Context, command string) (appconnection.ExecStream, error) {
	target, ok := quotedAfter(command, "scp -t -- ")
	if !ok {
		return nil, errors.New("malformed scp sink command: " + command)
	}
	process := exec.CommandContext(ctx, e.binary, "-t", "--", e.localPath(target))
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := process.Start(); err != nil {
		return nil, err
	}
	return &scpProcessStream{
		process: process, stdin: stdin, stdout: stdout, stderr: stderr,
		waitDone: make(chan struct{}),
	}, nil
}

// scpProcessStream is one local scp -t process as an exec channel. Wait is
// reaped exactly once; cancellation kills the process and then joins that
// wait, so a wedged sink cannot leak a process or call Wait twice.
type scpProcessStream struct {
	process *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  io.Reader

	closeOnce sync.Once
	waitOnce  sync.Once
	waitDone  chan struct{}
	waitErr   error
	exitCode  int
}

var _ appconnection.ExecStream = (*scpProcessStream)(nil)
var _ appconnection.HalfCloser = (*scpProcessStream)(nil)

func (s *scpProcessStream) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *scpProcessStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s *scpProcessStream) Stderr() io.Reader           { return s.stderr }
func (s *scpProcessStream) CloseWrite() error           { return s.stdin.Close() }

func (s *scpProcessStream) ExitCode() (int, error) { return s.wait() }

// wait reaps the process once and returns its status.
func (s *scpProcessStream) wait() (int, error) {
	s.waitOnce.Do(func() {
		err := s.process.Wait()
		var exit *exec.ExitError
		switch {
		case err == nil:
			// Exit status 0.
		case errors.As(err, &exit):
			s.exitCode = exit.ExitCode()
		default:
			s.waitErr = err
		}
		close(s.waitDone)
	})
	<-s.waitDone
	return s.exitCode, s.waitErr
}

func (s *scpProcessStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		select {
		case <-s.waitDone:
			// The command already exited; nothing to kill.
		default:
			_ = s.process.Process.Kill()
		}
		_, _ = s.wait()
	})
	return nil
}

// quotedAfter extracts the first single-quoted argument after prefix. The
// adapter quotes every path, so the test does not need a full shell parser.
func quotedAfter(command, prefix string) (string, bool) {
	index := strings.Index(command, prefix)
	if index < 0 {
		return "", false
	}
	rest := command[index+len(prefix):]
	if !strings.HasPrefix(rest, "'") {
		return "", false
	}
	end := strings.Index(rest[1:], "'")
	if end < 0 {
		return "", false
	}
	return rest[1 : 1+end], true
}
