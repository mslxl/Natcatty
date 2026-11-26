// Quick Connect's application seam. The phone form parses a target with the
// shared launch parser and hands the resulting Unsaved Host here; unlike a
// saved Host, there is no Vault record to resolve, so the runtime opens the
// StartSpec directly. The one-shot credential travels only into that StartSpec
// and is never written to the Vault or a workspace descriptor.

package app

import (
	"context"
	"errors"

	appsession "github.com/mslxl/natcatty/application/session"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// The runtime is the shell's Quick Connect opener, exactly as it is the saved
// Host opener.
var _ phoneshell.QuickConnectOpener = (*phoneSessionRuntime)(nil)

// errNoQuickConnectService reports a build whose Session service is absent.
var errNoQuickConnectService = errors.New("app: this build has no Session service")

// OpenTarget opens a Quick Connect target as an Unsaved Host Session. It
// follows the desktop rule for an Unsaved Host: the shared HostSpec maps the
// endpoint, the typed secret is offered as a one-shot credential, and no Vault
// lookup or saved record is involved. The returned handle is the same phone
// handle a saved Host gets, so close, hibernate, resume, and retry behave
// identically.
func (r *phoneSessionRuntime) OpenTarget(ctx context.Context, host domainvault.Host,
	secret string) (phoneshell.SessionHandle, error) {

	if r == nil || r.service == nil {
		return nil, errNoQuickConnectService
	}
	spec := appsession.HostSpec(host)
	if phoneRefusesKind(spec.Kind) {
		return nil, errPhoneLocalSession
	}
	// A one-shot credential belongs to this target alone. The Session factory
	// marks a Pane with an empty Host ID as unsaved, so the transport is never
	// parked or shared with another target.
	spec.Password = secret
	// A phone has no window size before its first frame; the terminal view
	// resizes the Session as soon as it is laid out, exactly as a saved Host's
	// open does.
	if spec.Cols == 0 {
		spec.Cols = 80
	}
	if spec.Rows == 0 {
		spec.Rows = 24
	}
	session, err := r.service.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	handle := &phoneSessionHandle{session: session}
	r.trackHandle(handle)
	return handle, nil
}
