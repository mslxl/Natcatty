// Package rfbtest provides an in-process RFB server for tests. It speaks RFB
// 3.3, 3.7 and 3.8, offers the None security type, records every client
// message, and serves a canned frame in Raw, CopyRect, RRE or Hextile
// encoding. It never opens a socket itself: callers hand Serve one end of a
// net.Pipe or an equivalent stream, so no live desktop is needed.
package rfbtest

import (
	"bytes"
	"compress/zlib"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"sync"

	"github.com/mslxl/natcatty/domain/model/rfb"
)

// Rectangle is one rectangle in a canned server update. For CopyRect, SrcX and
// SrcY select the source origin; other encodings draw from the server frame.
type Rectangle struct {
	X        uint16
	Y        uint16
	Width    uint16
	Height   uint16
	Encoding rfb.Encoding
	SrcX     uint16
	SrcY     uint16
}

// Config describes the frame and messages a Server serves.
type Config struct {
	// Version is the version the server advertises. Zero means 3.8.
	Version rfb.Version
	// Width and Height default to the Frame bounds when Frame is set.
	Width  uint16
	Height uint16
	// DesktopName is returned in ServerInit.
	DesktopName string
	// Frame is the canned image. A nil Frame serves black pixels.
	Frame image.Image
	// Encoding selects the encoding of the default full-frame rectangle.
	// Zero is Raw. CopyRect requires Rectangles.
	Encoding rfb.Encoding
	// Rectangles overrides Encoding with an explicit update plan, so a
	// test can send a Raw preamble followed by a CopyRect.
	Rectangles []Rectangle
	// ForcePixelFormat makes the server ignore SetPixelFormat and keep
	// PixelFormat, so a test can exercise a non-32-bit server format.
	ForcePixelFormat bool
	// PixelFormat is the server's format when ForcePixelFormat is set.
	PixelFormat rfb.PixelFormat
	// SendBell sends one Bell before the first framebuffer update.
	SendBell bool
	// CutText sends one ServerCutText before the first framebuffer update.
	CutText string
	// Password is the VNC password the server verifies for security type 2 and
	// the inner authentication of the strong types.
	// When SecurityTypes is empty a non-empty Password makes the server offer
	// VNC authentication alone; an empty Password offers None.
	Password string
	// Username is the account name the server verifies for Apple DH, the
	// VeNCrypt Plain family and an RA2 user+password subtype. It also selects
	// the RA2 credential subtype: subtype 1 when set, subtype 2 when empty.
	Username string
	// SecurityTypes overrides the offered list. A count of one is also used
	// for RFB 3.3, which has room for one security type. When empty the list
	// derives from Password: {VNCAuth} when it is set, otherwise {None}.
	SecurityTypes []rfb.SecurityType
	// VeNCryptSubtypes is the subtype list the server offers when security
	// type 19 is selected. It is required to exercise VeNCrypt.
	VeNCryptSubtypes []rfb.VeNCryptSubtype
	// TLSConfig is the server certificate for VeNCrypt TLS/X509 subtypes. The
	// TLS-prefixed subtypes use it too because Go has no anonymous cipher
	// suites; the client does not verify those subtypes.
	TLSConfig *tls.Config
	// AppleDHKeyBytes is the DH modulus size for Apple's type 30. Zero uses
	// 128 bytes (1024 bits), the smallest size TigerVNC accepts.
	AppleDHKeyBytes int
	// RA2KeyBits is the RSA modulus size for the RA2 family. Zero uses 1024
	// bits, the smallest the protocol accepts.
	RA2KeyBits int
	// ExtendedDesktopSize makes the server send an ExtendedDesktopSize
	// rectangle with the initial full update and follow a client
	// SetDesktopSize request.
	ExtendedDesktopSize bool
	// DesktopSizeStatus makes the server answer a client SetDesktopSize with
	// this nonzero ExtendedDesktopSize status instead of resizing. Status 1 is
	// "prohibited"; status 4 is "request forwarded", which a QEMU server uses
	// while the guest boots. The server replies with its current layout and
	// does not send a repaint unless DesktopSizeFollowUp is set.
	DesktopSizeStatus uint8
	// DesktopSizeFollowUp, when true, makes a status-4 answer complete later:
	// the server adopts the requested layout and streams a successful
	// server-initiated ExtendedDesktopSize (reason 0) plus a repaint at that
	// size, modeling QEMU finishing a forwarded request.
	DesktopSizeFollowUp bool
	// ScreenLayout is the desktop's initial screen layout. The frame is the
	// whole desktop, so each screen names a rectangle inside it. Empty means
	// one screen covering the whole framebuffer.
	ScreenLayout []rfb.Screen
	// IgnoreScreens makes the server resize its desktop while answering with a
	// single screen whose size is the whole framebuffer, the behavior of a
	// server that implements ExtendedDesktopSize but not multi-head.
	IgnoreScreens bool
	// FenceBurst sends this many additional ServerFence requests during
	// extension acknowledgement, reading one client message between each so a
	// synchronous client cannot deadlock on an unbuffered stream.
	FenceBurst int
	// ExtraEncoding, when nonzero, appends one rectangle header in that
	// encoding after the configured plan, with no payload. The client must
	// refuse the encoding before reading any payload.
	ExtraEncoding rfb.Encoding
	// ContinuousUpdates makes the server acknowledge the pseudo-encoding with
	// EndOfContinuousUpdates and stream a frame after the client enables it.
	ContinuousUpdates bool
	// Fence makes the server send a ServerFence request when it sees the
	// pseudo-encoding and answer ClientFence requests.
	Fence bool
	// FenceFlags overrides the flags on the initial ServerFence request. Zero
	// uses FenceRequest|FenceBlockBefore. FenceRequest is always added, so a
	// test can set an unknown bit to prove the client clears what it does not
	// understand.
	FenceFlags uint32
	// Cursor sends one CursorWithAlpha shape before the first framebuffer
	// update. HideCursor sends a zero-size (hidden) shape instead.
	Cursor     *image.RGBA
	CursorAt   image.Point
	HideCursor bool
	// ExtendedKeyEvent makes the server acknowledge the QEMU Extended Key
	// Event pseudo-encoding the way QEMU does: an update holding one
	// pseudo-rectangle for the whole surface. Extended key messages are
	// recorded and accepted either way, so a test can prove the client's
	// fallback.
	ExtendedKeyEvent bool
}

