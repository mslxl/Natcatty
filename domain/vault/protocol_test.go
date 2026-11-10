package vault

import (
	"errors"
	"testing"
)

func TestNormalizeDraftAcceptsAProviderProtocol(t *testing.T) {
	// A Host saved by a build with a Connection Provider must remain readable
	// and saveable here even when this build has no provider registered. The
	// Session factory fails closed when it opens.
	draft, err := NormalizeDraft(Draft{
		Label: "lab", Hostname: "lab.example.com", Protocol: "x-acme-loop",
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.Protocol != "x-acme-loop" {
		t.Fatalf("Protocol = %q, want the provider id unchanged", draft.Protocol)
	}

	host := Host{
		ID: "lab", Label: "lab", Hostname: "lab.example.com",
		Protocol: "x-acme-loop",
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("Validate rejected a provider protocol: %v", err)
	}
}

func TestNormalizeDraftRejectsAMalformedProviderProtocol(t *testing.T) {
	// Protocol input is trimmed and lowercased before validation, so only the
	// stored shape matters here.
	for _, protocol := range []Protocol{"x-acme", "x--loop", "x-acme_loop", "gopher"} {
		t.Run(string(protocol), func(t *testing.T) {
			_, err := NormalizeDraft(Draft{Label: "lab", Hostname: "lab", Protocol: protocol})
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("NormalizeDraft error = %v, want a ValidationError", err)
			}
			if validation.Field != FieldProtocol || validation.Rule != RuleUnsupported {
				t.Fatalf("validation = %s/%s, want protocol/unsupported",
					validation.Field, validation.Rule)
			}
		})
	}
}

func TestEffectivePortUsesEveryBuiltinDefault(t *testing.T) {
	tests := []struct {
		protocol Protocol
		want     int
	}{
		{ProtocolSSH, 22},
		{ProtocolTelnet, 23},
		{ProtocolLocal, 0},
		{ProtocolMosh, 22},
		{ProtocolET, 22},
	}
	for _, test := range tests {
		t.Run(string(test.protocol), func(t *testing.T) {
			if got := EffectivePort(test.protocol, 0); got != test.want {
				t.Fatalf("EffectivePort(%q, 0) = %d, want %d", test.protocol, got, test.want)
			}
		})
	}
}

func TestUsesSSHAuthResolvesThroughTheProtocolRegistry(t *testing.T) {
	tests := []struct {
		protocol Protocol
		want     bool
	}{
		{ProtocolSSH, true},
		{ProtocolMosh, true},
		{ProtocolET, true},
		{ProtocolTelnet, false},
		{ProtocolLocal, false},
		{"x-acme-loop", false},
	}
	for _, test := range tests {
		t.Run(string(test.protocol), func(t *testing.T) {
			if got := test.protocol.UsesSSHAuth(); got != test.want {
				t.Fatalf("%q.UsesSSHAuth() = %v, want %v", test.protocol, got, test.want)
			}
		})
	}
}

func TestGroupDefaultsStillRejectProtocolsWithoutGroupSupport(t *testing.T) {
	// Host Group defaults cover SSH and Telnet only; provider protocols and the
	// other built-ins must not sneak in through the registry.
	for _, protocol := range []Protocol{ProtocolLocal, ProtocolMosh, ProtocolET, "x-acme-loop"} {
		t.Run(string(protocol), func(t *testing.T) {
			_, err := NormalizeGroupDraft(GroupDraft{Path: "lab", Protocol: protocol})
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("NormalizeGroupDraft error = %v, want a ValidationError", err)
			}
			if validation.Field != FieldProtocol || validation.Rule != RuleUnsupported {
				t.Fatalf("validation = %s/%s, want protocol/unsupported",
					validation.Field, validation.Rule)
			}
		})
	}
}
