package app

// Phone Session Log tests: the platform workspace over a real temporary
// Documents directory, a cancelled export that leaves the file, and a real
// application Session whose teardown finalizes a running log.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestPhoneSessionLogServiceUsesDocumentsSessionLogs proves the folder is the
// app's Documents/Session Logs and the saved-log list names each Session.
func TestPhoneSessionLogServiceUsesDocumentsSessionLogs(t *testing.T) {
	documents := t.TempDir()
	service := &phoneSessionLogService{
		documents: func() (string, error) { return documents, nil },
	}
	options, err := service.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	wantRoot := filepath.Join(documents, "Session Logs")
	if options.Directory != wantRoot {
		t.Fatalf("log directory = %q, want %q", options.Directory, wantRoot)
	}
	if options.Format != domainterminal.LogFormatText {
		t.Fatalf("log format = %q, want text", options.Format)
	}
	sessionDir := filepath.Join(wantRoot, "prod box tab-1-pane-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "2026-11-24T10-00-00.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "2026-11-24T10-01-00.log"), []byte("raw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file the logger never writes is not a Session log.
	if err := os.WriteFile(filepath.Join(sessionDir, "notes.md"), []byte("skip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := service.Files()
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("Files listed %d logs, want 2: %+v", len(files), files)
	}
	for _, file := range files {
		if file.Session != "prod box tab-1-pane-1" {
			t.Fatalf("log %q lost its Session identity: %q", file.Name, file.Session)
		}
	}
}

// TestPhoneSessionLogExportCancelKeepsTheFile proves a cancelled system export
// reports no destination and leaves the source log in place.
func TestPhoneSessionLogExportCancelKeepsTheFile(t *testing.T) {
	documents := t.TempDir()
	logPath := filepath.Join(documents, "Session Logs", "prod", "2026-11-24T10-00-00.txt")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &phoneSessionLogService{
		documents: func() (string, error) { return documents, nil },
		saveCopy: func(path, name string) (string, error) {
			calls++
			if path != logPath || name != "2026-11-24T10-00-00.txt" {
				t.Fatalf("export asked for %q / %q", path, name)
			}
			return "", nil // the user cancelled the picker
		},
	}

	saved, err := service.Export(logPath, "")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if saved != "" {
		t.Fatalf("cancelled export returned %q, want empty", saved)
	}
	if calls != 1 {
		t.Fatalf("export picker called %d times, want 1", calls)
	}
	if data, err := os.ReadFile(logPath); err != nil || string(data) != "hello\n" {
		t.Fatalf("cancelled export changed the log: %q, %v", data, err)
	}
}

// logTestFactory hands a Session one in-process transport.
type logTestFactory struct {
	transport appsession.Transport
}

func (f logTestFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// logTestTransport is a writable input side and readable output side. Closing
// it releases the Session's read loop.
type logTestTransport struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
}

func newLogTestTransport() *logTestTransport {
	reader, writer := io.Pipe()
	return &logTestTransport{reader: reader, writer: writer}
}

func (t *logTestTransport) Read(p []byte) (int, error)  { return t.reader.Read(p) }
func (t *logTestTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *logTestTransport) Resize(int, int) error       { return nil }
func (t *logTestTransport) Close() error {
	t.once.Do(func() {
		_ = t.writer.Close()
		_ = t.reader.Close()
	})
	return nil
}

// TestPhoneSessionHandleFinalizesLogOnSessionEnd proves the real Session
// teardown flushes the log: the handle's log ends by itself when End closes the
// Session, and the file keeps everything the transport produced.
func TestPhoneSessionHandleFinalizesLogOnSessionEnd(t *testing.T) {
	transport := newLogTestTransport()
	service := appsession.NewService(logTestFactory{transport: transport})
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindLocal, Label: "prod box", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	handle := &phoneSessionHandle{session: session}
	t.Cleanup(func() { _ = session.Close() })

	root := filepath.Join(t.TempDir(), "Session Logs")
	if err := handle.StartSessionLog(phoneshell.SessionLogOptions{
		Directory: root,
		Format:    domainterminal.LogFormatRaw,
		Label:     "prod box tab-1/pane-1",
	}); err != nil {
		t.Fatalf("StartSessionLog: %v", err)
	}
	status := handle.SessionLogStatus()
	if !status.Active || status.Path == "" {
		t.Fatalf("log did not start: %+v", status)
	}
	// The shared logger sanitizes the label's path separator, keeping the
	// stable Session identity in the directory name.
	if !strings.Contains(status.Path, "tab-1_pane-1") {
		t.Fatalf("log path %q lost the Session identity", status.Path)
	}

	if _, err := transport.writer.Write([]byte("hello\r\n")); err != nil {
		t.Fatalf("write terminal output: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, readErr := os.ReadFile(status.Path)
		if readErr == nil && strings.Contains(string(data), "hello") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never received terminal output; last read: %q, %v", data, readErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := handle.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	// Close records the request synchronously and the teardown finishes on the
	// Session goroutine; Done reports when the logger has been finalized.
	select {
	case <-handle.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the Session did not finish ending")
	}
	final := handle.SessionLogStatus()
	if final.Active {
		t.Fatal("the log still reports active after the Session ended")
	}
	if final.Problem != "" {
		t.Fatalf("finalizing the log reported a problem: %q", final.Problem)
	}
	data, err := os.ReadFile(final.Path)
	if err != nil {
		t.Fatalf("read finalized log: %v", err)
	}
	if string(data) != "hello\r\n" {
		t.Fatalf("finalized log = %q, want the terminal output", data)
	}
}

// TestPhoneSessionRuntimeFlushesLogOnShutdown proves app shutdown flushes an
// active log before the process can exit, even though Session teardown is
// asynchronous. It runs over the production phone runtime and an in-process SSH
// endpoint for both a saved Host and an Unsaved Host opened through Quick
// Connect: both must be tracked for the shutdown flush.
func TestPhoneSessionRuntimeFlushesLogOnShutdown(t *testing.T) {
	signer := fixtureSigner(t)
	// The saved Host is both the saved-Host case's target and the Quick Connect
	// case's trust anchor: its recorded host key lets the Unsaved Host's dial be
	// accepted without a prompt.
	saved := domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "127.0.0.1", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	}
	unsaved := domainvault.Host{
		Label: "quick box", Hostname: "127.0.0.1", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	}
	cases := []struct {
		name string
		open func(t *testing.T, env *phoneTransportEnv) *phoneSessionHandle
	}{
		{
			name: "saved Host",
			open: func(t *testing.T, env *phoneTransportEnv) *phoneSessionHandle {
				env.addHost(t, saved, signer)
				return env.openHost(t, saved)
			},
		},
		{
			name: "Quick Connect",
			open: func(t *testing.T, env *phoneTransportEnv) *phoneSessionHandle {
				// The saved Host is only a trust anchor; the opened target is
				// the Unsaved Host with its one-shot credential.
				env.addHost(t, saved, signer)
				handle, err := env.runtime.OpenTarget(
					context.Background(), unsaved, phoneTestPassword)
				if err != nil {
					t.Fatalf("OpenTarget: %v", err)
				}
				real, ok := handle.(*phoneSessionHandle)
				if !ok {
					t.Fatalf("OpenTarget returned %T, want the application handle", handle)
				}
				return real
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newPhoneTransportEnv(t, func() fakessh.Options {
				return fakessh.Options{Password: phoneTestPassword, HostKey: signer}
			})
			handle := tc.open(t, env)
			t.Cleanup(func() { _ = handle.End() })

			root := filepath.Join(t.TempDir(), "Session Logs")
			if err := handle.StartSessionLog(phoneshell.SessionLogOptions{
				Directory: root,
				Format:    domainterminal.LogFormatRaw,
				Label:     "prod box tab-1/pane-1",
			}); err != nil {
				t.Fatalf("StartSessionLog: %v", err)
			}
			path := handle.SessionLogStatus().Path
			if status := handle.SessionLogStatus(); !status.Active || path == "" {
				t.Fatalf("log did not start: %+v", status)
			}

			// The fixture echoes typed input, so this produces terminal output
			// through the Session's read loop, exactly as a remote shell would.
			if !handle.InsertInput([]byte("goodbye\r")) {
				t.Fatal("the Session did not accept the input")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				data, readErr := os.ReadFile(path)
				if readErr == nil && strings.Contains(string(data), "goodbye") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the log never received terminal output; last read: %q, %v", data, readErr)
				}
				time.Sleep(20 * time.Millisecond)
			}

			// App shutdown's synchronous step: it flushes every tracked
			// Session's log before the asynchronous Session teardown is allowed
			// to be the only finalizer. An untracked Quick Connect handle is
			// missed here and its log is left active at process exit.
			env.runtime.stopSessionLogs()
			if status := handle.SessionLogStatus(); status.Active {
				t.Fatal("app shutdown did not finalize the active log")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read log after shutdown: %v", err)
			}
			if !strings.Contains(string(data), "goodbye") {
				t.Fatalf("shutdown log = %q, want the terminal output", data)
			}
		})
	}
}
