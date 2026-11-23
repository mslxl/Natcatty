package preferences

import (
	"encoding/json"
	"slices"
	"testing"
)

// nonDefaultMotion is a valid motion block whose every field differs from
// Defaults, so a reset that touches the wrong field is visible.
func nonDefaultMotion() Motion {
	return Motion{
		Enabled:     false,
		PaneOpen:    false,
		PaneClose:   false,
		PaneReorder: false,
		TabOpen:     false,
		TabClose:    false,
		TabSwitch:   false,
		TabReorder:  false,
		OpenStyle:   MotionGnomed,
		CloseStyle:  MotionSlide,
		SwitchStyle: MotionSwitchSlideFadeVert,
		Speed:       MotionFast,
	}
}

func TestDefaultMotionIsOnWithPopinAndStandardSpeed(t *testing.T) {
	motion := Defaults().Appearance.Motion
	want := Motion{
		Enabled:     true,
		PaneOpen:    true,
		PaneClose:   true,
		PaneReorder: true,
		TabOpen:     true,
		TabClose:    true,
		TabSwitch:   true,
		TabReorder:  true,
		OpenStyle:   MotionPopin,
		CloseStyle:  MotionPopin,
		SwitchStyle: MotionSwitchSlide,
		Speed:       MotionStandard,
	}
	if motion != want {
		t.Fatalf("motion defaults = %+v, want %+v", motion, want)
	}
}

func TestMotionRoundTripsThroughJSON(t *testing.T) {
	settings := Defaults()
	settings.Appearance.Motion = nonDefaultMotion()

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if got := Normalize(loaded).Appearance.Motion; got != settings.Appearance.Motion {
		t.Fatalf("round-trip Motion = %+v, want %+v", got, settings.Appearance.Motion)
	}
}

func TestMotionValidRecognizesEveryChoice(t *testing.T) {
	for _, style := range []MotionStyle{MotionPopin, MotionSlide, MotionGnomed, MotionFade} {
		if !style.Valid() {
			t.Errorf("MotionStyle %q is not valid", style)
		}
	}
	if MotionStyle("bounce").Valid() || MotionStyle("").Valid() {
		t.Error("an unknown MotionStyle is valid")
	}
	for _, style := range []MotionSwitchStyle{
		MotionSwitchSlide, MotionSwitchSlideVert, MotionSwitchSlideFade,
		MotionSwitchSlideFadeVert, MotionSwitchFade,
	} {
		if !style.Valid() {
			t.Errorf("MotionSwitchStyle %q is not valid", style)
		}
	}
	if MotionSwitchStyle("slidefadeleft").Valid() || MotionSwitchStyle("").Valid() {
		t.Error("an unknown MotionSwitchStyle is valid")
	}
	for _, speed := range []MotionSpeed{MotionSlow, MotionStandard, MotionFast} {
		if !speed.Valid() {
			t.Errorf("MotionSpeed %q is not valid", speed)
		}
	}
	if MotionSpeed("instant").Valid() || MotionSpeed("").Valid() {
		t.Error("an unknown MotionSpeed is valid")
	}
}

// TestMotionNormalizedRepairsOnlyStylesAndSpeed keeps false a real choice: a
// malformed document must not turn an event switch back on.
func TestMotionNormalizedRepairsOnlyStylesAndSpeed(t *testing.T) {
	defaults := Defaults().Appearance.Motion
	broken := Motion{
		Enabled:     false,
		PaneOpen:    false,
		PaneClose:   false,
		PaneReorder: false,
		TabOpen:     false,
		TabClose:    false,
		TabSwitch:   false,
		TabReorder:  false,
		OpenStyle:   "bounce",
		CloseStyle:  "",
		SwitchStyle: "spin",
		Speed:       "instant",
	}
	got := broken.Normalized(defaults)
	want := broken
	want.OpenStyle = defaults.OpenStyle
	want.CloseStyle = defaults.CloseStyle
	want.SwitchStyle = defaults.SwitchStyle
	want.Speed = defaults.Speed
	if got != want {
		t.Fatalf("Normalized = %+v, want %+v", got, want)
	}

	valid := nonDefaultMotion()
	if got := valid.Normalized(defaults); got != valid {
		t.Fatalf("Normalized changed valid choices: %+v, want %+v", got, valid)
	}
}

