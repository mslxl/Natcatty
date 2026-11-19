package settings

import (
	"github.com/egoist/mygo/ui"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// transportArea holds the Mosh client's display settings. Both transports use
// their in-process clients, so there is no client implementation to choose.
func transportArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		settings := state.Settings()
		prediction := string(settings.Transport.MoshPredictionDisplay)
		predictionChanged := false

		// The local-echo mode is stock mosh's MOSH_PREDICTION_DISPLAY choice.
		// Adaptive is stock's default; always and never are the explicit
		// overrides.
		kit.FormRow(c, m.SettingsMoshPredictionDisplay(), func() {
			ui.RadioGroup(c, func() {
				if ui.Radio(c, &prediction, string(moshmodel.PredictionAdaptive),
					m.SettingsMoshPredictionAdaptive()).Changed() {
					predictionChanged = true
				}
				if ui.Radio(c, &prediction, string(moshmodel.PredictionAlways),
					m.SettingsMoshPredictionAlways()).Changed() {
					predictionChanged = true
				}
				if ui.Radio(c, &prediction, string(moshmodel.PredictionNever),
					m.SettingsMoshPredictionNever()).Changed() {
					predictionChanged = true
				}
			}).Label(m.SettingsMoshPredictionDisplay())
		}).Description(m.SettingsMoshPredictionHint())

		if predictionChanged {
			state.Update(func(settings *domain.Settings) {
				settings.Transport.MoshPredictionDisplay = moshmodel.PredictionDisplay(prediction)
			})
			c.Invalidate()
		}

		// The app-wide default Proxy is the last level before a direct
		// connection. A Host or Host Group that selects its own Profile, or an
		// explicit Direct choice on either, still wins.
		defaultProxy := state.Settings().DefaultProxyProfileID()
		defaultProxyChanged := false
		kit.FormRow(c, m.SettingsDefaultProxy(), func() {
			ui.RadioGroup(c, func() {
				if ui.Radio(c, &defaultProxy, "", m.SettingsDefaultProxyOff()).Changed() {
					defaultProxyChanged = true
				}
				for _, profile := range state.ProxyProfiles() {
					if ui.Radio(c, &defaultProxy, profile.ID, profile.Label).Changed() {
						defaultProxyChanged = true
					}
				}
			}).Label(m.SettingsDefaultProxy())
		}).Description(m.SettingsDefaultProxyHint())

		if defaultProxyChanged {
			state.Update(func(settings *domain.Settings) {
				settings.Proxy.DefaultProfileID = defaultProxy
			})
			c.Invalidate()
		}

		groupReset(c, m, t, m.SettingsAreaTransport(), func() bool {
			return state.ResetGroup(domain.GroupTransport)
		}, true)
		saveProblem(c, state, m, t)
	})
}
