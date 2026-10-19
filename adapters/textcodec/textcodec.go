// Package textcodec adapts the legacy character encodings a terminal Session
// may speak to and from UTF-8. The tables live outside the domain and
// application layers, which may use only the standard library; app injects this
// adapter through the Session service's Codec port.
package textcodec

import (
	"errors"
	"io"
	"sync"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Codec incrementally decodes transport output and encodes terminal input for
// one Session. A legacy multibyte sequence may span two reads or writes; the
// Codec keeps the incomplete prefix until the next call. It is safe for use
// from the Session's output and input goroutines.
type Codec struct {
	mu       sync.Mutex
	encoding domainterminal.Encoding
	decoder  transform.Transformer
	encoder  transform.Transformer
	encInput []byte
	decInput []byte
}

// New returns a Codec for encoding. An unknown encoding is UTF-8.
func New(encoding domainterminal.Encoding) *Codec {
	codec := &Codec{}
	codec.setLocked(domainterminal.NormalizeEncoding(encoding))
	return codec
}

// Encoding returns the current encoding.
func (c *Codec) Encoding() domainterminal.Encoding {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.encoding
}

// SetEncoding changes the encoding and discards any incomplete sequence from
// the previous one. It reports whether the value was valid.
func (c *Codec) SetEncoding(encoding domainterminal.Encoding) bool {
	if !encoding.Valid() {
		return false
	}
	c.mu.Lock()
	c.setLocked(encoding)
	c.mu.Unlock()
	return true
}

func (c *Codec) setLocked(encoding domainterminal.Encoding) {
	c.encoding = encoding
	c.decInput = c.decInput[:0]
	c.encInput = c.encInput[:0]
	if text := textEncoding(encoding); text != nil {
		c.decoder = text.NewDecoder()
		c.encoder = text.NewEncoder()
	} else {
		c.decoder = nil
		c.encoder = nil
	}
}

// Decode converts one output chunk from the wire encoding to UTF-8. A partial
// trailing sequence is retained and completed by the next call. An invalid
// sequence yields U+FFFD rather than breaking the Session's output stream.
func (c *Codec) Decode(p []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(p) == 0 {
		return nil
	}
	if c.decoder == nil {
		return append([]byte(nil), p...)
	}
	source := append(c.decInput, p...)
	c.decInput = nil
	decoded, rest, err := transformChunk(c.decoder, source, false)
	if err != nil {
		// Invalid bytes cannot be carried forward forever. Return what was
		// decoded and one replacement so the stream keeps moving.
		rest = nil
		decoded = append(decoded, "�"...)
	}
	if len(rest) > 0 {
		c.decInput = append(c.decInput[:0], rest...)
	}
	return decoded
}

// Encode converts UTF-8 input to the wire encoding. A partial trailing UTF-8
// rune is retained until the next call. Invalid input is replaced rather than
// dropped.
func (c *Codec) Encode(p []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(p) == 0 {
		return nil
	}
	if c.encoder == nil {
		return append([]byte(nil), p...)
	}
	source := append(c.encInput, p...)
	c.encInput = nil
	encoded, rest, err := transformChunk(c.encoder, source, false)
	if err != nil {
		rest = nil
		// An unencodable rune becomes '?' rather than losing the keystroke or
		// a whole chunk of input.
		encoded = append(encoded, '?')
	}
	if len(rest) > 0 {
		c.encInput = append(c.encInput[:0], rest...)
	}
	return encoded
}

// textEncoding returns the x/text codec for encoding, or nil for UTF-8, which
// needs no transformation.
func textEncoding(encoding domainterminal.Encoding) encoding.Encoding {
	switch encoding {
	case domainterminal.EncodingGB18030:
		return simplifiedchinese.GB18030
	case domainterminal.EncodingShiftJIS:
		return japanese.ShiftJIS
	case domainterminal.EncodingLatin1:
		return charmap.ISO8859_1
	default:
		return nil
	}
}

// transformChunk runs one transformer step. ErrShortSrc is not a failure: the
// remaining source is the incomplete sequence the Codec keeps for next time.
func transformChunk(t transform.Transformer, source []byte, atEOF bool) ([]byte, []byte, error) {
	output := make([]byte, 0, len(source)*4+16)
	remaining := source
	for {
		dst := make([]byte, len(remaining)*4+16)
		nDst, nSrc, err := t.Transform(dst, remaining, atEOF)
		output = append(output, dst[:nDst]...)
		remaining = remaining[nSrc:]
		switch {
		case errors.Is(err, transform.ErrShortDst):
			continue
		case errors.Is(err, transform.ErrShortSrc):
			return output, remaining, nil
		case errors.Is(err, io.EOF):
			return output, nil, nil
		case err != nil:
			return output, remaining, err
		default:
			return output, remaining, nil
		}
	}
}
