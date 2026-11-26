package phoneshell

import (
	"github.com/egoist/mygo/ui"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// transport draws the shared Transport group: the Mosh prediction display and
// the app-wide default Proxy. Both are the same fields the desktop's Transport
// page writes through the shared preferences document. The phone Session
// runtime reads the prediction both when a Session opens and while one is
// already running; the default Proxy is portable, so Vault sync carries it to
// the user's other devices. A write goes through the shared preferences
// service, whose normalization maps an unknown stored prediction back to
// stock mosh's adaptive mode, so an old document never reaches a Session with
// an unusable choice.
func (p *settingsPages) transport(c *ui.Context) {
	m := locales.In(c.Locale())
	prediction := p.service.Preferences().Transport.MoshPredictionDisplay.Normalized()
	p.choices(c, m.SettingsMoshPredictionDisplay(), []choice{
		pick(m.SettingsMoshPredictionAdaptive(), prediction == moshmodel.PredictionAdaptive, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Transport.MoshPredictionDisplay = moshmodel.PredictionAdaptive
			})
		}),
		pick(m.SettingsMoshPredictionAlways(), prediction == moshmodel.PredictionAlways, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Transport.MoshPredictionDisplay = moshmodel.PredictionAlways
			})
		}),
		pick(m.SettingsMoshPredictionNever(), prediction == moshmodel.PredictionNever, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Transport.MoshPredictionDisplay = moshmodel.PredictionNever
			})
		}),
	})
	kit.EmptyHint(c, m.SettingsMoshPredictionHint())

	// The default sits below a Host or Host Group proxy choice, exactly as on
	// the desktop. The profile list is the Vault's shared list, so a profile
	// created on the Proxies page can be selected here without a reload.
	defaultProxy := p.service.Preferences().DefaultProxyProfileID()
	proxyChoices := []choice{
		pick(m.SettingsDefaultProxyOff(), defaultProxy == "", func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Proxy.DefaultProfileID = ""
			})
		}),
	}
	if p.proxyProfiles != nil {
		for _, profile := range p.proxyProfiles() {
			profile := profile
			proxyChoices = append(proxyChoices, pick(profile.Label, defaultProxy == profile.ID, func() {
				p.updatePreferences(func(s *domainpreferences.Settings) {
					s.Proxy.DefaultProfileID = profile.ID
				})
			}))
		}
	}
	p.choices(c, m.SettingsDefaultProxy(), proxyChoices)
	kit.EmptyHint(c, m.SettingsDefaultProxyHint())
}
