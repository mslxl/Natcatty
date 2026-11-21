package connection

import (
	"errors"
	"fmt"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// TestProblemTextForNamesVNCStrongAuthFailures proves the VNC certificate and
// unsupported-security failures get their own localized text in every
// language, and a rejected password still falls back to the auth text.
func TestProblemTextForNamesVNCStrongAuthFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want func(locales.Messages) string
	}{
		{
			name: "certificate",
			err:  fmt.Errorf("vnc: %w", rfb.ErrTLSVerification),
			want: func(m locales.Messages) string { return m.ConnectionErrorVNCCertificate() },
		},
		{
			name: "unsupported",
			err:  &rfb.SecurityError{Offered: []rfb.SecurityType{rfb.SecurityType(113)}},
			want: func(m locales.Messages) string { return m.ConnectionErrorVNCUnsupported() },
		},
	}
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		for _, tc := range cases {
			t.Run(locale+"/"+tc.name, func(t *testing.T) {
				got := ProblemTextFor(m, tc.err)
				if got == "" {
					t.Fatalf("no text for a %s failure", tc.name)
				}
				if want := tc.want(m); got != want {
					t.Fatalf("text = %q, want %q", got, want)
				}
			})
		}
	}

	// A rejected credential is an auth failure, not an unsupported-security
	// failure, even though both are *SecurityError.
	rejected := &appconnection.Error{
		Kind:  appconnection.KindAuth,
		Cause: &rfb.SecurityError{Rejected: true, Reason: "authentication failed"},
	}
	if got, want := ProblemTextFor(locales.In("en"), rejected),
		locales.In("en").ConnectionErrorAuth(); got != want {
		t.Fatalf("rejected password text = %q, want %q", got, want)
	}
}

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

// TestProblemTextForNamesPlaintextCancel proves cancelling the unencrypted-VNC
// confirmation is a deliberate user action with its own localized text, not the
// generic internal failure that used to replace it.
func TestProblemTextForNamesPlaintextCancel(t *testing.T) {
	declined := fmt.Errorf("%w: %w", rfb.ErrSecurityDeclined, sessionui.ErrPlaintextDeclined)
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		got := ProblemTextFor(m, declined)
		if want := m.ConnectionErrorVNCCancelled(); got == "" || got != want {
			t.Fatalf("%s cancel text = %q, want %q", locale, got, want)
		}
		if got == m.ConnectionErrorInternal() {
			t.Fatalf("%s cancel text fell back to the internal failure", locale)
		}
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

func TestProblemTextForNamesMoshFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want func(locales.Messages) string
	}{
		{"mosh-jump", moshmodel.ErrJumpUnsupported, func(m locales.Messages) string { return m.ConnectionErrorMoshJump() }},
		{"mosh-proxy", moshmodel.ErrProxyUnsupported, func(m locales.Messages) string { return m.ConnectionErrorMoshProxy() }},
		{"mosh-network", moshmodel.ErrNetworkUnsupported, func(m locales.Messages) string { return m.ConnectionErrorMoshNetwork() }},
		{"mosh-handshake", moshmodel.ErrHandshake, func(m locales.Messages) string { return m.ConnectionErrorMoshHandshake() }},
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
