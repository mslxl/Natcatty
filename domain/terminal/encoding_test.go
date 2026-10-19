package terminal

import (
	"slices"
	"testing"
)

func TestNormalizeEncodingAcceptsCharsetSpellings(t *testing.T) {
	cases := map[Encoding]Encoding{
		"":           EncodingUTF8,
		"utf8":       EncodingUTF8,
		"gbk":        EncodingGB18030,
		"GB2312":     EncodingGB18030,
		"cp936":      EncodingGB18030,
		"shift-jis":  EncodingShiftJIS,
		"sjis":       EncodingShiftJIS,
		"latin1":     EncodingLatin1,
		"ISO8859-1":  EncodingLatin1,
		"unknown-99": EncodingUTF8,
	}
	for input, want := range cases {
		if got := NormalizeEncoding(input); got != want {
			t.Errorf("NormalizeEncoding(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSupportedEncodingsKeepTheSelectorOrder(t *testing.T) {
	want := []Encoding{EncodingUTF8, EncodingGB18030, EncodingShiftJIS, EncodingLatin1}
	if got := SupportedEncodings(); !slices.Equal(got, want) {
		t.Fatalf("encodings = %v, want %v", got, want)
	}
}
