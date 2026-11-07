package terminal

import (
	"encoding/json"
	"testing"
)

func TestDefaults(t *testing.T) {
	settings := Defaults()
	if settings.LoggingEnabled || settings.LogDirectory != "" ||
		settings.LogFormat != LogFormatText || settings.LogTimestamps {
		t.Fatalf("logging defaults = %+v", settings)
	}
	if settings.OSC52 != ClipboardWriteOnly {
		t.Fatalf("OSC52 default = %q, want %q", settings.OSC52, ClipboardWriteOnly)
	}
	if settings.OSCNotifications != NotificationAlways {
		t.Fatalf("notification default = %q, want %q",
			settings.OSCNotifications, NotificationAlways)
	}
	if settings.DynamicTabTitleMode != DynamicTabTitleAll {
		t.Fatalf("dynamic tab title default = %q, want %q",
			settings.DynamicTabTitleMode, DynamicTabTitleAll)
	}
	if !settings.HistoryCaptureHookEnabled {
		t.Fatal("the history capture hook is off by default")
	}
	if !settings.HistoryPreferHostAtuin {
		t.Fatal("the Host Atuin preference is off by default")
	}
}

func TestNormalizeDynamicTabTitleMode(t *testing.T) {
	if got := NormalizeDynamicTabTitleMode("bogus"); got != DynamicTabTitleAll {
		t.Fatalf("unknown mode = %q, want %q", got, DynamicTabTitleAll)
	}
	for _, mode := range []DynamicTabTitleMode{
		DynamicTabTitleOff, DynamicTabTitleAgents, DynamicTabTitleAll,
	} {
		if got := NormalizeDynamicTabTitleMode(mode); got != mode {
			t.Errorf("known mode %q normalized to %q", mode, got)
		}
	}
}

func TestNormalizeFallsBackToOriginalDefaults(t *testing.T) {
	settings := Normalize(Settings{
		LogFormat:        LogFormat("bogus"),
		OSC52:            ClipboardAccess("bogus"),
		OSCNotifications: NotificationMode("bogus"),
	})
	if settings.LogFormat != LogFormatText || settings.OSC52 != ClipboardWriteOnly ||
		settings.OSCNotifications != NotificationAlways {
		t.Fatalf("normalized = %+v", settings)
	}
}

func TestNormalizeKeepsKnownValues(t *testing.T) {
	input := Settings{
		LoggingEnabled:   true,
		LogDirectory:     `C:\Users\me\logs`,
		LogFormat:        LogFormatHTML,
		LogTimestamps:    true,
		OSC52:            ClipboardPrompt,
		OSCNotifications: NotificationUnfocused,
		Encoding:         EncodingGB18030,
		Encodings:        map[string]Encoding{"host:abc": EncodingShiftJIS},
		PasteProtection:  true,
		PasteMinLines:    4,
		DropAction:       DropUpload,
	}
	got := Normalize(input)
	if got.LoggingEnabled != input.LoggingEnabled || got.LogDirectory != input.LogDirectory ||
		got.LogFormat != input.LogFormat || got.LogTimestamps != input.LogTimestamps ||
		got.OSC52 != input.OSC52 || got.OSCNotifications != input.OSCNotifications ||
		got.Encoding != input.Encoding || got.PasteProtection != input.PasteProtection ||
		got.PasteMinLines != input.PasteMinLines || got.DropAction != input.DropAction ||
		len(got.Encodings) != 1 || got.Encodings["host:abc"] != EncodingShiftJIS {
		t.Fatalf("normalize changed a valid value: %+v", got)
	}
}

func TestClipboardPolicy(t *testing.T) {
	tests := []struct {
		access        ClipboardAccess
		writes, reads bool
		asks          bool
	}{
		{ClipboardOff, false, false, false},
		{ClipboardWriteOnly, true, false, false},
		{ClipboardReadWrite, true, true, false},
		{ClipboardPrompt, true, false, true},
	}
	for _, test := range tests {
		if test.access.WritesAllowed() != test.writes {
			t.Errorf("%q writes = %t, want %t", test.access, test.access.WritesAllowed(), test.writes)
		}
		if test.access.ReadsAllowed() != test.reads {
			t.Errorf("%q reads = %t, want %t", test.access, test.access.ReadsAllowed(), test.reads)
		}
		if test.access.AsksOnRead() != test.asks {
			t.Errorf("%q asks = %t, want %t", test.access, test.access.AsksOnRead(), test.asks)
		}
	}
}

func TestPortableTerminalSettingsKeepDeviceLocalState(t *testing.T) {
	settings := Defaults()
	settings.LogDirectory = "/var/log/natcatty"
	settings.Encodings = map[string]Encoding{"host:host-1": EncodingGB18030}
	settings.OSC52 = ClipboardReadWrite
	settings.HistoryCaptureHookEnabled = true

	portable := Portable(settings)
	if portable.LogDirectory != "" || len(portable.Encodings) != 0 {
		t.Fatalf("portable settings = %+v, want local path and encodings stripped", portable)
	}
	if portable.OSC52 != ClipboardReadWrite {
		t.Fatalf("portable OSC52 = %q", portable.OSC52)
	}
	if !portable.HistoryCaptureHookEnabled {
		t.Fatalf("the capture hook preference is not portable: %+v", portable)
	}
	if portable.HistoryPreferHostAtuin {
		t.Fatalf("the device-local Host Atuin preference is portable: %+v", portable)
	}

	current := Defaults()
	current.LogDirectory = "/home/me/logs"
	current.Encodings = map[string]Encoding{"local": EncodingUTF8}
	current.HistoryCaptureHookEnabled = false
	current.HistoryPreferHostAtuin = true
	merged := MergePortable(current, portable)
	if merged.LogDirectory != "/home/me/logs" {
		t.Fatalf("merged LogDirectory = %q, want the local value", merged.LogDirectory)
	}
	if len(merged.Encodings) != 1 || merged.Encodings["local"] != EncodingUTF8 {
		t.Fatalf("merged Encodings = %+v, want the local map", merged.Encodings)
	}
	if merged.OSC52 != ClipboardReadWrite {
		t.Fatalf("merged OSC52 = %q, want the portable value", merged.OSC52)
	}
	if !merged.HistoryCaptureHookEnabled {
		t.Fatalf("merged capture hook = %t, want the portable value", merged.HistoryCaptureHookEnabled)
	}
	if !merged.HistoryPreferHostAtuin {
		t.Fatalf("merged Host Atuin preference = %t, want the local value", merged.HistoryPreferHostAtuin)
	}
}

// The Host Atuin preference is device-local; the portable settings payload
// must not carry it under either JSON spelling.
func TestPortableTerminalSettingsOmitTheHostAtuinPreference(t *testing.T) {
	settings := Defaults()
	settings.HistoryPreferHostAtuin = true
	data, err := json.Marshal(Portable(settings))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HistoryPreferHostAtuin", "historyPreferHostAtuin"} {
		if _, found := document[key]; found {
			t.Fatalf("portable settings carry the device-local key %q: %s", key, data)
		}
	}
}
