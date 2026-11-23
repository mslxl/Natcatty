package settings

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// newMotionTester renders the Appearance area with the Motion defaults at a
// window tall enough that every control is laid out without scrolling.
func newMotionTester(t *testing.T) (*fakePreferences, *ui.Tester) {
	t.Helper()
	prefs := newFakePreferences()
	tester := ui.NewTester(viewAt(AreaAppearance, Config{
		Preferences: NewPreferencesState(prefs),
	}), 1100, 1800)
	tester.SetLocale("en")
	tester.Frame()
	return prefs, tester
}

// motionDependentLabels are the controls the master switch hides.
func motionDependentLabels(m locales.Messages) []string {
	return []string{
		m.SettingsMotionPaneTitle(),
		m.SettingsMotionPaneOpen(), m.SettingsMotionPaneClose(), m.SettingsMotionPaneReorder(),
		m.SettingsMotionTabTitle(),
		m.SettingsMotionTabOpen(), m.SettingsMotionTabClose(),
		m.SettingsMotionTabSwitch(), m.SettingsMotionTabReorder(),
		m.SettingsMotionOpenStyle(), m.SettingsMotionCloseStyle(), m.SettingsMotionSwitchStyle(),
		m.SettingsMotionSpeed(),
	}
}

func TestMotionSectionRendersEveryControl(t *testing.T) {
	_, tester := newMotionTester(t)
	m := locales.In("en")

	for _, label := range append([]string{
		m.SettingsMotionTitle(), m.SettingsMotionDescription(), m.SettingsMotionEnabled(),
	}, motionDependentLabels(m)...) {
		if _, ok := tester.Find(label); !ok {
			t.Errorf("%q is missing: %v", label, tester.Texts())
		}
	}
	// Each selector previews its default choice, and the speed is a three-tier
	// segmented control. The other style names live in a closed popup.
	for _, label := range []string{
		m.SettingsMotionStylePopin(), m.SettingsMotionSwitchStyleSlide(),
		m.SettingsMotionSpeedSlow(), m.SettingsMotionSpeedStandard(), m.SettingsMotionSpeedFast(),
	} {
		if !tester.HasText(label) {
			t.Errorf("%q is missing: %v", label, tester.Texts())
		}
	}
}

