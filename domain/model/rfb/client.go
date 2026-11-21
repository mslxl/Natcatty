package rfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Client-to-server message types added by the extensions. EnableContinuousUpdates
// and EndOfContinuousUpdates share a number because they travel in opposite
// directions.
const (
	clientEnableContinuousUpdates = 150
	clientFence                   = 248
	clientSetDesktopSize          = 251
)

// ClientConfig configures the client handshake.
type ClientConfig struct {
	// Shared requests a shared session (ClientInit shared-flag 1). A server
	// may disconnect other clients when it is false.
	Shared bool
	// Password is the VNC password used by security type 2. It is plaintext
	// in memory only. An empty value fails with ErrMissingPassword when the
	// server selects VNC authentication instead of sending a guess.
	Password string
}

// ServerInfo is the ServerInit message.
type ServerInfo struct {
	Width       uint16
	Height      uint16
	PixelFormat PixelFormat
	DesktopName string
}

// Client is one RFB connection after the ServerInit handshake. It owns the
// framebuffer and holds no lock; callers must observe this concurrency model:
//
//   - ReadMessage is the only receive path. Exactly one goroutine may call it,
//     and only that goroutine may read or copy Frame's pixels.
//   - The write methods (SetPixelFormat, SetEncodings, RequestUpdate,
//     RequestFullUpdate, SendKey, SendPointer, SendCutText, SendFence,
//     SendContinuousUpdates, SendSetDesktopSize) may be called from any
//     goroutine, but the caller must serialise them so at most one write is in
//     flight at a time.
//   - Reads and writes may run concurrently. Version and ServerInfo are
//     immutable after Connect, so a writer may read them while the reader
//     decodes. Frame's pixels change on every FramebufferUpdate and its pixel
//     format changes on SetPixelFormat, so neither may be read concurrently
//     with ReadMessage; Bounds, Image and PixelFormat are safe to read from
//     any goroutine.
//
// The Client never closes conn; closing it is the caller's responsibility and
// is not coordinated with an in-flight read or write.
type Client struct {
	conn     io.ReadWriter
	reader   *bufio.Reader
	version  Version
	security SecurityType
	info     ServerInfo
	frame    *Framebuffer
}

// Connect performs the version, security and initialisation handshakes, reads
// ServerInit, and sends the 32-bit true-colour SetPixelFormat and the
// supported SetEncodings. The caller owns conn and must close it.
func Connect(conn io.ReadWriter, cfg ClientConfig) (*Client, error) {
	reader := bufio.NewReader(conn)

	var versionWire [12]byte
	if _, err := io.ReadFull(reader, versionWire[:]); err != nil {
		return nil, fmt.Errorf("%w: reading server version: %w", ErrHandshake, err)
	}
	serverVersion, err := ParseVersion(versionWire[:])
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	version, err := NegotiateVersion(serverVersion)
	if err != nil {
		return nil, err
	}
	if err := writeAll(conn, []byte(version.Wire())); err != nil {
		return nil, fmt.Errorf("%w: writing client version: %w", ErrHandshake, err)
	}
	security, err := clientSecurity(conn, reader, version, cfg.Password)
	if err != nil {
		return nil, err
	}

	shared := byte(0)
	if cfg.Shared {
		shared = 1
	}
	if err := writeAll(conn, []byte{shared}); err != nil {
		return nil, fmt.Errorf("%w: writing ClientInit: %w", ErrHandshake, err)
	}

	info, err := readServerInit(reader)
	if err != nil {
		return nil, err
	}
	frame, err := NewFramebuffer(int(info.Width), int(info.Height), DefaultPixelFormat())
	if err != nil {
		return nil, err
	}
	client := &Client{
		conn:     conn,
		reader:   reader,
		version:  version,
		security: security,
		info:     info,
		frame:    frame,
	}
	if err := client.SetPixelFormat(DefaultPixelFormat()); err != nil {
		return nil, err
	}
	if err := client.SetEncodings(ClientEncodings()); err != nil {
		return nil, err
	}
	return client, nil
}

