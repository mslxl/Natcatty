package ssh

import (
	"context"
	"net"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

// TestTerminalCloseDuringAWriteDoesNotRace is the sentEOF teardown regression.
// The shell handler refuses to read, so a payload larger than the SSH channel
// window parks the input write inside x/crypto's window reserve. x/crypto's
// channel.Write reads sentEOF without a lock and CloseWrite writes it without
// one, so an un-serialized Close during that write is the data race that tore
// down SSH Sessions. The adapter's writeMu must order the pair, and Close must
// still abort the channel so the parked write returns instead of hanging.
func TestTerminalCloseDuringAWriteDoesNotRace(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	_, dialFunc := startFixture(t, fakessh.Options{
		Password: "s3cret",
		Shell:    func(channel xssh.Channel) { <-release },
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "tester", Password: "s3cret",
		VerifyHostKey: acceptEveryKey,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	terminal, err := client.OpenTerminal(context.Background(),
		appconnection.TerminalSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	concrete, ok := terminal.(*channelTerminal)
	if !ok {
		t.Fatalf("OpenTerminal returned %T", terminal)
	}

	started := make(chan error, 1)
	finished := make(chan error, 1)
	startWrite := make(chan struct{})
	go func() {
		// The small write proves the channel works without parking.
		_, firstErr := concrete.Write([]byte("warm\n"))
		started <- firstErr
		<-startWrite
		// Larger than channelWindowSize (2 MiB): the handler does not read,
		// so this write parks in x/crypto's window reserve.
		_, writeErr := concrete.Write(make([]byte, 4<<20))
		finished <- writeErr
	}()
	if err := <-started; err != nil {
		t.Fatalf("first write: %v", err)
	}
	close(startWrite)
	// A wall-clock delay, deliberately without any synchronization: the race
	// detector must still see the write and Close as an unordered pair when
	// the adapter does not serialize them.
	time.Sleep(100 * time.Millisecond)
	if err := concrete.Close(); err != nil {
		t.Fatalf("terminal Close: %v", err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("the parked write succeeded after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not abort the parked write")
	}
}

// waitOnlyConn is an ssh.Conn whose Wait returns immediately. It lets the
// death watcher be driven without a network transport.
type waitOnlyConn struct{}

func (waitOnlyConn) User() string                                           { return "test" }
func (waitOnlyConn) SessionID() []byte                                      { return nil }
func (waitOnlyConn) ClientVersion() []byte                                  { return nil }
func (waitOnlyConn) ServerVersion() []byte                                  { return nil }
func (waitOnlyConn) RemoteAddr() net.Addr                                   { return nil }
func (waitOnlyConn) LocalAddr() net.Addr                                    { return nil }
func (waitOnlyConn) SendRequest(string, bool, []byte) (bool, []byte, error) { return false, nil, nil }
func (waitOnlyConn) OpenChannel(string, []byte) (xssh.Channel, <-chan *xssh.Request, error) {
	return nil, nil, nil
}
func (waitOnlyConn) Close() error { return nil }
func (waitOnlyConn) Wait() error  { return nil }

func newDeathClient(cancel context.CancelFunc) *sshClient {
	channels := make(chan xssh.NewChannel)
	close(channels)
	requests := make(chan *xssh.Request)
	close(requests)
	return &sshClient{client: xssh.NewClient(waitOnlyConn{}, channels, requests), cancel: cancel}
}

// TestWatchDeathToleratesANilCancel covers the latent panic: a client built
// without a host-key verification context has no cancel function, and the
// peer's transport may still end. Recording that death must not call nil.
func TestWatchDeathToleratesANilCancel(t *testing.T) {
	client := newDeathClient(nil)
	client.watchDeath()
	if client.Alive() {
		t.Fatal("the client is still alive after watchDeath")
	}
}

// TestWatchDeathReleasesTheVerificationContext proves the rekey-prompt
// cancellation still runs when a cancel function is installed.
func TestWatchDeathReleasesTheVerificationContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newDeathClient(cancel)
	client.watchDeath()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("watchDeath did not cancel the verification context")
	}
}
