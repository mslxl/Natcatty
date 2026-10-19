package ssh

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

type fakeX11Local struct {
	target  string
	dial    func(context.Context) (net.Conn, error)
	cookie  []byte
	dialErr error
}

func (f *fakeX11Local) Target() string { return f.target }

func (f *fakeX11Local) Dial(ctx context.Context) (net.Conn, error) {
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	if f.dial != nil {
		return f.dial(ctx)
	}
	return nil, errors.New("no local X server")
}

func (f *fakeX11Local) AuthCookie(context.Context) []byte { return f.cookie }

func buildX11SetupPacket(cookie []byte, endian byte) []byte {
	protocol := []byte("MIT-MAGIC-COOKIE-1")
	total := 12 + pad4(len(protocol)) + pad4(len(cookie))
	packet := make([]byte, total)
	packet[0] = endian

	put := func(offset int, value int) {
		if endian == 'l' {
			binary.LittleEndian.PutUint16(packet[offset:], uint16(value))
			return
		}
		binary.BigEndian.PutUint16(packet[offset:], uint16(value))
	}
	put(2, 11) // protocol major version
	put(4, 0)  // protocol minor version
	put(6, len(protocol))
	put(8, len(cookie))
	copy(packet[12:], protocol)
	copy(packet[12+pad4(len(protocol)):], cookie)
	return packet
}

// readX11Setup reads exactly one X11 setup packet from conn.
func readX11Setup(conn io.Reader) ([]byte, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	littleEndian := header[0] == 'l'
	read16 := func(offset int) int {
		if littleEndian {
			return int(binary.LittleEndian.Uint16(header[offset:]))
		}
		return int(binary.BigEndian.Uint16(header[offset:]))
	}
	protocolLength := read16(6)
	dataLength := read16(8)
	rest := make([]byte, pad4(protocolLength)+pad4(dataLength))
	if _, err := io.ReadFull(conn, rest); err != nil {
		return nil, err
	}
	return append(header, rest...), nil
}

func setupCookie(t *testing.T, packet []byte) []byte {
	t.Helper()
	if len(packet) < 12 {
		t.Fatalf("setup packet is only %d bytes", len(packet))
	}
	littleEndian := packet[0] == 'l'
	read16 := func(offset int) int {
		if littleEndian {
			return int(binary.LittleEndian.Uint16(packet[offset:]))
		}
		return int(binary.BigEndian.Uint16(packet[offset:]))
	}
	start := 12 + pad4(read16(6))
	return packet[start : start+read16(8)]
}

func dialX11(t *testing.T, server *fakessh.Server, dialFunc DialFunc, local X11Local) appconnection.Client {
	t.Helper()
	dialer := NewDialer(
		WithDialFunc(dialFunc),
		WithTimeouts(2*time.Second, 2*time.Second),
		WithX11Local(func(string) X11Local { return local }),
	)
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "pw",
		VerifyHostKey: acceptEveryKey,
		X11:           &appconnection.X11Options{Display: ":0"},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestX11ForwardsACookieRewrittenChannel(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "pw", AcceptX11: true})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen local X server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	realCookie := []byte("real-local-cooki")
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		packet, err := readX11Setup(conn)
		if err != nil {
			return
		}
		received <- packet
		_, _ = conn.Write([]byte("hello-back"))
		_, _ = io.Copy(io.Discard, conn)
	}()

	local := &fakeX11Local{
		target: listener.Addr().String(),
		cookie: realCookie,
		dial: func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", listener.Addr().String())
		},
	}
	client := dialX11(t, server, dialFunc, local)
	if _, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}

	requests := server.X11Requests()
	if len(requests) != 1 {
		t.Fatalf("x11-req requests = %+v, want one", requests)
	}
	request := requests[0]
	if request.Protocol != "MIT-MAGIC-COOKIE-1" {
		t.Fatalf("x11-req protocol = %q", request.Protocol)
	}
	if request.SingleConnection {
		t.Fatal("x11-req requested single-connection; the original uses a persistent display")
	}
	fakeCookie, err := hex.DecodeString(request.Cookie)
	if err != nil {
		t.Fatalf("x11-req cookie %q is not hex: %v", request.Cookie, err)
	}
	if len(fakeCookie) != 16 {
		t.Fatalf("x11-req cookie has %d bytes, want 16", len(fakeCookie))
	}

	channel, err := server.OpenX11()
	if err != nil {
		t.Fatalf("OpenX11: %v", err)
	}
	defer channel.Close()
	if _, err := channel.Write(buildX11SetupPacket(fakeCookie, 'l')); err != nil {
		t.Fatalf("write setup packet: %v", err)
	}

	var packet []byte
	select {
	case packet = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("the local X server never received the forwarded setup packet")
	}

	if got := setupCookie(t, packet); !bytes.Equal(got, realCookie) {
		t.Fatalf("local cookie = %x, want %x (fake %x)", got, realCookie, fakeCookie)
	}

	reply := make([]byte, len("hello-back"))
	if err := readFull(t, channel, reply); err != nil {
		t.Fatalf("read local server reply: %v", err)
	}
	if string(reply) != "hello-back" {
		t.Fatalf("reply = %q", reply)
	}

	// The remote-to-local direction remains byte-transparent after the setup
	// packet.
	if _, err := channel.Write([]byte("after-setup")); err != nil {
		t.Fatalf("write after setup: %v", err)
	}
}