// UpdateRequest is a recorded FramebufferUpdateRequest.
type UpdateRequest struct {
	Incremental bool
	X           uint16
	Y           uint16
	Width       uint16
	Height      uint16
}

// KeyEvent is a recorded KeyEvent.
type KeyEvent struct {
	Down   bool
	Keysym uint32
}

// ExtendedKeyEvent is a recorded QEMU Extended Key Event (message 255,
// submessage 0). Keycode is the QEMU key number the client sent.
type ExtendedKeyEvent struct {
	Down    bool
	Keysym  uint32
	Keycode uint32
}

// PointerEvent is a recorded PointerEvent.
type PointerEvent struct {
	Mask uint8
	X    uint16
	Y    uint16
}

// ContinuousUpdate is a recorded EnableContinuousUpdates message.
type ContinuousUpdate struct {
	Enable bool
	X      uint16
	Y      uint16
	Width  uint16
	Height uint16
}

// DesktopSizeRequest is a recorded SetDesktopSize message.
type DesktopSizeRequest struct {
	Width   uint16
	Height  uint16
	Screens []rfb.Screen
}

// Received is a snapshot of everything the client sent.
type Received struct {
	ClientVersion rfb.Version
	// Security is the type the client selected.
	Security rfb.SecurityType
	// SecurityChoice includes the VeNCrypt subtype when one was negotiated.
	SecurityChoice rfb.SecurityChoice
	// VeNCryptSubtype is the subtype the client selected, or zero.
	VeNCryptSubtype rfb.VeNCryptSubtype
	// Username is the account name the client sent in the inner plaintext
	// exchange (VeNCrypt Plain family or Apple DH), or "".
	Username string
	// Password is the password the client sent in a plaintext exchange, or "".
	Password string
	// TLSStarted reports that the server ran the VeNCrypt TLS handshake.
	TLSStarted bool
	// VNCAuthAccepted reports that a VNC password challenge was answered with
	// the server's password.
	VNCAuthAccepted bool
	// AppleDHAccepted reports that Apple DH credentials matched.
	AppleDHAccepted bool
	// AppleDHUsername is the account name carried by Apple DH.
	AppleDHUsername string
	// RA2Subtype is the credential subtype the server requested.
	RA2Subtype byte
	// RA2Username and RA2Password are the credentials the client sent.
	RA2Username string
	RA2Password string
	// RA2Accepted reports that the RA2 credentials matched.
	RA2Accepted  bool
	Shared       bool
	PixelFormat  rfb.PixelFormat
	Encodings    []rfb.Encoding
	Requests     []UpdateRequest
	Keys         []KeyEvent
	ExtendedKeys []ExtendedKeyEvent
	Pointers     []PointerEvent
	CutTexts     []string
	Continuous   []ContinuousUpdate
	Fences       []rfb.Fence
	DesktopSizes []DesktopSizeRequest
}

// Server is the in-process test server. New prepares it; Serve performs one
// connection and returns when the peer closes or the protocol fails.
type Server struct {
	cfg    Config
	frame  *image.RGBA
	width  int
	height int
	// screens is the desktop's current layout. An empty layout means one
	// screen covering the whole framebuffer.
	screens []rfb.Screen
	err     error
	pf      rfb.PixelFormat

	mu         sync.Mutex
	received   Received
	sentBell   bool
	sentCut    bool
	sentEnd    bool
	sentFence  bool
	sentCursor bool
	sentExtKey bool

	// zrleBuf and zrleWriter form the connection's persistent ZRLE stream.
	zrleBuf    bytes.Buffer
	zrleWriter *zlib.Writer
}

// New prepares a server for cfg. Invalid configurations are reported by Serve.
func New(cfg Config) *Server {
	if cfg.Version == (rfb.Version{}) {
		cfg.Version = rfb.Version3_8
	}
	s := &Server{cfg: cfg, pf: rfb.DefaultPixelFormat()}
	if cfg.ForcePixelFormat {
		s.pf = cfg.PixelFormat
	}

	if cfg.Frame != nil {
		bounds := cfg.Frame.Bounds()
		s.width = int(cfg.Width)
		if s.width == 0 {
			s.width = bounds.Dx()
		}
		s.height = int(cfg.Height)
		if s.height == 0 {
			s.height = bounds.Dy()
		}
		s.frame = image.NewRGBA(image.Rect(0, 0, s.width, s.height))
		draw.Draw(s.frame, s.frame.Bounds(), cfg.Frame, bounds.Min, draw.Src)
	} else {
		s.width = int(cfg.Width)
		s.height = int(cfg.Height)
	}
	if s.width <= 0 || s.height <= 0 {
		s.err = fmt.Errorf("rfbtest: zero frame size %dx%d", s.width, s.height)
		return s
	}
	if s.width > 0xffff || s.height > 0xffff {
		s.err = fmt.Errorf("rfbtest: frame size %dx%d does not fit RFB", s.width, s.height)
		return s
	}
	if s.frame == nil {
		s.frame = image.NewRGBA(image.Rect(0, 0, s.width, s.height))
	}
	if cfg.ScreenLayout != nil {
		if len(cfg.ScreenLayout) == 0 {
			s.err = fmt.Errorf("rfbtest: empty screen layout")
			return s
		}
		s.screens = append([]rfb.Screen(nil), cfg.ScreenLayout...)
	}
	if err := s.pf.Validate(); err != nil {
		s.err = err
		return s
	}
	if len(cfg.Rectangles) == 0 && cfg.Encoding == rfb.EncodingCopyRect {
		s.err = fmt.Errorf("rfbtest: CopyRect needs an explicit rectangle plan")
		return s
	}
	for _, rect := range cfg.Rectangles {
		if err := s.checkRectangle(rect); err != nil {
			s.err = err
			return s
		}
	}
	return s
}

