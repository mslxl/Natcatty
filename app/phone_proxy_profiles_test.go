package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestPhoneProxiesWriteTheSharedVault drives Add, Edit, and Delete for a
// ProxyCommand Profile through the real Vault service and SQLite store. A
// second Shell opened on the same store is the desktop reload.
func TestPhoneProxiesWriteTheSharedVault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	vault := newPhoneVault(db, nil)
	shell := phoneshell.New(phoneshell.WithVault(vault))
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := phonePage(t, shell, phoneshell.Vault, "Proxies")

	const firstCommand = `relay --target %h:%p  --label 'a  b'`
	if err := tt.Click("Add profile"); err != nil {
		t.Fatalf("click Add profile: %v", err)
	}
	if err := tt.Click("ProxyCommand"); err != nil {
		t.Fatalf("choose ProxyCommand: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("phone command")
	if err := tt.Click("Command"); err != nil {
		t.Fatalf("focus Command: %v", err)
	}
	tt.Type(firstCommand)
	if err := tt.Click("Save profile"); err != nil {
		t.Fatalf("click Save profile: %v", err)
	}
	if !tt.HasText("phone command") {
		t.Fatalf("the added Profile is not listed; texts: %v", tt.Texts())
	}

	profiles, err := vault.ListProxyProfiles(ctx)
	if err != nil {
		t.Fatalf("list Proxy Profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %+v, want one", profiles)
	}
	added := profiles[0]
	if added.Config.Type != domainvault.ProxyCommand || added.Config.Command != firstCommand {
		t.Fatalf("stored Profile = %+v, want the exact command %q", added, firstCommand)
	}

	// Edit the Profile: the ID and command stay stable; only the label and
	// command text the user changed move.
	if err := tt.Click("Edit profile phone command"); err != nil {
		t.Fatalf("click Edit profile: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label in edit: %v", err)
	}
	tt.Type(" v2")
	if err := tt.Click("Command"); err != nil {
		t.Fatalf("focus Command in edit: %v", err)
	}
	tt.Type(" --extra")
	if err := tt.Click("Save profile"); err != nil {
		t.Fatalf("click Save profile in edit: %v", err)
	}
	updated, err := vault.ProxyProfile(ctx, added.ID)
	if err != nil {
		t.Fatalf("read edited Profile: %v", err)
	}
	if updated.ID != added.ID || updated.Label != "phone command v2" ||
		updated.Config.Command != firstCommand+" --extra" {
		t.Fatalf("edited Profile = %+v, want the same ID and the appended command", updated)
	}

	// A reload of the same store sees the edit, exactly as the desktop does.
	desktop := phoneshell.New(phoneshell.WithVault(vault))
	if err := desktop.Load(ctx); err != nil {
		t.Fatalf("desktop Load: %v", err)
	}
	reloaded := phonePage(t, desktop, phoneshell.Vault, "Proxies")
	if !reloaded.HasText("phone command v2") {
		t.Fatalf("a reloaded Shell does not show the edited Profile; texts: %v", reloaded.Texts())
	}

	if err := tt.Click("Delete profile phone command v2"); err != nil {
		t.Fatalf("click Delete profile: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm delete: %v", err)
	}
	profiles, err = vault.ListProxyProfiles(ctx)
	if err != nil {
		t.Fatalf("list Proxy Profiles after delete: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("the deleted Profile is still in the shared store: %+v", profiles)
	}
}

// openPhoneProxyDevice opens one sync device and reopens its SQLite file
// through the application Store, so a test can make device-side edits the way
// the phone pages do. It mirrors the composition root's shared service.
func openPhoneProxyDevice(t *testing.T, ctx context.Context, remote *phoneSyncRemote, name string) (*syncDevice, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	device := newSyncDevice(t, ctx, dir, name, remote)
	db, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("open %s Vault for edits: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return device, db
}

func runPhoneProxySync(t *testing.T, ctx context.Context, device *syncDevice, phase string, upload bool) {
	t.Helper()
	result, err := device.sync.service.Sync(ctx, appsync.TriggerManual)
	if err != nil || result.Uploaded != upload || result.Downloaded != !upload {
		t.Fatalf("%s sync = %+v, %v", phase, result, err)
	}
}

// TestPhoneProxyCommandEditsSyncBackToDesktop is the delivery proof for
// ticket 20: a command Profile created on the phone, a Host Group switched to
// it, and a Host edited without touching its inline command all reach the
// desktop after a real Vault Sync, with the same IDs, command bytes, Host
// reference, and group inheritance.
func TestPhoneProxyCommandEditsSyncBackToDesktop(t *testing.T) {
	const (
		desktopCommand = `desktop-relay --target %h:%p  --label "two  words"`
		phoneCommand   = `phone-relay --target %h --port %p --literal '%%'`
		inlineCommand  = `inline-relay --port %p --label 'inline  command'`
		inlineLabel    = "inline (phone)"
	)

	ctx := context.Background()
	remote := &phoneSyncRemote{}

	desktop, desktopDB := openPhoneProxyDevice(t, ctx, remote, "desktop")
	desktopVault := newPhoneVault(desktopDB, nil)
	phone, phoneDB := openPhoneProxyDevice(t, ctx, remote, "phone")
	phoneVault := newPhoneVault(phoneDB, nil)

	// The desktop creates a command Profile, a parent group that uses it, a
	// nested group that inherits it, an explicit-reference Host, an inheriting
	// Host, and a Host with an inline command.
	profile, err := desktopVault.CreateProxyProfile(ctx, domainvault.ProxyProfileDraft{
		Label:  "Desktop command",
		Config: domainvault.ProxyConfig{Type: domainvault.ProxyCommand, Command: desktopCommand},
	})
	if err != nil || profile.ID == "" {
		t.Fatalf("desktop CreateProxyProfile = %+v, %v", profile, err)
	}
	parent, err := desktopVault.CreateGroup(ctx, domainvault.GroupDraft{
		Path: "Proxy acceptance", ProxyProfileID: profile.ID,
	})
	if err != nil {
		t.Fatalf("desktop parent group: %v", err)
	}
	child, err := desktopVault.CreateGroup(ctx, domainvault.GroupDraft{Path: parent.Path + "/Nested"})
	if err != nil {
		t.Fatalf("desktop child group: %v", err)
	}
	explicit := createPhoneProxyHost(t, desktopVault, domainvault.Draft{
		Label: "explicit", ProxyProfileID: profile.ID,
	})
	inherited := createPhoneProxyHost(t, desktopVault, domainvault.Draft{
		Label: "inherited", Group: child.Path,
	})
	inline := createPhoneProxyHost(t, desktopVault, domainvault.Draft{
		Label: "inline", Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxyCommand, Command: inlineCommand,
		},
	})

	connectSyncDevice(t, ctx, desktop)
	runPhoneProxySync(t, ctx, desktop, "desktop upload", true)
	connectSyncDevice(t, ctx, phone)
	if err := phone.sync.service.SetStrategy(ctx, domainsync.StrategyPreferCloud); err != nil {
		t.Fatalf("phone sync strategy: %v", err)
	}
	runPhoneProxySync(t, ctx, phone, "phone download", false)

	// Phone edits through the real pages. First create a new command Profile.
	shell := phoneshell.New(
		phoneshell.WithVault(phoneVault),
		phoneshell.WithHosts(phoneVault),
		phoneshell.WithHostCreator(phoneVault),
	)
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("phone Load: %v", err)
	}
	proxies := ui.NewTester(shell.View(phoneshell.Vault), 390, 844)
	if err := proxies.Click("Proxies"); err != nil {
		t.Fatalf("open Proxies: %v", err)
	}
	proxies.Advance(600 * time.Millisecond)
	for _, action := range []string{"Add profile", "ProxyCommand"} {
		if err := proxies.Click(action); err != nil {
			t.Fatalf("click %q: %v", action, err)
		}
	}
	if err := proxies.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	proxies.Type("Phone group command")
	if err := proxies.Click("Command"); err != nil {
		t.Fatalf("focus Command: %v", err)
	}
	proxies.Type(phoneCommand)
	if err := proxies.Click("Save profile"); err != nil {
		t.Fatalf("save phone Profile: %v", err)
	}
	phoneProfiles, err := phoneVault.ListProxyProfiles(ctx)
	if err != nil {
		t.Fatalf("phone list profiles: %v", err)
	}
	var phoneProfile domainvault.ProxyProfile
	for _, candidate := range phoneProfiles {
		if candidate.Label == "Phone group command" {
			phoneProfile = candidate
		}
	}
	if phoneProfile.ID == "" || phoneProfile.Config.Command != phoneCommand {
		t.Fatalf("phone-created Profile = %+v, want command %q", phoneProfile, phoneCommand)
	}

	// Switch the parent group to the phone-created Profile through the group
	// proxy editor. The nested group still inherits from the parent.
	if err := proxies.Click("Back"); err != nil {
		t.Fatalf("back to Vault root: %v", err)
	}
	proxies.Advance(600 * time.Millisecond)
	if err := proxies.Click("Hosts"); err != nil {
		t.Fatalf("open Vault Hosts: %v", err)
	}
	proxies.Advance(600 * time.Millisecond)
	if err := proxies.Click("Group " + parent.Path); err != nil {
		t.Fatalf("open the group proxy editor: %v", err)
	}
	if err := proxies.Click("Phone group command"); err != nil {
		t.Fatalf("select the phone Profile: %v", err)
	}
	if err := proxies.Click("Save group proxy"); err != nil {
		t.Fatalf("save the group proxy: %v", err)
	}
	phoneGroups, err := phoneVault.ListGroups(ctx)
	if err != nil {
		t.Fatalf("phone list groups: %v", err)
	}
	for _, group := range phoneGroups {
		if group.ID == parent.ID && group.ProxyProfileID != phoneProfile.ID {
			t.Fatalf("phone parent group after the editor = %+v, want %q", group, phoneProfile.ID)
		}
	}

	// Rename the inline Host. Editing another field must keep the command
	// bytes untouched.
	hosts := ui.NewTester(shell.View(phoneshell.Hosts), 390, 844)
	if err := hosts.Click("Details for inline"); err != nil {
		t.Fatalf("open the inline Host: %v", err)
	}
	hosts.Advance(600 * time.Millisecond)
	if err := hosts.Click("Edit"); err != nil {
		t.Fatalf("open the inline Host editor: %v", err)
	}
	hosts.Advance(600 * time.Millisecond)
	if err := hosts.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	hosts.Type(" (phone)")
	if err := hosts.Click("Save"); err != nil {
		t.Fatalf("save the inline Host: %v", err)
	}

	// The phone uploads; the desktop downloads and reads the same records.
	runPhoneProxySync(t, ctx, phone, "phone upload", true)
	runPhoneProxySync(t, ctx, desktop, "desktop download", false)

	gotProfile, err := desktopVault.ProxyProfile(ctx, phoneProfile.ID)
	if err != nil {
		t.Fatalf("desktop reads the phone-created Profile: %v", err)
	}
	if gotProfile.ID != phoneProfile.ID || gotProfile.Config.Type != domainvault.ProxyCommand ||
		gotProfile.Config.Command != phoneCommand {
		t.Fatalf("desktop Profile = %+v, want ID %q and command %q",
			gotProfile, phoneProfile.ID, phoneCommand)
	}

	groups, err := desktopVault.ListGroups(ctx)
	if err != nil {
		t.Fatalf("desktop list groups: %v", err)
	}
	byID := make(map[string]domainvault.Group, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	if got := byID[parent.ID]; got.ProxyProfileID != phoneProfile.ID {
		t.Fatalf("desktop parent group = %+v, want the phone Profile %q", got, phoneProfile.ID)
	}
	if got := byID[child.ID]; got.ProxyProfileID != "" || got.ParentID != parent.ID {
		t.Fatalf("desktop child group = %+v, want the parent inheritance", got)
	}

	storedExplicit, err := desktopDB.Get(ctx, explicit.ID)
	if err != nil || storedExplicit.ProxyProfileID != profile.ID || storedExplicit.Proxy != nil {
		t.Fatalf("desktop explicit Host = %+v, %v; want its own reference %q", storedExplicit, err, profile.ID)
	}
	storedInline, err := desktopDB.Get(ctx, inline.ID)
	if err != nil {
		t.Fatalf("desktop inline Host: %v", err)
	}
	if storedInline.Label != inlineLabel {
		t.Fatalf("inline Host label = %q, want the phone edit", storedInline.Label)
	}
	if storedInline.Proxy == nil || storedInline.Proxy.Type != domainvault.ProxyCommand ||
		storedInline.Proxy.Command != inlineCommand {
		t.Fatalf("inline command changed through a label edit: %+v", storedInline.Proxy)
	}

	// The nested Host's effective route is the parent group's phone Profile,
	// and the explicit-reference Host still names the desktop Profile.
	effectiveInherited, err := desktopVault.EffectiveHost(ctx, inherited.ID)
	if err != nil {
		t.Fatalf("desktop effective inherited Host: %v", err)
	}
	if effectiveInherited.Proxy == nil || effectiveInherited.Proxy.ProfileID != phoneProfile.ID ||
		effectiveInherited.Proxy.Command != phoneCommand {
		t.Fatalf("inherited Host proxy = %+v, want Profile %q and command %q",
			effectiveInherited.Proxy, phoneProfile.ID, phoneCommand)
	}
	effectiveExplicit, err := desktopVault.EffectiveHost(ctx, explicit.ID)
	if err != nil {
		t.Fatalf("desktop effective explicit Host: %v", err)
	}
	if effectiveExplicit.Proxy == nil || effectiveExplicit.Proxy.ProfileID != profile.ID ||
		effectiveExplicit.Proxy.Command != desktopCommand {
		t.Fatalf("explicit Host proxy = %+v, want Profile %q and command %q",
			effectiveExplicit.Proxy, profile.ID, desktopCommand)
	}
}
