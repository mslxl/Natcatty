package terminal

// Encoding is a terminal character encoding. The wire form is the value stored
// in settings; the UI's catalogs supply the selector labels. The codec that
// converts the legacy charsets lives in adapters/textcodec because the domain
// layer may import only the standard library.
type Encoding string

const (
	// EncodingUTF8 is the default and passes bytes through unchanged.
	EncodingUTF8 Encoding = "utf-8"
	// EncodingGB18030 is the original's legacy encoding for Chinese sessions.
	// GBK and GB2312 input is decoded as GB18030, as in the original.
	EncodingGB18030 Encoding = "gb18030"
	// EncodingShiftJIS is the Japanese legacy encoding.
	EncodingShiftJIS Encoding = "shift_jis"
	// EncodingLatin1 is ISO-8859-1.
	EncodingLatin1 Encoding = "iso-8859-1"
)

// supportedEncodings is the selector order. UTF-8 and GB18030 are the
// original's list; Shift_JIS and ISO-8859-1 extend the same selector for
// legacy hosts that use them.
var supportedEncodings = []Encoding{
	EncodingUTF8,
	EncodingGB18030,
	EncodingShiftJIS,
	EncodingLatin1,
}

// SupportedEncodings returns the selector's encodings in display order. The
// caller must not modify the returned slice.
func SupportedEncodings() []Encoding { return supportedEncodings }

// Valid reports whether the encoding is one the application can decode and
// encode.
func (e Encoding) Valid() bool {
	for _, supported := range supportedEncodings {
		if e == supported {
			return true
		}
	}
	return false
}

// NormalizeEncoding maps an unknown or empty value to UTF-8 and a charset
// token to its encoding. It accepts the charset spellings Host entries and
// remote shells commonly report.
func NormalizeEncoding(value Encoding) Encoding {
	for _, supported := range supportedEncodings {
		if value == supported {
			return supported
		}
	}
	switch string(value) {
	case "utf8", "utf_8", "UTF-8", "UTF8", "utf":
		return EncodingUTF8
	case "gbk", "gb2312", "gb_2312", "cp936", "ms936", "GBK", "GB2312":
		return EncodingGB18030
	case "shift-jis", "shiftjis", "sjis", "Shift_JIS", "Shift-JIS", "SJIS":
		return EncodingShiftJIS
	case "latin1", "latin-1", "iso8859-1", "ISO8859-1":
		return EncodingLatin1
	}
	return EncodingUTF8
}
