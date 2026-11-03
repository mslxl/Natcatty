package vault

import (
	"encoding/json"
	"testing"

	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
)

func TestHostJSONWithoutAnImplementationInheritsTheDefault(t *testing.T) {
	// A Host saved before ticket 21 has no transportImplementation key. It
	// must load with the zero value, which means "inherit the global default",
	// so the Host opens without being re-saved or re-created.
	var host Host
	if err := json.Unmarshal([]byte(`{"id":"old","label":"web","hostname":"example.com","protocol":"mosh"}`), &host); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if host.TransportImplementation != transportmodel.Unspecified {
		t.Fatalf("implementation = %q, want the unset inheritance value", host.TransportImplementation)
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("an old Host no longer validates: %v", err)
	}
}

func TestNormalizeDraftKeepsTheImplementationForMoshAndET(t *testing.T) {
	cases := []struct {
		protocol Protocol
		value    transportmodel.Implementation
		want     transportmodel.Implementation
	}{
		{ProtocolMosh, transportmodel.Bundled, transportmodel.Bundled},
		{ProtocolMosh, transportmodel.InProcess, transportmodel.InProcess},
		{ProtocolET, transportmodel.Bundled, transportmodel.Bundled},
		{ProtocolET, transportmodel.InProcess, transportmodel.InProcess},
		// A value from another build is preserved, not rewritten. The Session
		// factory is what fails closed on it.
		{ProtocolET, transportmodel.Implementation("future_client"), transportmodel.Implementation("future_client")},
		{ProtocolMosh, "  bundled  ", transportmodel.Bundled},
		// The choice only concerns the Mosh and ET transports.
		{ProtocolSSH, transportmodel.InProcess, transportmodel.Unspecified},
		{ProtocolTelnet, transportmodel.Bundled, transportmodel.Unspecified},
		{ProtocolLocal, transportmodel.Bundled, transportmodel.Unspecified},
	}
	for _, c := range cases {
		draft, err := NormalizeDraft(Draft{
			Label: "web", Hostname: "example.com", Protocol: c.protocol,
			TransportImplementation: c.value,
		})
		if err != nil {
			t.Fatalf("NormalizeDraft(%q, %q): %v", c.protocol, c.value, err)
		}
		if draft.TransportImplementation != c.want {
			t.Fatalf("NormalizeDraft(%q, %q) = %q, want %q",
				c.protocol, c.value, draft.TransportImplementation, c.want)
		}
	}
}

func TestHostValidationPreservesAnUnknownImplementation(t *testing.T) {
	// A Host written by a newer build must remain valid and saveable on an
	// unrelated edit. The factory rejects the value when the Session opens.
	host := Host{
		Label: "web", Hostname: "example.com", Protocol: ProtocolMosh,
		TransportImplementation: transportmodel.Implementation("future_client"),
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("Validate rejected forward-compatible value: %v", err)
	}
}
