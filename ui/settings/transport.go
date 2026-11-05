package settings

import (
	"github.com/egoist/mygo/ui"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// transportArea is the global Mosh and EternalTerminal client choice. The
// selection is persisted exactly as chosen, including the in-process client
// whose transport lands in later tickets: the factory fails closed with a
// typed ErrImplementationUnavailable for it rather than silently falling back
// to the bundled client. A value saved by a newer build stays in the document
// untouched and is called out so the user can repair it here.
func transportArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		selected := string(state.Settings().Transport.Implementation)
		prediction := string(state.Settings().Transport.MoshPredictionDisplay)
		changed := false
		ui.Field(c, m.SettingsTransportImplementation(), func() {
			ui.RadioGroup(c, func() {
				if ui.Radio(c, &selected, string(transportmodel.Bundled),
					m.SettingsTransportBundled()).Changed() {
					changed = true
				}
				// The in-process clients are later tickets, so the label
				// says they are not available yet. The choice stays
				// selectable so it can be persisted and rolled out; opening
				// a Session with it fails closed with a typed error.
				if ui.Radio(c, &selected, string(transportmodel.InProcess),
					m.SettingsTransportInProcess()).Changed() {
					changed = true
				}
			}).Label(m.SettingsTransportImplementation())
		}).Description(m.SettingsTransportImplementationHint())

		// The local-echo mode is stock mosh's MOSH_PREDICTION_DISPLAY choice.
		// Adaptive is stock's default; always and never are the explicit
		// overrides.
		ui.Field(c, m.SettingsMoshPredictionDisplay(), func() {
			ui.RadioGroup(c, func() {
				if ui.Radio(c, &prediction, string(moshmodel.PredictionAdaptive),
					m.SettingsMoshPredictionAdaptive()).Changed() {
					changed = true
				}
				if ui.Radio(c, &prediction, string(moshmodel.PredictionAlways),
					m.SettingsMoshPredictionAlways()).Changed() {
					changed = true
				}
				if ui.Radio(c, &prediction, string(moshmodel.PredictionNever),
					m.SettingsMoshPredictionNever()).Changed() {
					changed = true
				}
			}).Label(m.SettingsMoshPredictionDisplay())
		}).Description(m.SettingsMoshPredictionHint())

		if changed {
			state.Update(func(settings *domain.Settings) {
				settings.Transport.Implementation = transportmodel.Implementation(selected)
				settings.Transport.MoshPredictionDisplay = moshmodel.PredictionDisplay(prediction)
			})
			c.Invalidate()
		}

		value := transportmodel.Implementation(selected)
		if value != transportmodel.Unspecified && !transportmodel.Known(value) {
			ui.Text(c, m.SettingsTransportUnknown()).FontSize(12).TextColor(uitheme.DangerText(t))
		}
		groupReset(c, m, t, m.SettingsAreaTransport(), func() bool {
			return state.ResetGroup(domain.GroupTransport)
		}, true)
		saveProblem(c, state, m, t)
	})
}