func TestX11RequestDeniedFailsTheShell(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "pw"})
	local := &fakeX11Local{target: ":0", cookie: []byte("0123456789abcdef")}
	client := dialX11(t, server, dialFunc, local)

	_, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24})
	if !errors.Is(err, appconnection.ErrX11RequestDenied) {
		t.Fatalf("OpenTerminal error = %v, want ErrX11RequestDenied", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindX11 {
		t.Fatalf("error = %v, want KindX11", err)
	}
	if server.ShellOpened() {
		t.Fatal("the adapter opened a shell after x11-req was refused")
	}
	if len(server.X11Requests()) != 1 {
		t.Fatalf("x11-req requests = %+v, want one", server.X11Requests())
	}
}

func TestX11LocalDisplayFailureReportsANotice(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "pw", AcceptX11: true})
	notices := make(chan appconnection.X11Notice, 1)
	local := &fakeX11Local{
		target:  "localhost:6000",
		cookie:  []byte("0123456789abcdef"),
		dialErr: errors.New("connection refused"),
	}

	dialer := NewDialer(
		WithDialFunc(dialFunc),
		WithTimeouts(2*time.Second, 2*time.Second),
		WithX11Local(func(string) X11Local { return local }),
	)
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "pw",
		VerifyHostKey: acceptEveryKey,
		X11: &appconnection.X11Options{
			OnNotice: func(notice appconnection.X11Notice) { notices <- notice },
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}

	if _, err := server.OpenX11(); err == nil {
		t.Fatal("the server X11 channel was accepted despite a dead local display")
	}

	select {
	case notice := <-notices:
		if notice.Code != appconnection.X11NoticeLocalDisplay {
			t.Fatalf("notice = %+v, want local-display", notice)
		}
		if notice.Display != "localhost:6000" {
			t.Fatalf("notice display = %q", notice.Display)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no X11 notice reported")
	}
}

func TestX11ChannelTeardownClosesTheLocalDisplay(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "pw", AcceptX11: true})
	localConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	local := &fakeX11Local{
		target: "pipe",
		dial: func(context.Context) (net.Conn, error) {
			return localConn, nil
		},
	}
	client := dialX11(t, server, dialFunc, local)
	if _, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}

	channel, err := server.OpenX11()
	if err != nil {
		t.Fatalf("OpenX11: %v", err)
	}
	_ = channel.Close()

	_ = serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the local display connection stayed open after channel teardown")
	}
}

