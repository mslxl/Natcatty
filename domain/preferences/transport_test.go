package preferences

import (
	"testing"

	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
)

func TestTransportDefaultsToBundled(t *testing.T) {
	if got := Defaults().Transport.Implementation; got != transportmodel.Bundled {
		t.Fatalf("default implementation = %q, want bundled", got)
	}
}

func TestNormalizeKeepsTransportChoices(t *testing.T) {
	cases := []struct {
		name  string
		value transportmodel.Implementation
		want  transportmodel.Implementation
	}{
		// A missing global value means the bundled default.
		{"missing", transportmodel.Unspecified, transportmodel.Bundled},
		{"bundled", transportmodel.Bundled, transportmodel.Bundled},
		{"in process", transportmodel.InProcess, transportmodel.InProcess},
		// A value from another build must survive normalization so the
		// Session factory can report a typed error instead of silently
		// falling back to the bundled client.
		{"unknown", transportmodel.Implementation("bundled_v2"), transportmodel.Implementation("bundled_v2")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings := Defaults()
			settings.Transport.Implementation = c.value
			normalized := Normalize(settings)
			if normalized.Transport.Implementation != c.want {
				t.Fatalf("Normalize = %q, want %q", normalized.Transport.Implementation, c.want)
			}
		})
	}
}

func TestTransportFieldResetsToTheBundledDefault(t *testing.T) {
	settings := Defaults()
	settings.Transport.Implementation = transportmodel.InProcess

	reset := ResetField(settings, FieldTransportImplementation)
	if reset.Transport.Implementation != transportmodel.Bundled {
		t.Fatalf("ResetField = %q, want bundled", reset.Transport.Implementation)
	}

	group := ResetGroup(settings, GroupTransport)
	if group.Transport.Implementation != transportmodel.Bundled {
		t.Fatalf("ResetGroup = %q, want bundled", group.Transport.Implementation)
	}

	if owner, ok := GroupOf(FieldTransportImplementation); !ok || owner != GroupTransport {
		t.Fatalf("GroupOf = %q, %v, want %q", owner, ok, GroupTransport)
	}
}
