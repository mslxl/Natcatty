package app

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

var errPhoneProxyRecorded = errors.New("phone proxy test: dial recorded")

// phoneProxyRecorder is the phone stack's SSH dialer stand-in. It records the
// request the Manager built and fails the open so the test never needs a peer.
type phoneProxyRecorder struct {
	requests []appconnection.DialRequest
}

func (r *phoneProxyRecorder) Dial(_ context.Context, req appconnection.DialRequest) (appconnection.Client, error) {
	r.requests = append(r.requests, req)
	return nil, errPhoneProxyRecorded
}

func newPhoneProxyTestVault(t *testing.T) (*appvault.Service, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	secrets, err := store.OpenSecretStore(ctx, db,
		&writeTestKeyRing{values: map[string][]byte{}},
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open secret store: %v", err)
	}
	t.Cleanup(secrets.Close)
	return newPhoneVault(db, secrets), db, dir
}

func savePhoneProxyProfile(t *testing.T, db *store.Store, id, command string) {
	t.Helper()
	if err := db.SaveProxyProfile(context.Background(), domainvault.ProxyProfile{
		ID: id, Label: id,
		Config: domainvault.ProxyConfig{Type: domainvault.ProxyCommand, Command: command},
	}); err != nil {
		t.Fatalf("save proxy profile %s: %v", id, err)
	}
}

func createPhoneProxyHost(t *testing.T, vault *appvault.Service, draft domainvault.Draft) domainvault.Host {
	t.Helper()
	draft.Hostname = draft.Label + ".invalid"
	draft.Username = "probe"
	// Password mode bypasses the Vault's discovery of the login user's keys.
	draft.AuthMethod = domainvault.HostAuthPassword
	host, err := vault.Create(context.Background(), draft)
	if err != nil {
		t.Fatalf("create Host %s: %v", draft.Label, err)
	}
	return host
}

func loadPhoneProxyPreferences(t *testing.T, dir, name, defaultProfile string) *apppreferences.Service {
	t.Helper()
	ctx := context.Background()
	prefs := apppreferences.NewService(store.OpenPreferences(filepath.Join(dir, name)))
	if err := prefs.Load(ctx); err != nil {
		t.Fatalf("load preferences: %v", err)
	}
	if defaultProfile != "" {
		// The value Vault Sync writes when the desktop's portable settings
		// document is applied on this phone.
		if err := prefs.Update(func(s *domainpreferences.Settings) {
			s.Proxy.DefaultProfileID = defaultProfile
		}); err != nil {
			t.Fatalf("set default proxy profile: %v", err)
		}
	}
	return prefs
}

// TestPhoneProxyCommandUsesDesktopPrecedenceWithGlobalDefault proves the phone
// composition root passes its preferences into the shared route resolver, so a
// global default Profile synced from the desktop applies, while the existing
// precedence stays unchanged: explicit Profile, Host Group inheritance, inline
// configuration, first-hop Host, then explicit Direct.
func TestPhoneProxyCommandUsesDesktopPrecedenceWithGlobalDefault(t *testing.T) {
	ctx := context.Background()
	vault, db, dir := newPhoneProxyTestVault(t)
	const (
		explicit = "probe-explicit %h %p"
		grouped  = "probe-group %h %p"
		inline   = "probe-inline %h %p"
		firstHop = "probe-first-hop %h %p"
		global   = "probe-global %h %p"
	)
	savePhoneProxyProfile(t, db, "explicit", explicit)
	savePhoneProxyProfile(t, db, "grouped", grouped)
	savePhoneProxyProfile(t, db, "first-hop", firstHop)
	savePhoneProxyProfile(t, db, "global", global)
	for _, group := range []domainvault.GroupDraft{
		{Path: "parent", ProxyProfileID: "grouped"},
		{Path: "parent/child"},
	} {
		if _, err := vault.CreateGroup(ctx, group); err != nil {
			t.Fatalf("create group %s: %v", group.Path, err)
		}
	}
	jump := createPhoneProxyHost(t, vault, domainvault.Draft{
		Label: "bastion", Port: 2222, ProxyProfileID: "first-hop",
	})

	prefs := loadPhoneProxyPreferences(t, dir, "phone-preferences.json", "global")
	recorder := &phoneProxyRecorder{}
	phone := newPhoneSessionRuntimeWithDialer(recorder, vault, db, dir, nil, nil, nil, nil, prefs)
	t.Cleanup(phone.Close)

	for _, test := range []struct {
		name  string
		draft domainvault.Draft
		want  string
		jump  bool
	}{
		{
			name:  "explicit_profile",
			draft: domainvault.Draft{Group: "parent/child", ProxyProfileID: "explicit"},
			want:  explicit,
		},
		{
			name:  "inherited_group_profile",
			draft: domainvault.Draft{Group: "parent/child"},
			want:  grouped,
		},
		{
			name: "inline_over_group",
			draft: domainvault.Draft{Group: "parent/child", Proxy: &domainvault.ProxyConfig{
				Type: domainvault.ProxyCommand, Command: inline,
			}},
			want: inline,
		},
		{
			name: "first_hop_over_target",
			draft: domainvault.Draft{
				ProxyProfileID: "explicit",
				HostChain:      &domainvault.HostChain{HostIDs: []model.HostID{jump.ID}},
			},
			want: firstHop,
			jump: true,
		},
		{
			name:  "synced_global_default",
			draft: domainvault.Draft{},
			want:  global,
		},
		{
			name:  "explicit_direct_over_group_and_default",
			draft: domainvault.Draft{Group: "parent/child", DirectProxy: true},
			want:  "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.draft.Label = strings.ReplaceAll(test.name, "_", "-")
			host := createPhoneProxyHost(t, vault, test.draft)
			recorder.requests = nil

			handle, err := phone.OpenHost(ctx, host)
			if handle != nil || !errors.Is(err, errPhoneProxyRecorded) || len(recorder.requests) != 1 {
				t.Fatalf("phone open = handle %v / error %v / dials %d", handle, err, len(recorder.requests))
			}
			req := recorder.requests[0]
			if req.Hostname != host.Hostname {
				t.Fatalf("target hostname = %q, want %q", req.Hostname, host.Hostname)
			}
			assertPhoneProxyCommand(t, req.Proxy, test.want)
			if test.jump {
				if len(req.Jumps) != 1 || req.Jumps[0].Hostname != jump.Hostname ||
					req.Jumps[0].Port != 2222 {
					t.Fatalf("first hop = %+v, want the Host chain's bastion", req.Jumps)
				}
			} else if len(req.Jumps) != 0 {
				t.Fatalf("unexpected first hop: %+v", req.Jumps)
			}
		})
	}
}

