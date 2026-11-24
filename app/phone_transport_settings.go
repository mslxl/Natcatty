// Phone wiring for the shared Transport settings.
//
// The phone Settings page edits the same preferences document the desktop
// uses, and the shared Session service owns the live rule: a Mosh Session
// opened later reads the value at Open, while one already running is handed
// the new local-echo mode through its transport. This file is the one bridge
// between the phone's composition root and that service, so the change hook,
// new Sessions, and existing Sessions cannot drift apart.

package app

import (
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// ApplyTransportSettings pushes the shared Transport group into the phone's
// Session runtime. It is safe before any Session exists and on a nil runtime,
// which is what the preference change hook sees during construction.
func (r *phoneSessionRuntime) ApplyTransportSettings(settings domainpreferences.Settings) {
	if r == nil || r.service == nil {
		return
	}
	r.service.SetMoshPredictionDisplay(settings.Transport.MoshPredictionDisplay)
}