// TestMotionMasterOffHidesDependentsButKeepsTheirValues covers the visibility
// contract: off hides the seven event switches, three selectors and speed, and
// turning it back on restores every earlier choice.
func TestMotionMasterOffHidesDependentsButKeepsTheirValues(t *testing.T) {
	prefs, tester := newMotionTester(t)
	prefs.settings.Appearance.Motion.PaneClose = false
	prefs.settings.Appearance.Motion.TabSwitch = false
	prefs.settings.Appearance.Motion.OpenStyle = domain.MotionGnomed
	prefs.settings.Appearance.Motion.SwitchStyle = domain.MotionSwitchSlideFadeVert
	prefs.settings.Appearance.Motion.Speed = domain.MotionFast
	tester.Frame()
	m := locales.In("en")
	before := prefs.settings.Appearance.Motion

	if err := tester.Click(m.SettingsMotionEnabled()); err != nil {
		t.Fatalf("click the master switch: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion.Enabled {
		t.Fatal("the master switch did not turn motion off")
	}
	// Only Enabled changed; the hidden choices keep their values.
	want := before
	want.Enabled = false
	if prefs.settings.Appearance.Motion != want {
		t.Fatalf("motion after turning it off = %+v, want %+v",
			prefs.settings.Appearance.Motion, want)
	}
	for _, label := range motionDependentLabels(m) {
		if _, ok := tester.Find(label); ok {
			t.Errorf("%q is still visible while motion is off", label)
		}
	}
	if !tester.HasText(m.SettingsMotionEnabled()) {
		t.Fatal("the master switch itself disappeared")
	}
	// The master's own reset and the Appearance group reset stay available
	// while the dependent controls are hidden.
	if !tester.HasText(m.SettingsReset()) {
		t.Fatalf("the master switch lost its reset: %v", tester.Texts())
	}
	if !tester.HasText(m.SettingsResetGroup(m.SettingsAppearanceTitle())) {
		t.Fatalf("the Appearance group reset disappeared: %v", tester.Texts())
	}

	if err := tester.Click(m.SettingsMotionEnabled()); err != nil {
		t.Fatalf("click the master switch again: %v", err)
	}
	tester.Frame()
	restored := prefs.settings.Appearance.Motion
	if !restored.Enabled || restored.PaneClose || restored.TabSwitch ||
		restored.OpenStyle != domain.MotionGnomed || restored.Speed != domain.MotionFast {
		t.Fatalf("motion after re-enabling = %+v, want the earlier values", restored)
	}
	for _, label := range motionDependentLabels(m) {
		if _, ok := tester.Find(label); !ok {
			t.Errorf("%q did not come back: %v", label, tester.Texts())
		}
	}
}

// TestMotionStyleSelectsWriteTheChosenStyle drives all three selectors through
// their popups.
func TestMotionStyleSelectsWriteTheChosenStyle(t *testing.T) {
	prefs, tester := newMotionTester(t)
	m := locales.In("en")

	if err := tester.Click(m.SettingsMotionStylePopin()); err != nil {
		t.Fatalf("open the opening-style select: %v", err)
	}
	tester.Frame()
	if err := tester.Click(m.SettingsMotionStyleGnomed()); err != nil {
		t.Fatalf("choose GNOME collapse: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion.OpenStyle != domain.MotionGnomed {
		t.Fatalf("opening style = %q, want gnomed", prefs.settings.Appearance.Motion.OpenStyle)
	}

	// The opening select now shows GNOME collapse, so the remaining default
	// "Pop in" trigger is the closing style's.
	if err := tester.Click(m.SettingsMotionStylePopin()); err != nil {
		t.Fatalf("open the closing-style select: %v", err)
	}
	tester.Frame()
	if err := tester.Click(m.SettingsMotionStyleSlide()); err != nil {
		t.Fatalf("choose slide: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion.CloseStyle != domain.MotionSlide {
		t.Fatalf("closing style = %q, want slide", prefs.settings.Appearance.Motion.CloseStyle)
	}
	if prefs.settings.Appearance.Motion.OpenStyle != domain.MotionGnomed {
		t.Fatalf("the closing style changed the opening style to %q",
			prefs.settings.Appearance.Motion.OpenStyle)
	}

	if err := tester.Click(m.SettingsMotionSwitchStyleSlide()); err != nil {
		t.Fatalf("open the switching-style select: %v", err)
	}
	tester.Frame()
	if err := tester.Click(m.SettingsMotionSwitchStyleSlideFadeVert()); err != nil {
		t.Fatalf("choose vertical slide with fade: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion.SwitchStyle != domain.MotionSwitchSlideFadeVert {
		t.Fatalf("switching style = %q, want slidefadevert",
			prefs.settings.Appearance.Motion.SwitchStyle)
	}
}

func TestMotionSpeedWritesTheChosenTier(t *testing.T) {
	prefs, tester := newMotionTester(t)
	m := locales.In("en")

	if err := tester.Click(m.SettingsMotionSpeedFast()); err != nil {
		t.Fatalf("click Fast: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion.Speed != domain.MotionFast {
		t.Fatalf("speed = %q, want fast", prefs.settings.Appearance.Motion.Speed)
	}
	if err := tester.Click(m.SettingsMotionSpeedSlow()); err != nil {
		t.Fatalf("click Slow: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion.Speed != domain.MotionSlow {
		t.Fatalf("speed = %q, want slow", prefs.settings.Appearance.Motion.Speed)
	}
}

// TestMotionFieldAndGroupResets covers both reset paths through the view.
func TestMotionFieldAndGroupResets(t *testing.T) {
	prefs, tester := newMotionTester(t)
	m := locales.In("en")
	if err := prefs.Update(func(s *domain.Settings) {
		s.Appearance.Motion.Speed = domain.MotionFast
	}); err != nil {
		t.Fatal(err)
	}
	tester.Frame()

	// The changed field offers its own reset, which restores only the speed.
	if err := tester.Click(m.SettingsReset()); err != nil {
		t.Fatalf("click the speed reset: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion.Speed != domain.MotionStandard {
		t.Fatalf("speed after the field reset = %q", prefs.settings.Appearance.Motion.Speed)
	}

	// A whole non-default block is restored by the Appearance group reset.
	if err := prefs.Update(func(s *domain.Settings) {
		s.Appearance.Motion.Enabled = false
		s.Appearance.Motion.TabReorder = false
		s.Appearance.Motion.OpenStyle = domain.MotionFade
		s.Appearance.Motion.Speed = domain.MotionSlow
	}); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	resetGroup := m.SettingsResetGroup(m.SettingsAppearanceTitle())
	if err := tester.Click(resetGroup); err != nil {
		t.Fatalf("click the Appearance group reset: %v", err)
	}
	tester.Frame()
	if prefs.settings.Appearance.Motion != domain.Defaults().Appearance.Motion {
		t.Fatalf("motion after the group reset = %+v, want the defaults",
			prefs.settings.Appearance.Motion)
	}
	if !tester.HasText(m.SettingsMotionSpeedStandard()) {
		t.Fatalf("the restored speed is not shown: %v", tester.Texts())
	}
}

// TestMotionSearchEntries covers the twelve field hits and the section title,
// and proves the index keeps them while the master switch is off.
func TestMotionSearchEntries(t *testing.T) {
	m := locales.In("en")
	fields := []struct {
		field domain.Field
		label string
	}{
		{domain.FieldMotionEnabled, m.SettingsMotionEnabled()},
		{domain.FieldMotionPaneOpen, m.SettingsMotionPaneOpen()},
		{domain.FieldMotionPaneClose, m.SettingsMotionPaneClose()},
		{domain.FieldMotionPaneReorder, m.SettingsMotionPaneReorder()},
		{domain.FieldMotionTabOpen, m.SettingsMotionTabOpen()},
		{domain.FieldMotionTabClose, m.SettingsMotionTabClose()},
		{domain.FieldMotionTabSwitch, m.SettingsMotionTabSwitch()},
		{domain.FieldMotionTabReorder, m.SettingsMotionTabReorder()},
		{domain.FieldMotionOpenStyle, m.SettingsMotionOpenStyle()},
		{domain.FieldMotionCloseStyle, m.SettingsMotionCloseStyle()},
		{domain.FieldMotionSwitchStyle, m.SettingsMotionSwitchStyle()},
		{domain.FieldMotionSpeed, m.SettingsMotionSpeed()},
	}
	hits := SearchIndex(m)
	for _, test := range fields {
		found := false
		for _, hit := range hits {
			if hit.Area == AreaAppearance && hit.Field == test.field && hit.Label == test.label {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the search index has no appearance hit for %s (%q)", test.field, test.label)
		}
	}
	foundTitle := false
	for _, hit := range hits {
		if hit.Area == AreaAppearance && hit.Field == "" && hit.Label == m.SettingsMotionTitle() {
			foundTitle = true
			break
		}
	}
	if !foundTitle {
		t.Errorf("the section title %q is not searchable", m.SettingsMotionTitle())
	}
	for _, query := range []string{"motion animation", "pane open split",
		"tab switch workspace", "popin slide gnomed fade", "speed slow standard fast"} {
		matched := false
		for _, hit := range FilterHits(hits, query) {
			if hit.Area == AreaAppearance && strings.HasPrefix(string(hit.Field), "appearance.motion") {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("query %q reaches no motion field", query)
		}
	}
}

// TestMotionFieldsStaySearchableWhileMotionIsOff renders the search with the
// master switch off, where the dependent controls are hidden from the form but
// still reachable from the index.
func TestMotionFieldsStaySearchableWhileMotionIsOff(t *testing.T) {
	prefs := newFakePreferences()
	prefs.settings.Appearance.Motion.Enabled = false
	search := NewSearchState()
	tester := ui.NewTester(ViewWithSettings(Config{
		Preferences: NewPreferencesState(prefs), Search: search,
	}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	*search.Query() = "reorder tabs"
	tester.Frame()
	if !search.Filtering() {
		t.Fatal("the search query was not applied")
	}
	if !tester.HasText(m.SettingsMotionTabReorder()) {
		t.Fatalf("the hidden motion field is not searchable: %v", tester.Texts())
	}
}

// TestMotionStringsExistInEveryCatalog pins the 27 labels in all five shipped
// catalogs, so a generated accessor cannot silently fall back to English.
func TestMotionStringsExistInEveryCatalog(t *testing.T) {
	for _, tag := range []string{"en", "zh-CN", "zh-TW", "es", "ru"} {
		m := locales.In(tag)
		values := []string{
			m.SettingsMotionTitle(), m.SettingsMotionDescription(), m.SettingsMotionEnabled(),
			m.SettingsMotionPaneTitle(), m.SettingsMotionPaneOpen(), m.SettingsMotionPaneClose(),
			m.SettingsMotionPaneReorder(), m.SettingsMotionTabTitle(), m.SettingsMotionTabOpen(),
			m.SettingsMotionTabClose(), m.SettingsMotionTabSwitch(), m.SettingsMotionTabReorder(),
			m.SettingsMotionOpenStyle(), m.SettingsMotionCloseStyle(), m.SettingsMotionSwitchStyle(),
			m.SettingsMotionStylePopin(), m.SettingsMotionStyleSlide(), m.SettingsMotionStyleGnomed(),
			m.SettingsMotionStyleFade(), m.SettingsMotionSwitchStyleSlide(),
			m.SettingsMotionSwitchStyleSlideVert(), m.SettingsMotionSwitchStyleSlideFade(),
			m.SettingsMotionSwitchStyleSlideFadeVert(), m.SettingsMotionSpeed(),
			m.SettingsMotionSpeedSlow(), m.SettingsMotionSpeedStandard(), m.SettingsMotionSpeedFast(),
		}
		if len(values) != 27 {
			t.Fatalf("listed %d motion strings, want 27", len(values))
		}
		for index, value := range values {
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s: motion string %d is empty", tag, index)
			}
		}
	}
}
