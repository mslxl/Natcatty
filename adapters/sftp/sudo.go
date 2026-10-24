package sftp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// Sudo handshake errors. A caller must show the reason rather than silently
// falling back to the login account: a root-owned file that cannot be edited
// is a permission problem, not a reason to change transports.
var (
	// ErrSudoDenied reports a refused password or a sudo policy denial.
	ErrSudoDenied = errors.New("sftp: sudo refused the password")
	// ErrSudoServerMissing reports a remote without sftp-server.
	ErrSudoServerMissing = errors.New("sftp: sftp-server is not installed")
	// ErrSudoTimeout reports a handshake that did not reach its ready marker.
	ErrSudoTimeout = errors.New("sftp: sudo handshake timed out")
	// ErrSudoHandshake reports a malformed or closed handshake stream.
	ErrSudoHandshake = errors.New("sftp: sudo handshake failed")
)

// SudoHandshakeTimeout bounds the password prompt and the ready marker, so a
// sudo that waits for a terminal or a password on a closed stderr cannot hang
// a pane forever.
const SudoHandshakeTimeout = 20 * time.Second

const (
	sudoPasswordPrompt = "SUDOPASSWORD:"
	sudoReadyMarker    = "SFTPREADY"
)

// defaultSudoServerPath is the original's fallback when probing finds nothing.
// The sudo command still fails closed with exit 127 if the path is absent.
const defaultSudoServerPath = "/usr/lib/openssh/sftp-server"

// sudoServerPaths is the probe order from the original implementation. The
// locations cover Debian/Ubuntu, RHEL/Fedora, Alpine, and local builds.
var sudoServerPaths = []string{
	defaultSudoServerPath,
	"/usr/libexec/openssh/sftp-server",
	"/usr/lib/ssh/sftp-server",
	"/usr/libexec/sftp-server",
	"/usr/local/libexec/sftp-server",
	"/usr/local/lib/sftp-server",
}

// sudoServerProbeCommand checks the candidate paths in one round trip. The
// first executable path is printed; a remote without any exits 1.
func sudoServerProbeCommand() string {
	var steps []string
	for _, candidate := range sudoServerPaths {
		steps = append(steps, `if test -x '`+candidate+`'; then printf "%s\n" '`+candidate+`'; exit 0; fi`)
	}
	return strings.Join(steps, "; ") + "; exit 1"
}

// ProbeSudoServer finds the remote sftp-server binary that sudo can execute.
func ProbeSudoServer(ctx context.Context, exec appconnection.Executor) (string, error) {
	if exec == nil {
		return "", ErrSudoServerMissing
	}
	result, err := exec.Exec(ctx, sudoServerProbeCommand())
	if err != nil {
		return "", err
	}
	serverPath := strings.TrimSpace(strings.Split(result.Stdout, "\n")[0])
	if serverPath == "" {
		return "", ErrSudoServerMissing
	}
	for _, candidate := range sudoServerPaths {
		if serverPath == candidate {
			return serverPath, nil
		}
	}
	return "", fmt.Errorf("%w: unexpected sftp-server path %q", ErrSudoHandshake, serverPath)
}

// buildSudoSFTPCommand is the password-on-stdin handshake from the original:
// sudo prompts on stderr, reads the password from stdin, then execs the
// uploaded sftp-server after printing a marker on stdout.
func buildSudoSFTPCommand(serverPath string) (string, error) {
	if err := assertSafeRemotePath(serverPath); err != nil {
		return "", err
	}
	if strings.ContainsAny(serverPath, "'\"\\$`;&|<>*?(){}[] \t") {
		return "", scpFailure("SCP_SHELL_ERROR", "unsafe sftp-server path: "+serverPath)
	}
	return "sudo -S -p '" + sudoPasswordPrompt + "' sh -c 'printf " +
		sudoReadyMarker + "; exec " + serverPath + " -e'", nil
}

// OpenSudoSFTP starts the root-owned sftp-server through sudo and returns the
// stream that became the SFTP subsystem. It never falls back to the login
// account; every failure is typed so the caller can explain it.
func OpenSudoSFTP(ctx context.Context, exec appconnection.Executor, password string) (io.ReadWriteCloser, error) {
	if exec == nil {
		return nil, ErrSudoServerMissing
	}
	serverPath, err := ProbeSudoServer(ctx, exec)
	switch {
	case err == nil:
	case errors.Is(err, ErrSudoServerMissing):
		// The original warned and tried the standard path instead of giving
		// up, because an unusual layout can still answer the probe badly.
		serverPath = defaultSudoServerPath
	default:
		return nil, err
	}
	command, err := buildSudoSFTPCommand(serverPath)
	if err != nil {
		return nil, err
	}
	stream, err := exec.OpenExec(ctx, command)
	if err != nil {
		return nil, err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, SudoHandshakeTimeout)
	defer cancel()
	return sudoHandshake(handshakeCtx, stream, password)
}

