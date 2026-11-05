// Package connection maps application connection failures onto the UI's typed
// classification and localized text. The shell and the Vault share it so a
// failure is always reported the same way.
package connection

import (
	"errors"

	appconnection "github.com/mslxl/natcatty/application/connection"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
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
	// A ProxyCommand plus Network Profile selection is a specific unsupported
	// combination, not a generic negotiation failure: name both so the user
	// knows which setting to remove.
	case errors.Is(err, appconnection.ErrProxyCommandNetworkConflict):
		return m.ConnectionErrorProxyCommandNetwork()
	case errors.Is(err, appconnection.ErrSecurityKeyUnsupported):
		return m.ConnectionErrorSecurityKeyUnavailable()
	case errors.Is(err, appconnection.ErrSecurityKeyDeviceTimeout):
		return m.ConnectionErrorSecurityKeyTimeout()
	// A persisted implementation choice this build cannot honor is the
	// actionable answer, ahead of the generic internal text: the user can
	// repair it in the Host editor or Settings.
	case errors.Is(err, transportmodel.ErrImplementationUnavailable):
		return m.ConnectionErrorTransportUnavailable()
	case errors.Is(err, transportmodel.ErrUnknownImplementation):
		return m.ConnectionErrorTransportUnknown()
	// A Mosh or EternalTerminal failure is more specific than the generic
	// internal text: the user can act on a missing bundle, a checksum failure,
	// an unsupported route, or a server that never announced itself.
	case errors.Is(err, moshmodel.ErrChecksumMismatch):
		return m.ConnectionErrorMoshChecksum()
	case errors.Is(err, moshmodel.ErrBundleUnavailable),
		errors.Is(err, moshmodel.ErrPlatformUnsupported):
		return m.ConnectionErrorMoshUnavailable()
	case errors.Is(err, moshmodel.ErrJumpUnsupported):
		return m.ConnectionErrorMoshJump()
	case errors.Is(err, moshmodel.ErrProxyUnsupported):
		return m.ConnectionErrorMoshProxy()
	case errors.Is(err, moshmodel.ErrNetworkUnsupported):
		return m.ConnectionErrorMoshNetwork()
	case errors.Is(err, moshmodel.ErrHandshake):
		return m.ConnectionErrorMoshHandshake()
	case errors.Is(err, etmodel.ErrNetworkUnsupported):
		return m.ConnectionErrorETNetwork()
	case errors.Is(err, etmodel.ErrChecksumMismatch):
		return m.ConnectionErrorETChecksum()
	case errors.Is(err, etmodel.ErrBundleUnavailable),
		errors.Is(err, etmodel.ErrPlatformUnsupported):
		return m.ConnectionErrorETUnavailable()
	case errors.Is(err, etmodel.ErrMultipleJumpHosts):
		return m.ConnectionErrorETJump()
	case errors.Is(err, etmodel.ErrProxyUnsupported):
		return m.ConnectionErrorETProxy()
	}
	return ProblemText(m, Kind(err))
}

// ProviderPhaseText localizes a Network Provider lifecycle phase. State stores
// the opaque phase; the Logs and Vault surfaces share this mapping. An unknown
// phase renders nothing, so the UI never shows an untranslated token.
func ProviderPhaseText(m locales.Messages, phase appconnection.ProviderPhase) string {
	switch phase {
	case appconnection.ProviderPhaseStopped:
		return m.NetworkProviderPhaseStopped()
	case appconnection.ProviderPhaseStarting:
		return m.NetworkProviderPhaseStarting()
	case appconnection.ProviderPhaseNeedsLogin:
		return m.NetworkProviderPhaseNeedsLogin()
	case appconnection.ProviderPhaseRunning:
		return m.NetworkProviderPhaseRunning()
	case appconnection.ProviderPhaseStopping:
		return m.NetworkProviderPhaseStopping()
	case appconnection.ProviderPhaseError:
		return m.NetworkProviderPhaseError()
	}
	return ""
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
	case appconnection.KindProvider:
		return m.ConnectionErrorProvider()
	case appconnection.KindInternal:
		return m.ConnectionErrorInternal()
	}
	return ""
}
