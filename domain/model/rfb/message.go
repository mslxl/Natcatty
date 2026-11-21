package rfb

import "image"

// Encoding is an RFB rectangle encoding number. Pseudo-encodings are
// negative.
type Encoding int32

// Rectangle encodings. The client decodes Raw, CopyRect, RRE, Hextile and
// ZRLE. Tight and TightPNG are listed so callers can recognise and refuse
// them, and they are never advertised. The pseudo-encodings change framing or
// carry metadata rather than pixels; the client understands and may advertise
// each one listed here.
const (
	EncodingRaw      Encoding = 0
	EncodingCopyRect Encoding = 1
	EncodingRRE      Encoding = 2
	EncodingHextile  Encoding = 5
	EncodingTight    Encoding = 7
	EncodingZRLE     Encoding = 16
	EncodingTightPNG Encoding = -260
	// EncodingExtendedDesktopSize carries a screen layout and a new desktop
	// size; its header is not a framebuffer rectangle.
	EncodingExtendedDesktopSize Encoding = -308
	// EncodingFence negotiates the Fence synchronisation extension.
	EncodingFence Encoding = -312
	// EncodingContinuousUpdates negotiates server-driven update streaming.
	EncodingContinuousUpdates Encoding = -313
	// EncodingCursorWithAlpha carries a locally drawn cursor bitmap.
	EncodingCursorWithAlpha Encoding = -314
)

// supportedEncodings is the single source of truth for the encodings this
// client can decode and may advertise, in preference order. Tight and TightPNG
// are deliberately absent: their decoders are not implemented, so advertising
// them would desynchronise the stream. Add a decoder and an entry here together.
var supportedEncodings = []Encoding{
	EncodingZRLE,
	EncodingHextile,
	EncodingRRE,
	EncodingRaw,
	EncodingCopyRect,
	EncodingExtendedDesktopSize,
	EncodingCursorWithAlpha,
	EncodingContinuousUpdates,
	EncodingFence,
}

// ClientEncodings returns the encodings the compositor truly decodes, in
// preference order. The returned slice is a copy, so a caller may modify it.
func ClientEncodings() []Encoding {
	return append([]Encoding(nil), supportedEncodings...)
}

// SupportsEncoding reports whether the framebuffer decoder implements encoding,
// including the pseudo-encodings the client negotiates.
func SupportsEncoding(encoding Encoding) bool {
	for _, supported := range supportedEncodings {
		if encoding == supported {
			return true
		}
	}
	return false
}

// Screen is one head in an ExtendedDesktopSize layout.
type Screen struct {
	ID     uint32
	X      uint16
	Y      uint16
	Width  uint16
	Height uint16
	Flags  uint32
}

// Cursor is a parsed CursorWithAlpha shape. Image is non-nil for a visible
// cursor; Hotspot is relative to Image's origin. A hidden cursor (zero size)
// has a nil Image.
type Cursor struct {
	Image   *image.RGBA
	Hotspot image.Point
}

// ExtendedDesktopSize describes one ExtendedDesktopSize pseudo-rectangle.
//
// Reason is the header's x-position: 0 for a server-side change, 1 when this
// client asked, 2 when another client asked. Status is the header's
// y-position: 0 means success. Width and Height are the new desktop size.
type ExtendedDesktopSize struct {
	Reason  uint8
	Status  uint8
	Width   uint16
	Height  uint16
	Screens []Screen
}

// Rectangle is the header of one FramebufferUpdate rectangle. For CopyRect
// the source origin is carried separately by the decoder. For the metadata
// pseudo-encodings, Extended or Cursor is set and the rectangle did not touch
// the framebuffer.
type Rectangle struct {
	X        uint16
	Y        uint16
	Width    uint16
	Height   uint16
	Encoding Encoding
	Extended *ExtendedDesktopSize
	Cursor   *Cursor
}

// ServerMessageType is the one-byte type of a server-to-client message.
type ServerMessageType uint8

// Server message types handled by this package.
const (
	ServerFramebufferUpdate      ServerMessageType = 0
	ServerSetColourMapEntries    ServerMessageType = 1
	ServerBell                   ServerMessageType = 2
	ServerCutText                ServerMessageType = 3
	ServerEndOfContinuousUpdates ServerMessageType = 150
	ServerFenceMessage           ServerMessageType = 248
)

// Fence flags shared by ClientFence and ServerFence.
const (
	FenceBlockBefore uint32 = 1 << 0
	FenceBlockAfter  uint32 = 1 << 1
	FenceSyncNext    uint32 = 1 << 2
	FenceRequest     uint32 = 1 << 31
	// FenceMaxPayload is the protocol's 64-byte limit on fence payloads.
	FenceMaxPayload = 64
)

// Fence is one ClientFence or ServerFence message.
type Fence struct {
	Flags   uint32
	Payload []byte
}

// Request reports that the peer expects a response.
func (f Fence) Request() bool { return f.Flags&FenceRequest != 0 }

// ServerMessageKind classifies a decoded server message.
type ServerMessageKind uint8

// Kinds of decoded server message.
const (
	MessageFramebufferUpdate ServerMessageKind = iota + 1
	MessageSetColourMap
	MessageBell
	MessageCutText
	// MessageFence carries a ServerFence synchronisation message.
	MessageFence
	// MessageEndOfContinuousUpdates reports that server-driven updates are
	// supported (or were just disabled).
	MessageEndOfContinuousUpdates
)

// ServerMessage is one decoded server-to-client message. Framebuffer updates
// have already been composited into the Client's frame; Rectangles describes
// what was applied. Metadata pseudo-rectangles carry their parsed value in the
// matching Rectangle rather than changing pixels.
type ServerMessage struct {
	Kind       ServerMessageKind
	Rectangles []Rectangle
	CutText    string
	Fence      Fence
}
