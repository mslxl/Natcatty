package preferences

import (
	"testing"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
)

func TestTransportDefaultsToOneReleaseSwitch(t *testing.T) {
	settings := Defaults()
	if got := settings.Transport.Implementation; got != transportmodel.CurrentDefault() {
		t.Fatalf("default implementation = %q, want the release default %q",
			got, transportmodel.CurrentDefault())
	}
	if settings.Transport.ImplementationExplicit {
		t.Fatal("a release default looks like an explicit user choice")
	}
}

func TestNormalizeKeepsTransportChoices(t *testing.T) {
	cases := []struct {
		name  string
		value transportmodel.Implementation
		want  transportmodel.Implementation
	}{
		// A missing global value means the release default.
		{"missing", transportmodel.Unspecified, transportmodel.CurrentDefault()},
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

func TestNormalizeKeepsAnExplicitChoiceButClearsAnEmptyOne(t *testing.T) {
	settings := Defaults()
	settings.Transport.Implementation = transportmodel.Bundled
	settings.Transport.ImplementationExplicit = true
	normalized := Normalize(settings)
	if normalized.Transport.Implementation != transportmodel.Bundled ||
		!normalized.Transport.ImplementationExplicit {
		t.Fatalf("Normalize = %+v, want the explicit bundled choice kept",
			normalized.Transport)
	}

	settings.Transport.Implementation = transportmodel.Unspecified
	settings.Transport.ImplementationExplicit = true
	normalized = Normalize(settings)
	if normalized.Transport.Implementation != transportmodel.CurrentDefault() {
		t.Fatalf("Normalize(empty) = %q, want the release default",
			normalized.Transport.Implementation)
	}
	if normalized.Transport.ImplementationExplicit {
		t.Fatal("Normalize called an empty value an explicit choice")
	}
}

func TestTransportFieldResetsToTheReleaseDefault(t *testing.T) {
	settings := Defaults()
	settings.Transport.Implementation = transportmodel.InProcess
	settings.Transport.ImplementationExplicit = true

	reset := ResetField(settings, FieldTransportImplementation)
	if reset.Transport.Implementation != transportmodel.CurrentDefault() {
		t.Fatalf("ResetField = %q, want the release default %q",
			reset.Transport.Implementation, transportmodel.CurrentDefault())
	}
	if reset.Transport.ImplementationExplicit {
		t.Fatal("ResetField kept the explicit-choice flag")
	}

	group := ResetGroup(settings, GroupTransport)
	if group.Transport.Implementation != transportmodel.CurrentDefault() {
		t.Fatalf("ResetGroup = %q, want the release default %q",
			group.Transport.Implementation, transportmodel.CurrentDefault())
	}
	if group.Transport.ImplementationExplicit {
		t.Fatal("ResetGroup kept the explicit-choice flag")
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
			before := settings.Transport.Implementation
			normalized := Normalize(settings)
			if normalized.Transport.MoshPredictionDisplay != c.want {
				t.Fatalf("Normalize = %q, want %q",
					normalized.Transport.MoshPredictionDisplay, c.want)
			}
			// The transport client choice is independent, whatever the release
			// default currently is.
			if normalized.Transport.Implementation != before {
				t.Fatalf("Normalize changed implementation from %q to %q",
					before, normalized.Transport.Implementation)
			}
		})
	}
}

func TestPredictionDisplayFieldResetsIndependently(t *testing.T) {
	settings := Defaults()
	// Pick the implementation the release default does not currently select,
	// so a reset that clobbered the choice would be visible under either
	// policy.
	other := transportmodel.InProcess
	if transportmodel.CurrentDefault() == transportmodel.InProcess {
		other = transportmodel.Bundled
	}
	settings.Transport.Implementation = other
	settings.Transport.MoshPredictionDisplay = moshmodel.PredictionNever

	reset := ResetField(settings, FieldMoshPredictionDisplay)
	if reset.Transport.MoshPredictionDisplay != moshmodel.PredictionAdaptive {
		t.Fatalf("ResetField = %q, want adaptive", reset.Transport.MoshPredictionDisplay)
	}
	if reset.Transport.Implementation != other {
		t.Fatalf("ResetField changed the transport choice to %q", reset.Transport.Implementation)
	}

	group := ResetGroup(settings, GroupTransport)
	if group.Transport.MoshPredictionDisplay != moshmodel.PredictionAdaptive ||
		group.Transport.Implementation != transportmodel.CurrentDefault() {
		t.Fatalf("ResetGroup = %+v, want stock transport defaults", group.Transport)
	}

	if owner, ok := GroupOf(FieldMoshPredictionDisplay); !ok || owner != GroupTransport {
		t.Fatalf("GroupOf = %q, %v, want %q", owner, ok, GroupTransport)
	}
}
