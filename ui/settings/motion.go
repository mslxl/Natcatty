package settings

import (
	"slices"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// motionSection is the Pane and Tab motion form at the end of Appearance. It
// is a subsection inside the Appearance card, not a card of its own. While
// motion is off the dependent controls are hidden, not disabled; their values
// survive, so re-enabling restores the previous choices.
func motionSection(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	settings domain.Settings) {
	motion := settings.Appearance.Motion
	ui.Text(c, m.SettingsMotionTitle()).Bold()
	ui.Text(c, m.SettingsMotionDescription()).
		FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)

	checkboxRow(c, state, m, domain.FieldMotionEnabled, m.SettingsMotionEnabled(),
		motion.Enabled, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.Enabled = value
		})
	if !motion.Enabled {
		return
	}

	ui.Text(c, m.SettingsMotionPaneTitle()).
		FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
	checkboxRow(c, state, m, domain.FieldMotionPaneOpen, m.SettingsMotionPaneOpen(),
		motion.PaneOpen, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.PaneOpen = value
		})
	checkboxRow(c, state, m, domain.FieldMotionPaneClose, m.SettingsMotionPaneClose(),
		motion.PaneClose, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.PaneClose = value
		})
	checkboxRow(c, state, m, domain.FieldMotionPaneReorder, m.SettingsMotionPaneReorder(),
		motion.PaneReorder, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.PaneReorder = value
		})

	ui.Text(c, m.SettingsMotionTabTitle()).
		FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
	checkboxRow(c, state, m, domain.FieldMotionTabOpen, m.SettingsMotionTabOpen(),
		motion.TabOpen, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.TabOpen = value
		})
	checkboxRow(c, state, m, domain.FieldMotionTabClose, m.SettingsMotionTabClose(),
		motion.TabClose, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.TabClose = value
		})
	checkboxRow(c, state, m, domain.FieldMotionTabSwitch, m.SettingsMotionTabSwitch(),
		motion.TabSwitch, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.TabSwitch = value
		})
	checkboxRow(c, state, m, domain.FieldMotionTabReorder, m.SettingsMotionTabReorder(),
		motion.TabReorder, func(s *domain.Settings, value bool) {
			s.Appearance.Motion.TabReorder = value
		})

	motionStylePicker(c, state, m, t, domain.FieldMotionOpenStyle, m.SettingsMotionOpenStyle(),
		[]domain.MotionStyle{domain.MotionPopin, domain.MotionSlide, domain.MotionGnomed, domain.MotionFade},
		[]string{m.SettingsMotionStylePopin(), m.SettingsMotionStyleSlide(),
			m.SettingsMotionStyleGnomed(), m.SettingsMotionStyleFade()},
		motion.OpenStyle, 220, func(s *domain.Settings, style domain.MotionStyle) {
			s.Appearance.Motion.OpenStyle = style
		})
	motionStylePicker(c, state, m, t, domain.FieldMotionCloseStyle, m.SettingsMotionCloseStyle(),
		[]domain.MotionStyle{domain.MotionPopin, domain.MotionSlide, domain.MotionGnomed, domain.MotionFade},
		[]string{m.SettingsMotionStylePopin(), m.SettingsMotionStyleSlide(),
			m.SettingsMotionStyleGnomed(), m.SettingsMotionStyleFade()},
		motion.CloseStyle, 220, func(s *domain.Settings, style domain.MotionStyle) {
			s.Appearance.Motion.CloseStyle = style
		})

	// The switch style offers its own five looks: two directions, with and
	// without a fade, and a plain fade.
	switchStyles := []domain.MotionSwitchStyle{
		domain.MotionSwitchSlide, domain.MotionSwitchSlideVert,
		domain.MotionSwitchSlideFade, domain.MotionSwitchSlideFadeVert, domain.MotionSwitchFade,
	}
	labels := []string{
		m.SettingsMotionSwitchStyleSlide(), m.SettingsMotionSwitchStyleSlideVert(),
		m.SettingsMotionSwitchStyleSlideFade(), m.SettingsMotionSwitchStyleSlideFadeVert(),
		m.SettingsMotionStyleFade(),
	}
	chosen := labels[max(slices.Index(switchStyles, motion.SwitchStyle), 0)]
	fieldRow(c, state, m, t, domain.FieldMotionSwitchStyle, m.SettingsMotionSwitchStyle(), func() {
		if ui.Select(c, &chosen, labels).Width(260).Changed() {
			state.Update(func(s *domain.Settings) {
				s.Appearance.Motion.SwitchStyle = switchStyles[max(slices.Index(labels, chosen), 0)]
			})
			c.Invalidate()
		}
	})

	speeds := []domain.MotionSpeed{domain.MotionSlow, domain.MotionStandard, domain.MotionFast}
	speed := max(slices.Index(speeds, motion.Speed), 0)
	fieldRow(c, state, m, t, domain.FieldMotionSpeed, m.SettingsMotionSpeed(), func() {
		segments := []kit.Segment{
			{Label: m.SettingsMotionSpeedSlow()},
			{Label: m.SettingsMotionSpeedStandard()},
			{Label: m.SettingsMotionSpeedFast()},
		}
		if kit.Segmented(c, kit.SegmentedText, &speed, m.SettingsMotionSpeed(), segments) {
			state.Update(func(s *domain.Settings) {
				s.Appearance.Motion.Speed = speeds[min(max(speed, 0), len(speeds)-1)]
			})
			c.Invalidate()
		}
	})
}

// motionStylePicker is one open or close style select. The parallel arrays keep
// the persisted enum and its localized label aligned, as interfaceStylePicker
// does.
func motionStylePicker(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	field domain.Field, label string, styles []domain.MotionStyle, labels []string,
	current domain.MotionStyle, width float32, assign func(*domain.Settings, domain.MotionStyle)) {
	chosen := labels[max(slices.Index(styles, current), 0)]
	fieldRow(c, state, m, t, field, label, func() {
		if ui.Select(c, &chosen, labels).Width(width).Changed() {
			style := styles[min(max(slices.Index(labels, chosen), 0), len(styles)-1)]
			state.Update(func(s *domain.Settings) { assign(s, style) })
			c.Invalidate()
		}
	})
}