func (s *Server) checkRectangle(rect Rectangle) error {
	if rect.Width == 0 || rect.Height == 0 {
		return fmt.Errorf("rfbtest: zero rectangle size")
	}
	if int(rect.X)+int(rect.Width) > s.width || int(rect.Y)+int(rect.Height) > s.height {
		return fmt.Errorf("rfbtest: rectangle %d,%d %dx%d outside %dx%d",
			rect.X, rect.Y, rect.Width, rect.Height, s.width, s.height)
	}
	switch rect.Encoding {
	case rfb.EncodingRaw, rfb.EncodingRRE, rfb.EncodingHextile, rfb.EncodingZRLE:
	case rfb.EncodingCopyRect:
		if int(rect.SrcX)+int(rect.Width) > s.width || int(rect.SrcY)+int(rect.Height) > s.height {
			return fmt.Errorf("rfbtest: CopyRect source %d,%d %dx%d outside %dx%d",
				rect.SrcX, rect.SrcY, rect.Width, rect.Height, s.width, s.height)
		}
	default:
		return fmt.Errorf("rfbtest: unsupported encoding %d", rect.Encoding)
	}
	return nil
}

// Received returns a copy of the client messages recorded so far.
func (s *Server) Received() Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	received := s.received
	received.Encodings = append([]rfb.Encoding(nil), s.received.Encodings...)
	received.Requests = append([]UpdateRequest(nil), s.received.Requests...)
	received.Keys = append([]KeyEvent(nil), s.received.Keys...)
	received.ExtendedKeys = append([]ExtendedKeyEvent(nil), s.received.ExtendedKeys...)
	received.Pointers = append([]PointerEvent(nil), s.received.Pointers...)
	received.CutTexts = append([]string(nil), s.received.CutTexts...)
	received.Continuous = append([]ContinuousUpdate(nil), s.received.Continuous...)
	received.Fences = append([]rfb.Fence(nil), s.received.Fences...)
	for i := range received.Fences {
		received.Fences[i].Payload = append([]byte(nil), s.received.Fences[i].Payload...)
	}
	received.DesktopSizes = append([]DesktopSizeRequest(nil), s.received.DesktopSizes...)
	for i := range received.DesktopSizes {
		received.DesktopSizes[i].Screens = append([]rfb.Screen(nil), s.received.DesktopSizes[i].Screens...)
	}
	return received
}

// Serve performs one server-side RFB session on conn.
func (s *Server) Serve(conn io.ReadWriter) error {
	if s.err != nil {
		return s.err
	}
	var versionWire [12]byte
	if err := writeAll(conn, []byte(s.cfg.Version.Wire())); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, versionWire[:]); err != nil {
		return err
	}
	clientVersion, err := rfb.ParseVersion(versionWire[:])
	if err != nil {
		return err
	}
	version, err := rfb.NegotiateVersionPair(s.cfg.Version, clientVersion)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.received.ClientVersion = clientVersion
	s.mu.Unlock()

	stream, err := s.handshakeSecurity(conn, version, s.securityTypes())
	if err != nil {
		return err
	}
	conn = stream

	var shared [1]byte
	if _, err := io.ReadFull(conn, shared[:]); err != nil {
		return err
	}
	s.mu.Lock()
	s.received.Shared = shared[0] != 0
	s.mu.Unlock()

	if err := s.writeServerInit(conn); err != nil {
		return err
	}

	for {
		var messageType [1]byte
		if _, err := io.ReadFull(conn, messageType[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch messageType[0] {
		case 0:
			if err := s.readSetPixelFormat(conn); err != nil {
				return err
			}
		case 2:
			if err := s.readSetEncodings(conn); err != nil {
				return err
			}
		case 3:
			request, err := s.readUpdateRequest(conn)
			if err != nil {
				return err
			}
			if err := s.sendUpdate(conn, request.Incremental); err != nil {
				return err
			}
		case 4:
			if err := s.readKey(conn); err != nil {
				return err
			}
		case 5:
			if err := s.readPointer(conn); err != nil {
				return err
			}
		case 6:
			if err := s.readCutText(conn); err != nil {
				return err
			}
		case 150:
			if err := s.readContinuousUpdate(conn); err != nil {
				return err
			}
		case 248:
			if err := s.readFence(conn); err != nil {
				return err
			}
		case 251:
			if err := s.readSetDesktopSize(conn); err != nil {
				return err
			}
		case 255:
			if err := s.readQEMU(conn); err != nil {
				return err
			}
		default:
			return fmt.Errorf("rfbtest: unknown client message type %d", messageType[0])
		}
	}
}

// securityTypes returns the offered list. An explicit list wins; otherwise a
// configured password selects VNC authentication and no password selects None.
func (s *Server) securityTypes() []rfb.SecurityType {
	if len(s.cfg.SecurityTypes) > 0 {
		return append([]rfb.SecurityType(nil), s.cfg.SecurityTypes...)
	}
	if s.cfg.Password != "" {
		return []rfb.SecurityType{rfb.SecurityVNCAuth}
	}
	return []rfb.SecurityType{rfb.SecurityNone}
}

// handshakeSecurity offers the configured security types and completes the
// selected exchange. RFB 3.3 has one type and no client selection; 3.7 and 3.8
// send a list and read the client's choice. It returns the stream the rest of
// the protocol runs on, which a TLS or encrypted RA2 handshake may replace.
func (s *Server) handshakeSecurity(conn io.ReadWriter, version rfb.Version, offered []rfb.SecurityType) (io.ReadWriter, error) {
	if len(offered) == 0 {
		return nil, fmt.Errorf("rfbtest: no security types configured")
	}
	var selected rfb.SecurityType
	if version.Minor < rfb.Version3_7.Minor {
		selected = offered[0]
		var wire [4]byte
		binary.BigEndian.PutUint32(wire[:], uint32(selected))
		if err := writeAll(conn, wire[:]); err != nil {
			return nil, err
		}
	} else {
		wire := make([]byte, 1+len(offered))
		wire[0] = byte(len(offered))
		for i, security := range offered {
			wire[1+i] = byte(security)
		}
		if err := writeAll(conn, wire); err != nil {
			return nil, err
		}
		var selection [1]byte
		if _, err := io.ReadFull(conn, selection[:]); err != nil {
			return nil, err
		}
		selected = rfb.SecurityType(selection[0])
		if !containsSecurity(offered, selected) {
			_ = writeSecurityResult(conn, version, false, "unsupported security type")
			return nil, fmt.Errorf("rfbtest: client selected security type %d", selected)
		}
	}
	s.mu.Lock()
	s.received.Security = selected
	s.received.SecurityChoice = rfb.SecurityChoice{Type: selected}
	s.mu.Unlock()
	return s.runSecurity(conn, version, selected)
}

// runSecurity performs one selected security exchange and returns the stream
// the rest of the handshake must use.
func (s *Server) runSecurity(conn io.ReadWriter, version rfb.Version, selected rfb.SecurityType) (io.ReadWriter, error) {
	switch selected {
	case rfb.SecurityNone:
		// RFB 3.7 with None has no SecurityResult (RFC 6143 A.2); 3.8 does.
		if version.Minor >= rfb.Version3_8.Minor {
			if err := writeSecurityResult(conn, version, true, ""); err != nil {
				return nil, err
			}
		}
		return conn, nil
	case rfb.SecurityVNCAuth:
		if err := s.exchangeVNCAuth(conn); err != nil {
			_ = writeSecurityResult(conn, version, false, "authentication failed")
			return nil, err
		}
		return conn, writeSecurityResult(conn, version, true, "")
	case rfb.SecurityAppleDH:
		if err := s.exchangeAppleDH(conn); err != nil {
			_ = writeSecurityResult(conn, version, false, "authentication failed")
			return nil, err
		}
		return conn, writeSecurityResult(conn, version, true, "")
	case rfb.SecurityVeNCrypt:
		return s.exchangeVeNCrypt(conn, version)
	case rfb.SecurityRA2, rfb.SecurityRA2ne, rfb.SecurityRA2_256, rfb.SecurityRA2ne_256:
		return s.exchangeRA2(conn, version, selected)
	default:
		_ = writeSecurityResult(conn, version, false, "unsupported security type")
		return nil, fmt.Errorf("rfbtest: server cannot perform security type %d", selected)
	}
}

// vncTestChallenge is the deterministic challenge the in-process server uses,
// so a wrong-password test compares against a stable value.
var vncTestChallenge = [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// exchangeVNCAuth sends the challenge, reads the client's response and
// compares it with the response the configured password yields.
func (s *Server) exchangeVNCAuth(conn io.ReadWriter) error {
	if err := writeAll(conn, vncTestChallenge[:]); err != nil {
		return err
	}
	var response [16]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return err
	}
	expected, err := rfb.VNCAuthResponse(s.cfg.Password, vncTestChallenge[:])
	if err != nil {
		return err
	}
	if response != expected {
		return fmt.Errorf("rfbtest: VNC authentication failed")
	}
	s.mu.Lock()
	s.received.VNCAuthAccepted = true
	s.mu.Unlock()
	return nil
}

// writeSecurityResult writes a status word and, on failure where the version
// carries one, a reason string. RFB 3.7 omits the reason.
func writeSecurityResult(w io.Writer, version rfb.Version, success bool, reason string) error {
	var wire [4]byte
	if !success {
		binary.BigEndian.PutUint32(wire[:], 1)
	}
	if err := writeAll(w, wire[:]); err != nil {
		return err
	}
	if success || version.Minor == rfb.Version3_7.Minor || reason == "" {
		return nil
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(reason)))
	if err := writeAll(w, length[:]); err != nil {
		return err
	}
	return writeAll(w, []byte(reason))
}

