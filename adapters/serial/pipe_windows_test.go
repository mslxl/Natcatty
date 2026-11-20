//go:build windows

package serial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	appsession "github.com/mslxl/natcatty/application/session"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

func testPipeConfig(path string) domainserial.Config {
	return domainserial.Config{Path: path, BaudRate: 115200, DataBits: 8,
		StopBits: domainserial.StopBitsOne, Parity: domainserial.ParityNone,
		FlowControl: domainserial.FlowNone}
}

func uniquePipeName() string {
	return fmt.Sprintf(`\\.\pipe\natcatty-serial-%d-%d`, os.Getpid(), time.Now().UnixNano())
}

func createPipeServer(t *testing.T, name string) windows.Handle {
	t.Helper()
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	handle, err := windows.CreateNamedPipe(namePtr,
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatalf("CreateNamedPipe: %v", err)
	}
	return handle
}

// TestNamedPipeCarriesBidirectionalData is the pipe-backed fixture: a local
// server accepts the client, receives a write, and answers it.
func TestNamedPipeCarriesBidirectionalData(t *testing.T) {
	name := uniquePipeName()
	handle := createPipeServer(t, name)
	defer windows.CloseHandle(handle)

	server := make(chan error, 1)
	client := make(chan error, 1)
	go func() { server <- servePipeOnce(handle) }()
	go func() { client <- exercisePipeClient(name) }()

	completed := 0
	deadline := time.After(10 * time.Second)
	for completed < 2 {
		select {
		case err := <-server:
			if err != nil {
				t.Fatalf("pipe server: %v", err)
			}
			completed++
		case err := <-client:
			if err != nil {
				t.Fatalf("pipe client: %v", err)
			}
			completed++
		case <-deadline:
			t.Fatal("the named pipe exchange did not finish within 10s")
		}
	}
}

func servePipeOnce(handle windows.Handle) error {
	if err := windows.ConnectNamedPipe(handle, nil); err != nil &&
		!errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return fmt.Errorf("connect: %w", err)
	}
	ping := make([]byte, 4)
	if err := readPipeExactly(handle, ping); err != nil {
		return fmt.Errorf("read ping: %w", err)
	}
	if string(ping) != "ping" {
		return fmt.Errorf("server read %q, want ping", ping)
	}
	var written uint32
	if err := windows.WriteFile(handle, []byte("pong"), &written, nil); err != nil {
		return fmt.Errorf("write pong: %w", err)
	}
	if written != 4 {
		return fmt.Errorf("server wrote %d bytes, want 4", written)
	}
	// Hold the instance open until the client disconnects so the reply cannot
	// be lost to a close race.
	buf := make([]byte, 1)
	for {
		var done uint32
		err := windows.ReadFile(handle, buf, &done, nil)
		if err != nil {
			if errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
				errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
				return nil
			}
			return fmt.Errorf("drain: %w", err)
		}
		if done == 0 {
			return nil
		}
	}
}

func readPipeExactly(handle windows.Handle, buf []byte) error {
	for read := 0; read < len(buf); {
		var done uint32
		if err := windows.ReadFile(handle, buf[read:], &done, nil); err != nil {
			return err
		}
		if done == 0 {
			return io.ErrUnexpectedEOF
		}
		read += int(done)
	}
	return nil
}

func exercisePipeClient(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := Factory{}.Open(ctx, serialSpec(testPipeConfig(name)))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		return fmt.Errorf("write ping: %w", err)
	}
	pong := make([]byte, 4)
	for read := 0; read < len(pong); {
		n, err := conn.Read(pong[read:])
		if err != nil {
			return fmt.Errorf("read pong: %w", err)
		}
		read += n
	}
	if string(pong) != "pong" {
		return fmt.Errorf("client read %q, want pong", pong)
	}
	return nil
}

func TestMissingNamedPipeFailsWithATypedError(t *testing.T) {
	name := uniquePipeName()
	_, err := Factory{}.Open(context.Background(), serialSpec(testPipeConfig(name)))
	if err == nil {
		t.Fatal("opening a pipe with no server succeeded")
	}
	if !errors.Is(err, domainserial.ErrOpen) {
		t.Fatalf("Open = %v, want ErrOpen", err)
	}
	var typed *domainserial.OpenError
	if !errors.As(err, &typed) || typed.Path != name {
		t.Fatalf("error = %#v, want an OpenError naming %q", err, name)
	}
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		t.Fatalf("error = %v, want the missing-pipe cause", err)
	}
}

// TestClosingANamedPipeSettlesAPendingRead proves a Session shutdown does not
// wait for the pipe server to send data. The test closes only after the read
// is observably pending, never after a delay.
func TestClosingANamedPipeSettlesAPendingRead(t *testing.T) {
	name := uniquePipeName()
	handle := createPipeServer(t, name)
	defer windows.CloseHandle(handle)
	go func() { _ = windows.ConnectNamedPipe(handle, nil) }()

	conn, err := Factory{}.Open(context.Background(), serialSpec(testPipeConfig(name)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	port := conn.(*transport).port.(*pipePort)
	pending := make(chan struct{}, 1)
	port.onPending = func() {
		select {
		case pending <- struct{}{}:
		default:
		}
	}

	result := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 8))
		result <- err
	}()
	select {
	case <-pending:
	case <-time.After(5 * time.Second):
		t.Fatal("the Read never became an overlapped pending operation")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("pending Read = %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not settle the pending Read within 5s")
	}
}

// TestPipeServerDisconnectEndsTheSessionAsAnError covers a crashed console:
// the server closes its pipe handle while the Session is reading. That is a
// device failure, not the user hanging up, so the Read reports an error other
// than io.EOF and End() carries exit code 1.
func TestPipeServerDisconnectEndsTheSessionAsAnError(t *testing.T) {
	name := uniquePipeName()
	handle := createPipeServer(t, name)
	handleClosed := false
	defer func() {
		if !handleClosed {
			_ = windows.CloseHandle(handle)
		}
	}()
	connected := make(chan error, 1)
	go func() {
		err := windows.ConnectNamedPipe(handle, nil)
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			connected <- fmt.Errorf("connect: %w", err)
			return
		}
		connected <- nil
	}()

	conn, err := Factory{}.Open(context.Background(), serialSpec(testPipeConfig(name)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	select {
	case err := <-connected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pipe server never accepted the client")
	}

	if err := windows.CloseHandle(handle); err != nil {
		t.Fatalf("close server pipe: %v", err)
	}
	handleClosed = true

	result := make(chan error, 1)
	go func() {
		_, readErr := conn.Read(make([]byte, 8))
		result <- readErr
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Read after a server disconnect returned no error")
		}
		if errors.Is(err, io.EOF) {
			t.Fatalf("server disconnect = %v, want a device failure rather than a clean EOF", err)
		}
		if !errors.Is(err, errPipeDisconnected) {
			t.Fatalf("server disconnect = %v, want errPipeDisconnected", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server disconnect never settled the pending Read")
	}

	reporter, ok := conn.(appsession.EndReporter)
	if !ok {
		t.Fatal("the serial transport does not classify its end")
	}
	end := reporter.End()
	if end.Reason != appsession.EndReasonError || !end.HasExitCode || end.ExitCode != 1 {
		t.Fatalf("End() = %+v, want an error exit 1 after a server disconnect", end)
	}
}
