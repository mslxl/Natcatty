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

func TestMacOSFormGroupPutsEachLabelBesideItsControl(t *testing.T) {
	SetStyle(MacOS)
	t.Cleanup(func() { SetStyle(nil) })
	on := false
	tt := ui.NewTester(func(c *ui.Context) {
		FormGroup(c, "General", func() {
			FormRow(c, "Name", func() { SecondaryButton(c, "", "Pick") })
			FormToggle(c, "Copy on select", &on, func() { ui.Text(c, "reset") })
		})
	}, 500, 200)
	tt.Frame()
	// The field names its control, so "Name" finds the whole row.
	row, _ := tt.Find("Name")
	control, ok := tt.Find("Pick")
	if !ok {
		t.Fatalf("the control is not drawn; texts: %v", tt.Texts())
	}
	// Beside the label: the control starts past the label's column, and the
	// row is one control tall plus its inset, not a label stacked above it.
	if control.X < row.X+40 || row.H > control.H+2*groupRowPaddingY+1 {
		t.Fatalf("row at %+v, control at %+v: want the control beside the label", row, control)
	}
	if err := tt.Click("Copy on select"); err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("a click on the toggle's label did not switch it on")
	}
	if !tt.HasText("reset") {
		t.Fatal("the toggle's accessory is not drawn")
	}
}
