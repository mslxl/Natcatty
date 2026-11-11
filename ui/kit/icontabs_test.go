package kit

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

func testIconTabs() []IconTabItem {
	return []IconTabItem{
		{Key: "overview", Label: "Overview", Icon: icons.Activity},
		{Key: "processes", Label: "Processes", Icon: icons.List},
		{Key: "ports", Label: "Ports", Icon: icons.Network},
		{Key: "gpu", Label: "GPU", Icon: icons.Cpu},
	}
}

// countText is how many labels in texts are exactly want.
func countText(texts []string, want string) int {
	count := 0
	for _, text := range texts {
		if text == want {
			count++
		}
	}
	return count
}

// TestIconTabsDrawsOneFormAtATime proves the strip measures the whole labeled
// form, including any trailing controls, and switches every tab to icon-only
// together when it does not fit. Labels stay available to assistive
// technology and as tooltips, so Find still reaches a tab.
func TestIconTabsDrawsOneFormAtATime(t *testing.T) {
	tabs := testIconTabs()
	width := float32(1000)
	chosen := 0
	tt := ui.NewTester(func(c *ui.Context) {
		IconTabs(c, tabs, &chosen, IconTabsOptions{
			Label:         "System tabs",
			Width:         width,
			Reveal:        true,
			Trailing:      func() { ui.Box(c).Size(28, 28).Label("layout") },
			TrailingWidth: 28,
		})
	}, 1200, 80)
	tt.Frame()

	// Wide: every tab draws its label beside its icon.
	for _, tab := range tabs {
		rect, ok := tt.Find(tab.Label)
		if !ok || rect.W <= 0 {
			t.Fatalf("wide: tab %q is not reachable: %v", tab.Label, tt.Texts())
		}
		if got := countText(tt.Texts(), tab.Label); got != 2 {
			t.Fatalf("wide: tab %q has %d texts, want its accessible name and drawn label: %v",
				tab.Label, got, tt.Texts())
		}
	}
	if _, ok := tt.Find("System tabs"); !ok {
		t.Fatalf("wide: the strip's accessible label is missing: %v", tt.Texts())
	}
	if _, ok := tt.Find("layout"); !ok {
		t.Fatalf("wide: the trailing control is missing: %v", tt.Texts())
	}

	// Narrow: the labels stop fitting, so every tab drops its label at once.
	width = 300
	tt.Frame()
	for _, tab := range tabs {
		rect, ok := tt.Find(tab.Label)
		if !ok || rect.W <= 0 {
			t.Fatalf("narrow: tab %q is not reachable: %v", tab.Label, tt.Texts())
		}
		if got := countText(tt.Texts(), tab.Label); got != 1 {
			t.Fatalf("narrow: tab %q has %d texts, want only its accessible name: %v",
				tab.Label, got, tt.Texts())
		}
		if rect.W > 60 {
			t.Fatalf("narrow: tab %q is %.0f DIPs wide, wider than an icon-only tab: %+v",
				tab.Label, rect.W, rect)
		}
	}
	if _, ok := tt.Find("layout"); !ok {
		t.Fatalf("narrow: the trailing layout control scrolled or dropped: %v", tt.Texts())
	}

	// Compact is explicit even when the labels would fit.
	width, compact := 1000, true
	tt = ui.NewTester(func(c *ui.Context) {
		IconTabs(c, tabs, &chosen, IconTabsOptions{
			Label: "System tabs", Width: width, Compact: compact,
		})
	}, 1200, 80)
	tt.Frame()
	for _, tab := range tabs {
		if got := countText(tt.Texts(), tab.Label); got != 1 {
			t.Fatalf("compact: tab %q has %d texts, want only its accessible name: %v",
				tab.Label, got, tt.Texts())
		}
	}
}

