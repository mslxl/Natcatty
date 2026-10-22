package vault

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestHostRowActionsStayInsideTheRowAt720x480 guards the production Host row:
// every action must be laid out and the Delete label must keep its full width,
// not clip to "Dele", at the minimum window size.
func TestHostRowActionsStayInsideTheRowAt720x480(t *testing.T) {
	m := locales.In("en")
	state, hosts, groups, _, _, tt := newProductionEntityView(t, 720, 480)
	hosts.items = []vault.Host{{
		ID: "host-1", Label: "web-1-with-a-long-label-that-pushes-the-actions", Hostname: "127.0.0.1", Port: 22,
		Protocol: vault.ProtocolSSH, Group: "prod",
	}}
	_ = groups
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.SetLocale("ru")
	tt.Frame()
	m = locales.In("ru")

	labels := []string{
		m.VaultHostTest(), m.VaultHostConnect(), m.VaultHostEdit(),
		m.VaultHostPin(), m.VaultHostDelete(),
	}
	narrow := make(map[string]ui.Rect, len(labels))
	for _, label := range labels {
		box, ok := tt.Find(label)
		if !ok {
			t.Fatalf("%q is not rendered at 720x480: %q", label, tt.Texts())
		}
		if box.W <= 0 || box.H <= 0 {
			t.Fatalf("%q has no laid-out box: %+v", label, box)
		}
		if box.X < -0.5 || box.X+box.W > 720.5 ||
			box.Y < -0.5 || box.Y+box.H > 480.5 {
			t.Fatalf("%q is outside the window: %+v", label, box)
		}
		narrow[label] = box
	}

	wide := newProductionTester(t, state, 1100, 720)
	wide.SetLocale("ru")
	wide.Frame()
	firstLine := float32(0)
	for index, label := range labels {
		box, ok := wide.Find(label)
		if !ok || box.W <= 0 {
			t.Fatalf("%q is not laid out at 1100x720: %q", label, wide.Texts())
		}
		// At the default size the five actions stay on one line.
		if index == 0 {
			firstLine = box.Y
		} else if box.Y < firstLine-0.5 || box.Y > firstLine+0.5 {
			t.Fatalf("%q moved to another line at 1100x720: %+v", label, box)
		}
		// Wrapping at the minimum size keeps every button's full width, not
		// only Delete's; none may be squeezed or clipped.
		if narrow[label].W < box.W-0.5 {
			t.Fatalf("%q is clipped at 720x480: %+v, want the width it gets at 1100x720 (%+v)",
				label, narrow[label], box)
		}
	}
	assertNoHorizontalOverflow(t, tt, 720, 480)
}
