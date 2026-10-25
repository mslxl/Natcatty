package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/scptest/scpfixture"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// protocolWait bounds a protocol completion that a real OpenSSH peer performs
// immediately. It is a hard deadline for a wedged channel, never a delay: a
// passing test returns as soon as the operation completes.
const protocolWait = 10 * time.Second

// closeWithin runs one protocol Close and fails the test if it has not
// returned by the deadline. The upload writer is unblocked by the test's
// context cleanup, so a failure does not leak its goroutine into later tests.
func closeWithin(t *testing.T, what string, closeFn func() error) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- closeFn() }()
	select {
	case err := <-result:
		return err
	case <-time.After(protocolWait):
		t.Fatalf("%s did not return within %s: the scp -t sink is still waiting for end-of-transfer",
			what, protocolWait)
		return nil
	}
}

// waitFor polls one observable condition with a hard deadline. The interval is
// only a cadence; success returns on the first satisfied check.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(protocolWait)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", protocolWait, what)
}

// newSCPFixture builds an SCP-only remote with a home directory and a few
// entries, and returns the adapter plus a release counter.
func newSCPFixture(t *testing.T) (*SCP, *scpfixture.Server, *int) {
	t.Helper()
	server := scpfixture.New()
	server.WriteFile("/home/tester/notes.txt", "hello scp", 0o640)
	server.WriteFile("/home/tester/big.bin", strings.Repeat("x", 4096), 0o600)
	server.MkdirAll("/home/tester/docs")
	server.Symlink("/home/tester/docs-link", "docs")
	releases := 0
	files, err := NewSCP(server, func(error) error {
		releases++
		return nil
	}, WithSCPBacking("host-1"))
	if err != nil {
		t.Fatalf("NewSCP: %v", err)
	}
	t.Cleanup(func() { _ = files.Close() })
	return files, server, &releases
}

