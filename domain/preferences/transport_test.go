package preferences

import (
	"testing"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
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

func TestPredictionDisplayDefaultsToStockAdaptive(t *testing.T) {
	if got := Defaults().Transport.MoshPredictionDisplay; got != moshmodel.PredictionAdaptive {
		t.Fatalf("default prediction display = %q, want adaptive", got)
	}
}

func TestNormalizePredictionDisplayUsesStockValues(t *testing.T) {
	cases := []struct {
		name  string
		value moshmodel.PredictionDisplay
		want  moshmodel.PredictionDisplay
	}{
		// A missing value means mosh's stock adaptive default.
		{"missing", "", moshmodel.PredictionAdaptive},
		{"adaptive", moshmodel.PredictionAdaptive, moshmodel.PredictionAdaptive},
		{"always", moshmodel.PredictionAlways, moshmodel.PredictionAlways},
		{"never", moshmodel.PredictionNever, moshmodel.PredictionNever},
		// Prediction is display-only, so an unknown value safely becomes the
		// stock default rather than making a Session unusable.
		{"unknown", moshmodel.PredictionDisplay("experimental"), moshmodel.PredictionAdaptive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings := Defaults()
			settings.Transport.MoshPredictionDisplay = c.value
			normalized := Normalize(settings)
			if normalized.Transport.MoshPredictionDisplay != c.want {
				t.Fatalf("Normalize = %q, want %q",
					normalized.Transport.MoshPredictionDisplay, c.want)
			}
			// The transport client choice is independent.
			if normalized.Transport.Implementation != transportmodel.Bundled {
				t.Fatalf("Normalize changed implementation to %q", normalized.Transport.Implementation)
			}
		})
	}
}

func TestPredictionDisplayFieldResetsIndependently(t *testing.T) {
	settings := Defaults()
	settings.Transport.Implementation = transportmodel.InProcess
	settings.Transport.MoshPredictionDisplay = moshmodel.PredictionNever

	reset := ResetField(settings, FieldMoshPredictionDisplay)
	if reset.Transport.MoshPredictionDisplay != moshmodel.PredictionAdaptive {
		t.Fatalf("ResetField = %q, want adaptive", reset.Transport.MoshPredictionDisplay)
	}
	if reset.Transport.Implementation != transportmodel.InProcess {
		t.Fatalf("ResetField changed the transport choice to %q", reset.Transport.Implementation)
	}

	group := ResetGroup(settings, GroupTransport)
	if group.Transport.MoshPredictionDisplay != moshmodel.PredictionAdaptive ||
		group.Transport.Implementation != transportmodel.Bundled {
		t.Fatalf("ResetGroup = %+v, want stock transport defaults", group.Transport)
	}

	if owner, ok := GroupOf(FieldMoshPredictionDisplay); !ok || owner != GroupTransport {
		t.Fatalf("GroupOf = %q, %v, want %q", owner, ok, GroupTransport)
	}
}
