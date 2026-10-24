package sftp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/scptest/scpfixture"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

func TestProbeSudoServerFindsTheFirstExecutablePath(t *testing.T) {
	server := scpfixture.New(scpfixture.WithSudoPassword("s3cr3t"))
	serverPath, err := ProbeSudoServer(context.Background(), server)
	if err != nil {
		t.Fatalf("ProbeSudoServer: %v", err)
	}
	if serverPath != "/usr/lib/openssh/sftp-server" {
		t.Fatalf("serverPath = %q", serverPath)
	}
	missing := scpfixture.New(scpfixture.WithSudoServerPath(""))
	if _, err := ProbeSudoServer(context.Background(), missing); !errors.Is(err, ErrSudoServerMissing) {
		t.Fatalf("no server error = %v, want ErrSudoServerMissing", err)
	}
}

func TestSudoSFTPUsesThePasswordOnStandardInputHandshake(t *testing.T) {
	server := scpfixture.New(scpfixture.WithSudoPassword("s3cr3t"))
	stream, err := OpenSudoSFTP(context.Background(), server, "s3cr3t")
	if err != nil {
		t.Fatalf("OpenSudoSFTP: %v", err)
	}
	_ = stream.Close()
	var sudoCommand string
	for _, command := range server.Commands() {
		if strings.HasPrefix(command, "sudo -S -p ") {
			sudoCommand = command
		}
	}
	want := "sudo -S -p 'SUDOPASSWORD:' sh -c 'printf SFTPREADY; exec /usr/lib/openssh/sftp-server -e'"
	if sudoCommand != want {
		t.Fatalf("sudo command = %q, want %q", sudoCommand, want)
	}
}

func TestSudoSFTPReportsADeniedPasswordWithoutFallback(t *testing.T) {
	server := scpfixture.New(scpfixture.WithSudoPassword("s3cr3t"))
	if _, err := OpenSudoSFTP(context.Background(), server, "wrong"); !errors.Is(err, ErrSudoDenied) {
		t.Fatalf("wrong password error = %v, want ErrSudoDenied", err)
	}
	for _, command := range server.Commands() {
		if strings.HasPrefix(command, "scp ") {
			t.Fatalf("a denied sudo fell back to SCP: %q", command)
		}
	}
}

func TestSudoSFTPFallsBackToTheDefaultServerAndReportsAMissingBinary(t *testing.T) {
	server := scpfixture.New()
	if _, err := ProbeSudoServer(context.Background(), server); !errors.Is(err, ErrSudoServerMissing) {
		t.Fatalf("probe error = %v, want ErrSudoServerMissing", err)
	}
	if _, err := OpenSudoSFTP(context.Background(), server, "s3cr3t"); !errors.Is(err, ErrSudoServerMissing) {
		t.Fatalf("missing server error = %v, want ErrSudoServerMissing", err)
	}
	var sudoCommand string
	for _, command := range server.Commands() {
		if strings.HasPrefix(command, "sudo -S") {
			sudoCommand = command
		}
	}
	want := "sudo -S -p 'SUDOPASSWORD:' sh -c 'printf SFTPREADY; exec /usr/lib/openssh/sftp-server -e'"
	if sudoCommand != want {
		t.Fatalf("fallback command = %q, want %q", sudoCommand, want)
	}
}

func TestSudoSFTPBoundsTheHandshake(t *testing.T) {
	// A stream that emits neither a prompt nor a marker proves the handshake
	// returns on its deadline instead of waiting forever.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := OpenSudoSFTP(ctx, &silentSudoExec{}, "s3cr3t")
	if !errors.Is(err, ErrSudoTimeout) {
		t.Fatalf("error = %v, want ErrSudoTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("handshake waited %v", elapsed)
	}
}

// silentSudoExec answers the probe and then opens a stream that stays silent.
type silentSudoExec struct{}

func (e *silentSudoExec) Exec(context.Context, string) (appconnection.ExecResult, error) {
	return appconnection.ExecResult{Stdout: "/usr/lib/openssh/sftp-server\n", HasExitCode: true}, nil
}

func (e *silentSudoExec) OpenExec(context.Context, string) (appconnection.ExecStream, error) {
	return newSilentStream(), nil
}

type silentStream struct {
	closed chan struct{}
	once   bool
}

func newSilentStream() *silentStream { return &silentStream{closed: make(chan struct{})} }

func (s *silentStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}

func (s *silentStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *silentStream) Stderr() io.Reader           { return nil }
func (s *silentStream) ExitCode() (int, error)      { return 0, nil }

func (s *silentStream) Close() error {
	if !s.once {
		s.once = true
		close(s.closed)
	}
	return nil
}