func containsSecurity(offered []rfb.SecurityType, want rfb.SecurityType) bool {
	for _, security := range offered {
		if security == want {
			return true
		}
	}
	return false
}

func (s *Server) writeServerInit(conn io.Writer) error {
	var header [24]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(s.width))
	binary.BigEndian.PutUint16(header[2:4], uint16(s.height))
	var format bytes.Buffer
	if _, err := s.pf.WriteTo(&format); err != nil {
		return err
	}
	copy(header[4:20], format.Bytes())
	binary.BigEndian.PutUint32(header[20:24], uint32(len(s.cfg.DesktopName)))
	if err := writeAll(conn, header[:]); err != nil {
		return err
	}
	return writeAll(conn, []byte(s.cfg.DesktopName))
}

func (s *Server) readSetPixelFormat(conn io.Reader) error {
	var payload [19]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		return err
	}
	format, err := rfb.ReadPixelFormat(bytes.NewReader(payload[3:]))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.received.PixelFormat = format
	s.mu.Unlock()
	if !s.cfg.ForcePixelFormat {
		if err := format.Validate(); err != nil {
			return err
		}
		s.pf = format
	}
	return nil
}

func (s *Server) readSetEncodings(conn io.ReadWriter) error {
	var header [3]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	count := int(binary.BigEndian.Uint16(header[1:3]))
	if count > 1<<12 {
		return fmt.Errorf("rfbtest: %d encodings", count)
	}
	payload := make([]byte, 4*count)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return err
	}
	encodings := make([]rfb.Encoding, count)
	for i := range encodings {
		encodings[i] = rfb.Encoding(int32(binary.BigEndian.Uint32(payload[4*i:])))
	}
	s.mu.Lock()
	s.received.Encodings = encodings
	s.mu.Unlock()
	return s.acknowledgeExtensions(conn, encodings)
}