// clientSecurity negotiates the security type and, when it selects VNC
// password authentication, performs the DES challenge/response. It returns the
// selected type so the caller can report whether the wire is plaintext.
func clientSecurity(w io.Writer, r *bufio.Reader, version Version, password string) (SecurityType, error) {
	if version.Minor < Version3_7.Minor {
		var wire [4]byte
		if _, err := io.ReadFull(r, wire[:]); err != nil {
			return SecurityInvalid, fmt.Errorf("%w: reading security type: %w", ErrHandshake, err)
		}
		offered := SecurityType(binary.BigEndian.Uint32(wire[:]))
		switch offered {
		case SecurityInvalid:
			// RFB 3.3 signals a rejected connection with security type 0
			// followed by a length-prefixed reason string.
			return SecurityInvalid, &SecurityError{Rejected: true, Reason: readReason(r)}
		case SecurityNone:
			return SecurityNone, nil
		case SecurityVNCAuth:
			if password == "" {
				return SecurityInvalid, &SecurityError{
					Offered: []SecurityType{offered}, MissingPassword: true,
				}
			}
			if err := vncAuthenticate(r, w, password); err != nil {
				return SecurityInvalid, err
			}
			// RFB 3.3 reports the outcome of VNC authentication after the
			// response, and a failure appends a reason string.
			if err := readSecurityResult(r, version, []SecurityType{offered}); err != nil {
				return SecurityInvalid, err
			}
			return offered, nil
		default:
			return SecurityInvalid, &SecurityError{Offered: []SecurityType{offered}}
		}
	}

	var count [1]byte
	if _, err := io.ReadFull(r, count[:]); err != nil {
		return SecurityInvalid, fmt.Errorf("%w: reading security count: %w", ErrHandshake, err)
	}
	if count[0] == 0 {
		// RFB 3.7 and 3.8 both send a length-prefixed reason string here.
		return SecurityInvalid, &SecurityError{Rejected: true, Reason: readReason(r)}
	}
	var wire [255]byte
	if _, err := io.ReadFull(r, wire[:count[0]]); err != nil {
		return SecurityInvalid, fmt.Errorf("%w: reading security types: %w", ErrHandshake, err)
	}
	offered := make([]SecurityType, int(count[0]))
	for i := range offered {
		offered[i] = SecurityType(wire[i])
	}
	selected, ok := SelectSecurity(offered)
	if !ok {
		return SecurityInvalid, &SecurityError{Offered: offered}
	}
	if selected == SecurityVNCAuth && password == "" {
		return SecurityInvalid, &SecurityError{Offered: offered, MissingPassword: true}
	}
	if err := writeAll(w, []byte{byte(selected)}); err != nil {
		return SecurityInvalid, fmt.Errorf("%w: writing security type: %w", ErrHandshake, err)
	}
	// RFB 3.7 with None proceeds directly to ClientInit (RFC 6143 A.2);
	// 3.8, and every authenticating type, reports a SecurityResult.
	if selected == SecurityNone && version.Minor < Version3_8.Minor {
		return selected, nil
	}
	if selected == SecurityVNCAuth {
		if err := vncAuthenticate(r, w, password); err != nil {
			return SecurityInvalid, err
		}
	}
	if err := readSecurityResult(r, version, offered); err != nil {
		return SecurityInvalid, err
	}
	return selected, nil
}

// readSecurityResult consumes the four-byte status every authenticating type
// sends, plus the failure reason where the version carries one.
func readSecurityResult(r io.Reader, version Version, offered []SecurityType) error {
	var result [4]byte
	if _, err := io.ReadFull(r, result[:]); err != nil {
		return fmt.Errorf("%w: reading security result: %w", ErrHandshake, err)
	}
	if binary.BigEndian.Uint32(result[:]) == 0 {
		return nil
	}
	return &SecurityError{Offered: offered, Rejected: true, Reason: readResultReason(r, version)}
}