// TestIconTabsMeasuresItsTrailingControls proves the layout button and the
// Collapsed Tabs trigger count toward the whole strip: a width that holds the
// labeled tabs alone compacts once the trailing controls are measured.
func TestIconTabsMeasuresItsTrailingControls(t *testing.T) {
	tabs := testIconTabs()
	chosen := 0
	width := float32(380)

	render := func(trailing bool) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			options := IconTabsOptions{Label: "System tabs", Width: width}
			options.Trailing = func() { ui.Box(c).Size(120, 28).Label("trailing") }
			options.TrailingWidth = 120
			if !trailing {
				options.Trailing, options.TrailingWidth = nil, 0
			}
			IconTabs(c, tabs, &chosen, options)
		}, 1200, 80)
		tt.Frame()
		return tt
	}

	without := render(false)
	for _, tab := range tabs {
		if got := countText(without.Texts(), tab.Label); got != 2 {
			t.Fatalf("without trailing: tab %q has %d texts, want the labeled form: %v",
				tab.Label, got, without.Texts())
		}
	}
	with := render(true)
	for _, tab := range tabs {
		if got := countText(with.Texts(), tab.Label); got != 1 {
			t.Fatalf("with trailing: tab %q has %d texts, want the icon-only form: %v",
				tab.Label, got, with.Texts())
		}
	}
	if _, ok := with.Find("trailing"); !ok {
		t.Fatalf("with trailing: the trailing control is missing: %v", with.Texts())
	}
}

// TestIconTabsChangesTheChosenTab proves a click reports a change once and
// moves the chosen index; the chosen tab's tooltip names it in icon-only form.
func TestIconTabsChangesTheChosenTab(t *testing.T) {
	tabs := testIconTabs()
	chosen, changes := 0, 0
	tt := ui.NewTester(func(c *ui.Context) {
		if IconTabs(c, tabs, &chosen, IconTabsOptions{
			Label: "System tabs", Compact: true,
		}) {
			changes++
		}
	}, 400, 80)
	tt.Frame()
	if err := tt.Click("GPU"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if chosen != 3 || changes != 1 {
		t.Fatalf("clicking GPU chose %d after %d changes, want 3 after 1", chosen, changes)
	}
	if got := countText(tt.Texts(), "GPU"); got != 1 {
		t.Fatalf("icon-only GPU has %d texts before hovering, want its accessible name: %v",
			got, tt.Texts())
	}

	// Every tab keeps a tooltip with its label in icon-only form.
	rect, ok := tt.Find("Ports")
	if !ok || rect.W <= 0 {
		t.Fatalf("the Ports tab is not rendered: %v", tt.Texts())
	}
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if got := countText(tt.Texts(), "Ports"); got < 2 {
		t.Fatalf("hovering the Ports tab shows %d texts, want its tooltip: %v",
			got, tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, "Ports") && text != "Ports" {
			t.Fatalf("the Ports tooltip reads %q, want its label", text)
		}
	}

	// Clicking the chosen tab again reports no change.
	if err := tt.Click("GPU"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if changes != 1 {
		t.Fatalf("clicking the chosen tab reported %d changes, want 1", changes)
	}
}

// TestIconTabsRevealsTheChosenTabInAScroll proves the strip scrolls sideways
// when even the icon-only form does not fit, and that reveal keeps the chosen
// tab whole inside the strip's own box.
func TestIconTabsRevealsTheChosenTabInAScroll(t *testing.T) {
	tabs := testIconTabs()
	chosen := len(tabs) - 1
	tt := ui.NewTester(func(c *ui.Context) {
		IconTabs(c, tabs, &chosen, IconTabsOptions{
			Label: "System tabs", Compact: true, Width: 60, Reveal: true,
		})
	}, 300, 80)
	tt.Frame()
	tt.Frame()

	strip, ok := tt.Find("System tabs")
	if !ok || strip.W <= 0 {
		t.Fatalf("the strip is not rendered: %v", tt.Texts())
	}
	active, ok := tt.Find("GPU")
	if !ok || active.W <= 0 {
		t.Fatalf("the chosen GPU tab is not rendered: %v", tt.Texts())
	}
	if active.X < strip.X-0.5 || active.X+active.W > strip.X+strip.W+0.5 {
		t.Fatalf("the chosen GPU tab %.0f..%.0f is not whole inside the strip %.0f..%.0f",
			active.X, active.X+active.W, strip.X, strip.X+strip.W)
	}
	if active.X <= strip.X+0.5 {
		t.Fatalf("the strip did not scroll the chosen last tab into view: GPU at %.0f, strip at %.0f",
			active.X, strip.X)
	}
}