func TestX11WithoutALocalCookieReportsANoticeAndStillProxies(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "pw", AcceptX11: true})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen local X server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- struct{}{}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}()

	notices := make(chan appconnection.X11Notice, 1)
	local := &fakeX11Local{
		target: "localhost:6000",
		dial: func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", listener.Addr().String())
		},
	}
	dialer := NewDialer(
		WithDialFunc(dialFunc),
		WithTimeouts(2*time.Second, 2*time.Second),
		WithX11Local(func(string) X11Local { return local }),
	)
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "pw",
		VerifyHostKey: acceptEveryKey,
		X11: &appconnection.X11Options{
			OnNotice: func(notice appconnection.X11Notice) { notices <- notice },
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}

	channel, err := server.OpenX11()
	if err != nil {
		t.Fatalf("OpenX11: %v", err)
	}
	defer channel.Close()
	if _, err := channel.Write([]byte("setup")); err != nil {
		t.Fatalf("write setup: %v", err)
	}
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the local X server never received the channel")
	}
	select {
	case notice := <-notices:
		if notice.Code != appconnection.X11NoticeCookie || notice.Display != "localhost:6000" {
			t.Fatalf("notice = %+v, want the missing-cookie notice", notice)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no missing-cookie notice reported")
	}
}

// failingChannel models a server channel that cannot be accepted.
type failingChannel struct {
	rejected bool
}

func (f *failingChannel) ChannelType() string { return x11ChannelType }
func (f *failingChannel) ExtraData() []byte   { return nil }
func (f *failingChannel) Accept() (xssh.Channel, <-chan *xssh.Request, error) {
	return nil, nil, errors.New("accept failed")
}
func (f *failingChannel) Reject(xssh.RejectionReason, string) error {
	f.rejected = true
	return nil
}

func TestX11ChannelSetupFailureReportsANotice(t *testing.T) {
	localConn, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	notices := make(chan appconnection.X11Notice, 1)
	bridge := &x11Bridge{
		local: &fakeX11Local{
			target: "pipe",
			dial: func(context.Context) (net.Conn, error) {
				return localConn, nil
			},
		},
		onNotice: func(notice appconnection.X11Notice) { notices <- notice },
	}

	incoming := &failingChannel{}
	bridge.forward(incoming)

	if incoming.rejected {
		t.Fatal("forward rejected a channel that was never accepted")
	}
	select {
	case notice := <-notices:
		if notice.Code != appconnection.X11NoticeChannel || notice.Display != "pipe" {
			t.Fatalf("notice = %+v, want the channel notice", notice)
		}
	default:
		t.Fatal("no channel-setup notice reported")
	}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("the local display connection stayed open after Accept failed")
	}
}

func TestRewriteX11AuthSetupPacket(t *testing.T) {
	fake := []byte("fake-fake-fake!")
	real := []byte("real-real-real!")
	packet := buildX11SetupPacket(fake, 'l')

	rewritten := rewriteX11AuthSetupPacket(packet, fake, real)
	if !rewritten.complete || !rewritten.rewritten {
		t.Fatalf("rewrite = %+v, want complete and rewritten", rewritten)
	}
	if got := setupCookie(t, rewritten.buffer); !bytes.Equal(got, real) {
		t.Fatalf("cookie = %x, want %x", got, real)
	}
	if bytes.Equal(packet, rewritten.buffer) {
		t.Fatal("rewrite mutated the original buffer")
	}

	bigEndian := buildX11SetupPacket(fake, 'B')
	rewrittenBig := rewriteX11AuthSetupPacket(bigEndian, fake, real)
	if !rewrittenBig.rewritten || !bytes.Equal(setupCookie(t, rewrittenBig.buffer), real) {
		t.Fatal("big-endian setup packet was not rewritten")
	}

	unchanged := rewriteX11AuthSetupPacket(buildX11SetupPacket([]byte("other-cookie-!!!"), 'l'), fake, real)
	if unchanged.rewritten {
		t.Fatal("an unrelated cookie was overwritten")
	}

	withoutReal := rewriteX11AuthSetupPacket(packet, fake, nil)
	if withoutReal.rewritten || !bytes.Equal(setupCookie(t, withoutReal.buffer), fake) {
		t.Fatal("a missing local cookie changed the packet")
	}

	partial := rewriteX11AuthSetupPacket(packet[:12], fake, real)
	if partial.complete || partial.rewritten {
		t.Fatal("a partial setup packet was treated as complete")
	}
}