// readReason consumes the 32-bit length-prefixed reason string that follows a
// rejected security handshake (RFB 3.3 and 3.7) or a failed RFB 3.8
// SecurityResult. It returns "" when the string cannot be read, leaving the
// connection failing either way.
func readReason(r io.Reader) string {
	var lengthWire [4]byte
	if _, err := io.ReadFull(r, lengthWire[:]); err != nil {
		return ""
	}
	length := binary.BigEndian.Uint32(lengthWire[:])
	if length > maxFieldLength {
		return ""
	}
	reason := make([]byte, length)
	if _, err := io.ReadFull(r, reason); err != nil {
		return ""
	}
	return string(reason)
}

// readResultReason reads the failure text carried by a SecurityResult. RFB 3.7
// reports only the four-byte status, so there is nothing to consume; 3.3 and
// 3.8 both append a length-prefixed reason.
func readResultReason(r io.Reader, version Version) string {
	if version.Minor == Version3_7.Minor {
		return ""
	}
	return readReason(r)
}

func readServerInit(r io.Reader) (ServerInfo, error) {
	var wire [24]byte
	if _, err := io.ReadFull(r, wire[:]); err != nil {
		return ServerInfo{}, fmt.Errorf("%w: reading ServerInit: %w", ErrHandshake, err)
	}
	info := ServerInfo{
		Width:  binary.BigEndian.Uint16(wire[0:2]),
		Height: binary.BigEndian.Uint16(wire[2:4]),
	}
	pf, err := ReadPixelFormat(bytes.NewReader(wire[4:20]))
	if err != nil {
		return ServerInfo{}, fmt.Errorf("%w: reading ServerInit pixel format: %w", ErrHandshake, err)
	}
	info.PixelFormat = pf
	if info.Width == 0 || info.Height == 0 {
		return ServerInfo{}, fmt.Errorf("%w: zero desktop size", ErrHandshake)
	}
	nameLength := binary.BigEndian.Uint32(wire[20:24])
	if nameLength > maxFieldLength {
		return ServerInfo{}, fmt.Errorf("%w: desktop name is %d bytes", ErrOversize, nameLength)
	}
	name := make([]byte, nameLength)
	if _, err := io.ReadFull(r, name); err != nil {
		return ServerInfo{}, fmt.Errorf("%w: reading desktop name: %w", ErrHandshake, err)
	}
	info.DesktopName = string(name)
	return info, nil
}

// Version returns the negotiated protocol version.
func (c *Client) Version() Version { return c.version }

// Security returns the security type the handshake selected.
func (c *Client) Security() SecurityType { return c.security }

// ServerInfo returns the ServerInit fields.
func (c *Client) ServerInfo() ServerInfo { return c.info }

// Frame returns the framebuffer the decoder writes into. The pointer is owned
// by the client, and its pixel format changes on SetPixelFormat. Its pixels
// change on every FramebufferUpdate, so they are only safe to read from the
// ReadMessage goroutine; Image, Bounds and PixelFormat are safe to call from
// any goroutine (see Framebuffer).
func (c *Client) Frame() *Framebuffer { return c.frame }

// SetPixelFormat asks the server to switch to pf and updates the decoder to
// match. The protocol has no acknowledgement, so the caller must serialise it
// with every other write, and it changes the decoder's pixel format, so it
// must not run concurrently with ReadMessage.
func (c *Client) SetPixelFormat(pf PixelFormat) error {
	if err := pf.Validate(); err != nil {
		return err
	}
	var wire [20]byte
	encoded := pf.appendWire(make([]byte, 0, 16))
	copy(wire[4:], encoded)
	if err := writeAll(c.conn, wire[:]); err != nil {
		return err
	}
	return c.frame.SetPixelFormat(pf)
}

// SetEncodings advertises the encodings the client can decode. Any encoding
// without a decoder is refused before anything is written, because a server
// could otherwise select it and ReadRectangles would fail before consuming the
// rectangle payload, desynchronising the stream.
func (c *Client) SetEncodings(encodings []Encoding) error {
	for _, encoding := range encodings {
		if !SupportsEncoding(encoding) {
			return fmt.Errorf("%w: %d cannot be advertised", ErrUnsupportedEncoding, encoding)
		}
	}
	if len(encodings) > 0xffff {
		return fmt.Errorf("%w: %d encodings", ErrOversize, len(encodings))
	}
	wire := make([]byte, 4+4*len(encodings))
	wire[0] = 2
	binary.BigEndian.PutUint16(wire[2:4], uint16(len(encodings)))
	for i, encoding := range encodings {
		binary.BigEndian.PutUint32(wire[4+4*i:], uint32(encoding))
	}
	return writeAll(c.conn, wire)
}

