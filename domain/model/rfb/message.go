package rfb

// Encoding is an RFB rectangle encoding number. Pseudo-encodings are
// negative.
type Encoding int32

// Rectangle encodings. The client decodes and advertises only Raw, CopyRect,
// RRE and Hextile in this release; Tight and TightPNG are listed so callers
// can recognise and refuse them, and they are never advertised.
const (
	EncodingRaw      Encoding = 0
	EncodingCopyRect Encoding = 1
	EncodingRRE      Encoding = 2
	EncodingHextile  Encoding = 5
	EncodingTight    Encoding = 7
	EncodingTightPNG Encoding = -260
)

// supportedEncodings is the single source of truth for the encodings this
// client can decode and may advertise, in preference order. Tight and TightPNG
// are deliberately absent: their decoders are not implemented, so advertising
// them would desynchronise the stream. Add a decoder and an entry here together.
var supportedEncodings = []Encoding{EncodingRaw, EncodingCopyRect, EncodingRRE, EncodingHextile}

// ClientEncodings returns the encodings the compositor truly decodes, in
// preference order. The returned slice is a copy, so a caller may modify it.
func ClientEncodings() []Encoding {
	return append([]Encoding(nil), supportedEncodings...)
}

// SupportsEncoding reports whether the framebuffer decoder implements encoding.
func SupportsEncoding(encoding Encoding) bool {
	for _, supported := range supportedEncodings {
		if encoding == supported {
			return true
		}
	}
	return false
}

// Rectangle is the header of one FramebufferUpdate rectangle. For CopyRect
// the source origin is carried separately by the decoder.
type Rectangle struct {
	X        uint16
	Y        uint16
	Width    uint16
	Height   uint16
	Encoding Encoding
}

// ServerMessageType is the one-byte type of a server-to-client message.
type ServerMessageType uint8

// Server message types handled by this package.
const (
	ServerFramebufferUpdate   ServerMessageType = 0
	ServerSetColourMapEntries ServerMessageType = 1
	ServerBell                ServerMessageType = 2
	ServerCutText             ServerMessageType = 3
)

// ServerMessageKind classifies a decoded server message.
type ServerMessageKind uint8

// Kinds of decoded server message.
const (
	MessageFramebufferUpdate ServerMessageKind = iota + 1
	MessageSetColourMap
	MessageBell
	MessageCutText
)

// ServerMessage is one decoded server-to-client message. Framebuffer updates
// have already been composited into the Client's frame; Rectangles describes
// what was applied.
type ServerMessage struct {
	Kind       ServerMessageKind
	Rectangles []Rectangle
	CutText    string
}
