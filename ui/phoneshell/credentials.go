package phoneshell

import (
	"context"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// CredentialService is the phone shell's seam onto the field-secret service's
// availability. The composition root implements it over the system credential
// store; a test substitutes one to simulate an outage and a later recovery.
//
// Status reports whether credentials can be opened and, when they cannot, a
// sentence that says why. Retry attempts the open again and reports whether the
// service is now usable. The Vault home shows the reason, and returning to the
// foreground calls Retry; a successful retry clears the notice and lets the
// composition root attach Vault Sync.
type CredentialService interface {
	Status() (available bool, reason string)
	Retry(ctx context.Context) bool
}

// WithCredentialService gives the shell the field-secret availability seam.
// Without it the Vault home has no credential notice and Foreground has
// nothing to retry.
func WithCredentialService(service CredentialService) Option {
	return func(s *Shell) { s.credentials = service }
}

// credentialUnavailableNotice is the fallback when a CredentialService reports
// an outage without a reason. The production implementation always supplies
// its own sentence.
const credentialUnavailableNotice = "The system credential store is unavailable, so saved credentials are locked and Vault Sync is paused."

// drawCredentialNotice renders the Vault home's outage notice. It is the
// section-root notice only: the other Vault pages stay usable, and their own
// load states report list failures.
func (s *Shell) drawCredentialNotice(c *ui.Context) {
	if s.credentials == nil {
		return
	}
	available, reason := s.credentials.Status()
	if available {
		return
	}
	if reason == "" {
		reason = credentialUnavailableNotice
	}
	kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, reason, nil)
}

// AttachVaultSync gives the Settings page the shared sync engine once the
// credential service becomes usable after a foreground retry. Before that the
// page reports sync as unavailable; attaching the real engine restores the
// configuration surface without rebuilding the Shell.
func (s *Shell) AttachVaultSync(service VaultSyncService) {
	if s.settings != nil {
		s.settings.sync = service
	}
	s.invalidateWindows()
}

// retryCredentials runs when the app returns to the foreground. A successful
// retry repaints every section so the Vault notice disappears; the credential
// owner also fires its own hook to build and attach Vault Sync.
func (s *Shell) retryCredentials() {
	if s.credentials == nil {
		return
	}
	if available, _ := s.credentials.Status(); available {
		return
	}
	if s.credentials.Retry(context.Background()) {
		s.invalidateWindows()
	}
}

// invalidateWindows repaints the open sections. Offscreen testers render on
// demand and have no windows; they still observe the state on the next Frame.
func (s *Shell) invalidateWindows() {
	for _, window := range s.windows {
		if window != nil {
			window.Invalidate()
		}
	}
}