// acknowledgeExtensions answers the pseudo-encodings the client advertised, as
// the extensions require: EndOfContinuousUpdates announces ContinuousUpdates
// and a ServerFence announces Fence.
func (s *Server) acknowledgeExtensions(conn io.ReadWriter, encodings []rfb.Encoding) error {
	advertised := func(want rfb.Encoding) bool {
		for _, encoding := range encodings {
			if encoding == want {
				return true
			}
		}
		return false
	}
	s.mu.Lock()
	continuous := s.cfg.ContinuousUpdates && advertised(rfb.EncodingContinuousUpdates) && !s.sentEnd
	if continuous {
		s.sentEnd = true
	}
	fence := s.cfg.Fence && advertised(rfb.EncodingFence) && !s.sentFence
	if fence {
		s.sentFence = true
	}
	extKey := s.cfg.ExtendedKeyEvent && advertised(rfb.EncodingExtendedKeyEvent) && !s.sentExtKey
	if extKey {
		s.sentExtKey = true
	}
	s.mu.Unlock()
	if continuous {
		if err := writeAll(conn, []byte{byte(rfb.ServerEndOfContinuousUpdates)}); err != nil {
			return err
		}
	}
	if fence {
		flags := rfb.FenceRequest | rfb.FenceBlockBefore
		if s.cfg.FenceFlags != 0 {
			flags = s.cfg.FenceFlags | rfb.FenceRequest
		}
		if err := writeServerFence(conn, rfb.Fence{
			Flags:   flags,
			Payload: []byte("rfbtest"),
		}); err != nil {
			return err
		}
	}
	if extKey {
		if err := s.sendExtendedKeyEventAck(conn); err != nil {
			return err
		}
	}
	for i := 0; i < s.cfg.FenceBurst; i++ {
		if err := writeServerFence(conn, rfb.Fence{
			Flags:   rfb.FenceRequest,
			Payload: []byte("burst"),
		}); err != nil {
			return err
		}
		// Read one client message so its reply can make progress; it is
		// recorded by the same handlers the main loop uses.
		if err := s.readBurstMessage(conn); err != nil {
			return err
		}
	}
	return nil
}

// readBurstMessage consumes one client message during the fence burst without
// sending a frame, so reading cannot block the burst on a peer that is not
// requesting updates.
func (s *Server) readBurstMessage(conn io.ReadWriter) error {
	var messageType [1]byte
	if _, err := io.ReadFull(conn, messageType[:]); err != nil {
		return err
	}
	switch messageType[0] {
	case 0:
		return s.readSetPixelFormat(conn)
	case 2:
		return s.readSetEncodings(conn)
	case 3:
		_, err := s.readUpdateRequest(conn)
		return err
	case 4:
		return s.readKey(conn)
	case 5:
		return s.readPointer(conn)
	case 6:
		return s.readCutText(conn)
	case 150:
		return s.readContinuousUpdate(conn)
	case 248:
		return s.readFence(conn)
	case 251:
		return s.readSetDesktopSize(conn)
	case 255:
		return s.readQEMU(conn)
	default:
		return fmt.Errorf("rfbtest: unknown client message type %d", messageType[0])
	}
}

func (s *Server) readUpdateRequest(conn io.Reader) (UpdateRequest, error) {
	var payload [9]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		return UpdateRequest{}, err
	}
	request := UpdateRequest{
		Incremental: payload[0] != 0,
		X:           binary.BigEndian.Uint16(payload[1:3]),
		Y:           binary.BigEndian.Uint16(payload[3:5]),
		Width:       binary.BigEndian.Uint16(payload[5:7]),
		Height:      binary.BigEndian.Uint16(payload[7:9]),
	}
	s.mu.Lock()
	s.received.Requests = append(s.received.Requests, request)
	s.mu.Unlock()
	return request, nil
}

func (s *Server) readContinuousUpdate(conn io.ReadWriter) error {
	var payload [9]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		return err
	}
	update := ContinuousUpdate{
		Enable: payload[0] != 0,
		X:      binary.BigEndian.Uint16(payload[1:3]),
		Y:      binary.BigEndian.Uint16(payload[3:5]),
		Width:  binary.BigEndian.Uint16(payload[5:7]),
		Height: binary.BigEndian.Uint16(payload[7:9]),
	}
	s.mu.Lock()
	s.received.Continuous = append(s.received.Continuous, update)
	s.mu.Unlock()
	if update.Enable && s.cfg.ContinuousUpdates {
		// Continuous updates mean the server sends frames without a new
		// FramebufferUpdateRequest; one suffices to prove the path.
		if err := s.sendFrame(conn, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) readFence(conn io.ReadWriter) error {
	fence, err := readFencePayload(conn)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.received.Fences = append(s.received.Fences, fence)
	s.mu.Unlock()
	if fence.Request() {
		response := rfb.Fence{
			Flags:   fence.Flags &^ rfb.FenceRequest,
			Payload: append([]byte(nil), fence.Payload...),
		}
		return writeServerFence(conn, response)
	}
	return nil
}

func readFencePayload(conn io.Reader) (rfb.Fence, error) {
	var header [8]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return rfb.Fence{}, err
	}
	flags := binary.BigEndian.Uint32(header[3:7])
	length := int(header[7])
	if length > rfb.FenceMaxPayload {
		return rfb.Fence{}, fmt.Errorf("rfbtest: fence payload is %d bytes", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return rfb.Fence{}, err
	}
	return rfb.Fence{Flags: flags, Payload: payload}, nil
}

func writeServerFence(conn io.Writer, fence rfb.Fence) error {
	var header [9]byte
	header[0] = byte(rfb.ServerFenceMessage)
	binary.BigEndian.PutUint32(header[4:8], fence.Flags)
	header[8] = byte(len(fence.Payload))
	if err := writeAll(conn, header[:]); err != nil {
		return err
	}
	return writeAll(conn, fence.Payload)
}

func (s *Server) readSetDesktopSize(conn io.ReadWriter) error {
	var header [7]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	request := DesktopSizeRequest{
		Width:  binary.BigEndian.Uint16(header[1:3]),
		Height: binary.BigEndian.Uint16(header[3:5]),
	}
	count := int(header[5])
	payload := make([]byte, count*16)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return err
	}
	request.Screens = make([]rfb.Screen, count)
	for i := range request.Screens {
		wire := payload[i*16:]
		request.Screens[i] = rfb.Screen{
			ID:     binary.BigEndian.Uint32(wire[0:4]),
			X:      binary.BigEndian.Uint16(wire[4:6]),
			Y:      binary.BigEndian.Uint16(wire[6:8]),
			Width:  binary.BigEndian.Uint16(wire[8:10]),
			Height: binary.BigEndian.Uint16(wire[10:12]),
			Flags:  binary.BigEndian.Uint32(wire[12:16]),
		}
	}
	s.mu.Lock()
	s.received.DesktopSizes = append(s.received.DesktopSizes, request)
	s.mu.Unlock()
	if !s.cfg.ExtendedDesktopSize {
		return nil
	}
	if s.cfg.DesktopSizeStatus != 0 {
		// Answer with the current desktop and the configured status. For a
		// refusal this is final; for status 4 with follow-up the server later
		// adopts the request, as QEMU does after the guest boots.
		if err := s.sendExtendedDesktopSize(conn, 1, s.cfg.DesktopSizeStatus, uint16(s.width), uint16(s.height), s.screenLayout()); err != nil {
			return err
		}
		if s.cfg.DesktopSizeStatus == 4 && s.cfg.DesktopSizeFollowUp {
			if err := s.resizeFrame(int(request.Width), int(request.Height)); err != nil {
				return err
			}
			s.adoptScreens(request)
			if err := s.sendExtendedDesktopSize(conn, 0, 0, request.Width, request.Height, s.screenLayout()); err != nil {
				return err
			}
			return s.sendFrame(conn, false)
		}
		return nil
	}
	if err := s.resizeFrame(int(request.Width), int(request.Height)); err != nil {
		return err
	}
	s.adoptScreens(request)
	if err := s.sendExtendedDesktopSize(conn, 1, 0, request.Width, request.Height, s.screenLayout()); err != nil {
		return err
	}
	// A following data update lets the client repaint at the new size.
	return s.sendFrame(conn, false)
}

