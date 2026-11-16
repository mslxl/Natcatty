package state

import (
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TOTPQRPicker opens the native image picker and returns every QR payload it
// can decode from the chosen file. The app wires the MyGo dialog and
// adapters/qrimage; tests provide a fake. A cancelled dialog returns no
// payloads and no error.
type TOTPQRPicker interface {
	PickTOTPQRPayloads() (payloads []string, err error)
}

// WithTOTPQRPicker gives the Host editor QR-image import for its
// authenticator. Without it the editor still accepts manual URI or secret
// entry.
func WithTOTPQRPicker(picker TOTPQRPicker) Option {
	return func(s *State) { s.totpPicker = picker }
}

// ChooseTOTPQRCode opens the image picker and fills the editor with the first
// TOTP it can read from a QR symbol. A cancelled dialog leaves the form
// unchanged; an unreadable image, a HOTP symbol, or a migration payload is
// reported through Problem.
func (s *State) ChooseTOTPQRCode() bool {
	if !s.editing {
		return false
	}
	if s.totpPicker == nil {
		s.problem = ProblemTOTPImage
		s.redraw()
		return false
	}
	payloads, err := s.totpPicker.PickTOTPQRPayloads()
	if err != nil {
		s.problem = ProblemTOTPImage
		s.redraw()
		return false
	}
	if len(payloads) == 0 {
		return false
	}
	parsed, err := domainvault.SelectTOTPFromQRPayloads(payloads)
	if err != nil {
		s.problem = ProblemTOTP
		s.redraw()
		return false
	}
	s.form.TOTP = parsed.URI()
	s.form.ClearTOTP = false
	s.problem = ProblemNone
	s.redraw()
	return true
}