func TestSCPProbeAndBrowseAgainstAnSCPOnlyServer(t *testing.T) {
	files, server, _ := newSCPFixture(t)
	ctx := context.Background()

	if err := ProbeSCP(ctx, server); err != nil {
		t.Fatalf("ProbeSCP: %v", err)
	}
	root, err := files.Root(ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if root != "/home/tester" {
		t.Fatalf("Root = %q", root)
	}
	entries, err := files.List(ctx, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	want := []string{"big.bin", "docs", "docs-link", "notes.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v", names)
	}
	byName := map[string]domainsftp.Entry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	if byName["notes.txt"].Kind != domainsftp.KindFile || byName["notes.txt"].Size != int64(len("hello scp")) {
		t.Fatalf("notes.txt = %+v", byName["notes.txt"])
	}
	if byName["notes.txt"].Mode&0o777 != 0o640 {
		t.Fatalf("notes.txt mode = %04o", byName["notes.txt"].Mode)
	}
	if byName["docs"].Kind != domainsftp.KindDir {
		t.Fatalf("docs = %+v", byName["docs"])
	}
	link := byName["docs-link"]
	if !link.Symlink || link.Kind != domainsftp.KindDir {
		t.Fatalf("docs-link = %+v", link)
	}
	usedStructured := false
	for _, command := range server.Commands() {
		if strings.Contains(command, "for f in *") {
			usedStructured = true
		}
		if strings.HasPrefix(command, "LC_ALL=C ls -la") {
			t.Fatal("the structured listing fell back to ls on a POSIX server")
		}
	}
	if !usedStructured {
		t.Fatal("the structured listing command never ran")
	}

	stat, err := files.Stat(ctx, "/home/tester/notes.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Kind != domainsftp.KindFile || stat.Size != int64(len("hello scp")) || stat.Name != "notes.txt" {
		t.Fatalf("Stat = %+v", stat)
	}
	if _, err := files.Stat(ctx, "/home/tester/missing"); apptransfer.KindOf(err) != apptransfer.KindNotFound {
		t.Fatalf("missing Stat error = %v (kind %s)", err, apptransfer.KindOf(err))
	}
}

func TestSCPTransfersFilesBothWays(t *testing.T) {
	files, server, _ := newSCPFixture(t)
	ctx := context.Background()

	reader, err := files.Open(ctx, "/home/tester/notes.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	downloaded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	_ = reader.Close()
	if string(downloaded) != "hello scp" {
		t.Fatalf("downloaded = %q", downloaded)
	}

	sized, err := files.CreateSized(ctx, "/home/tester/upload.bin", 5)
	if err != nil {
		t.Fatalf("CreateSized: %v", err)
	}
	if _, err := io.WriteString(sized, "12345"); err != nil {
		t.Fatalf("write sized: %v", err)
	}
	if err := sized.Close(); err != nil {
		t.Fatalf("close sized: %v", err)
	}
	if got, ok := server.ReadFile("/home/tester/upload.bin"); !ok || got != "12345" {
		t.Fatalf("uploaded = %q, exists = %v", got, ok)
	}

	buffered, err := files.Create(ctx, "/home/tester/buffered.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := io.WriteString(buffered, "buffered body"); err != nil {
		t.Fatalf("write buffered: %v", err)
	}
	if err := buffered.Close(); err != nil {
		t.Fatalf("close buffered: %v", err)
	}
	if got, ok := server.ReadFile("/home/tester/buffered.txt"); !ok || got != "buffered body" {
		t.Fatalf("buffered upload = %q, exists = %v", got, ok)
	}

	// A declared size that does not match what was written fails closed and
	// leaves no destination behind.
	short, err := files.CreateSized(ctx, "/home/tester/short.bin", 10)
	if err != nil {
		t.Fatalf("CreateSized short: %v", err)
	}
	if _, err := io.WriteString(short, "abc"); err != nil {
		t.Fatalf("write short: %v", err)
	}
	if err := short.Close(); err == nil {
		t.Fatal("a short upload was accepted")
	}

	missing, err := files.Open(ctx, "/home/tester/absent")
	if err != nil {
		t.Fatalf("Open missing: %v", err)
	}
	_, readErr := io.ReadAll(missing)
	_ = missing.Close()
	if apptransfer.KindOf(readErr) != apptransfer.KindNotFound {
		t.Fatalf("missing download error = %v (kind %s)", readErr, apptransfer.KindOf(readErr))
	}
}

// TestSCPUploadCompletesAgainstARealSink is the regression for
// 35-SCP-UPLOAD-HANG. OpenSSH's scp -t acknowledges the file body and then
// waits for end-of-transfer before it exits, so a writer that calls ExitCode
// without first closing its side of the channel wedges with the whole body
// already staged. The fixture now models that sink; this test fails at the
// pre-rework revision and returns as soon as the protocol is ended correctly.
func TestSCPUploadCompletesAgainstARealSink(t *testing.T) {
	files, server, _ := newSCPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	payload := bytes.Repeat([]byte("x"), 1<<20)
	writer, err := files.CreateSized(ctx, "/home/tester/pushed.bin", int64(len(payload)))
	if err != nil {
		t.Fatalf("CreateSized: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := closeWithin(t, "upload Close", writer.Close); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertRemoteBytes(t, server, "/home/tester/pushed.bin", payload)
}

// TestSCPUploadCompletesWhenCloseReportsAPeerClosedChannel is the regression
// for 35-SCP-UPLOAD-CLOSE-EOF. A real scp -t sink exits after end-of-transfer
// and the SSH server closes the channel; x/crypto's Session.Close then answers
// the peer close and reports io.EOF. The upload has already succeeded, so Close
// must report success instead of failing the transfer.
func TestSCPUploadCompletesWhenCloseReportsAPeerClosedChannel(t *testing.T) {
	payload := []byte("peer-closed upload")
	stream := newPeerCloseStream()
	stream.closeErr = io.EOF
	writer := newPeerCloseUpload(t, stream, "/home/tester/peer-closed.bin", payload)

	if err := writer.Close(); err != nil {
		t.Fatalf("Close after a successful scp -t transaction = %v, want success", err)
	}
	want := append(append([]byte(nil), payload...), 0)
	if !bytes.HasSuffix(stream.body.Bytes(), want) {
		t.Fatalf("upload bytes = %q, want the payload followed by the body terminator", stream.body.Bytes())
	}
}

// TestSCPUploadRejectsAPeerCloseUnlessTheSinkSucceeded proves the peer-close
// EOF is never a blanket success: a transport failure on Close, a non-zero
// sink status, and an unavailable status all still fail the upload.
func TestSCPUploadRejectsAPeerCloseUnlessTheSinkSucceeded(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*peerCloseStream)
	}{
		{"a genuine transport failure", func(stream *peerCloseStream) {
			stream.closeErr = errors.New("connection reset")
		}},
		{"a non-zero sink status", func(stream *peerCloseStream) {
			stream.closeErr = io.EOF
			stream.code = 1
		}},
		{"an unavailable sink status", func(stream *peerCloseStream) {
			stream.closeErr = io.EOF
			stream.codeErr = errors.New("wait: connection lost")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stream := newPeerCloseStream()
			testCase.setup(stream)
			writer := newPeerCloseUpload(t, stream, "/home/tester/torn.bin", []byte("body"))
			if err := writer.Close(); err == nil {
				t.Fatal("a failed teardown was reported as a successful upload")
			}
		})
	}
}

// peerCloseStream answers the SCP sink handshake from ready bytes and records
// what the uploader writes. Close reports the teardown error a real exec
// channel produces: io.EOF once the peer closed the channel, or a transport
// failure when the protocol did not complete.
type peerCloseStream struct {
	acks     *bytes.Reader
	body     bytes.Buffer
	code     int
	codeErr  error
	closeErr error
}

func newPeerCloseStream() *peerCloseStream {
	// The sink sends ready, then acknowledges the file control record, then
	// acknowledges the body terminator.
	return &peerCloseStream{acks: bytes.NewReader([]byte{0, 0, 0})}
}

var _ appconnection.ExecStream = (*peerCloseStream)(nil)
var _ appconnection.HalfCloser = (*peerCloseStream)(nil)

func (s *peerCloseStream) Read(p []byte) (int, error)  { return s.acks.Read(p) }
func (s *peerCloseStream) Write(p []byte) (int, error) { return s.body.Write(p) }
func (s *peerCloseStream) Stderr() io.Reader           { return strings.NewReader("") }
func (s *peerCloseStream) CloseWrite() error           { return nil }
func (s *peerCloseStream) ExitCode() (int, error)      { return s.code, s.codeErr }
func (s *peerCloseStream) Close() error                { return s.closeErr }

// newPeerCloseUpload starts one sized upload over stream and writes payload.
func newPeerCloseUpload(t *testing.T, stream *peerCloseStream, remotePath string, payload []byte) io.WriteCloser {
	t.Helper()
	files, err := NewSCP(&singleStreamExecutor{stream: stream}, nil)
	if err != nil {
		t.Fatalf("NewSCP: %v", err)
	}
	t.Cleanup(func() { _ = files.Close() })
	writer, err := files.CreateSized(context.Background(), remotePath, int64(len(payload)))
	if err != nil {
		t.Fatalf("CreateSized: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	return writer
}

// TestSCPDownloadReportsASourceThatFailsAfterTheFile proves the download path
// checks the source's exit after the final acknowledgement. The body arrives
// completely, so only the exit status can reveal that the transaction failed.
func TestSCPDownloadReportsASourceThatFailsAfterTheFile(t *testing.T) {
	server := scpfixture.New(scpfixture.WithSourceExitCode(1))
	server.WriteFile("/home/tester/notes.txt", "hello scp", 0o640)
	files, err := NewSCP(server, nil)
	if err != nil {
		t.Fatalf("NewSCP: %v", err)
	}
	t.Cleanup(func() { _ = files.Close() })

	reader, err := files.Open(context.Background(), "/home/tester/notes.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if string(data) != "hello scp" {
		t.Fatalf("downloaded %q; the body should arrive before the exit is known", data)
	}
	if readErr == nil {
		t.Fatal("a source that exited 1 after the final ack was treated as a complete download")
	}
	if !strings.Contains(readErr.Error(), "exited 1") {
		t.Fatalf("download error = %v, want a remote exit failure", readErr)
	}
}

// TestSCPCopyEngineUploadsDownloadsAndCancelsCleanly runs the real copy engine
// over the SCP adapter against the OpenSSH-shaped sink. It covers staging,
// verification and promotion for an upload, the symmetric download, and a
// cancellation mid-file that must remove the staging file. The fixture's
// stream reports the peer-close io.EOF on teardown, so the upload subtest is
// also the promotion proof for 35-SCP-UPLOAD-CLOSE-EOF: the failure that
// deleted staging and withheld the destination must not come back.
func TestSCPCopyEngineUploadsDownloadsAndCancelsCleanly(t *testing.T) {
	ctx := context.Background()
	files, server, _ := newSCPFixture(t)
	local, err := NewLocal().OpenLocal(ctx)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	service := apptransfer.NewService(nil, nil)
	payload := bytes.Repeat([]byte("netcatty-scp"), 8192)

	t.Run("upload promotes the staged file", func(t *testing.T) {
		sourceDir := t.TempDir()
		sourcePath := filepath.Join(sourceDir, "report.bin")
		if err := os.WriteFile(sourcePath, payload, 0o640); err != nil {
			t.Fatalf("write source: %v", err)
		}
		result, err := service.Copy(ctx, apptransfer.CopyRequest{
			Source: local, Dest: files, Sources: []string{sourcePath},
			DestDir: "/home/tester", Kind: domainsftp.OpUpload,
			Conflict: apptransfer.ConflictReplace,
		})
		if err != nil {
			t.Fatalf("Copy upload: %v", err)
		}
		assertRemoteBytes(t, server, "/home/tester/report.bin", payload)
		if len(result.Written) != 1 || result.Bytes != int64(len(payload)) {
			t.Fatalf("result = %+v", result)
		}
		assertNoStagingFiles(t, server, "/home/tester")
	})

	t.Run("download copies the real body", func(t *testing.T) {
		downloadDir := t.TempDir()
		result, err := service.Copy(ctx, apptransfer.CopyRequest{
			Source: files, Dest: local, Sources: []string{"/home/tester/notes.txt"},
			DestDir: downloadDir, Kind: domainsftp.OpDownload,
			Conflict: apptransfer.ConflictReplace,
		})
		if err != nil {
			t.Fatalf("Copy download: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(downloadDir, "notes.txt"))
		if err != nil || string(got) != "hello scp" {
			t.Fatalf("downloaded %q (%v)", got, err)
		}
		if len(result.Written) != 1 {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("cancel removes the staging file", func(t *testing.T) {
		sourceDir := t.TempDir()
		sourcePath := filepath.Join(sourceDir, "cancel.bin")
		if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		stalled := &stalledSource{FileSystem: local, path: sourcePath}
		done := make(chan error, 1)
		go func() {
			_, copyErr := service.Copy(cancelCtx, apptransfer.CopyRequest{
				Source: stalled, Dest: files, Sources: []string{sourcePath},
				DestDir: "/home/tester", Kind: domainsftp.OpUpload,
				Conflict: apptransfer.ConflictReplace, StageKey: "selftest-cancel",
			})
			done <- copyErr
		}()

		// The sink creates the staging file when it accepts the C record, so
		// its appearance proves the upload channel is live before cancelling.
		waitFor(t, "the SCP sink to create a staging file", func() bool {
			for _, name := range server.Names("/home/tester") {
				if apptransfer.IsStagingName(name) {
					return true
				}
			}
			return false
		})
		cancel()
		select {
		case copyErr := <-done:
			if apptransfer.KindOf(copyErr) != apptransfer.KindCancelled {
				t.Fatalf("cancelled copy error = %v (kind %q)", copyErr, apptransfer.KindOf(copyErr))
			}
		case <-time.After(protocolWait):
			t.Fatalf("the cancelled copy did not return within %s", protocolWait)
		}
		if server.Has("/home/tester/cancel.bin") {
			t.Fatal("a cancelled upload promoted its destination")
		}
		assertNoStagingFiles(t, server, "/home/tester")
	})
}

// assertNoStagingFiles fails when a destination directory still holds one of
// the copy engine's temporary names.
func assertNoStagingFiles(t *testing.T, server *scpfixture.Server, dir string) {
	t.Helper()
	for _, name := range server.Names(dir) {
		if apptransfer.IsStagingName(name) {
			t.Fatalf("staging file survived: %s", name)
		}
	}
}

// assertRemoteBytes compares one remote fixture file with the expected body.
func assertRemoteBytes(t *testing.T, server *scpfixture.Server, path string, want []byte) {
	t.Helper()
	got, ok := server.ReadFile(path)
	if !ok || !bytes.Equal([]byte(got), want) {
		t.Fatalf("%s exists=%v with %d bytes, want %d", path, ok, len(got), len(want))
	}
}

// singleStreamExecutor serves one fixed exec stream for a test that exercises
// the stream's own capabilities, and deliberately answers shell commands with
// an empty success so upload setup can run.
type singleStreamExecutor struct {
	stream appconnection.ExecStream
}

func (e *singleStreamExecutor) Exec(context.Context, string) (appconnection.ExecResult, error) {
	return appconnection.ExecResult{}, nil
}

func (e *singleStreamExecutor) OpenExec(context.Context, string) (appconnection.ExecStream, error) {
	return e.stream, nil
}

// plainStream implements ExecStream but deliberately not HalfCloser.
type plainStream struct{}

var _ appconnection.ExecStream = (*plainStream)(nil)

func (*plainStream) Read([]byte) (int, error)  { return 0, io.EOF }
func (*plainStream) Write([]byte) (int, error) { return 0, io.EOF }
func (*plainStream) Close() error              { return nil }
func (*plainStream) Stderr() io.Reader         { return nil }
func (*plainStream) ExitCode() (int, error)    { return 0, nil }

// TestSCPUploadRefusesATransportWithoutHalfClose proves an upload fails before
// writing when the channel cannot signal end-of-transfer while the sink's
// status stays readable.
func TestSCPUploadRefusesATransportWithoutHalfClose(t *testing.T) {
	files, err := NewSCP(&singleStreamExecutor{stream: &plainStream{}}, nil)
	if err != nil {
		t.Fatalf("NewSCP: %v", err)
	}
	_, err = files.CreateSized(context.Background(), "/home/tester/x.bin", 1)
	if !errors.Is(err, errSCPHalfClose) {
		t.Fatalf("CreateSized error = %v, want errSCPHalfClose", err)
	}
}

// stalledSource is a local filesystem whose reader delivers one small chunk
// and then blocks until the copy is cancelled. It makes a mid-file
// cancellation deterministic without timers.
type stalledSource struct {
	apptransfer.FileSystem
	path string
}

func (s *stalledSource) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	reader, err := s.FileSystem.Open(ctx, p)
	if err != nil || p != s.path {
		return reader, err
	}
	return &stalledReader{inner: reader, ctx: ctx}, nil
}

type stalledReader struct {
	inner   io.ReadCloser
	ctx     context.Context
	started bool
}

func (r *stalledReader) Read(p []byte) (int, error) {
	if r.started {
		<-r.ctx.Done()
		return 0, r.ctx.Err()
	}
	r.started = true
	if len(p) > 1024 {
		p = p[:1024]
	}
	return r.inner.Read(p)
}

func (r *stalledReader) Close() error { return r.inner.Close() }

func TestSCPManagesDirectoriesAndModes(t *testing.T) {
	files, server, _ := newSCPFixture(t)
	ctx := context.Background()

	if err := files.Mkdir(ctx, "/home/tester/new"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if !server.Has("/home/tester/new") {
		t.Fatal("Mkdir created nothing")
	}
	if err := files.MkdirAll(ctx, "/home/tester/a/b/c"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if !server.Has("/home/tester/a/b/c") {
		t.Fatal("MkdirAll created nothing")
	}
	if err := files.Rename(ctx, "/home/tester/new", "/home/tester/renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if server.Has("/home/tester/new") || !server.Has("/home/tester/renamed") {
		t.Fatal("Rename did not move the directory")
	}
	if err := files.Chmod(ctx, "/home/tester/notes.txt", 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if mode, _ := server.Mode("/home/tester/notes.txt"); mode&0o777 != 0o600 {
		t.Fatalf("mode = %04o", mode)
	}
	if err := files.Remove(ctx, "/home/tester/notes.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if server.Has("/home/tester/notes.txt") {
		t.Fatal("Remove left the file")
	}
	if err := files.RemoveAll(ctx, "/home/tester/a"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if server.Has("/home/tester/a/b/c") {
		t.Fatal("RemoveAll left the tree")
	}
}

func TestSCPProbeRefusesAServerWithoutSCP(t *testing.T) {
	server := scpfixture.New(scpfixture.WithoutSCP())
	if err := ProbeSCP(context.Background(), server); !errors.Is(err, ErrSCPMissing) {
		t.Fatalf("ProbeSCP = %v, want ErrSCPMissing", err)
	}
}

func TestSCPCloseReleasesOnce(t *testing.T) {
	files, _, releases := newSCPFixture(t)
	if err := files.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := files.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if *releases != 1 {
		t.Fatalf("releases = %d", *releases)
	}
}
