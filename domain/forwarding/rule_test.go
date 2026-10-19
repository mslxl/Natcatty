package forwarding

import (
	"errors"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestNormalizeDraftDefaultsAndValidatesEachKind(t *testing.T) {
	tests := []struct {
		name       string
		draft      Draft
		wantType   Type
		wantBind   string
		wantRemote string
	}{
		{
			name: "local defaults its bind address",
			draft: Draft{
				Label: "web", Type: "LOCAL", HostID: "host-1",
				LocalPort: 8080, RemoteHost: "db.internal", RemotePort: 5432,
			},
			wantType: TypeLocal, wantBind: "127.0.0.1", wantRemote: "db.internal",
		},
		{
			name: "remote keeps an explicit wildcard bind",
			draft: Draft{
				Label: "db", Type: TypeRemote, HostID: "host-1",
				BindAddress: "0.0.0.0", LocalPort: 15432,
				RemoteHost: "127.0.0.1", RemotePort: 5432,
			},
			wantType: TypeRemote, wantBind: "0.0.0.0", wantRemote: "127.0.0.1",
		},
		{
			name: "dynamic clears destination fields",
			draft: Draft{
				Label: "socks", Type: TypeDynamic, HostID: "host-1",
				LocalPort: 1080, RemoteHost: "ignored", RemotePort: 22,
			},
			wantType: TypeDynamic, wantBind: "127.0.0.1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := NormalizeDraft(test.draft)
			if err != nil {
				t.Fatalf("NormalizeDraft: %v", err)
			}
			if normalized.Type != test.wantType {
				t.Errorf("Type = %q, want %q", normalized.Type, test.wantType)
			}
			if normalized.BindAddress != test.wantBind {
				t.Errorf("BindAddress = %q, want %q", normalized.BindAddress, test.wantBind)
			}
			if normalized.RemoteHost != test.wantRemote {
				t.Errorf("RemoteHost = %q, want %q", normalized.RemoteHost, test.wantRemote)
			}
			if test.wantType == TypeDynamic && (normalized.RemoteHost != "" || normalized.RemotePort != 0) {
				t.Errorf("dynamic rule kept a destination: %+v", normalized)
			}
		})
	}
}

func TestNormalizeDraftRejectsInvalidRules(t *testing.T) {
	valid := Draft{
		Label: "web", Type: TypeLocal, HostID: "host-1",
		LocalPort: 8080, RemoteHost: "db", RemotePort: 5432,
	}
	tests := []struct {
		name  string
		edit  func(*Draft)
		field Field
		rule  Issue
	}{
		{"missing label", func(d *Draft) { d.Label = " " }, FieldLabel, RuleRequired},
		{"missing host", func(d *Draft) { d.HostID = "" }, FieldHost, RuleRequired},
		{"unknown type", func(d *Draft) { d.Type = "tunnel" }, FieldType, RuleUnsupported},
		{"local port out of range", func(d *Draft) { d.LocalPort = 70000 }, FieldLocalPort, RuleOutOfRange},
		{"missing destination host", func(d *Draft) { d.RemoteHost = "" }, FieldRemoteHost, RuleRequired},
		{"destination port out of range", func(d *Draft) { d.RemotePort = 0 }, FieldRemotePort, RuleOutOfRange},
		{"bind address has a space", func(d *Draft) { d.BindAddress = "127.0.0.1 x" }, FieldBindAddress, RuleInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := valid
			test.edit(&draft)
			_, err := NormalizeDraft(draft)
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if invalid.Field != test.field || invalid.Rule != test.rule {
				t.Fatalf("error = %s/%s, want %s/%s", invalid.Field, invalid.Rule, test.field, test.rule)
			}
		})
	}
}

func TestAutoReconnectFallsBackToAutoStart(t *testing.T) {
	enabled, disabled := true, false
	tests := []struct {
		name string
		rule Rule
		want bool
	}{
		{"unset follows auto start", Rule{AutoStart: true}, true},
		{"unset follows a disabled auto start", Rule{AutoStart: false}, false},
		{"explicit true wins", Rule{AutoStart: false, AutoReconnect: &enabled}, true},
		{"explicit false wins", Rule{AutoStart: true, AutoReconnect: &disabled}, false},
	}
	for _, test := range tests {
		if got := test.rule.AutoReconnectEnabled(); got != test.want {
			t.Errorf("%s: AutoReconnectEnabled = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestEnabledDefaultsToTrue(t *testing.T) {
	disabled := false
	if !(Rule{}).IsEnabled() {
		t.Fatal("a rule with no enabled flag must be enabled")
	}
	if (Rule{Enabled: &disabled}).IsEnabled() {
		t.Fatal("an explicit disabled flag must disable the rule")
	}
}

func TestSanitizeRuleDropsMalformedRecordsAndKeepsRuntimeOutOfTheRule(t *testing.T) {
	valid := Rule{
		ID: "rule-1", Label: "web", Type: TypeLocal, HostID: "host-1",
		BindAddress: "127.0.0.1", LocalPort: 8080,
		RemoteHost: "db", RemotePort: 5432, OrderKey: 1000,
	}
	sanitized, ok := SanitizeRule(valid)
	if !ok {
		t.Fatal("SanitizeRule dropped a valid rule")
	}
	if sanitized.ID != valid.ID || sanitized.Label != valid.Label {
		t.Fatalf("sanitized = %+v, want identity preserved", sanitized)
	}

	malformed := valid
	malformed.Type = "tunnel"
	if _, ok := SanitizeRule(malformed); ok {
		t.Fatal("SanitizeRule kept a rule with an unknown type")
	}

	missingID := valid
	missingID.ID = ""
	if _, ok := SanitizeRule(missingID); ok {
		t.Fatal("SanitizeRule kept a rule without an id")
	}
}

func TestSanitizeRuleNormalizesStoredText(t *testing.T) {
	rule := Rule{
		ID: "rule-1", Label: "  web  ", Type: " LOCAL ", HostID: " host-1 ",
		LocalPort: 8080, RemoteHost: " db ", RemotePort: 5432,
	}
	sanitized, ok := SanitizeRule(rule)
	if !ok {
		t.Fatal("SanitizeRule dropped a normalizable rule")
	}
	if sanitized.Label != "web" || sanitized.Type != TypeLocal ||
		sanitized.HostID != model.HostID("host-1") || sanitized.RemoteHost != "db" {
		t.Fatalf("sanitized = %+v, want trimmed fields", sanitized)
	}
	if sanitized.BindAddress != "127.0.0.1" {
		t.Fatalf("BindAddress = %q, want the default", sanitized.BindAddress)
	}
}