// sudoStream keeps the unread bytes that followed the ready marker.
type sudoStream struct {
	appconnection.ExecStream
	reader *bufio.Reader
}

func (s *sudoStream) Read(p []byte) (int, error) { return s.reader.Read(p) }

// sudoHandshake waits for the password prompt on stderr, writes the password
// plus a newline to stdin, then waits for the ready marker on stdout.
func sudoHandshake(ctx context.Context, stream appconnection.ExecStream, password string) (io.ReadWriteCloser, error) {
	reader := bufio.NewReader(stream)
	prompted := make(chan struct{}, 1)
	go watchSudoPrompt(stream.Stderr(), prompted)
	ready := make(chan error, 1)
	go watchSudoReady(reader, ready)

	passwordSent := false
	select {
	case <-prompted:
		if _, err := io.WriteString(stream, password+"\n"); err != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("%w: writing the password: %v", ErrSudoHandshake, err)
		}
		passwordSent = true
	case err := <-ready:
		// A passwordless sudo policy runs the command without a prompt.
		if err != nil {
			return nil, sudoFailure(stream, err)
		}
		return &sudoStream{ExecStream: stream, reader: reader}, nil
	case <-ctx.Done():
		_ = stream.Close()
		return nil, fmt.Errorf("%w: %v", ErrSudoTimeout, ctx.Err())
	}
	if !passwordSent {
		_ = stream.Close()
		return nil, ErrSudoHandshake
	}
	select {
	case err := <-ready:
		if err != nil {
			return nil, sudoFailure(stream, err)
		}
		return &sudoStream{ExecStream: stream, reader: reader}, nil
	case <-ctx.Done():
		_ = stream.Close()
		return nil, fmt.Errorf("%w: %v", ErrSudoTimeout, ctx.Err())
	}
}

// watchSudoPrompt reports the first password prompt on the stderr stream and
// keeps draining it: sudo writes its denial reason to the same pipe, and an
// unread stderr can block the remote process.
func watchSudoPrompt(stderr io.Reader, prompted chan<- struct{}) {
	if stderr == nil {
		return
	}
	var seen bytes.Buffer
	buffer := make([]byte, 256)
	notified := false
	for {
		n, err := stderr.Read(buffer)
		if n > 0 {
			seen.Write(buffer[:n])
			if !notified && bytes.Contains(seen.Bytes(), []byte(sudoPasswordPrompt)) {
				notified = true
				seen.Reset()
				select {
				case prompted <- struct{}{}:
				default:
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// watchSudoReady reads stdout until the ready marker appears. Bytes before
// the marker are consumed; bytes after it stay buffered for the SFTP client.
func watchSudoReady(reader *bufio.Reader, ready chan<- error) {
	var seen bytes.Buffer
	for {
		value, err := reader.ReadByte()
		if err != nil {
			if seen.Len() > 0 {
				ready <- fmt.Errorf("%w: remote said %q", ErrSudoHandshake, clip(seen.String()))
			} else {
				ready <- fmt.Errorf("%w: %v", ErrSudoHandshake, err)
			}
			return
		}
		seen.WriteByte(value)
		if bytes.HasSuffix(seen.Bytes(), []byte(sudoReadyMarker)) {
			ready <- nil
			return
		}
		if seen.Len() > 4096 {
			ready <- fmt.Errorf("%w: no ready marker before the audit limit", ErrSudoHandshake)
			return
		}
	}
}

// sudoFailure classifies a failed handshake by the remote exit status.
func sudoFailure(stream appconnection.ExecStream, cause error) error {
	code, _ := stream.ExitCode()
	_ = stream.Close()
	switch code {
	case 1:
		return fmt.Errorf("%w: %v", ErrSudoDenied, cause)
	case 127:
		return fmt.Errorf("%w: %v", ErrSudoServerMissing, cause)
	}
	return fmt.Errorf("%w: %v", ErrSudoHandshake, cause)
}
