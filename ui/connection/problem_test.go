package connection

import (
	"errors"
	"fmt"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/ui/locales"
)

func TestProblemTextForNamesSecurityKeyFailures(t *testing.T) {
	cases := []struct {
		locale string
		err    error
		want   func(locales.Messages) string
	}{
		{
			locale: "en",
			err: appconnection.SecurityKeyUnsupported(
				errors.New("ssh: agent unavailable")),
			want: func(m locales.Messages) string { return m.ConnectionErrorSecurityKeyUnavailable() },
		},
		{
			locale: "en",
			err:    appconnection.SecurityKeyTimeout(nil),
			want:   func(m locales.Messages) string { return m.ConnectionErrorSecurityKeyTimeout() },
		},
		{
			locale: "zh-CN",
			err: appconnection.SecurityKeyUnsupported(
				errors.New("ssh: agent unavailable")),
			want: func(m locales.Messages) string { return m.ConnectionErrorSecurityKeyUnavailable() },
		},
		{
			locale: "zh-CN",
			err:    appconnection.SecurityKeyTimeout(nil),
			want:   func(m locales.Messages) string { return m.ConnectionErrorSecurityKeyTimeout() },
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s-%d", tc.locale, i), func(t *testing.T) {
			m := locales.In(tc.locale)
			got := ProblemTextFor(m, tc.err)
			if got == "" {
				t.Fatal("no text for a security key failure")
			}
			if want := tc.want(m); got != want {
				t.Fatalf("text = %q, want %q", got, want)
			}
		})
	}
}

func TestProblemTextForFallsBackToTheProtocolKind(t *testing.T) {
	m := locales.In("en")
	err := &appconnection.Error{Kind: appconnection.KindAuth, Cause: appconnection.ErrAuthentication}
	if got, want := ProblemTextFor(m, err), m.ConnectionErrorAuth(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if got := ProblemTextFor(m, nil); got != "" {
		t.Fatalf("text for a nil error = %q, want none", got)
	}
}

func TestSecurityKeyMessagesExistInEveryLocale(t *testing.T) {
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		for name, text := range map[string]string{
			"unavailable": m.ConnectionErrorSecurityKeyUnavailable(),
			"timeout":     m.ConnectionErrorSecurityKeyTimeout(),
			"empty":       m.VaultHostSecurityKeyEmpty(),
			"agent-miss":  m.VaultHostSecurityKeyUnavailable(),
			"ready":       m.VaultHostSecurityKeyReady(2),
		} {
			if text == "" {
				t.Fatalf("%s: %s message is empty", locale, name)
			}
		}
	}
}
