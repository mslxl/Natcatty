// Package locales holds Netcatty's ARB/ICU message catalogs and the accessors
// mygo generate writes from them. The accessors are generated code: edit the
// JSON catalogs, then run `go tool mygo generate`.
package locales

import (
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Tags returns the languages the app ships, source language first, as the
// embedded catalogs declare them. The shell and the language menu offer them
// in this order.
func Tags() []string { return Catalog.Languages() }

// RelativeTime writes the distance from now to value in locale, as "3 minutes
// ago" or "in 2 days". It is the host formatter for plugin text: messages stay
// plain text and the plugin never formats a number or date itself.
func RelativeTime(locale string, value, now time.Time) string {
	msgs := In(locale)
	delta := now.Sub(value)
	future := delta < 0
	if future {
		delta = -delta
	}
	switch {
	case delta < time.Minute:
		count := int(delta / time.Second)
		if count == 0 {
			return msgs.PluginTimeJustNow()
		}
		if future {
			return msgs.PluginTimeSecondsIn(count)
		}
		return msgs.PluginTimeSecondsAgo(count)
	case delta < time.Hour:
		count := int(delta / time.Minute)
		if future {
			return msgs.PluginTimeMinutesIn(count)
		}
		return msgs.PluginTimeMinutesAgo(count)
	case delta < 24*time.Hour:
		count := int(delta / time.Hour)
		if future {
			return msgs.PluginTimeHoursIn(count)
		}
		return msgs.PluginTimeHoursAgo(count)
	case delta < 30*24*time.Hour:
		count := int(delta / (24 * time.Hour))
		if future {
			return msgs.PluginTimeDaysIn(count)
		}
		return msgs.PluginTimeDaysAgo(count)
	case delta < 365*24*time.Hour:
		count := int(delta / (30 * 24 * time.Hour))
		if future {
			return msgs.PluginTimeMonthsIn(count)
		}
		return msgs.PluginTimeMonthsAgo(count)
	default:
		count := int(delta / (365 * 24 * time.Hour))
		if future {
			return msgs.PluginTimeYearsIn(count)
		}
		return msgs.PluginTimeYearsAgo(count)
	}
}

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
