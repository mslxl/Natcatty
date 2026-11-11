package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

func testOverflowItems() []OverflowItem {
	return []OverflowItem{
		{Key: "docker", Label: "Docker", Icon: icons.Archive},
		{Key: "gpu", Label: "GPU", Icon: icons.Cpu},
		{Key: "tmux", Label: "tmux", Icon: icons.Terminal},
	}
}

// TestOverflowMenuHiddenWhenEmpty proves an empty menu draws no trigger, and
// that a menu that empties while open closes and draws nothing.
func TestOverflowMenuHiddenWhenEmpty(t *testing.T) {
	open := true
	items := []OverflowItem(nil)
	tt := ui.NewTester(func(c *ui.Context) {
		OverflowMenu(c, items, &open, OverflowMenuOptions{
			Label: "More tabs", Icon: icons.Ellipsis,
		}, nil)
	}, 400, 120)
	tt.Frame()
	if open {
		t.Fatal("an empty menu stayed open")
	}
	if _, ok := tt.Find("More tabs"); ok {
		t.Fatalf("an empty menu drew a trigger: %v", tt.Texts())
	}

	// A populated menu draws its trigger and opens; when every item goes
	// away, it closes and hides the trigger again.
	items, open = testOverflowItems(), false
	tt.Frame()
	if _, ok := tt.Find("More tabs"); !ok {
		t.Fatalf("a populated menu drew no trigger: %v", tt.Texts())
	}
	if err := tt.Click("More tabs"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !open {
		t.Fatal("clicking the trigger did not open the menu")
	}
	if _, ok := tt.Find("Docker"); !ok {
		t.Fatalf("the open menu drew no items: %v", tt.Texts())
	}
	items = nil
	tt.Frame()
	if open {
		t.Fatal("a menu whose items all went away stayed open")
	}
	if _, ok := tt.Find("Docker"); ok {
		t.Fatalf("a menu whose items all went away still drew them: %v", tt.Texts())
	}
	if _, ok := tt.Find("More tabs"); ok {
		t.Fatalf("a menu whose items all went away kept an empty trigger: %v", tt.Texts())
	}
}

// TestOverflowMenuListsItemsAndChooses proves the menu lists its items in
// order as icon and label rows, reports the chosen key once, and closes.
func TestOverflowMenuListsItemsAndChooses(t *testing.T) {
	open, chose := false, ""
	tt := ui.NewTester(func(c *ui.Context) {
		OverflowMenu(c, testOverflowItems(), &open, OverflowMenuOptions{
			Label: "More tabs", Icon: icons.Ellipsis,
		}, func(key string) { chose = key })
	}, 400, 240)
	tt.Frame()
	if err := tt.Click("More tabs"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	texts := tt.Texts()
	at := func(label string) int {
		for i, text := range texts {
			if text == label {
				return i
			}
		}
		return -1
	}
	docker, gpu, tmux := at("Docker"), at("GPU"), at("tmux")
	if docker < 0 || gpu < 0 || tmux < 0 || !(docker < gpu && gpu < tmux) {
		t.Fatalf("the menu items are unordered or missing: %v", texts)
	}
	row, ok := tt.Find("GPU")
	if !ok {
		t.Fatalf("the GPU row is not reachable: %v", texts)
	}
	if row.W < 120 || row.H < 24 {
		t.Fatalf("the GPU row is %.0f x %.0f DIPs, want a full icon-and-label row: %+v",
			row.W, row.H, row)
	}
	if err := tt.Click("GPU"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if chose != "gpu" {
		t.Fatalf("choosing GPU reported %q, want gpu", chose)
	}
	if open {
		t.Fatal("choosing an item left the menu open")
	}
	if _, ok := tt.Find("Docker"); ok {
		t.Fatalf("choosing an item left the menu drawn: %v", tt.Texts())
	}

	// The trigger toggles the menu closed again.
	if err := tt.Click("More tabs"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !open {
		t.Fatal("clicking the trigger did not reopen the menu")
	}
	if err := tt.Click("More tabs"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if open {
		t.Fatal("clicking the trigger again did not close the menu")
	}
}

// TestOverflowMenuClosesOnEscape proves Escape closes the open menu without
// choosing anything.
func TestOverflowMenuClosesOnEscape(t *testing.T) {
	open, chose := true, ""
	tt := ui.NewTester(func(c *ui.Context) {
		OverflowMenu(c, testOverflowItems(), &open, OverflowMenuOptions{
			Label: "More tabs", Icon: icons.Ellipsis,
		}, func(key string) { chose = key })
	}, 400, 240)
	tt.Frame()
	if _, ok := tt.Find("Docker"); !ok {
		t.Fatalf("the menu did not open: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if open {
		t.Fatal("Escape left the menu open")
	}
	if chose != "" {
		t.Fatalf("Escape chose %q, want no choice", chose)
	}
	if _, ok := tt.Find("Docker"); ok {
		t.Fatalf("Escape left the menu drawn: %v", tt.Texts())
	}
}