// adoptScreens records the layout the client asked for, unless the server
// models one that resizes its desktop but ignores the screen list.
func (s *Server) adoptScreens(request DesktopSizeRequest) {
	if s.cfg.IgnoreScreens || len(request.Screens) == 0 {
		s.screens = nil
		return
	}
	s.screens = append([]rfb.Screen(nil), request.Screens...)
}

// resizeFrame reallocates the canned frame for a requested desktop size,
// keeping the old pixels in the top-left corner.
func (s *Server) resizeFrame(width, height int) error {
	if width <= 0 || height <= 0 || width > 0xffff || height > 0xffff {
		return fmt.Errorf("rfbtest: invalid desktop size %dx%d", width, height)
	}
	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(resized, resized.Bounds(), s.frame, s.frame.Bounds().Min, draw.Src)
	s.frame = resized
	s.width, s.height = width, height
	return nil
}

// sendExtendedKeyEventAck advertises QEMU's Extended Key Event support the way
// QEMU does: a framebuffer update holding one pseudo-rectangle covering the
// whole surface, in place of any pixel payload.
func (s *Server) sendExtendedKeyEventAck(conn io.Writer) error {
	var update bytes.Buffer
	var updateHeader [4]byte
	binary.BigEndian.PutUint16(updateHeader[2:4], 1)
	update.Write(updateHeader[:])
	var rect [12]byte
	binary.BigEndian.PutUint16(rect[4:6], uint16(s.width))
	binary.BigEndian.PutUint16(rect[6:8], uint16(s.height))
	extendedKeyEncoding := int32(rfb.EncodingExtendedKeyEvent)
	binary.BigEndian.PutUint32(rect[8:12], uint32(extendedKeyEncoding))
	update.Write(rect[:])
	return writeAll(conn, update.Bytes())
}

// readQEMU records one QEMU client message. Only the Extended Key Event
// submessage is defined for this client; another submessage is a protocol
// error, so a desynchronised stream fails instead of being misread.
func (s *Server) readQEMU(conn io.Reader) error {
	var header [11]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if header[0] != 0 {
		return fmt.Errorf("rfbtest: unknown QEMU submessage %d", header[0])
	}
	event := ExtendedKeyEvent{
		Down:    binary.BigEndian.Uint16(header[1:3]) != 0,
		Keysym:  binary.BigEndian.Uint32(header[3:7]),
		Keycode: binary.BigEndian.Uint32(header[7:11]),
	}
	s.mu.Lock()
	s.received.ExtendedKeys = append(s.received.ExtendedKeys, event)
	s.mu.Unlock()
	return nil
}

func (s *Server) readKey(conn io.Reader) error {
	var payload [7]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		return err
	}
	event := KeyEvent{Down: payload[0] != 0, Keysym: binary.BigEndian.Uint32(payload[3:7])}
	s.mu.Lock()
	s.received.Keys = append(s.received.Keys, event)
	s.mu.Unlock()
	return nil
}

func (s *Server) readPointer(conn io.Reader) error {
	var payload [5]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		return err
	}
	event := PointerEvent{
		Mask: payload[0],
		X:    binary.BigEndian.Uint16(payload[1:3]),
		Y:    binary.BigEndian.Uint16(payload[3:5]),
	}
	s.mu.Lock()
	s.received.Pointers = append(s.received.Pointers, event)
	s.mu.Unlock()
	return nil
}

func (s *Server) readCutText(conn io.Reader) error {
	var header [7]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[3:7])
	if length > 1<<20 {
		return fmt.Errorf("rfbtest: cut text is %d bytes", length)
	}
	text := make([]byte, length)
	if _, err := io.ReadFull(conn, text); err != nil {
		return err
	}
	s.mu.Lock()
	s.received.CutTexts = append(s.received.CutTexts, string(text))
	s.mu.Unlock()
	return nil
}

// sendUpdate answers one FramebufferUpdateRequest. A server must answer every
// non-incremental request with an ExtendedDesktopSize rectangle naming its
// current desktop, even when nothing changed; that is how a client learns the
// extension is supported and what the current layout is. The rectangle must be
// the only one in its update, so it is sent on its own before the framebuffer
// data.
func (s *Server) sendUpdate(conn io.Writer, incremental bool) error {
	if s.cfg.ExtendedDesktopSize && !incremental && s.advertised(rfb.EncodingExtendedDesktopSize) {
		if err := s.sendExtendedDesktopSize(conn, 0, 0, uint16(s.width), uint16(s.height), s.screenLayout()); err != nil {
			return err
		}
	}
	return s.sendFrame(conn, incremental)
}

// screenLayout returns the layout the server reports: its stored layout, or one
// screen covering the whole framebuffer.
func (s *Server) screenLayout() []rfb.Screen {
	if len(s.screens) > 0 {
		return append([]rfb.Screen(nil), s.screens...)
	}
	return []rfb.Screen{{Width: uint16(s.width), Height: uint16(s.height)}}
}

