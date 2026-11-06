package settings

import (
	"github.com/egoist/mygo/ui"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// transportChoice is one of the three global selections. Following the
// release default is a distinct state from choosing the same client
// explicitly, so it cannot be represented by an Implementation value alone.
type transportChoice struct {
	followDefault  bool
	implementation transportmodel.Implementation
}

var releaseDefaultChoice = transportChoice{followDefault: true}

func chosenTransport(value transportmodel.Implementation) transportChoice {
	return transportChoice{implementation: value}
}

// transportArea is the global Mosh and EternalTerminal client choice. The
// in-process client is a first-class opt-in beta and is selectable both
// globally and per Host. The bundled client remains the release default and
// the rollback path until the release process flips transport.ReleasePolicy
// after both parity gates are green. A value saved by a newer build stays in
// the document untouched and is called out so the user can repair it here
// without a silent fallback.
func transportArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		settings := state.Settings()
		// "Release default" is a distinct choice from an explicit bundled
		// choice, even while the two currently resolve to the same client.
		choice := releaseDefaultChoice
		if settings.Transport.ImplementationExplicit {
			choice = chosenTransport(settings.Transport.Implementation)
		}
		prediction := string(settings.Transport.MoshPredictionDisplay)
		implementationChanged := false
		predictionChanged := false

		ui.Field(c, m.SettingsTransportImplementation(), func() {
			ui.RadioGroup(c, func() {
				if ui.Radio(c, &choice, releaseDefaultChoice,
					m.SettingsTransportUseReleaseDefault()).Changed() {
					implementationChanged = true
				}
				if ui.Radio(c, &choice, chosenTransport(transportmodel.Bundled),
					m.SettingsTransportBundled()).Changed() {
					implementationChanged = true
				}
				if ui.Radio(c, &choice, chosenTransport(transportmodel.InProcess),
					m.SettingsTransportInProcess()).Changed() {
					implementationChanged = true
				}
			}).Label(m.SettingsTransportImplementation())
		}).Description(m.SettingsTransportImplementationHint())

		defaultLabel := m.SettingsTransportBundled()
		if transportmodel.CurrentDefault() == transportmodel.InProcess {
			defaultLabel = m.SettingsTransportInProcess()
		}
		ui.Text(c, m.SettingsTransportEffectiveDefault(defaultLabel)).
			FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, m.SettingsTransportBetaHint(defaultLabel)).
			FontSize(12).TextColor(t.TextMuted)

		// The local-echo mode is stock mosh's MOSH_PREDICTION_DISPLAY choice.
		// Adaptive is stock's default; always and never are the explicit
		// overrides.
		ui.Field(c, m.SettingsMoshPredictionDisplay(), func() {
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

		if implementationChanged || predictionChanged {
			state.Update(func(settings *domain.Settings) {
				if implementationChanged {
					if choice.followDefault {
						settings.Transport.FollowReleaseDefault()
					} else {
						settings.Transport.ChooseImplementation(choice.implementation)
					}
				}
				if predictionChanged {
					settings.Transport.MoshPredictionDisplay = moshmodel.PredictionDisplay(prediction)
				}
			})
			c.Invalidate()
		}

		value := choice.implementation
		if !choice.followDefault && value != transportmodel.Unspecified &&
			!transportmodel.Known(value) {
			ui.Text(c, m.SettingsTransportUnknown()).FontSize(12).TextColor(uitheme.DangerText(t))
		}
		groupReset(c, m, t, m.SettingsAreaTransport(), func() bool {
			return state.ResetGroup(domain.GroupTransport)
		}, true)
		saveProblem(c, state, m, t)
	})
}
