package vault

import (
	"strings"
	"testing"

	"github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestHostCardMenuStaysInsideTheCardAt720x480 guards the production Host
// card: a long label gives way, so the card's menu button keeps its full size
// inside the window at the minimum window size, and its menu holds every
// action.
func TestHostCardMenuStaysInsideTheCardAt720x480(t *testing.T) {
	label := "web-1-with-a-long-label-that-pushes-the-actions"
	state, hosts, _, _, _, tt := newProductionEntityView(t, 720, 480)
	hosts.items = []vault.Host{{
		ID: "host-1", Label: label, Hostname: "127.0.0.1", Port: 22,
		Protocol: vault.ProtocolSSH, Group: "prod",
	}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.SetLocale("ru")
	tt.Frame()
	m := locales.In("ru")

	wide := newProductionTester(t, state, 1100, 720)
	wide.SetLocale("ru")
	wide.Frame()
	roomy, ok := wide.Find(m.VaultItemActions(label))
	if !ok || roomy.W <= 0 {
		t.Fatalf("the card menu is not laid out at 1100x720: %q", wide.Texts())
	}
	narrow, ok := tt.Find(m.VaultItemActions(label))
	if !ok || narrow.W <= 0 || narrow.H <= 0 {
		t.Fatalf("the card menu is not laid out at 720x480: %q", tt.Texts())
	}
	if narrow.X < -0.5 || narrow.X+narrow.W > 720.5 ||
		narrow.Y < -0.5 || narrow.Y+narrow.H > 480.5 {
		t.Fatalf("the card menu is outside the window: %+v", narrow)
	}
	if narrow.W < roomy.W-0.5 || narrow.H < roomy.H-0.5 {
		t.Fatalf("the card menu is squeezed at 720x480: %+v, want %+v", narrow, roomy)
	}
	assertNoHorizontalOverflow(t, tt, 720, 480)

	if err := tt.Click(m.VaultItemActions(label)); err != nil {
		t.Fatalf("open the card menu: %v", err)
	}
	menu := strings.Join(tt.Menu(), "|")
	for _, want := range []string{
		m.VaultHostConnect(), m.VaultHostTest(), m.VaultHostEdit(),
		m.VaultHostPin(), m.VaultHostDelete(),
	} {
		if !strings.Contains(menu, want) {
			t.Fatalf("the card menu %q has no %q", menu, want)
		}
	}
}