// RequestUpdate sends a FramebufferUpdateRequest.
func (c *Client) RequestUpdate(incremental bool, x, y, width, height uint16) error {
	var wire [10]byte
	wire[0] = 3
	if incremental {
		wire[1] = 1
	}
	binary.BigEndian.PutUint16(wire[2:4], x)
	binary.BigEndian.PutUint16(wire[4:6], y)
	binary.BigEndian.PutUint16(wire[6:8], width)
	binary.BigEndian.PutUint16(wire[8:10], height)
	return writeAll(c.conn, wire[:])
}

// RequestFullUpdate requests the whole current framebuffer. The size comes
// from the framebuffer rather than the immutable ServerInit dimensions, so a
// desktop resized by ExtendedDesktopSize is fully repainted on a server that
// does not use ContinuousUpdates.
func (c *Client) RequestFullUpdate(incremental bool) error {
	bounds := c.frame.Bounds()
	return c.RequestUpdate(incremental, 0, 0, uint16(bounds.Dx()), uint16(bounds.Dy()))
}

// SendKey sends a KeyEvent for one X11 keysym.
func (c *Client) SendKey(keysym uint32, down bool) error {
	var wire [8]byte
	wire[0] = 4
	if down {
		wire[1] = 1
	}
	binary.BigEndian.PutUint32(wire[4:8], keysym)
	return writeAll(c.conn, wire[:])
}

// SendPointer sends a PointerEvent with the RFB button mask.
func (c *Client) SendPointer(mask uint8, x, y uint16) error {
	var wire [6]byte
	wire[0] = 5
	wire[1] = mask
	binary.BigEndian.PutUint16(wire[2:4], x)
	binary.BigEndian.PutUint16(wire[4:6], y)
	return writeAll(c.conn, wire[:])
}

// SendFence sends a ClientFence message. The reply to a server request clears
// FenceRequest and echoes the remaining flags and payload.
func (c *Client) SendFence(flags uint32, payload []byte) error {
	if len(payload) > FenceMaxPayload {
		return fmt.Errorf("%w: fence payload is %d bytes", ErrOversize, len(payload))
	}
	var wire [9]byte
	wire[0] = clientFence
	binary.BigEndian.PutUint32(wire[4:8], flags)
	wire[8] = byte(len(payload))
	if err := writeAll(c.conn, wire[:]); err != nil {
		return err
	}
	return writeAll(c.conn, payload)
}

// SendContinuousUpdates enables or disables server-driven updates for one
// region. The caller must have established support, normally by receiving a
// MessageEndOfContinuousUpdates.
func (c *Client) SendContinuousUpdates(enable bool, x, y, width, height uint16) error {
	var wire [10]byte
	wire[0] = clientEnableContinuousUpdates
	if enable {
		wire[1] = 1
	}
	binary.BigEndian.PutUint16(wire[2:4], x)
	binary.BigEndian.PutUint16(wire[4:6], y)
	binary.BigEndian.PutUint16(wire[6:8], width)
	binary.BigEndian.PutUint16(wire[8:10], height)
	return writeAll(c.conn, wire[:])
}

// SendSetDesktopSize requests a new desktop size and screen layout. The caller
// must have seen an ExtendedDesktopSize rectangle first.
func (c *Client) SendSetDesktopSize(width, height uint16, screens []Screen) error {
	if len(screens) > 255 {
		return fmt.Errorf("%w: %d screens", ErrOversize, len(screens))
	}
	wire := make([]byte, 8+16*len(screens))
	wire[0] = clientSetDesktopSize
	binary.BigEndian.PutUint16(wire[2:4], width)
	binary.BigEndian.PutUint16(wire[4:6], height)
	wire[6] = byte(len(screens))
	for i, screen := range screens {
		offset := 8 + i*16
		binary.BigEndian.PutUint32(wire[offset:offset+4], screen.ID)
		binary.BigEndian.PutUint16(wire[offset+4:offset+6], screen.X)
		binary.BigEndian.PutUint16(wire[offset+6:offset+8], screen.Y)
		binary.BigEndian.PutUint16(wire[offset+8:offset+10], screen.Width)
		binary.BigEndian.PutUint16(wire[offset+10:offset+12], screen.Height)
		binary.BigEndian.PutUint32(wire[offset+12:offset+16], screen.Flags)
	}
	return writeAll(c.conn, wire)
}