func (s *Server) sendFrame(conn io.Writer, incremental bool) error {
	plan := s.cfg.Rectangles
	if len(plan) == 0 {
		plan = []Rectangle{{
			Width: uint16(s.width), Height: uint16(s.height), Encoding: s.cfg.Encoding,
		}}
	}
	if err := s.checkAdvertised(plan); err != nil {
		return err
	}

	if s.cfg.SendBell && !s.sentBell {
		s.sentBell = true
		if err := writeAll(conn, []byte{byte(rfb.ServerBell), 0}); err != nil {
			return err
		}
	}
	if s.cfg.CutText != "" && !s.sentCut {
		s.sentCut = true
		if err := writeServerCutText(conn, s.cfg.CutText); err != nil {
			return err
		}
	}

	s.mu.Lock()
	sendCursor := (s.cfg.Cursor != nil || s.cfg.HideCursor) && !s.sentCursor && s.advertisedLocked(rfb.EncodingCursorWithAlpha)
	if sendCursor {
		s.sentCursor = true
	}
	s.mu.Unlock()

	var update bytes.Buffer
	var header [4]byte
	count := len(plan)
	if sendCursor {
		count++
	}
	if s.cfg.ExtraEncoding != 0 {
		count++
	}
	binary.BigEndian.PutUint16(header[2:4], uint16(count))
	update.Write(header[:])
	if sendCursor {
		if err := s.writeCursorRectangle(&update); err != nil {
			return err
		}
	}
	for _, rect := range plan {
		if err := s.writeRectangle(&update, rect); err != nil {
			return err
		}
	}
	if s.cfg.ExtraEncoding != 0 {
		// One in-bounds 1x1 rectangle in an encoding the client must refuse.
		var extra [12]byte
		binary.BigEndian.PutUint16(extra[4:6], 1)
		binary.BigEndian.PutUint16(extra[6:8], 1)
		binary.BigEndian.PutUint32(extra[8:12], uint32(int32(s.cfg.ExtraEncoding)))
		update.Write(extra[:])
	}
	return writeAll(conn, update.Bytes())
}

// sendExtendedDesktopSize writes one update holding only an
// ExtendedDesktopSize pseudo-rectangle. reason and status are the header's
// x-position and y-position.
func (s *Server) sendExtendedDesktopSize(conn io.Writer, reason, status uint8, width, height uint16, screens []rfb.Screen) error {
	var update bytes.Buffer
	var updateHeader [4]byte
	binary.BigEndian.PutUint16(updateHeader[2:4], 1)
	update.Write(updateHeader[:])
	var header [12]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(reason))
	binary.BigEndian.PutUint16(header[2:4], uint16(status))
	binary.BigEndian.PutUint16(header[4:6], width)
	binary.BigEndian.PutUint16(header[6:8], height)
	extendedEncoding := int32(rfb.EncodingExtendedDesktopSize)
	binary.BigEndian.PutUint32(header[8:12], uint32(extendedEncoding))
	update.Write(header[:])
	payload := make([]byte, 4+len(screens)*16)
	payload[0] = byte(len(screens))
	for i, screen := range screens {
		offset := 4 + i*16
		binary.BigEndian.PutUint32(payload[offset:offset+4], screen.ID)
		binary.BigEndian.PutUint16(payload[offset+4:offset+6], screen.X)
		binary.BigEndian.PutUint16(payload[offset+6:offset+8], screen.Y)
		binary.BigEndian.PutUint16(payload[offset+8:offset+10], screen.Width)
		binary.BigEndian.PutUint16(payload[offset+10:offset+12], screen.Height)
		binary.BigEndian.PutUint32(payload[offset+12:offset+16], screen.Flags)
	}
	update.Write(payload)
	return writeAll(conn, update.Bytes())
}

// writeCursorRectangle writes one CursorWithAlpha pseudo-rectangle in the raw
// 32-bit RGBA form the client accepts.
func (s *Server) writeCursorRectangle(update *bytes.Buffer) error {
	cursor := s.cfg.Cursor
	width, height := 0, 0
	hotspot := s.cfg.CursorAt
	if cursor != nil && !s.cfg.HideCursor {
		bounds := cursor.Bounds()
		width, height = bounds.Dx(), bounds.Dy()
	}
	var header [12]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(hotspot.X))
	binary.BigEndian.PutUint16(header[2:4], uint16(hotspot.Y))
	binary.BigEndian.PutUint16(header[4:6], uint16(width))
	binary.BigEndian.PutUint16(header[6:8], uint16(height))
	cursorEncoding := int32(rfb.EncodingCursorWithAlpha)
	binary.BigEndian.PutUint32(header[8:12], uint32(cursorEncoding))
	update.Write(header[:])
	var encoding [4]byte
	binary.BigEndian.PutUint32(encoding[:], uint32(rfb.EncodingRaw))
	update.Write(encoding[:])
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			update.Write(encodeCursorPixel(cursor.RGBAAt(x, y)))
		}
	}
	return nil
}

// encodeCursorPixel writes the pre-multiplied RGBA byte order the
// CursorWithAlpha extension specifies.
func encodeCursorPixel(c color.RGBA) []byte {
	premultiply := func(value, alpha uint8) uint8 {
		return uint8(uint32(value) * uint32(alpha) / 255)
	}
	return []byte{
		premultiply(c.R, c.A), premultiply(c.G, c.A), premultiply(c.B, c.A), c.A,
	}
}

func (s *Server) advertised(encoding rfb.Encoding) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.advertisedLocked(encoding)
}

func (s *Server) advertisedLocked(encoding rfb.Encoding) bool {
	for _, advertised := range s.received.Encodings {
		if advertised == encoding {
			return true
		}
	}
	return false
}

func (s *Server) checkAdvertised(plan []Rectangle) error {
	s.mu.Lock()
	encodings := append([]rfb.Encoding(nil), s.received.Encodings...)
	s.mu.Unlock()
	for _, rect := range plan {
		advertised := false
		for _, encoding := range encodings {
			if encoding == rect.Encoding {
				advertised = true
				break
			}
		}
		if !advertised {
			return fmt.Errorf("rfbtest: client did not advertise encoding %d", rect.Encoding)
		}
	}
	return nil
}

