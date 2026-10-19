package session

import domainterminal "github.com/mslxl/natcatty/domain/terminal"

// Codec converts one Session's transport bytes to UTF-8 and its terminal input
// back to the wire encoding. Implementations retain an incomplete multibyte
// sequence between calls. The legacy charset tables live in an adapter; the
// application layer declares only this port.
type Codec interface {
	Decode([]byte) []byte
	Encode([]byte) []byte
	SetEncoding(domainterminal.Encoding) bool
}

// CodecFactory builds the Codec for a Session's initial encoding. The
// composition root supplies the real adapter.
type CodecFactory func(domainterminal.Encoding) Codec

// identityCodec passes UTF-8 bytes through unchanged. A Service without a
// CodecFactory uses it and refuses a non-UTF-8 live switch.
type identityCodec struct{}

func (identityCodec) Decode(p []byte) []byte { return p }
func (identityCodec) Encode(p []byte) []byte { return p }
func (identityCodec) SetEncoding(encoding domainterminal.Encoding) bool {
	return encoding == domainterminal.EncodingUTF8
}

// Encoding returns the Session's current wire encoding.
func (s *Session) Encoding() domainterminal.Encoding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Encoding
}

// EncodingScope returns the persisted preference key this Session uses.
func (s *Session) EncodingScope() string {
	return s.spec.EncodingScope
}

// SetEncoding applies a new encoding to output decoded from now on and input
// encoded from now on. It reports whether the encoding is supported. The
// terminal surface calls it when the user chooses from the encoding menu.
func (s *Session) SetEncoding(encoding domainterminal.Encoding) bool {
	if !encoding.Valid() {
		return false
	}
	if s.codec == nil || !s.codec.SetEncoding(encoding) {
		return false
	}
	s.spec.Encoding = encoding
	s.update(func(snapshot *Snapshot) {
		snapshot.Encoding = encoding
	})
	return true
}

// decodeOutput converts one transport chunk to UTF-8 before the tap, log, and
// terminal see it. The codec retains a multibyte sequence split across reads.
func (s *Session) decodeOutput(p []byte) []byte {
	if s.codec == nil {
		return p
	}
	return s.codec.Decode(p)
}

// encodeInput converts UTF-8 keys to the wire encoding. It runs after the
// tap's input hook, so command tracking, broadcast, and compose always see
// UTF-8 no matter which legacy encoding the transport uses.
func (s *Session) encodeInput(p []byte) []byte {
	if s.codec == nil {
		return p
	}
	return s.codec.Encode(p)
}
