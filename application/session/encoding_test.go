package session

import (
	"bytes"
	"context"
	"sync"
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// recordingCodec marks the bytes it converts so the test can tell which side
// of the Session's codec port produced them. The real charset conversion is
// tested in adapters/textcodec and in the app self-test.
type recordingCodec struct {
	mu       sync.Mutex
	encoding domainterminal.Encoding
	initial  domainterminal.Encoding
	decoded  [][]byte
	encoded  [][]byte
	switches []domainterminal.Encoding
}

func (c *recordingCodec) Decode(p []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decoded = append(c.decoded, append([]byte(nil), p...))
	return append([]byte("decoded:"), p...)
}

func (c *recordingCodec) Encode(p []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.encoded = append(c.encoded, append([]byte(nil), p...))
	return append([]byte("encoded:"), p...)
}

func (c *recordingCodec) SetEncoding(encoding domainterminal.Encoding) bool {
	if !encoding.Valid() {
		return false
	}
	c.mu.Lock()
	c.encoding = encoding
	c.switches = append(c.switches, encoding)
	c.mu.Unlock()
	return true
}

func (c *recordingCodec) snapshot() (decoded, encoded [][]byte, switches []domainterminal.Encoding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.decoded...),
		append([][]byte(nil), c.encoded...),
		append([]domainterminal.Encoding(nil), c.switches...)
}

// readUntil reads the Session's terminal output until it contains want. The
// decoded text is observed through the same TerminalConn the UI reads.
func readUntil(t *testing.T, session *Session, want []byte) []byte {
	t.Helper()
	var got []byte
	buffer := make([]byte, 64)
	for !bytes.Contains(got, want) {
		n, err := session.TerminalConn().Read(buffer)
		if err != nil {
			t.Fatalf("read output: %v (got %q, want %q)", err, got, want)
		}
		got = append(got, buffer[:n]...)
	}
	return got
}

func TestSessionRoutesOutputAndInputThroughTheCodec(t *testing.T) {
	transport := newFakeTransport()
	codec := &recordingCodec{}
	service := newTestService(&fakeFactory{transport: transport},
		WithCodecFactory(func(encoding domainterminal.Encoding) Codec {
			codec.initial = encoding
			codec.encoding = encoding
			return codec
		}))
	session, err := service.Open(context.Background(), StartSpec{
		Kind:     KindLocal,
		Encoding: domainterminal.EncodingGB18030,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if codec.initial != domainterminal.EncodingGB18030 {
		t.Fatalf("factory encoding = %q", codec.initial)
	}
	if got := session.Snapshot().Encoding; got != domainterminal.EncodingGB18030 {
		t.Fatalf("snapshot encoding = %q", got)
	}

	transport.send("wire")
	readUntil(t, session, []byte("decoded:wire"))

	if _, err := session.TerminalConn().Write([]byte("中")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForInput(t, transport, "encoded:中")

	decoded, encoded, _ := codec.snapshot()
	if len(decoded) == 0 || string(decoded[0]) != "wire" {
		t.Fatalf("codec decoded %q, want the transport bytes", decoded)
	}
	if len(encoded) == 0 || string(encoded[0]) != "中" {
		t.Fatalf("codec encoded %q, want the UTF-8 keystroke", encoded)
	}

	if !session.SetEncoding(domainterminal.EncodingShiftJIS) {
		t.Fatal("Shift_JIS was rejected")
	}
	if session.SetEncoding("unknown-99") {
		t.Fatal("an unknown encoding was accepted")
	}
	if got := session.Snapshot().Encoding; got != domainterminal.EncodingShiftJIS {
		t.Fatalf("live encoding = %q", got)
	}
	_, _, switches := codec.snapshot()
	if len(switches) != 1 || switches[0] != domainterminal.EncodingShiftJIS {
		t.Fatalf("codec switches = %v", switches)
	}
}

func TestSessionEncodingResolverUsesTheHostScope(t *testing.T) {
	var seenScope string
	service := newTestService(&fakeFactory{transport: newFakeTransport()},
		WithEncodingResolver(func(spec StartSpec) domainterminal.Encoding {
			seenScope = spec.EncodingScope
			return domainterminal.EncodingLatin1
		}),
		WithCodecFactory(func(encoding domainterminal.Encoding) Codec {
			return &recordingCodec{encoding: encoding}
		}))
	session, err := service.Open(context.Background(), StartSpec{
		Kind:   KindSSH,
		HostID: "host-7",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if seenScope != "host:host-7" {
		t.Fatalf("resolver scope = %q", seenScope)
	}
	if got := session.Encoding(); got != domainterminal.EncodingLatin1 {
		t.Fatalf("encoding = %q", got)
	}
	if got := session.EncodingScope(); got != "host:host-7" {
		t.Fatalf("scope = %q", got)
	}
}

func TestOpenRefusesALegacyEncodingWithoutACodecFactory(t *testing.T) {
	service := newTestService(&fakeFactory{transport: newFakeTransport()})
	if _, err := service.Open(context.Background(), StartSpec{
		Kind:     KindLocal,
		Encoding: domainterminal.EncodingGB18030,
	}); err == nil {
		t.Fatal("Open accepted GB18030 without a codec factory")
	}
}
