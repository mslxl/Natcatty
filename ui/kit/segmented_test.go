package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSegmentedReportsOnlyAChangedChoice(t *testing.T) {
	segments := []Segment{{Key: "a", Label: "System", Icon: "monitor"}, {Key: "b", Label: "Light", Icon: "sun"}}
	for _, role := range []SegmentedRole{SegmentedIcons, SegmentedTrack, SegmentedFill} {
		chosen, changes := 0, 0
		tt := ui.NewTester(func(c *ui.Context) {
			if Segmented(c, role, &chosen, "", segments) {
				changes++
			}
		}, 400, 80)
		if err := tt.Click("System"); err != nil {
			t.Fatalf("role %d: %v", role, err)
		}
		if changes != 0 {
			t.Fatalf("role %d: clicking the chosen segment reported %d changes", role, changes)
		}
		if err := tt.Click("Light"); err != nil {
			t.Fatalf("role %d: %v", role, err)
		}
		if chosen != 1 || changes != 1 {
			t.Fatalf("role %d: chosen = %d, changes = %d; want 1 and 1", role, chosen, changes)
		}
	}
}
