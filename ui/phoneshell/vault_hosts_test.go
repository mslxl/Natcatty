package phoneshell

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// vaultHosts is the Vault's Host fixture: one jump Host, one Host whose route
// and credentials come entirely from a Host Group, one Host with its own jump
// chain, and a Local Host the phone must filter out.
func vaultHosts() []domainvault.Host {
	return []domainvault.Host{
		{
			ID: "bastion", Label: "bastion", Hostname: "bastion.example.com",
			Port: 2222, Username: "jump", Protocol: domainvault.ProtocolSSH,
		},
		{
			ID: "prod", Label: "prod box", Hostname: "prod.example.com",
			Protocol: domainvault.ProtocolSSH, GroupID: "g-prod",
		},
		{
			ID: "web", Label: "web box", Hostname: "web.example.com", Port: 22,
			Protocol:  domainvault.ProtocolSSH,
			HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"bastion"}},
		},
		{
			ID: "local", Label: "desktop shell", Hostname: "localhost",
			Protocol: domainvault.ProtocolLocal,
		},
	}
}

// vaultGroups is the Host Group fixture. Its defaults prove the detail page
// resolves the same route the Session would: username, port and jump chain.
func vaultGroups() []domainvault.Group {
	return []domainvault.Group{
		{
			ID: "g-prod", Name: "prod", Path: "prod",
			Username: "deploy", Port: 2200,
			HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"bastion"}},
		},
	}
}

// openVaultHosts loads a shell over the fixture and opens Vault > Hosts.
func openVaultHosts(t *testing.T, store *fakeHostStore, opts ...Option) (*Shell, *ui.Tester) {
	t.Helper()
	options := []Option{WithHosts(store)}
	options = append(options, opts...)
	shell := New(options...)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Hosts")
	if !tt.HasText("Remote Hosts") {
		t.Fatalf("Vault Hosts page did not render; texts: %v", texts(t, tt))
	}
	return shell, tt
}

// TestVaultRootListsSectionsInDesktopOrder pins the eight sections and their
// order to the desktop sidebar's, which is what makes the two surfaces match.
func TestVaultRootListsSectionsInDesktopOrder(t *testing.T) {
	shell := New(WithVault(&fakeVault{}), WithHosts(&fakeHostStore{}))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := ui.NewTester(shell.View(Vault), 390, 844)
	tt.Frame()

	want := []string{"Hosts", "Keychain", "Proxies", "Networks", "Forwarding", "Snippets", "Notes", "Known hosts"}
	next := 0
	for _, text := range tt.Texts() {
		if next < len(want) && text == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("Vault sections are not in desktop order: missing %q; texts: %v", want[next], texts(t, tt))
	}
}

// TestVaultSectionsNavigate opens every section in order. The five sections
// with later tickets must say so instead of drawing a broken page, and the
// pages that exist must be reachable under their new paths.
func TestVaultSectionsNavigate(t *testing.T) {
	hostStore := &fakeHostStore{hosts: vaultHosts(), groups: vaultGroups()}
	shell := New(WithVault(&fakeVault{}), WithHosts(hostStore))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := ui.NewTester(shell.View(Vault), 390, 844)
	tt.Frame()

	cases := []struct {
		title string
		path  string
		text  string
	}{
		{"Hosts", "/hosts", "Remote Hosts"},
		{"Keychain", "/keychain", "SSH keys"},
		{"Proxies", "/proxies", "No Proxy Profiles yet"},
		{"Networks", "/networks", "Network Profiles are not available on this device."},
		{"Forwarding", "/forwarding", "Port forwarding rules are not on the phone yet."},
		{"Snippets", "/snippets", "Snippets are not on the phone yet."},
		{"Notes", "/notes", "Vault Notes are not on the phone yet."},
		{"Known hosts", "/known-hosts", "No known hosts yet"},
	}
	for _, tc := range cases {
		if err := tt.Click(tc.title); err != nil {
			t.Fatalf("click %q: %v", tc.title, err)
		}
		tt.Advance(600 * time.Millisecond)
		if got := shell.Router(Vault).Path(); got != tc.path {
			t.Fatalf("path after clicking %q = %q, want %q", tc.title, got, tc.path)
		}
		if !tt.HasText(tc.text) {
			t.Fatalf("%q page does not show %q; texts: %v", tc.title, tc.text, texts(t, tt))
		}
		if err := tt.Click("Back"); err != nil {
			t.Fatalf("click Back from %q: %v", tc.title, err)
		}
		tt.Advance(600 * time.Millisecond)
	}
}