// TestNormalizeDoesNotTurnZeroValueBooleansOn keeps Normalize about enums and
// numbers only; Defaults is the one place a fresh profile becomes on.
func TestNormalizeDoesNotTurnZeroValueBooleansOn(t *testing.T) {
	got := Normalize(Settings{}).Appearance.Motion
	if got.Enabled || got.PaneOpen || got.PaneClose || got.PaneReorder ||
		got.TabOpen || got.TabClose || got.TabSwitch || got.TabReorder {
		t.Fatalf("Normalize turned zero booleans on: %+v", got)
	}
	if got.OpenStyle != MotionPopin || got.CloseStyle != MotionPopin ||
		got.SwitchStyle != MotionSwitchSlide || got.Speed != MotionStandard {
		t.Fatalf("Normalize left invalid defaults in place: %+v", got)
	}
}

func TestMotionIsRegisteredInEveryAppearancePoint(t *testing.T) {
	fields := []Field{
		FieldMotionEnabled,
		FieldMotionPaneOpen, FieldMotionPaneClose, FieldMotionPaneReorder,
		FieldMotionTabOpen, FieldMotionTabClose, FieldMotionTabSwitch, FieldMotionTabReorder,
		FieldMotionOpenStyle, FieldMotionCloseStyle, FieldMotionSwitchStyle, FieldMotionSpeed,
	}
	all := Fields()
	blink := slices.Index(all, FieldTerminalCursorBlink)
	shiftEnter := slices.Index(all, FieldTerminalShiftEnter)
	if blink < 0 || shiftEnter < 0 {
		t.Fatalf("terminal fields are missing: blink=%d shiftEnter=%d", blink, shiftEnter)
	}
	for _, field := range fields {
		index := slices.Index(all, field)
		if index < 0 {
			t.Errorf("field %q is not in Fields()", field)
			continue
		}
		if index < blink || index > shiftEnter {
			t.Errorf("field %q is at %d, outside the motion block %d..%d",
				field, index, blink, shiftEnter)
		}
		group, ok := GroupOf(field)
		if !ok || group != GroupAppearance {
			t.Errorf("GroupOf(%q) = %q, %v; want appearance", field, group, ok)
		}
	}
}

// TestResetFieldResetsOneMotionFieldAtATime proves each of the twelve fields
// has its own reset and leaves its siblings alone.
func TestResetFieldResetsOneMotionFieldAtATime(t *testing.T) {
	defaults := Defaults().Appearance.Motion
	settings := Defaults()
	settings.Appearance.Motion = nonDefaultMotion()

	reset := func(motion *Motion, field Field) {
		switch field {
		case FieldMotionEnabled:
			motion.Enabled = defaults.Enabled
		case FieldMotionPaneOpen:
			motion.PaneOpen = defaults.PaneOpen
		case FieldMotionPaneClose:
			motion.PaneClose = defaults.PaneClose
		case FieldMotionPaneReorder:
			motion.PaneReorder = defaults.PaneReorder
		case FieldMotionTabOpen:
			motion.TabOpen = defaults.TabOpen
		case FieldMotionTabClose:
			motion.TabClose = defaults.TabClose
		case FieldMotionTabSwitch:
			motion.TabSwitch = defaults.TabSwitch
		case FieldMotionTabReorder:
			motion.TabReorder = defaults.TabReorder
		case FieldMotionOpenStyle:
			motion.OpenStyle = defaults.OpenStyle
		case FieldMotionCloseStyle:
			motion.CloseStyle = defaults.CloseStyle
		case FieldMotionSwitchStyle:
			motion.SwitchStyle = defaults.SwitchStyle
		case FieldMotionSpeed:
			motion.Speed = defaults.Speed
		default:
			t.Fatalf("unexpected reset field %q", field)
		}
	}

	for _, field := range []Field{
		FieldMotionEnabled,
		FieldMotionPaneOpen, FieldMotionPaneClose, FieldMotionPaneReorder,
		FieldMotionTabOpen, FieldMotionTabClose, FieldMotionTabSwitch, FieldMotionTabReorder,
		FieldMotionOpenStyle, FieldMotionCloseStyle, FieldMotionSwitchStyle, FieldMotionSpeed,
	} {
		t.Run(string(field), func(t *testing.T) {
			want := settings
			reset(&want.Appearance.Motion, field)
			got := ResetField(settings, field)
			if !Equal(got, want) {
				t.Fatalf("ResetField(%q) = %+v, want %+v", field, got.Appearance.Motion, want.Appearance.Motion)
			}
			// Every other motion field still differs from its default, so the
			// reset cannot have restored the whole block.
			if got.Appearance.Motion == defaults {
				t.Fatalf("ResetField(%q) reset the whole motion block", field)
			}
		})
	}
}

