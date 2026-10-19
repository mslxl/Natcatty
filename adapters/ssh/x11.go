package ssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	xssh "golang.org/x/crypto/ssh"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/platform/xauth"
)

// X11Local is the small OS-facing seam the X11 bridge consumes. Production uses
// platform/xauth; adapter tests inject an in-process local X server.
type X11Local interface {
	// Target names the resolved local display for diagnostics.
	Target() string
	// Dial connects to the local X server.
	Dial(ctx context.Context) (net.Conn, error)
	// AuthCookie returns the local MIT-MAGIC-COOKIE-1 as raw bytes, or nil when
	// xauth has no matching entry.
	AuthCookie(ctx context.Context) []byte
}

// X11LocalFactory builds the local X11 seam for a display override.
type X11LocalFactory func(display string) X11Local

const (
	x11ChannelType    = "x11"
	x11RequestType    = "x11-req"
	x11DialTimeout    = 10 * time.Second
	x11SetupMaxLength = 1 << 20
)

// defaultX11Local is the production local display.
func defaultX11Local(display string) X11Local { return xauth.New(display) }

// x11Bridge owns one connection's fake cookie and dispatches the x11 channels
// the SSH Host opens back to the client.
type x11Bridge struct {
	local    X11Local
	fake     []byte
	fakeHex  string
	onNotice func(appconnection.X11Notice)

	channels  <-chan xssh.NewChannel
	done      chan struct{}
	closeOnce sync.Once

	cookieOnce sync.Once
	realCookie []byte
}

// newX11Bridge generates a per-connection fake cookie and starts dispatching
// x11 channels. The fake cookie is sent in x11-req; the remote program presents
// it in its setup packet, which is rewritten to the local real cookie.
func newX11Bridge(client *xssh.Client, options *appconnection.X11Options, factory X11LocalFactory) (*x11Bridge, error) {
	fake := make([]byte, 16)
	if _, err := rand.Read(fake); err != nil {
		return nil, &appconnection.Error{
			Kind:  appconnection.KindInternal,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrX11Unavailable, err),
		}
	}
	display := ""
	var notice func(appconnection.X11Notice)
	if options != nil {
		display = options.Display
		notice = options.OnNotice
	}
	bridge := &x11Bridge{
		local:    factory(display),
		fake:     fake,
		fakeHex:  hex.EncodeToString(fake),
		onNotice: notice,
		channels: client.HandleChannelOpen(x11ChannelType),
		done:     make(chan struct{}),
	}
	go bridge.dispatch()
	return bridge, nil
}

// RequestX11 asks the SSH Host to forward X11 for session and registers the
// fake cookie the bridge rewrites. A refusal is a typed X11 failure.
func (b *x11Bridge) RequestX11(session *xssh.Session) error {
	if b == nil {
		return nil
	}
	payload := xssh.Marshal(struct {
		SingleConnection bool
		AuthProtocol     string
		AuthCookie       string
		ScreenNumber     uint32
	}{false, xauth.AuthProtocol, b.fakeHex, 0})

	ok, err := session.SendRequest(x11RequestType, true, payload)
	if err != nil {
		return &appconnection.Error{
			Kind:  appconnection.KindX11,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrX11RequestDenied, err),
		}
	}
	if !ok {
		return &appconnection.Error{
			Kind:  appconnection.KindX11,
			Cause: appconnection.ErrX11RequestDenied,
		}
	}
	return nil
}

// Close stops accepting new channels. Open channels end with the transport.
func (b *x11Bridge) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() { close(b.done) })
}

func (b *x11Bridge) dispatch() {
	if b.channels == nil {
		return
	}
	for {
		select {
		case <-b.done:
			return
		case incoming, ok := <-b.channels:
			if !ok {
				return
			}
			go b.forward(incoming)
		}
	}
}

// forward accepts one server-opened x11 channel, dials the local X server, and
// proxies both directions. On a local failure the remote channel is rejected
// and a notice reports the resolved display.
func (b *x11Bridge) forward(incoming xssh.NewChannel) {
	dialCtx, cancelDial := context.WithTimeout(context.Background(), x11DialTimeout)
	local, err := b.local.Dial(dialCtx)
	cancelDial()
	if err != nil {
		_ = incoming.Reject(xssh.ConnectionFailed, "local X11 display unavailable")
		b.notify(appconnection.X11Notice{
			Code:    appconnection.X11NoticeLocalDisplay,
			Display: b.local.Target(),
		})
		return
	}

	channel, requests, err := incoming.Accept()
	if err != nil {
		_ = local.Close()
		b.notify(appconnection.X11Notice{
			Code:    appconnection.X11NoticeChannel,
			Display: b.local.Target(),
		})
		return
	}
	go xssh.DiscardRequests(requests)

	remote := io.Reader(channel)
	realCookie := b.localCookie()
	if len(realCookie) > 0 {
		remote = &x11SetupRewriter{
			source:     channel,
			fakeCookie: b.fake,
			realCookie: realCookie,
		}
	} else {
		// Without a local cookie the channel is still proxied, matching the
		// original's fallback, but the authentication failure should not be
		// silent.
		b.notify(appconnection.X11Notice{
			Code:    appconnection.X11NoticeCookie,
			Display: b.local.Target(),
		})
	}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(local, remote)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(channel, local)
		done <- struct{}{}
	}()
	<-done
	_ = channel.Close()
	_ = local.Close()
	<-done
}

