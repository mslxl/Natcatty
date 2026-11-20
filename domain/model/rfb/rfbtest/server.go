// Package rfbtest provides an in-process RFB server for tests. It speaks RFB
// 3.3, 3.7 and 3.8, offers the None security type, records every client
// message, and serves a canned frame in Raw, CopyRect, RRE or Hextile
// encoding. It never opens a socket itself: callers hand Serve one end of a
// net.Pipe or an equivalent stream, so no live desktop is needed.
package rfbtest

import (
	"bytes"
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

// PointerEvent is a recorded PointerEvent.
type PointerEvent struct {
	Mask uint8
	X    uint16
	Y    uint16
}

// Received is a snapshot of everything the client sent.
type Received struct {
	ClientVersion rfb.Version
	Shared        bool
	PixelFormat   rfb.PixelFormat
	Encodings     []rfb.Encoding
	Requests      []UpdateRequest
	Keys          []KeyEvent
	Pointers      []PointerEvent
	CutTexts      []string
}

// Server is the in-process test server. New prepares it; Serve performs one
// connection and returns when the peer closes or the protocol fails.
type Server struct {
	cfg    Config
	frame  *image.RGBA
	width  int
	height int
	err    error
	pf     rfb.PixelFormat

	mu       sync.Mutex
	received Received
	sentBell bool
	sentCut  bool
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
	case rfb.EncodingRaw, rfb.EncodingRRE, rfb.EncodingHextile:
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
	received.Pointers = append([]PointerEvent(nil), s.received.Pointers...)
	received.CutTexts = append([]string(nil), s.received.CutTexts...)
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

	if version.Minor < rfb.Version3_7.Minor {
		var wire [4]byte
		binary.BigEndian.PutUint32(wire[:], uint32(rfb.SecurityNone))
		if err := writeAll(conn, wire[:]); err != nil {
			return err
		}
	} else {
		if err := writeAll(conn, []byte{1, byte(rfb.SecurityNone)}); err != nil {
			return err
		}
		var selected [1]byte
		if _, err := io.ReadFull(conn, selected[:]); err != nil {
			return err
		}
		if rfb.SecurityType(selected[0]) != rfb.SecurityNone {
			var failure [4]byte
			binary.BigEndian.PutUint32(failure[:], 1)
			_ = writeAll(conn, failure[:])
			return fmt.Errorf("rfbtest: client selected security type %d", selected[0])
		}
		// RFB 3.7 with None has no SecurityResult (RFC 6143 A.2); 3.8 does.
		if version.Minor >= rfb.Version3_8.Minor {
			var success [4]byte
			if err := writeAll(conn, success[:]); err != nil {
				return err
			}
		}
	}

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
			if err := s.readUpdateRequest(conn); err != nil {
				return err
			}
			if err := s.sendFrame(conn); err != nil {
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
		default:
			return fmt.Errorf("rfbtest: unknown client message type %d", messageType[0])
		}
	}
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

func (s *Server) readSetEncodings(conn io.Reader) error {
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
	return nil
}

func (s *Server) readUpdateRequest(conn io.Reader) error {
	var payload [9]byte
	if _, err := io.ReadFull(conn, payload[:]); err != nil {
		return err
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

func (s *Server) sendFrame(conn io.Writer) error {
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

	var update bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint16(header[2:4], uint16(len(plan)))
	update.Write(header[:])
	for _, rect := range plan {
		if err := s.writeRectangle(&update, rect); err != nil {
			return err
		}
	}
	return writeAll(conn, update.Bytes())
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
	default:
		return fmt.Errorf("rfbtest: unsupported encoding %d", rect.Encoding)
	}
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
