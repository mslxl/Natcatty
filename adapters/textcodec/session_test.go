package textcodec

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// wireTransport is the minimum application/session.Transport this integration
// test needs: the test feeds output and observes input.
type wireTransport struct {
	output    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	written   bytes.Buffer
}

func newWireTransport() *wireTransport {
	return &wireTransport{output: make(chan []byte, 4), closed: make(chan struct{})}
}

func (t *wireTransport) Read(p []byte) (int, error) {
	select {
	case chunk := <-t.output:
		return copy(p, chunk), nil
	case <-t.closed:
		return 0, io.EOF
	}
}

func (t *wireTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.Write(p)
}

func (t *wireTransport) Resize(int, int) error { return nil }

func (t *wireTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func (t *wireTransport) send(p []byte) { t.output <- p }

func (t *wireTransport) writtenBytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.written.Bytes()...)
}

type wireFactory struct{ transport *wireTransport }

func (f wireFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

func readTerminalUntil(t *testing.T, session *appsession.Session, want string) {
	t.Helper()
	var got []byte
	buffer := make([]byte, 64)
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(got, []byte(want)) {
		if time.Now().After(deadline) {
			t.Fatalf("terminal output = %q, want %q", got, want)
		}
		n, err := session.TerminalConn().Read(buffer)
		if err != nil {
			t.Fatalf("read output: %v (got %q, want %q)", err, got, want)
		}
		got = append(got, buffer[:n]...)
	}
}

func waitForWireInput(t *testing.T, transport *wireTransport, want []byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if bytes.Equal(transport.writtenBytes(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("wire input = % X, want % X", transport.writtenBytes(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSessionDecodesSplitLegacyOutputAndEncodesInput runs the real Session
// codec through the application's Codec port. The wire bytes are independent
// known-good literals for the characters in each charset.
func TestSessionDecodesSplitLegacyOutputAndEncodesInput(t *testing.T) {
	transport := newWireTransport()
	service := appsession.NewService(wireFactory{transport: transport},
		appsession.WithCodecFactory(func(encoding domainterminal.Encoding) appsession.Codec {
			return New(encoding)
		}))
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:     appsession.KindLocal,
		Encoding: domainterminal.EncodingGB18030,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if got := session.Snapshot().Encoding; got != domainterminal.EncodingGB18030 {
		t.Fatalf("snapshot encoding = %q", got)
	}

	// 中 is D6 D0 in GB18030. Sending the bytes in two reads proves the
	// decoder keeps the incomplete lead byte instead of emitting it once.
	transport.send([]byte{0xD6})
	transport.send([]byte{0xD0})
	readTerminalUntil(t, session, "中")

	if _, err := session.TerminalConn().Write([]byte("中")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForWireInput(t, transport, []byte{0xD6, 0xD0})

	if !session.SetEncoding(domainterminal.EncodingShiftJIS) {
		t.Fatal("Shift_JIS was rejected")
	}
	if got := session.Snapshot().Encoding; got != domainterminal.EncodingShiftJIS {
		t.Fatalf("live encoding = %q", got)
	}
	// 日 is 93 FA in Shift_JIS; split it across reads after the live switch.
	transport.send([]byte{0x93})
	transport.send([]byte{0xFA})
	readTerminalUntil(t, session, "日")

	if _, err := session.TerminalConn().Write([]byte("日")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForWireInput(t, transport, []byte{0xD6, 0xD0, 0x93, 0xFA})

	if !session.SetEncoding(domainterminal.EncodingLatin1) {
		t.Fatal("ISO-8859-1 was rejected")
	}
	transport.send([]byte{0xE9})
	readTerminalUntil(t, session, "é")
	if _, err := session.TerminalConn().Write([]byte("é")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForWireInput(t, transport, []byte{0xD6, 0xD0, 0x93, 0xFA, 0xE9})
}
