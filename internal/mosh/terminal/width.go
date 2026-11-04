// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"unicode"

	"golang.org/x/text/width"
)

// IsPrintISO8859_1 reports the cheap ASCII and Latin-1 printable test stock
// mosh applies before calling wcwidth.
func IsPrintISO8859_1(r rune) bool {
	return (r >= 0xa0 && r <= 0xff) || (r >= 0x20 && r <= 0x7e)
}

// RuneWidth returns the display width stock mosh gets from wcwidth in a UTF-8
// locale: -1 for unprintable, 0 for combining and other zero-width code
// points, 2 for East Asian wide and fullwidth, and 1 otherwise. Ambiguous
// characters are one column, as they are in a non-CJK UTF-8 locale.
func RuneWidth(r rune) int {
	if r == 0 {
		return -1
	}
	if IsPrintISO8859_1(r) {
		return 1
	}
	if r < 0x20 || (r >= 0x7f && r < 0xa0) {
		return -1
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if !unicode.IsPrint(r) {
		return -1
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}
