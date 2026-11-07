package preferences

import (
	"testing"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

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
		})
	}
}

func TestPredictionDisplayResetsWithTheTransportGroup(t *testing.T) {
	settings := Defaults()
	settings.Transport.MoshPredictionDisplay = moshmodel.PredictionNever

	reset := ResetField(settings, FieldMoshPredictionDisplay)
	if reset.Transport.MoshPredictionDisplay != moshmodel.PredictionAdaptive {
		t.Fatalf("ResetField = %q, want adaptive", reset.Transport.MoshPredictionDisplay)
	}

	group := ResetGroup(settings, GroupTransport)
	if group.Transport.MoshPredictionDisplay != moshmodel.PredictionAdaptive {
		t.Fatalf("ResetGroup = %q, want adaptive", group.Transport.MoshPredictionDisplay)
	}

	if owner, ok := GroupOf(FieldMoshPredictionDisplay); !ok || owner != GroupTransport {
		t.Fatalf("GroupOf = %q, %v, want %q", owner, ok, GroupTransport)
	}
}
