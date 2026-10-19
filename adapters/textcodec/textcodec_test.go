package textcodec

import (
	"bytes"
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// The literals are the encodings of those words in the named legacy charset,
// independent of this package's tables.
func TestCodecDecodesLegacyBytesSplitAcrossChunks(t *testing.T) {
	tests := []struct {
		name     string
		encoding domainterminal.Encoding
		wire     []byte
		want     string
	}{
		{"GB18030", domainterminal.EncodingGB18030, []byte{0xD6, 0xD0, 0xCE, 0xC4}, "中文"},
		{"ShiftJIS", domainterminal.EncodingShiftJIS, []byte{0x93, 0xFA, 0x96, 0x7B, 0x8C, 0xEA}, "日本語"},
		{"Latin1", domainterminal.EncodingLatin1, []byte{0xE9}, "é"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			codec := New(test.encoding)
			var got []byte
			for _, b := range test.wire {
				got = append(got, codec.Decode([]byte{b})...)
			}
			if string(got) != test.want {
				t.Fatalf("decoded %q, want %q", got, test.want)
			}
		})
	}
}

func TestCodecEncodeUsesTheSelectedEncodingAndHoldsSplitRunes(t *testing.T) {
	codec := New(domainterminal.EncodingGB18030)
	// 中 is E4 B8 AD in UTF-8; the first call must hold its incomplete prefix.
	if got := codec.Encode([]byte{0xE4, 0xB8}); len(got) != 0 {
		t.Fatalf("an incomplete rune encoded as % X", got)
	}
	got := codec.Encode([]byte{0xAD})
	if !bytes.Equal(got, []byte{0xD6, 0xD0}) {
		t.Fatalf("encoded % X, want D6 D0", got)
	}

	shiftJIS := New(domainterminal.EncodingShiftJIS)
	encoded := shiftJIS.Encode([]byte("日本語"))
	if !bytes.Equal(encoded, []byte{0x93, 0xFA, 0x96, 0x7B, 0x8C, 0xEA}) {
		t.Fatalf("Shift_JIS encoded % X", encoded)
	}

	latin := New(domainterminal.EncodingLatin1)
	if got := latin.Encode([]byte("é")); !bytes.Equal(got, []byte{0xE9}) {
		t.Fatalf("ISO-8859-1 encoded % X", got)
	}
}

func TestCodecSetEncodingDropsThePreviousIncompleteSequence(t *testing.T) {
	codec := New(domainterminal.EncodingGB18030)
	codec.Decode([]byte{0xD6}) // incomplete GB18030 lead byte
	if !codec.SetEncoding(domainterminal.EncodingUTF8) {
		t.Fatal("UTF-8 was rejected")
	}
	if got := codec.Decode([]byte{0xD0}); !bytes.Equal(got, []byte{0xD0}) {
		t.Fatalf("after the switch decoded % X, want the raw byte", got)
	}
	if codec.Encoding() != domainterminal.EncodingUTF8 {
		t.Fatalf("encoding = %q", codec.Encoding())
	}
}

func TestCodecRejectsAnUnknownEncoding(t *testing.T) {
	codec := New(domainterminal.EncodingUTF8)
	if codec.SetEncoding("unknown-99") {
		t.Fatal("an unknown encoding was accepted")
	}
}