func assertPhoneProxyCommand(t *testing.T, proxy *appconnection.Proxy, want string) {
	t.Helper()
	if want == "" {
		if proxy != nil {
			t.Fatalf("proxy = %+v, want a direct connection", proxy)
		}
		return
	}
	if proxy == nil || proxy.Type != appconnection.ProxyCommand || proxy.Command != want {
		t.Fatalf("proxy = %+v, want ProxyCommand %q", proxy, want)
	}
}

// phoneProxyOpener runs the phone's real opener and reports its result to the
// test, so the offscreen shell can display the failure the way a user sees it.
type phoneProxyOpener struct {
	runtime *phoneSessionRuntime
	result  chan error
}

func (o phoneProxyOpener) OpenHost(ctx context.Context, host domainvault.Host) (phoneshell.SessionHandle, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	handle, err := o.runtime.OpenHost(ctx, host)
	o.result <- err
	return handle, err
}

// TestPhoneProxyCommandUnsupportedShowsReasonWithoutStarting proves an
// unverified platform shows a clear reason in the phone UI and never starts a
// process or dials directly.
func TestPhoneProxyCommandUnsupportedShowsReasonWithoutStarting(t *testing.T) {
	ctx := context.Background()
	vault, db, dir := newPhoneProxyTestVault(t)
	savePhoneProxyProfile(t, db, "probe", "probe-command %h %p")
	host := createPhoneProxyHost(t, vault, domainvault.Draft{
		Label: "denied", Protocol: domainvault.ProtocolSSH, ProxyProfileID: "probe",
	})

	var started, dialed []string
	dialer := sshadapter.NewDialer(
		sshadapter.WithProxyCommandUnavailable(),
		sshadapter.WithProcessStart(func(command string) (net.Conn, error) {
			started = append(started, command)
			return nil, errors.New("the ProxyCommand process must not start")
		}),
		sshadapter.WithDialFunc(func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("no direct dial is allowed")
		}),
	)
	phone := newPhoneSessionRuntimeWithDialer(dialer, vault, db, dir, nil, nil, nil, nil, nil)
	t.Cleanup(phone.Close)

	results := make(chan error, 1)
	shell := phoneshell.New(
		phoneshell.WithHosts(vault),
		phoneshell.WithSessionOpener(phoneProxyOpener{phone, results}),
	)
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("load shell: %v", err)
	}
	shell.Select(phoneshell.Hosts)
	tester := ui.NewTester(func(c *ui.Context) { shell.View(shell.Selected())(c) }, 390, 844)
	if err := tester.Click("Open " + host.Label); err != nil {
		t.Fatalf("click the Host row: %v", err)
	}

	const reason = "ProxyCommand is unavailable on this platform"
	awaitCondition(t, "the phone error page to show the ProxyCommand reason", 5*time.Second, func() bool {
		tester.Advance(20 * time.Millisecond)
		return tester.HasText(reason)
	})

	err := <-results
	if err == nil {
		t.Fatal("the phone opened a Session through a gated ProxyCommand")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("error = %v, want KindUnsupported", err)
	}
	if !errors.Is(err, appconnection.ErrProxyCommandUnavailable) {
		t.Fatalf("error = %v, want ErrProxyCommandUnavailable", err)
	}
	if len(started) != 0 || len(dialed) != 0 {
		t.Fatalf("process starts = %v, direct dials = %v, want zero of each", started, dialed)
	}
}
