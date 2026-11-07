// SPDX-License-Identifier: GPL-3.0-or-later

package settings

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

func legalFixture() LegalDocuments {
	return LegalDocuments{
		Version:           "0.1.0",
		License:           "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n",
		Notice:            "This is a modified version of Netcatty.\nGPL-3.0-or-later\n",
		ThirdPartyNotices: "MyGo\nlibghostty-vt\nmosh-go\nEternalTerminal\nNetcatty\n",
		SourceURL:         "https://github.com/mslxl/natcatty/tree/v0.1.0",
	}
}

func TestAboutAreaShowsTheLicenseSourceAndNotices(t *testing.T) {
	m := locales.In("en")
	var opened string
	state := NewLicensesState(legalFixture(),
		WithLicensesSourceOpener(func(url string) error {
			opened = url
			return nil
		}))
	tester := ui.NewTester(viewAt(AreaAbout, Config{Licenses: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	for _, want := range []string{
		m.SettingsAreaAbout(),
		m.SettingsAboutVersion("0.1.0"),
		m.SettingsAboutLicenseTitle(),
		m.SettingsAboutModificationTitle(),
		m.SettingsAboutSourceTitle(),
		m.SettingsAboutNoticesTitle(),
		"https://github.com/mslxl/natcatty/tree/v0.1.0",
	} {
		if !tester.HasText(want) {
			t.Errorf("the About area does not show %q: %v", want, tester.Texts())
		}
	}

	if state.Missing() {
		t.Fatal("a complete fixture reported missing documents")
	}
	scrollToText(t, tester, m.SettingsAboutOpenSource(), 1100, 720)
	if err := tester.Click(m.SettingsAboutOpenSource()); err != nil {
		t.Fatalf("the corresponding-source button is missing: %v", err)
	}
	tester.Frame()
	if opened != state.SourceURL() {
		t.Fatalf("opened %q, want %q", opened, state.SourceURL())
	}
}

func TestAboutAreaReportsMissingDocuments(t *testing.T) {
	m := locales.In("en")
	state := NewLicensesState(LegalDocuments{Version: "dev"},
		WithLicensesSourceOpener(func(string) error { return nil }))
	tester := ui.NewTester(viewAt(AreaAbout, Config{Licenses: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	if !state.Missing() {
		t.Fatal("an empty fixture did not report missing documents")
	}
	if !tester.HasText(m.SettingsAboutUnavailable()) {
		t.Fatalf("the missing-document message is not shown: %v", tester.Texts())
	}
}

func TestOpenSourceReportsFailureWithoutHidingTheURL(t *testing.T) {
	state := NewLicensesState(legalFixture(),
		WithLicensesSourceOpener(func(string) error { return errors.New("no browser") }))
	if state.OpenSource() {
		t.Fatal("OpenSource reported success after the opener failed")
	}
	if !state.OpenFailed() {
		t.Fatal("OpenFailed did not report the failed opener")
	}
	if state.SourceURL() == "" {
		t.Fatal("the source URL disappeared after a failed open")
	}

	withoutOpener := NewLicensesState(legalFixture())
	if withoutOpener.OpenSource() || !withoutOpener.OpenFailed() {
		t.Fatal("OpenSource without an opener did not report a failure")
	}
}

func TestAboutAreaIsSearchableByLicenseTerms(t *testing.T) {
	hits := FilterHits(SearchIndex(locales.In("en")), "third party notices")
	found := false
	for _, hit := range hits {
		if hit.Area == AreaAbout {
			found = true
		}
	}
	if !found {
		t.Fatal("the third-party notices are not reachable through Settings search")
	}
}
