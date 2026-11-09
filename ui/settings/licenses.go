// SPDX-License-Identifier: GPL-3.0-or-later

package settings

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// LegalDocuments is the text the About and Licenses area shows. The app loads
// it from the files shipped beside the executable; a test supplies fixtures.
type LegalDocuments struct {
	// Version is the application version, shown beside the product name.
	Version string
	// License is the full GNU GPL text.
	License string
	// Notice is the modification and copyright notice.
	Notice string
	// ThirdPartyNotices is the complete third-party notice document.
	ThirdPartyNotices string
	// SourceURL is the release tag's corresponding-source URL.
	SourceURL string
}

// LicensesState owns the About and Licenses area. It is read-only; the only
// action opens the corresponding-source URL in the system browser.
type LicensesState struct {
	documents LegalDocuments
	openURL   func(string) error
	openTried bool
	openOK    bool
}

// LicensesOption customizes a LicensesState.
type LicensesOption func(*LicensesState)

// WithLicensesSourceOpener sets the function that opens the corresponding
// source URL. Without one, the URL is still displayed for the user to copy.
func WithLicensesSourceOpener(fn func(string) error) LicensesOption {
	return func(state *LicensesState) { state.openURL = fn }
}

// NewLicensesState returns the About and Licenses area for documents.
func NewLicensesState(documents LegalDocuments, opts ...LicensesOption) *LicensesState {
	documents.Version = strings.TrimSpace(documents.Version)
	documents.SourceURL = strings.TrimSpace(documents.SourceURL)
	documents.License = strings.TrimSpace(documents.License)
	documents.Notice = strings.TrimSpace(documents.Notice)
	documents.ThirdPartyNotices = strings.TrimSpace(documents.ThirdPartyNotices)
	state := &LicensesState{documents: documents}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Version returns the application version.
func (s *LicensesState) Version() string { return s.documents.Version }

// SourceURL returns the corresponding-source URL.
func (s *LicensesState) SourceURL() string { return s.documents.SourceURL }

// License returns the full license text.
func (s *LicensesState) License() string { return s.documents.License }

// Notice returns the modification notice.
func (s *LicensesState) Notice() string { return s.documents.Notice }

// ThirdPartyNotices returns the third-party notice text.
func (s *LicensesState) ThirdPartyNotices() string { return s.documents.ThirdPartyNotices }

// NoticeText returns the read-only text area's value. The pointer belongs to
// the state so it stays valid across frames.
func (s *LicensesState) NoticeText() *string { return &s.documents.Notice }

// LicenseText returns the read-only text area's value.
func (s *LicensesState) LicenseText() *string { return &s.documents.License }

// ThirdPartyNoticesText returns the read-only text area's value.
func (s *LicensesState) ThirdPartyNoticesText() *string { return &s.documents.ThirdPartyNotices }

// Missing reports whether a document the surface needs is absent. The release
// check fails a published artifact in that state; the surface still opens so a
// developer sees why.
func (s *LicensesState) Missing() bool {
	return s.License() == "" || s.Notice() == "" ||
		s.ThirdPartyNotices() == "" || s.SourceURL() == ""
}

// OpenSource asks the configured opener to show the corresponding source.
// It reports whether an opener ran and succeeded.
func (s *LicensesState) OpenSource() bool {
	s.openTried = true
	s.openOK = false
	if s.openURL == nil || s.SourceURL() == "" {
		return false
	}
	if err := s.openURL(s.SourceURL()); err != nil {
		return false
	}
	s.openOK = true
	return true
}

// OpenFailed reports whether the last OpenSource call failed. The URL stays
// visible so the user can copy it when the system browser cannot open it.
func (s *LicensesState) OpenFailed() bool { return s.openTried && !s.openOK }

// licensesArea is the GPLv3 Appropriate Legal Notices surface: the product and
// version, the license and modification notice, the corresponding-source
// offer, the full license text, and the third-party notices. The texts are
// read-only because they are legal documents, not settings.
func licensesArea(c *ui.Context, state *LicensesState, m locales.Messages, t *ui.Theme) {
	if state == nil {
		return
	}
	section(c, m.SettingsAboutLicenseTitle(), "", t, func() {
		version := state.Version()
		if version != "" {
			ui.Text(c, m.SettingsAboutVersion(version)).FontSize(kit.M(c).Font.Callout).FontWeight(500)
		}
		ui.Text(c, m.SettingsAboutLicenseBody()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
		ui.Text(c, m.SettingsAboutLegalReview()).FontSize(kit.M(c).Font.Caption).TextColor(uitheme.WarningText(t))
	})

	section(c, m.SettingsAboutModificationTitle(), "", t, func() {
		if state.Notice() == "" {
			ui.Text(c, m.SettingsAboutUnavailable()).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
			return
		}
		ui.TextArea(c, state.NoticeText()).ReadOnly(true).FillWidth().Height(170)
	})

	section(c, m.SettingsAboutSourceTitle(), m.SettingsAboutSourceBody(), t, func() {
		if source := state.SourceURL(); source != "" {
			ui.Text(c, source).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
			if kit.PrimaryButton(c, "", m.SettingsAboutOpenSource()).Clicked() {
				state.OpenSource()
				c.Invalidate()
			}
			if state.OpenFailed() {
				ui.Text(c, m.SettingsAboutUnavailable()).FontSize(kit.M(c).Font.Caption).TextColor(uitheme.WarningText(t))
			}
		} else {
			ui.Text(c, m.SettingsAboutUnavailable()).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
		}
	})

	section(c, m.SettingsAboutLicenseTextTitle(), "", t, func() {
		if state.License() == "" {
			ui.Text(c, m.SettingsAboutUnavailable()).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
			return
		}
		ui.TextArea(c, state.LicenseText()).ReadOnly(true).FillWidth().Height(300)
	})

	section(c, m.SettingsAboutNoticesTitle(), m.SettingsAboutNoticesBody(), t, func() {
		if state.ThirdPartyNotices() == "" {
			ui.Text(c, m.SettingsAboutUnavailable()).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
			return
		}
		ui.TextArea(c, state.ThirdPartyNoticesText()).ReadOnly(true).FillWidth().Height(300)
	})
}
