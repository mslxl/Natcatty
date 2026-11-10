package locales_test

import (
	"testing"
	"time"

	"github.com/mslxl/natcatty/ui/locales"
)

func TestRelativeTimeUsesTheLocaleCatalogAndPluralRules(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		locale string
		delta  time.Duration
		want   string
	}{
		{"just now", "en", 0, "just now"},
		{"seconds", "en", -5 * time.Second, "5 seconds ago"},
		{"one minute", "en", -time.Minute, "1 minute ago"},
		{"future hours", "en", 3 * time.Hour, "in 3 hours"},
		{"days", "en", -5 * 24 * time.Hour, "5 days ago"},
		{"months", "en", -60 * 24 * time.Hour, "2 months ago"},
		{"years", "en", -400 * 24 * time.Hour, "1 year ago"},
		{"spanish", "es", -2 * time.Hour, "hace 2 horas"},
		{"spanish one", "es", -time.Hour, "hace 1 hora"},
		{"russian few", "ru", -2 * time.Minute, "2 минуты назад"},
		{"russian many", "ru", -5 * time.Minute, "5 минут назад"},
		{"russian one", "ru", -21 * time.Minute, "21 минуту назад"},
		{"chinese", "zh-CN", -3 * time.Hour, "3 小时前"},
		{"chinese future", "zh-CN", 3 * 24 * time.Hour, "3 天后"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := locales.RelativeTime(test.locale, now.Add(test.delta), now); got != test.want {
				t.Fatalf("RelativeTime(%s, %s) = %q, want %q", test.locale, test.delta, got, test.want)
			}
		})
	}
}