// TestResetFieldMotionEnabledKeepsTheChildren pins the master's own reset:
// turning motion back on keeps every event switch and style as it was.
func TestResetFieldMotionEnabledKeepsTheChildren(t *testing.T) {
	settings := Defaults()
	settings.Appearance.Motion = nonDefaultMotion()
	got := ResetField(settings, FieldMotionEnabled)
	if !got.Appearance.Motion.Enabled {
		t.Fatal("resetting the master did not turn motion back on")
	}
	want := settings.Appearance.Motion
	want.Enabled = true
	if got.Appearance.Motion != want {
		t.Fatalf("motion = %+v, want %+v", got.Appearance.Motion, want)
	}
}

func TestResetGroupAppearanceRestoresTheWholeMotionBlock(t *testing.T) {
	settings := Defaults()
	settings.Appearance.Motion = nonDefaultMotion()
	settings.Appearance.TerminalFontSize = 20
	settings.Layout.ShowSFTPTab = false

	got := ResetGroup(settings, GroupAppearance)
	if got.Appearance.Motion != Defaults().Appearance.Motion {
		t.Fatalf("motion = %+v, want the defaults", got.Appearance.Motion)
	}
	if got.Appearance.TerminalFontSize != Defaults().Appearance.TerminalFontSize {
		t.Fatal("the appearance group reset left a terminal field changed")
	}
	if got.Layout != settings.Layout {
		t.Fatalf("the appearance group reset changed Layout: %+v", got.Layout)
	}
}

// TestMotionIsDeviceLocal proves cloud sync never carries the local animation
// choices and a remote document cannot overwrite them.
func TestMotionIsDeviceLocal(t *testing.T) {
	local := Defaults()
	local.Appearance.Motion = nonDefaultMotion()
	local.Appearance.TerminalFontSize = 18

	portable := Portable(local)
	if portable.Appearance.Motion != (Motion{}) {
		t.Fatalf("Portable kept motion: %+v", portable.Appearance.Motion)
	}
	if portable.Appearance.TerminalFontSize != 18 {
		t.Fatalf("Portable dropped a portable field: %v", portable.Appearance.TerminalFontSize)
	}

	remote := portable
	remote.Appearance.Motion = Motion{
		Enabled:   true,
		OpenStyle: MotionFade,
		Speed:     MotionSlow,
	}
	remote.Appearance.TerminalFontSize = 22
	merged := MergePortable(local, remote)
	if merged.Appearance.Motion != local.Appearance.Motion {
		t.Fatalf("MergePortable motion = %+v, want the local value %+v",
			merged.Appearance.Motion, local.Appearance.Motion)
	}
	if merged.Appearance.TerminalFontSize != 22 {
		t.Fatalf("MergePortable font size = %v, want the remote value", merged.Appearance.TerminalFontSize)
	}
}
