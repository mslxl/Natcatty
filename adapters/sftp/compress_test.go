package sftp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	scpfixture "github.com/mslxl/natcatty/adapters/scptest/scpfixture"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

// fakeExtractor records the command and the stream it handed out.
type fakeExtractor struct {
	command string
	stream  appconnection.ExecStream
	openErr error
}

func (f *fakeExtractor) Exec(context.Context, string) (appconnection.ExecResult, error) {
	return appconnection.ExecResult{}, errors.New("unexpected Exec")
}

func (f *fakeExtractor) OpenExec(_ context.Context, command string) (appconnection.ExecStream, error) {
	f.command = command
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.stream, nil
}

// fakeExtractStream accepts the archive and reports a configurable status.
type fakeExtractStream struct {
	stdin    bytes.Buffer
	stderr   io.Reader
	code     int
	closed   bool
	halfDone bool
}

func (s *fakeExtractStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *fakeExtractStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s *fakeExtractStream) Stderr() io.Reader           { return s.stderr }
func (s *fakeExtractStream) ExitCode() (int, error)      { return s.code, nil }
func (s *fakeExtractStream) Close() error                { s.closed = true; return nil }
func (s *fakeExtractStream) CloseWrite() error           { s.halfDone = true; return nil }

// plainExtractStream is an exec stream without the half-close capability.
type plainExtractStream struct {
	closed  bool
	written int
}

func (s *plainExtractStream) Read([]byte) (int, error) { return 0, io.EOF }
func (s *plainExtractStream) Write(p []byte) (int, error) {
	s.written += len(p)
	return len(p), nil
}
func (s *plainExtractStream) Stderr() io.Reader      { return strings.NewReader("") }
func (s *plainExtractStream) ExitCode() (int, error) { return 0, nil }
func (s *plainExtractStream) Close() error           { s.closed = true; return nil }

// gzipArchive builds a gzip tar with the given names and bodies.
func gzipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for name, body := range files {
		if err := archive.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

func TestExtractTarStreamsOneArchiveToTheRemoteCommand(t *testing.T) {
	stream := &fakeExtractStream{stderr: strings.NewReader("")}
	executor := &fakeExtractor{stream: stream}
	archive := gzipArchive(t, map[string]string{"docs/a.txt": "hello"})

	if err := extractTar(context.Background(), executor, "/tmp/stage", bytes.NewReader(archive)); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if want := "tar -xzf - -C '/tmp/stage'"; executor.command != want {
		t.Fatalf("command = %q, want %q", executor.command, want)
	}
	if !stream.halfDone {
		t.Fatal("the archive stream was not half-closed before waiting for tar")
	}
	if !stream.closed {
		t.Fatal("the exec stream was not closed")
	}
	reader, err := gzip.NewReader(bytes.NewReader(stream.stdin.Bytes()))
	if err != nil {
		t.Fatalf("remote input is not gzip: %v", err)
	}
	entries := tar.NewReader(reader)
	header, err := entries.Next()
	if err != nil || header.Name != "docs/a.txt" {
		t.Fatalf("remote archive entry = %+v, %v", header, err)
	}
	body, _ := io.ReadAll(entries)
	if string(body) != "hello" {
		t.Fatalf("remote archive body = %q", body)
	}
}

func TestExtractTarReportsTheRemoteStatusAndDiagnostics(t *testing.T) {
	stream := &fakeExtractStream{stderr: strings.NewReader("tar: not found\n"), code: 127}
	executor := &fakeExtractor{stream: stream}

	err := extractTar(context.Background(), executor, "/tmp/stage",
		bytes.NewReader(gzipArchive(t, map[string]string{"docs/a.txt": "hello"})))
	if err == nil {
		t.Fatal("a non-zero tar status must fail")
	}
	if !strings.Contains(err.Error(), "127") || !strings.Contains(err.Error(), "tar: not found") {
		t.Fatalf("error = %q, want the status and diagnostics", err)
	}
}

func TestExtractTarRefusesAStreamWithoutHalfClose(t *testing.T) {
	stream := &plainExtractStream{}
	executor := &fakeExtractor{stream: stream}

	err := extractTar(context.Background(), executor, "/tmp/stage",
		bytes.NewReader(gzipArchive(t, map[string]string{"docs/a.txt": "hello"})))
	if !errors.Is(err, errExtractionUnsupported) {
		t.Fatalf("error = %v, want the unsupported capability", err)
	}
	if !stream.closed {
		t.Fatal("the refused stream was left open")
	}
	if stream.written != 0 {
		t.Fatalf("the refused stream received %d archive bytes before the check", stream.written)
	}
}

func TestExtractTarWithoutAnExecutorIsUnsupported(t *testing.T) {
	if err := extractTar(context.Background(), nil, "/tmp/stage", bytes.NewReader(nil)); !errors.Is(err, errExtractionUnsupported) {
		t.Fatalf("error = %v, want the unsupported capability", err)
	}
}

func TestExtractTarQuotesTheStagingDirectory(t *testing.T) {
	stream := &fakeExtractStream{stderr: strings.NewReader("")}
	executor := &fakeExtractor{stream: stream}

	if err := extractTar(context.Background(), executor, "/tmp/we'd", bytes.NewReader(gzipArchive(t, nil))); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if want := `tar -xzf - -C '/tmp/we'\''d'`; executor.command != want {
		t.Fatalf("command = %q, want %q", executor.command, want)
	}
}

// TestSCPExtractTarOverTheFixture proves the whole SCP path: the archive is
// streamed over the fixture's exec channel, half-closed so the fixture's tar
// sees end-of-input, and extracted into the fixture's tree.
func TestSCPExtractTarOverTheFixture(t *testing.T) {
	fixture := scpfixture.New()
	fixture.MkdirAll("/tmp/stage")
	files, err := NewSCP(fixture, nil, WithSCPBacking("selftest"))
	if err != nil {
		t.Fatalf("new SCP: %v", err)
	}
	defer files.Close()

	archive := gzipArchive(t, map[string]string{"docs/a.txt": "hello", "docs/sub/b.bin": "0123456789"})
	if err := files.ExtractTar(context.Background(), "/tmp/stage", bytes.NewReader(archive)); err != nil {
		t.Fatalf("extract over the SCP fixture: %v", err)
	}
	for path, want := range map[string]string{
		"/tmp/stage/docs/a.txt":     "hello",
		"/tmp/stage/docs/sub/b.bin": "0123456789",
	} {
		if got, ok := fixture.ReadFile(path); !ok || got != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, ok, want)
		}
	}
}

func TestSCPExtractTarUsesItsExecutor(t *testing.T) {
	stream := &fakeExtractStream{stderr: strings.NewReader("")}
	executor := &fakeExtractor{stream: stream}
	files, err := NewSCP(executor, nil)
	if err != nil {
		t.Fatalf("new SCP: %v", err)
	}
	if err := files.ExtractTar(context.Background(), "/tmp/stage",
		bytes.NewReader(gzipArchive(t, map[string]string{"docs/a.txt": "hello"}))); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if executor.command == "" {
		t.Fatal("SCP extraction did not open an exec command")
	}
}