// SendCutText sends a ClientCutText message. The protocol's baseline form is
// Latin-1; text is sent as its existing bytes.
func (c *Client) SendCutText(text string) error {
	if uint64(len(text)) > uint64(^uint32(0)) {
		return fmt.Errorf("%w: cut text is %d bytes", ErrOversize, len(text))
	}
	var wire [8]byte
	wire[0] = 6
	binary.BigEndian.PutUint32(wire[4:8], uint32(len(text)))
	if err := writeAll(c.conn, wire[:]); err != nil {
		return err
	}
	return writeAll(c.conn, []byte(text))
}

// ReadMessage reads and applies one server message. Framebuffer updates are
// composited into Frame before they are returned.
func (c *Client) ReadMessage() (ServerMessage, error) {
	var typeWire [1]byte
	if _, err := io.ReadFull(c.reader, typeWire[:]); err != nil {
		return ServerMessage{}, err
	}
	switch ServerMessageType(typeWire[0]) {
	case ServerFramebufferUpdate:
		var header [3]byte
		if _, err := io.ReadFull(c.reader, header[:]); err != nil {
			return ServerMessage{}, err
		}
		count := int(binary.BigEndian.Uint16(header[1:3]))
		rectangles, err := c.frame.ReadRectangles(c.reader, count)
		if err != nil {
			return ServerMessage{}, err
		}
		return ServerMessage{Kind: MessageFramebufferUpdate, Rectangles: rectangles}, nil
	case ServerSetColourMapEntries:
		var header [5]byte
		if _, err := io.ReadFull(c.reader, header[:]); err != nil {
			return ServerMessage{}, err
		}
		count := int(binary.BigEndian.Uint16(header[3:5]))
		if _, err := io.CopyN(io.Discard, c.reader, int64(count)*6); err != nil {
			return ServerMessage{}, err
		}
		return ServerMessage{Kind: MessageSetColourMap}, nil
	case ServerBell:
		var padding [1]byte
		if _, err := io.ReadFull(c.reader, padding[:]); err != nil {
			return ServerMessage{}, err
		}
		return ServerMessage{Kind: MessageBell}, nil
	case ServerCutText:
		var header [7]byte
		if _, err := io.ReadFull(c.reader, header[:]); err != nil {
			return ServerMessage{}, err
		}
		length := binary.BigEndian.Uint32(header[3:7])
		if length > maxFieldLength {
			return ServerMessage{}, fmt.Errorf("%w: cut text is %d bytes", ErrOversize, length)
		}
		text := make([]byte, length)
		if _, err := io.ReadFull(c.reader, text); err != nil {
			return ServerMessage{}, err
		}
		return ServerMessage{Kind: MessageCutText, CutText: string(text)}, nil
	case ServerEndOfContinuousUpdates:
		return ServerMessage{Kind: MessageEndOfContinuousUpdates}, nil
	case ServerFenceMessage:
		var header [8]byte
		if _, err := io.ReadFull(c.reader, header[:]); err != nil {
			return ServerMessage{}, err
		}
		flags := binary.BigEndian.Uint32(header[3:7])
		length := int(header[7])
		if length > FenceMaxPayload {
			return ServerMessage{}, fmt.Errorf("%w: fence payload is %d bytes", ErrOversize, length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.reader, payload); err != nil {
			return ServerMessage{}, err
		}
		return ServerMessage{
			Kind:  MessageFence,
			Fence: Fence{Flags: flags, Payload: payload},
		}, nil
	default:
		return ServerMessage{}, fmt.Errorf("%w: server message type %d", ErrMalformed, typeWire[0])
	}
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
