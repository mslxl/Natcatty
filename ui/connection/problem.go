// Package connection maps application connection failures onto the UI's typed
// classification and localized text. The shell and the Vault share it so a
// failure is always reported the same way.
package connection

import (
	"errors"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/ui/locales"
)

// Kind classifies a connection failure. An error without a typed kind is
// internal, matching the application's own fallback.
func Kind(err error) appconnection.Kind {
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return appconnection.KindInternal
}

// ProblemTextFor localizes a connection failure, preferring the specific
// security-key reason when the cause carries one. Those failures keep the
// generic unsupported/timeout Kind at the protocol level, but the user needs a
// different answer: install OpenSSH, or touch the key before the deadline.
func ProblemTextFor(m locales.Messages, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, appconnection.ErrSecurityKeyUnsupported):
		return m.ConnectionErrorSecurityKeyUnavailable()
	case errors.Is(err, appconnection.ErrSecurityKeyDeviceTimeout):
		return m.ConnectionErrorSecurityKeyTimeout()
	}
	return ProblemText(m, Kind(err))
}

// ProblemText localizes a connection failure. Unknown kinds render nothing, so
// the UI never shows an untranslated token.
func ProblemText(m locales.Messages, kind appconnection.Kind) string {
	switch kind {
	case appconnection.KindAuth:
		return m.ConnectionErrorAuth()
	case appconnection.KindNetwork:
		return m.ConnectionErrorNetwork()
	case appconnection.KindTimeout:
		return m.ConnectionErrorTimeout()
	case appconnection.KindHostKey:
		return m.ConnectionErrorHostKey()
	case appconnection.KindUnsupported:
		return m.ConnectionErrorUnsupported()
	case appconnection.KindCancelled:
		return m.ConnectionErrorCancelled()
	case appconnection.KindX11:
		return m.ConnectionErrorX11()
	case appconnection.KindInternal:
		return m.ConnectionErrorInternal()
	}
	return ""
}
