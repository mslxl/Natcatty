package preferences

import (
	"encoding/json"
	"strings"
	"testing"

	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

func TestHistorySettingsDefaults(t *testing.T) {
	history := Defaults().History
	if history.SyncEnabled {
		t.Error("history sync is enabled by default")
	}
	if history.SyncAddress != DefaultHistorySyncAddress {
		t.Errorf("sync address = %q, want %q", history.SyncAddress, DefaultHistorySyncAddress)
	}
	if history.AuthMode != atuinmodel.AuthModeSession {
		t.Errorf("auth mode = %q, want session", history.AuthMode)
	}
	if history.SyncIntervalMinutes != DefaultHistorySyncIntervalMinutes {
		t.Errorf("interval = %d, want %d", history.SyncIntervalMinutes, DefaultHistorySyncIntervalMinutes)
	}
}

func TestNormalizeHistorySettings(t *testing.T) {
	settings := Defaults()
	settings.History.SyncAddress = "  https://atuin.example.test/base/  "
	settings.History.Username = "  alice  "
	settings.History.AuthMode = "unknown"
	settings.History.SyncIntervalMinutes = 0
	normalized := Normalize(settings)
	if normalized.History.SyncAddress != "https://atuin.example.test/base/" {
		t.Errorf("address = %q", normalized.History.SyncAddress)
	}
	if normalized.History.Username != "alice" {
		t.Errorf("username = %q", normalized.History.Username)
	}
	if normalized.History.AuthMode != atuinmodel.AuthModeSession {
		t.Errorf("auth mode = %q, want session", normalized.History.AuthMode)
	}
	if normalized.History.SyncIntervalMinutes != DefaultHistorySyncIntervalMinutes {
		t.Errorf("zero interval = %d, want default %d",
			normalized.History.SyncIntervalMinutes, DefaultHistorySyncIntervalMinutes)
	}
	normalized.History.SyncIntervalMinutes = 500
	if got := Normalize(normalized).History.SyncIntervalMinutes; got != MaxHistorySyncIntervalMinutes {
		t.Errorf("high interval = %d, want %d", got, MaxHistorySyncIntervalMinutes)
	}
	normalized.History.SyncIntervalMinutes = 2
	if got := Normalize(normalized).History.SyncIntervalMinutes; got != 2 {
		t.Errorf("interval = %d, want 2", got)
	}
}

func TestPortableStripsHistoryAccountSettings(t *testing.T) {
	settings := Defaults()
	settings.History = History{
		SyncEnabled:         true,
		SyncAddress:         "https://atuin.example.test",
		Username:            "alice",
		AuthMode:            atuinmodel.AuthModeBearer,
		SyncIntervalMinutes: 15,
	}
	portable := Portable(settings)
	encoded, err := json.Marshal(portable)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, leaked := range []string{"history", "atuin.example", "alice", `"syncAddress"`, `"bearer"`} {
		if strings.Contains(text, leaked) {
			t.Errorf("portable settings contain device-local history account data %q: %s", leaked, text)
		}
	}

	// A cloud download must not turn off history sync enabled locally.
	downloaded := PortableSettings{}
	merged := MergePortable(settings, downloaded)
	if merged.History != settings.History {
		t.Errorf("MergePortable changed local history settings: %+v", merged.History)
	}
}

func TestHistorySettingsResetWithTheLogsGroup(t *testing.T) {
	settings := Defaults()
	settings.History = History{
		SyncEnabled:         true,
		SyncAddress:         "http://127.0.0.1:8888",
		Username:            "alice",
		AuthMode:            atuinmodel.AuthModeBearer,
		SyncIntervalMinutes: 15,
	}
	reset := ResetGroup(settings, GroupLogs)
	if reset.History != Defaults().History {
		t.Errorf("history group = %+v, want defaults", reset.History)
	}

	reset = ResetField(settings, FieldHistorySyncInterval)
	if reset.History.SyncIntervalMinutes != DefaultHistorySyncIntervalMinutes {
		t.Errorf("interval = %d, want default %d",
			reset.History.SyncIntervalMinutes, DefaultHistorySyncIntervalMinutes)
	}
	if reset.History.SyncAddress != "http://127.0.0.1:8888" {
		t.Error("resetting one field changed another")
	}
}