// TestVaultHostDetailShowsEffectiveChainAndInheritance checks the detail page
// explains the route a Session would use: the resolved endpoint and the jump
// chain, with the Host owning it or the Host Group it came from.
func TestVaultHostDetailShowsEffectiveChainAndInheritance(t *testing.T) {
	store := &fakeHostStore{hosts: vaultHosts(), groups: vaultGroups()}
	shell, tt := openVaultHosts(t, store)

	// A Host with no route of its own resolves the group's username, port and
	// jump chain.
	if err := tt.Click("Details for prod box"); err != nil {
		t.Fatalf("click prod details: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if got := shell.Router(Vault).Path(); got != "/hosts/prod" {
		t.Fatalf("Vault path = %q, want the pushed detail page", got)
	}
	for _, want := range []string{
		"deploy@prod.example.com:2200", "Jump chain", "bastion", "Inherited from prod", "prod",
	} {
		if !tt.HasText(want) {
			t.Fatalf("prod detail does not show %q; texts: %v", want, texts(t, tt))
		}
	}
	if tt.HasText("desktop shell") {
		t.Fatalf("a Local Host appeared on the phone: %v", texts(t, tt))
	}

	// A Host that owns its chain reports that, rather than an inheritance.
	if err := tt.Click("Back"); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if err := tt.Click("Details for web box"); err != nil {
		t.Fatalf("click web details: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	for _, want := range []string{"Jump chain", "bastion", "This Host"} {
		if !tt.HasText(want) {
			t.Fatalf("web detail does not show %q; texts: %v", want, texts(t, tt))
		}
	}
}

// TestVaultHostsMultiSelectMoveCallsBatchService drives the picker path: the
// selected Hosts go to the shared batch operation with the chosen group and
// the page reloads the same store afterwards.
func TestVaultHostsMultiSelectMoveCallsBatchService(t *testing.T) {
	store := &fakeHostStore{hosts: vaultHosts(), groups: vaultGroups()}
	_, tt := openVaultHosts(t, store)

	if err := tt.Click("Select"); err != nil {
		t.Fatalf("click Select: %v", err)
	}
	tt.Frame()
	for _, row := range []string{"Select prod box", "Select web box"} {
		if err := tt.Click(row); err != nil {
			t.Fatalf("click %q: %v", row, err)
		}
		tt.Frame()
	}
	if !tt.HasText("2 selected") {
		t.Fatalf("selection count is wrong; texts: %v", texts(t, tt))
	}
	if err := tt.Click("Move"); err != nil {
		t.Fatalf("click Move: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Move to prod"); err != nil {
		t.Fatalf("click Move to prod: %v", err)
	}
	tt.Frame()

	if len(store.assignments) != 1 {
		t.Fatalf("batch assignments = %+v, want one", store.assignments)
	}
	got := store.assignments[0]
	if got.GroupID != "g-prod" || len(got.IDs) != 2 ||
		got.IDs[0] != "prod" || got.IDs[1] != "web" {
		t.Fatalf("batch assignment = %+v, want prod and web into g-prod", got)
	}
	for _, host := range store.hosts {
		if host.ID == "prod" || host.ID == "web" {
			if host.GroupID != "g-prod" {
				t.Fatalf("Host %s GroupID = %q, want g-prod", host.ID, host.GroupID)
			}
		}
	}
	if tt.HasText("2 selected") {
		t.Fatalf("selection survived a successful move; texts: %v", texts(t, tt))
	}
}

// TestVaultHostsDropOnGroupMovesSelection proves the drag gesture runs the
// same batch rule as the picker; a selected row drags the whole selection.
func TestVaultHostsDropOnGroupMovesSelection(t *testing.T) {
	store := &fakeHostStore{hosts: vaultHosts(), groups: vaultGroups()}
	_, tt := openVaultHosts(t, store)

	if err := tt.Click("Select"); err != nil {
		t.Fatalf("click Select: %v", err)
	}
	tt.Frame()
	for _, row := range []string{"Select prod box", "Select web box"} {
		if err := tt.Click(row); err != nil {
			t.Fatalf("click %q: %v", row, err)
		}
		tt.Frame()
	}

	src, ok := tt.Find("Select prod box")
	if !ok {
		t.Fatalf("drag source is missing; texts: %v", texts(t, tt))
	}
	dst, ok := tt.Find("Group prod")
	if !ok {
		t.Fatalf("group drop target is missing; texts: %v", texts(t, tt))
	}
	dragBetween(tt, src, dst)
	tt.Frame()

	if len(store.assignments) != 1 {
		t.Fatalf("drop assignments = %+v, want one", store.assignments)
	}
	got := store.assignments[0]
	if got.GroupID != "g-prod" || len(got.IDs) != 2 ||
		got.IDs[0] != "prod" || got.IDs[1] != "web" {
		t.Fatalf("drop assignment = %+v, want the two selected Hosts into g-prod", got)
	}
}

// TestVaultHostsMoveFailureKeepsSelectionAndRetries is the failure path: the
// selection remains, the message names the target, and Retry re-runs the same
// batch until it succeeds.
func TestVaultHostsMoveFailureKeepsSelectionAndRetries(t *testing.T) {
	store := &fakeHostStore{
		hosts: vaultHosts(), groups: vaultGroups(),
		assignErr: errors.New("disk full"),
	}
	_, tt := openVaultHosts(t, store)

	if err := tt.Click("Select"); err != nil {
		t.Fatalf("click Select: %v", err)
	}
	tt.Frame()
	for _, row := range []string{"Select prod box", "Select web box"} {
		if err := tt.Click(row); err != nil {
			t.Fatalf("click %q: %v", row, err)
		}
		tt.Frame()
	}
	if err := tt.Click("Move"); err != nil {
		t.Fatalf("click Move: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Move to prod"); err != nil {
		t.Fatalf("click Move to prod: %v", err)
	}
	tt.Frame()

	if len(store.assignments) != 1 {
		t.Fatalf("assignments after failure = %+v, want the one attempt", store.assignments)
	}
	if !tt.HasText("Could not move the Hosts to prod.") {
		t.Fatalf("failure message is missing; texts: %v", texts(t, tt))
	}
	if !tt.HasText("2 selected") {
		t.Fatalf("selection was lost on failure; texts: %v", texts(t, tt))
	}
	for _, host := range store.hosts {
		if host.ID == "web" && host.GroupID != "" {
			t.Fatalf("a failed move still changed the store: %+v", host)
		}
	}

	store.assignErr = nil
	if err := tt.Click("Retry"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	tt.Frame()

	if len(store.assignments) != 2 {
		t.Fatalf("assignments after retry = %+v, want two attempts", store.assignments)
	}
	retried := store.assignments[1]
	if retried.GroupID != "g-prod" || len(retried.IDs) != 2 ||
		retried.IDs[0] != "prod" || retried.IDs[1] != "web" {
		t.Fatalf("retry = %+v, want the same batch", retried)
	}
	if tt.HasText("2 selected") {
		t.Fatalf("selection survived the successful retry; texts: %v", texts(t, tt))
	}
}

// TestVaultSectionsFailIndependently keeps a failing page local: a Key read
// failure leaves Known Hosts usable, and a Host Group read failure leaves the
// Host list usable.
func TestVaultSectionsFailIndependently(t *testing.T) {
	t.Run("keychain failure", func(t *testing.T) {
		vault := &fakeVault{keysErr: errors.New("keys unreadable")}
		vault.known = append(vault.known, sampleKnownHost())
		shell := New(WithVault(vault), WithHosts(&fakeHostStore{}))
		if err := shell.Load(context.Background()); err == nil {
			t.Fatal("Load returned nil for a failing Key read")
		}
		tt := openSectionPage(t, shell, Vault, "Keychain")
		if !tt.HasText("Could not load SSH keys.") {
			t.Fatalf("Keychain failure is not shown; texts: %v", texts(t, tt))
		}
		if err := tt.Click("Back"); err != nil {
			t.Fatalf("click Back: %v", err)
		}
		tt.Advance(600 * time.Millisecond)
		if err := tt.Click("Known hosts"); err != nil {
			t.Fatalf("click Known hosts: %v", err)
		}
		tt.Advance(600 * time.Millisecond)
		if !tt.HasText("example.com:22") {
			t.Fatalf("Known Hosts page was blocked by the Key failure; texts: %v", texts(t, tt))
		}
	})

	t.Run("group failure", func(t *testing.T) {
		store := &fakeHostStore{
			hosts: vaultHosts(), groupsErr: errors.New("groups unreadable"),
		}
		shell := New(WithHosts(store))
		if err := shell.Load(context.Background()); err != nil {
			t.Fatalf("a group read failure must not fail Load: %v", err)
		}
		tt := openSectionPage(t, shell, Vault, "Hosts")
		if !tt.HasText("prod box") {
			t.Fatalf("Host list was blocked by the group failure; texts: %v", texts(t, tt))
		}
		if !tt.HasText("Could not load Host Groups.") {
			t.Fatalf("group failure is not shown in place; texts: %v", texts(t, tt))
		}
	})
}

// TestVaultHostTapConnects keeps the Hosts tab's primary gesture on the Vault
// page too: outside selection mode a row tap opens a Session.
func TestVaultHostTapConnects(t *testing.T) {
	opener := &fakeOpener{output: "ready\r\n"}
	store := &fakeHostStore{hosts: vaultHosts()}
	shell := New(WithHosts(store), WithSessionOpener(opener), WithScreenFactory(fakeScreens))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(Vault)
	tt := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	tt.Frame()

	if err := tt.Click("Hosts"); err != nil {
		t.Fatalf("click Hosts: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if err := tt.Click("Open prod box"); err != nil {
		t.Fatalf("click Host row: %v", err)
	}
	tt.Frame()
	if got := shell.Selected(); got != Sessions {
		t.Fatalf("selected section = %v, want Sessions", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(opener.opened()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the Host tap did not open a Session; texts: %v", texts(t, tt))
		}
		tt.Frame()
		tt.Advance(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	if opened := opener.opened(); opened[0].ID != "prod" {
		t.Fatalf("opened Host = %q, want prod", opened[0].ID)
	}
}

// dragBetween presses the first box's centre, moves to the second's centre and
// releases there. A press that moves starts a value drag; the drop target takes
// it on the next frame.
func dragBetween(tt *ui.Tester, from, to ui.Rect) {
	sx, sy := from.X+from.W/2, from.Y+from.H/2
	tx, ty := to.X+to.W/2, to.Y+to.H/2
	tt.Press(sx, sy)
	for k := 1; k <= 4; k++ {
		f := float32(k) / 4
		tt.Move(sx+(tx-sx)*f, sy+(ty-sy)*f)
	}
	tt.Release(tx, ty)
}