func (s *Server) writeRectangle(update *bytes.Buffer, rect Rectangle) error {
	var header [12]byte
	binary.BigEndian.PutUint16(header[0:2], rect.X)
	binary.BigEndian.PutUint16(header[2:4], rect.Y)
	binary.BigEndian.PutUint16(header[4:6], rect.Width)
	binary.BigEndian.PutUint16(header[6:8], rect.Height)
	binary.BigEndian.PutUint32(header[8:12], uint32(rect.Encoding))
	update.Write(header[:])

	switch rect.Encoding {
	case rfb.EncodingRaw:
		return s.writeRaw(update, rect)
	case rfb.EncodingCopyRect:
		var source [4]byte
		binary.BigEndian.PutUint16(source[0:2], rect.SrcX)
		binary.BigEndian.PutUint16(source[2:4], rect.SrcY)
		update.Write(source[:])
		return nil
	case rfb.EncodingRRE:
		return s.writeRRE(update, rect)
	case rfb.EncodingHextile:
		return s.writeHextile(update, rect)
	case rfb.EncodingZRLE:
		return s.writeZRLE(update, rect)
	default:
		return fmt.Errorf("rfbtest: unsupported encoding %d", rect.Encoding)
	}
}

// writeZRLE encodes one rectangle into the connection's persistent zlib
// stream. Each tile is sent solid when it is one colour and raw otherwise, so
// the test server exercises both the compact and the general path.
func (s *Server) writeZRLE(update *bytes.Buffer, rect Rectangle) error {
	if s.zrleWriter == nil {
		s.zrleWriter = zlib.NewWriter(&s.zrleBuf)
	}
	writePixel := func(c color.RGBA) error {
		encoded, err := s.pf.EncodeCPixel(c)
		if err != nil {
			return err
		}
		_, err = s.zrleWriter.Write(encoded)
		return err
	}
	for tileY := 0; tileY < int(rect.Height); tileY += 64 {
		height := min(64, int(rect.Height)-tileY)
		for tileX := 0; tileX < int(rect.Width); tileX += 64 {
			width := min(64, int(rect.Width)-tileX)
			solid := true
			first := s.pixel(int(rect.X)+tileX, int(rect.Y)+tileY)
			for row := 0; row < height && solid; row++ {
				for column := 0; column < width; column++ {
					if s.pixel(int(rect.X)+tileX+column, int(rect.Y)+tileY+row) != first {
						solid = false
						break
					}
				}
			}
			if solid {
				if _, err := s.zrleWriter.Write([]byte{1}); err != nil {
					return err
				}
				if err := writePixel(first); err != nil {
					return err
				}
				continue
			}
			if _, err := s.zrleWriter.Write([]byte{0}); err != nil {
				return err
			}
			for row := 0; row < height; row++ {
				for column := 0; column < width; column++ {
					pixel := s.pixel(int(rect.X)+tileX+column, int(rect.Y)+tileY+row)
					if err := writePixel(pixel); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := s.zrleWriter.Flush(); err != nil {
		return err
	}
	compressed := append([]byte(nil), s.zrleBuf.Bytes()...)
	s.zrleBuf.Reset()
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(compressed)))
	update.Write(length[:])
	update.Write(compressed)
	return nil
}

func (s *Server) writeRaw(update *bytes.Buffer, rect Rectangle) error {
	for y := int(rect.Y); y < int(rect.Y)+int(rect.Height); y++ {
		for x := int(rect.X); x < int(rect.X)+int(rect.Width); x++ {
			if err := s.writePixel(update, x, y); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) writeRRE(update *bytes.Buffer, rect Rectangle) error {
	background := s.pixel(int(rect.X), int(rect.Y))
	type subrect struct {
		color      color.RGBA
		x, y, w, h uint16
	}
	var subrects []subrect
	for row := 0; row < int(rect.Height); row++ {
		y := int(rect.Y) + row
		for x := 0; x < int(rect.Width); {
			pixel := s.pixel(int(rect.X)+x, y)
			if pixel == background {
				x++
				continue
			}
			start := x
			for x < int(rect.Width) && s.pixel(int(rect.X)+x, y) == pixel {
				x++
			}
			subrects = append(subrects, subrect{
				color: pixel,
				x:     uint16(start),
				y:     uint16(row),
				w:     uint16(x - start),
				h:     1,
			})
		}
	}
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(subrects)))
	update.Write(count[:])
	if err := s.writePixelColor(update, background); err != nil {
		return err
	}
	for _, sub := range subrects {
		if err := s.writePixelColor(update, sub.color); err != nil {
			return err
		}
		var geometry [8]byte
		binary.BigEndian.PutUint16(geometry[0:2], sub.x)
		binary.BigEndian.PutUint16(geometry[2:4], sub.y)
		binary.BigEndian.PutUint16(geometry[4:6], sub.w)
		binary.BigEndian.PutUint16(geometry[6:8], sub.h)
		update.Write(geometry[:])
	}
	return nil
}

func (s *Server) writeHextile(update *bytes.Buffer, rect Rectangle) error {
	for tileY := 0; tileY < int(rect.Height); tileY += 16 {
		height := min(16, int(rect.Height)-tileY)
		for tileX := 0; tileX < int(rect.Width); tileX += 16 {
			width := min(16, int(rect.Width)-tileX)
			update.WriteByte(1) // Raw tile.
			for row := 0; row < height; row++ {
				for column := 0; column < width; column++ {
					x := int(rect.X) + tileX + column
					y := int(rect.Y) + tileY + row
					if err := s.writePixel(update, x, y); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (s *Server) writePixel(update *bytes.Buffer, x, y int) error {
	return s.writePixelColor(update, s.pixel(x, y))
}

func (s *Server) writePixelColor(update *bytes.Buffer, c color.RGBA) error {
	encoded, err := s.pf.Encode(c)
	if err != nil {
		return err
	}
	update.Write(encoded)
	return nil
}

func (s *Server) pixel(x, y int) color.RGBA {
	return color.RGBAModel.Convert(s.frame.At(x, y)).(color.RGBA)
}

func writeServerCutText(w io.Writer, text string) error {
	var header [8]byte
	header[0] = byte(rfb.ServerCutText)
	binary.BigEndian.PutUint32(header[4:8], uint32(len(text)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, []byte(text))
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
