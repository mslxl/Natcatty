package connection

import (
	"errors"
	"fmt"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
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

// TestProblemTextForNamesProviderFailures checks that a Provider failure and
// the ProxyCommand/Network Profile conflict reach the user as actionable text
// instead of the generic unsupported fallback.
func TestProblemTextForNamesProviderFailures(t *testing.T) {
	providerErr := &appconnection.Error{
		Kind:  appconnection.KindProvider,
		Cause: errors.New("tunnel is down"),
	}
	conflict := appconnection.ProxyCommandNetworkConflict(appconnection.NetworkRoute{
		ProfileID: "net-target", Kind: "tailscale",
	})
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		if got, want := ProblemTextFor(m, providerErr), m.ConnectionErrorProvider(); got == "" || got != want {
			t.Fatalf("%s provider text = %q, want %q", locale, got, want)
		}
		if got, want := ProblemTextFor(m, conflict), m.ConnectionErrorProxyCommandNetwork(); got == "" || got != want {
			t.Fatalf("%s ProxyCommand conflict text = %q, want %q", locale, got, want)
		}
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

// TestProblemTextForNamesTransportImplementationFailures checks the typed
// fail-closed errors of ticket 21 reach the user as actionable text, not the
// generic internal fallback.
func TestProblemTextForNamesTransportImplementationFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want func(locales.Messages) string
	}{
		{"unavailable", fmt.Errorf("%w: in-process", transportmodel.ErrImplementationUnavailable),
			func(m locales.Messages) string { return m.ConnectionErrorTransportUnavailable() }},
		{"unknown", fmt.Errorf("%w: %q", transportmodel.ErrUnknownImplementation, "future_client"),
			func(m locales.Messages) string { return m.ConnectionErrorTransportUnknown() }},
	}
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		for _, tc := range cases {
			if got, want := ProblemTextFor(m, tc.err), tc.want(m); got == "" || got != want {
				t.Fatalf("%s/%s = %q, want %q", locale, tc.name, got, want)
			}
		}
	}
}

func TestProblemTextForNamesMoshAndETFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want func(locales.Messages) string
	}{
		{"mosh-checksum", moshmodel.ErrChecksumMismatch, func(m locales.Messages) string { return m.ConnectionErrorMoshChecksum() }},
		{"mosh-unavailable", moshmodel.ErrBundleUnavailable, func(m locales.Messages) string { return m.ConnectionErrorMoshUnavailable() }},
		{"mosh-platform", moshmodel.ErrPlatformUnsupported, func(m locales.Messages) string { return m.ConnectionErrorMoshUnavailable() }},
		{"mosh-jump", moshmodel.ErrJumpUnsupported, func(m locales.Messages) string { return m.ConnectionErrorMoshJump() }},
		{"mosh-proxy", moshmodel.ErrProxyUnsupported, func(m locales.Messages) string { return m.ConnectionErrorMoshProxy() }},
		{"mosh-handshake", moshmodel.ErrHandshake, func(m locales.Messages) string { return m.ConnectionErrorMoshHandshake() }},
		{"et-checksum", etmodel.ErrChecksumMismatch, func(m locales.Messages) string { return m.ConnectionErrorETChecksum() }},
		{"et-unavailable", etmodel.ErrBundleUnavailable, func(m locales.Messages) string { return m.ConnectionErrorETUnavailable() }},
		{"et-platform", etmodel.ErrPlatformUnsupported, func(m locales.Messages) string { return m.ConnectionErrorETUnavailable() }},
		{"et-jump", etmodel.ErrMultipleJumpHosts, func(m locales.Messages) string { return m.ConnectionErrorETJump() }},
		{"et-proxy", etmodel.ErrProxyUnsupported, func(m locales.Messages) string { return m.ConnectionErrorETProxy() }},
	}
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		for _, tc := range cases {
			if got, want := ProblemTextFor(m, tc.err), tc.want(m); got == "" || got != want {
				t.Fatalf("%s/%s = %q, want %q", locale, tc.name, got, want)
			}
		}
	}
}
