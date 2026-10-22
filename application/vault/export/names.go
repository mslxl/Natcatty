package vaultexport

import (
	"strings"
	"unicode"
)

// reservedWindowsNames are names Windows refuses as a file stem.
var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitizeName matches the original Notes export file-name rules: control and
// Windows-unsafe characters become dashes, whitespace collapses to one space,
// trailing dots and spaces are stripped, a reserved Windows stem gains an
// underscore, and the result is capped at 120 runes.
func SanitizeName(name, fallback string) string {
	name = strings.TrimSpace(name)
	var builder strings.Builder
	lastSpace := false
	count := 0
	for _, r := range name {
		if count >= 120 {
			break
		}
		switch {
		case unicode.IsControl(r), r == '<', r == '>', r == ':', r == '"',
			r == '/', r == '\\', r == '|', r == '?', r == '*':
			builder.WriteByte('-')
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace && builder.Len() > 0 {
				builder.WriteByte(' ')
				lastSpace = true
			}
		default:
			builder.WriteRune(r)
			lastSpace = false
		}
		count++
	}
	result := strings.TrimRight(builder.String(), ". ")
	if result == "" || result == "." || result == ".." {
		return fallback
	}
	stem := result
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	if reservedWindowsNames[strings.ToLower(stem)] {
		result += "_"
	}
	return result
}
