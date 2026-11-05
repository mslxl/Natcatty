// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import "sort"

// IsPrintISO8859_1 reports the cheap ASCII and Latin-1 printable test stock
// mosh applies before calling wcwidth.
func IsPrintISO8859_1(r rune) bool {
	return (r >= 0xa0 && r <= 0xff) || (r >= 0x20 && r <= 0x7e)
}

// RuneWidth returns exactly what stock mosh 1.4.0 obtains from glibc
// wcwidth(3) in its UTF-8 locale: -1 for unprintable, 0 for combining and
// other zero-width code points, 2 for East Asian wide and fullwidth, and 1
// otherwise. Ambiguous characters are one column, as they are in a non-CJK
// UTF-8 locale.
//
// The target semantics are the parity oracle: glibc 2.39 in Ubuntu 24.04 with
// LC_CTYPE=C.UTF-8 (see widthtable.go). That differs from Go's unicode.IsPrint
// and x/text/width: all non-ASCII Zs separators (including U+1680, U+2000 to
// U+200A, U+202F, and U+205F) are width 1; U+3000 is width 2 in this locale;
// private-use code points (U+E000 to U+F8FF and the supplementary planes) are
// width 1; Hangul leading Jamo U+1100 to U+115F are width 2 while medial and
// final Jamo U+1160 to U+11FF are width 0.
func RuneWidth(r rune) int {
	// Stock mosh special-cases NUL before calling wcwidth.
	if r == 0 {
		return -1
	}
	if IsPrintISO8859_1(r) {
		return 1
	}
	if r < 0 || r > 0x10FFFF {
		return -1
	}
	index := sort.Search(len(wcwidthRanges), func(i int) bool {
		return wcwidthRanges[i].hi >= r
	})
	if index < len(wcwidthRanges) && wcwidthRanges[index].lo <= r {
		return int(wcwidthRanges[index].width)
	}
	return 1
}
