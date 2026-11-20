package session

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/mslxl/natcatty/adapters/textcodec"
	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// gbkLikeCodec models a two-byte legacy charset: ASCII stays one byte and every
// other rune occupies two wire bytes. It is deliberately not GB18030 so the
// test proves the translator asks the codec's width instead of assuming one.
type gbkLikeCodec struct{}

func (gbkLikeCodec) Decode(p []byte) []byte { return p }

func (gbkLikeCodec) Encode(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for len(p) > 0 {
		r, size := utf8.DecodeRune(p)
		if r < utf8.RuneSelf {
			out = append(out, p[:size]...)
		} else {
			out = append(out, 0x81, byte(r))
		}
		p = p[size:]
	}
	return out
}

func (gbkLikeCodec) SetEncoding(domainterminal.Encoding) bool { return true }

func (gbkLikeCodec) EncodedWidth(r rune) int {
	if r < utf8.RuneSelf {
		return 1
	}
	return 2
}

// TestByteOrientedBackspaceCountsWireBytes proves a byte-oriented Backspace
// erases the character's wire width, not its UTF-8 length. With a two-byte
// legacy encoding a CJK character is three UTF-8 bytes but two wire bytes, so
// the old UTF-8 count sent one erase too many.
func TestByteOrientedBackspaceCountsWireBytes(t *testing.T) {
	transport := newFakeTransport()
	service := newTestService(&fakeFactory{transport: transport},
		WithCodecFactory(func(domainterminal.Encoding) Codec { return gbkLikeCodec{} }))
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), StartSpec{
		Kind:     KindSSH,
		Label:    "example.com",
		Encoding: domainterminal.EncodingGB18030,
		InputCompatibility: modelterminal.InputCompatibility{
			LineEditing: modelterminal.LineEditingByte,
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.TerminalConn().Write([]byte("中\x7f")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	encoded := string(gbkLikeCodec{}.Encode([]byte("中")))
	want := encoded + "\x7f\x7f"
	eventually(t, "the wire-width Backspace", func() bool { return transport.input() == want })
	if got := transport.input(); got != want {
		t.Fatalf("transport input = %q, want %q (one erase per wire byte)", got, want)
	}
}

// TestByteOrientedBackspaceCountsAnInvalidByteOnce covers the over-count: an
// invalid UTF-8 byte decodes as (U+FFFD, 1), and the UTF-8 codec's width for
// RuneError is three bytes, but the wire received that single source byte. A
// genuine U+FFFD is three bytes and must still erase three times.
func TestByteOrientedBackspaceCountsAnInvalidByteOnce(t *testing.T) {
	tests := []struct {
		name  string
		typed []byte
		want  string
	}{
		{"invalid byte 0x80", []byte{0x80, 0x7f}, "\x80\x7f"},
		{"invalid byte 0xff", []byte{0xff, 0x7f}, "\xff\x7f"},
		{"real U+FFFD still counts three wire bytes", []byte("�\x7f"), "�\x7f\x7f\x7f"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newFakeTransport()
			service := newTestService(&fakeFactory{transport: transport},
				WithCodecFactory(func(encoding domainterminal.Encoding) Codec {
					return textcodec.New(encoding)
				}))
			t.Cleanup(service.CloseAll)
			session, err := service.Open(context.Background(), StartSpec{
				Kind:  KindSSH,
				Label: "example.com",
				InputCompatibility: modelterminal.InputCompatibility{
					LineEditing: modelterminal.LineEditingByte,
				},
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = session.Close() })

			if _, err := session.TerminalConn().Write(test.typed); err != nil {
				t.Fatalf("Write: %v", err)
			}
			eventually(t, "the invalid-byte Backspace", func() bool {
				return transport.input() == test.want
			})
			if got := transport.input(); got != test.want {
				t.Fatalf("transport input = %q, want %q", got, test.want)
			}
		})
	}
}
