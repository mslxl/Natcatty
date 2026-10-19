package terminal

import "testing"

func TestDefaultsMatchTheOriginal(t *testing.T) {
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
	}
	if got := Normalize(input); got != input {
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
