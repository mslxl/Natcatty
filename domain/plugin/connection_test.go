package plugin

import (
	"errors"
	"strings"
	"testing"
)

func validConnectionManifest() Manifest {
	manifest := validManifest()
	manifest.Contributions = Contributions{
		Connections: []ConnectionProvider{{
			Protocol: "x-acme-loopback",
			Name:     "Acme Loopback",
			HostFields: []ConnectionField{
				{Key: "target", Label: "Target", Kind: "text", Required: true},
			},
			Capabilities: ConnectionCapabilities{
				Resize:          true,
				Probe:           true,
				Reconnect:       true,
				Signals:         []string{"interrupt", "terminate"},
				MaxSessions:     8,
				RestoreStateMax: 512,
			},
			AuthMethods:   []string{"none", "password"},
			RestoreSchema: []string{"token"},
		}},
	}
	manifest.Permissions = Permissions{
		Required: []string{"provider.connection"},
		Scopes: map[string]Scope{
			"provider.connection": {Provider: "acme.hello", Access: AccessFull},
		},
	}
	return manifest
}

func TestManifestAcceptsConnectionProvider(t *testing.T) {
	manifest := validConnectionManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestManifestRejectsConnectionProviderClaimProblems(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(*Manifest)
	}{
		{"builtin protocol", "contributions.connections[0].protocol", func(m *Manifest) {
			m.Contributions.Connections[0].Protocol = "ssh"
		}},
		{"invalid protocol", "contributions.connections[0].protocol", func(m *Manifest) {
			m.Contributions.Connections[0].Protocol = "acme-loopback"
		}},
		{"duplicate protocol in manifest", "contributions.connections[1].protocol", func(m *Manifest) {
			m.Contributions.Connections = append(m.Contributions.Connections,
				m.Contributions.Connections[0])
		}},
		{"missing permission", "permissions", func(m *Manifest) {
			m.Permissions = Permissions{}
		}},
		{"scope names another plugin", "permissions.scopes.provider.connection", func(m *Manifest) {
			m.Permissions.Scopes["provider.connection"] = Scope{
				Provider: "other.plugin", Access: AccessFull,
			}
		}},
		{"unknown auth method", "contributions.connections[0].auth_methods[2]", func(m *Manifest) {
			m.Contributions.Connections[0].AuthMethods =
				append(m.Contributions.Connections[0].AuthMethods, "kerberos")
		}},
		{"duplicate signal", "contributions.connections[0].capabilities.signals[2]", func(m *Manifest) {
			m.Contributions.Connections[0].Capabilities.Signals =
				append(m.Contributions.Connections[0].Capabilities.Signals, "interrupt")
		}},
		{"restore schema without bound", "contributions.connections[0].restore_schema", func(m *Manifest) {
			m.Contributions.Connections[0].Capabilities.RestoreStateMax = 0
		}},
		{"restore bound without schema", "contributions.connections[0].restore_schema", func(m *Manifest) {
			m.Contributions.Connections[0].RestoreSchema = nil
		}},
		{"oversized restore", "contributions.connections[0].capabilities.restore_state_max", func(m *Manifest) {
			m.Contributions.Connections[0].Capabilities.RestoreStateMax =
				MaxConnectionRestoreBytes + 1
		}},
		{"unknown field kind", "contributions.connections[0].host_fields[0].kind", func(m *Manifest) {
			m.Contributions.Connections[0].HostFields[0].Kind = "color"
		}},
		{"credential names missing field", "contributions.connections[0].credentials[0]", func(m *Manifest) {
			m.Contributions.Connections[0].Credentials = []string{"password"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validConnectionManifest()
			test.edit(&manifest)
			err := manifest.Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid connection declaration")
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if validation.Field != test.field {
				t.Fatalf("field = %q, want %q (%v)", validation.Field, test.field, err)
			}
		})
	}
}

func TestConnectionCapabilitiesApplyDefaultsAndCaps(t *testing.T) {
	capabilities := ConnectionCapabilities{}
	if got := capabilities.EffectiveMaxSessions(); got != DefaultConnectionMaxSessions {
		t.Fatalf("default sessions = %d, want %d", got, DefaultConnectionMaxSessions)
	}
	if got := capabilities.EffectiveRestoreMax(); got != 0 {
		t.Fatalf("default restore = %d, want 0", got)
	}
	capabilities.MaxSessions = MaxConnectionSessions + 100
	capabilities.RestoreStateMax = MaxConnectionRestoreBytes + 100
	if got := capabilities.EffectiveMaxSessions(); got != MaxConnectionSessions {
		t.Fatalf("capped sessions = %d, want %d", got, MaxConnectionSessions)
	}
	if got := capabilities.EffectiveRestoreMax(); got != MaxConnectionRestoreBytes {
		t.Fatalf("capped restore = %d, want %d", got, MaxConnectionRestoreBytes)
	}
	if !capabilities.SupportsSignal("terminate") {
		// Signals are empty here; this proves the lookup is exact.
	} else {
		t.Fatal("empty capability set reported a signal")
	}
}

func TestProviderErrorKindsAreClosedAndPayloadFree(t *testing.T) {
	for _, kind := range []ProviderErrorKind{
		ProviderErrorAuthFailed, ProviderErrorUnreachable,
		ProviderErrorHostKeyChanged, ProviderErrorProtocol,
		ProviderErrorTimeout, ProviderErrorCancelled, ProviderErrorFaulted,
	} {
		got, ok := ProviderErrorKindByName(string(kind))
		if !ok || got != kind {
			t.Fatalf("ProviderErrorKindByName(%q) = %q, %t", kind, got, ok)
		}
	}
	if kind, ok := ProviderErrorKindByName("super-secret-token"); ok ||
		kind != ProviderErrorProtocol {
		t.Fatalf("unknown kind = %q, %t; want protocol-error, false", kind, ok)
	}
	err := &ProviderError{
		Kind: ProviderErrorFaulted, Provider: "acme.loopback",
		Protocol: "x-acme-loopback", Message: "instance unloaded",
	}
	text := err.Error()
	for _, want := range []string{"provider-faulted", "acme.loopback", "instance unloaded"} {
		if !strings.Contains(text, want) {
			t.Fatalf("error %q is missing %q", text, want)
		}
	}
	if strings.Contains(text, "secret") {
		t.Fatalf("error unexpectedly carries payload material: %q", text)
	}
}
