package app

import (
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// The phone handle is the Shell's link from a notification's SessionID to the
// Tab and Pane that own it.
var _ phoneshell.SessionIdentity = (*phoneSessionHandle)(nil)

// SessionID reports the application Session behind the phone's handle, so the
// shell can resolve a terminal notification's SessionID to the Tab and Pane
// that own it. It is a runtime identity and never reaches a notification
// payload; the persisted Tab and Pane identities do.
func (h *phoneSessionHandle) SessionID() model.SessionID {
	if h == nil || h.session == nil {
		return ""
	}
	return h.session.ID()
}
