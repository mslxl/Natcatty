package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFormToggleReportsTheUsersChange(t *testing.T) {
	value, changes := false, 0
	tt := ui.NewTester(func(c *ui.Context) {
		if FormToggle(c, "Copy on select", &value, func() { ui.Text(c, "reset") }) {
			changes++
		}
	}, 400, 80)
	if !tt.HasText("reset") {
		t.Fatal("the accessory is not drawn")
	}
	if err := tt.Click("Copy on select"); err != nil {
		t.Fatal(err)
	}
	if !value || changes != 1 {
		t.Fatalf("value = %v, changes = %d; want true and 1", value, changes)
	}
}
