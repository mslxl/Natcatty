package app

import (
	"time"

	"github.com/egoist/mygo/i18n"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/ui/locales"
)

// pluginLocaleFormatter is the app's host formatter for plugin text. Numbers,
// dates, and relative times use the same MyGo i18n and shipped messages as the
// rest of the app, so a plugin's formatted values match the host's.
type pluginLocaleFormatter struct{}

var _ appplugin.LocaleFormatter = pluginLocaleFormatter{}

// FormatNumber writes a number in the host locale.
func (pluginLocaleFormatter) FormatNumber(locale string, value float64) string {
	return i18n.FormatNumber(locale, value, i18n.NumberOptions{})
}

// FormatDate writes a medium date in the host locale.
func (pluginLocaleFormatter) FormatDate(locale string, value time.Time) string {
	return i18n.FormatDate(locale, value, i18n.DateOptions{Date: i18n.MediumStyle})
}

// FormatRelativeTime writes the distance between now and value in the host
// locale.
func (pluginLocaleFormatter) FormatRelativeTime(locale string, value, now time.Time) string {
	return locales.RelativeTime(locale, value, now)
}

// PluralCategory returns the CLDR category MyGo's plural rules select, so
// plugin text and host text agree on every shipped language.
func (pluginLocaleFormatter) PluralCategory(locale string, count int) string {
	operands, err := i18n.NewOperands(count)
	if err != nil {
		return "other"
	}
	return i18n.PluralCategory(locale, operands).String()
}

// Direction returns MyGo's script-aware writing direction.
func (pluginLocaleFormatter) Direction(locale string) appplugin.UIDirection {
	if i18n.Direction(locale) == i18n.RightToLeft {
		return appplugin.UIDirectionRTL
	}
	return appplugin.UIDirectionLTR
}