// localCookie resolves the local xauth cookie once per Connection, matching
// the original's lazy session-level cache. A nil result is cached too: repeat
// channel opens must not keep spawning xauth.
func (b *x11Bridge) localCookie() []byte {
	b.cookieOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), x11DialTimeout)
		defer cancel()
		b.realCookie = b.local.AuthCookie(ctx)
	})
	return b.realCookie
}

func (b *x11Bridge) notify(notice appconnection.X11Notice) {
	if b.onNotice != nil {
		b.onNotice(notice)
	}
}

// x11SetupRewriter rewrites the first X11 setup packet from the SSH fake cookie
// to the local real cookie, then streams every remaining byte unchanged.
type x11SetupRewriter struct {
	source     io.Reader
	fakeCookie []byte
	realCookie []byte
	pending    []byte
	loaded     bool
	err        error
}

func (r *x11SetupRewriter) Read(p []byte) (int, error) {
	if !r.loaded {
		r.load()
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return r.source.Read(p)
}

// load accumulates bytes until the setup packet is complete or the source
// ends. An incomplete packet is forwarded unchanged only when the stream ends;
// this mirrors the original transform's flush behavior.
func (r *x11SetupRewriter) load() {
	r.loaded = true
	for {
		result := rewriteX11AuthSetupPacket(r.pending, r.fakeCookie, r.realCookie)
		if result.complete {
			r.pending = result.buffer
			return
		}
		chunk := make([]byte, 4096)
		n, err := r.source.Read(chunk)
		if n > 0 {
			r.pending = append(r.pending, chunk[:n]...)
		}
		if err != nil {
			r.err = err
			return
		}
		if len(r.pending) > x11SetupMaxLength {
			// A malformed peer must not grow the buffer without bound. Forward
			// what arrived and let the local server reject it.
			return
		}
	}
}

// x11Rewrite describes one setup-packet rewrite attempt.
type x11Rewrite struct {
	buffer    []byte
	complete  bool
	rewritten bool
}

// rewriteX11AuthSetupPacket replaces the fake MIT-MAGIC-COOKIE-1 with the local
// real cookie. It is the Go port of the original bridge's byte-level rewrite:
// little- and big-endian setup packets are supported, and anything unexpected
// passes through unchanged.
func rewriteX11AuthSetupPacket(buffer, fakeCookie, realCookie []byte) x11Rewrite {
	if len(realCookie) == 0 || len(buffer) < 12 {
		return x11Rewrite{buffer: buffer, complete: len(buffer) >= 12}
	}

	littleEndian := buffer[0] == 'l'
	if !littleEndian && buffer[0] != 'B' {
		return x11Rewrite{buffer: buffer, complete: true}
	}
	read16 := func(offset int) int {
		if littleEndian {
			return int(binary.LittleEndian.Uint16(buffer[offset:]))
		}
		return int(binary.BigEndian.Uint16(buffer[offset:]))
	}

	protocolLength := read16(6)
	dataLength := read16(8)
	protocolStart := 12
	dataStart := protocolStart + pad4(protocolLength)
	totalLength := dataStart + pad4(dataLength)
	if len(buffer) < totalLength {
		return x11Rewrite{buffer: buffer, complete: false}
	}

	protocol := string(buffer[protocolStart : protocolStart+protocolLength])
	if protocol != xauth.AuthProtocol || dataLength != len(realCookie) {
		return x11Rewrite{buffer: buffer, complete: true}
	}

	dataEnd := dataStart + dataLength
	current := buffer[dataStart:dataEnd]
	if len(fakeCookie) == len(current) && !bytes.Equal(current, fakeCookie) {
		// The remote program did not use the fake cookie. Do not overwrite an
		// unrelated authorization value.
		return x11Rewrite{buffer: buffer, complete: true}
	}

	next := append([]byte(nil), buffer...)
	copy(next[dataStart:dataEnd], realCookie)
	return x11Rewrite{buffer: next, complete: true, rewritten: true}
}

// pad4 rounds n up to the four-byte alignment X11 setup data uses.
func pad4(n int) int { return (n + 3) &^ 3 }
