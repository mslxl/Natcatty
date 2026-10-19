// Package locales holds Netcatty's ARB/ICU message catalogs and the accessors
// mygo generate writes from them. The accessors are generated code: edit the
// JSON catalogs, then run `go tool mygo generate`.
package locales

import domainterminal "github.com/mslxl/natcatty/domain/terminal"

// Tags returns the languages the app ships, source language first, as the
// embedded catalogs declare them. The shell and the language menu offer them
// in this order.
func Tags() []string { return Catalog.Languages() }

// EncodingLabel returns the selector name for encoding. Encoding names are
// IANA identifiers, so every catalog spells them the same way, but they are
// still display strings and therefore live in the catalogs.
func EncodingLabel(msgs Messages, encoding domainterminal.Encoding) string {
	switch domainterminal.NormalizeEncoding(encoding) {
	case domainterminal.EncodingGB18030:
		return msgs.SessionEncodingGB18030()
	case domainterminal.EncodingShiftJIS:
		return msgs.SessionEncodingShiftJIS()
	case domainterminal.EncodingLatin1:
		return msgs.SessionEncodingLatin1()
	default:
		return msgs.SessionEncodingUTF8()
	}
}
