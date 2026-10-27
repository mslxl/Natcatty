//go:build selftest

package app

// Ticket 20 self-test. Press F11, then 2, then 0. The check proves the Vault's
// Host Groups, group defaults, Proxy Profiles, and Known Hosts through the real
// SQLite store, the real application service, and the real Vault view rendered
// by ui.Tester: it creates a Host and a Host Group, drags the Host into the
// group, shows the group's defaults on the Host, creates and selects a Proxy
// Profile, lists and removes a Known Host, and deletes the group without
// deleting its Host. A layout step renders the same surface at 1100x720 and
// 720x480 and checks that no visible text overflows the window.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-20.json. The steps close their temporary
// store and remove their directory, so F11,2,0 passes twice in one process.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest20Ticket = 20

type selftest20 struct {
	dir     string
	store   *store.Store
	service *appvault.Service
	state   *vaultstate.State
	tester  *ui.Tester
}

func (t *selftest20) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest20Ticket,
		Name:   "Vault Host Groups, Proxy Profiles, and Known Hosts",
		Steps: []selftest.Step{
			{Name: "group-defaults-and-drag", Run: t.groupStep},
			{Name: "proxy-profile-selection", Run: t.proxyStep},
			{Name: "known-hosts-trust", Run: t.knownHostStep},
			{Name: "group-delete-keeps-hosts", Run: t.deleteGroupStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest20) groupStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")

	if err := t.tester.Click(m.VaultGroupNew()); err != nil {
		return t.fail(fmt.Errorf("open the Host Group editor: %w", err))
	}
	t.tester.Frame()
	t.state.GroupForm().Path = "prod/web"
	t.state.GroupForm().Username = "deploy"
	t.state.GroupForm().Port = "2222"
	if err := t.state.SaveGroup(); err != nil {
		return t.fail(fmt.Errorf("save Host Group: %w", err))
	}
	t.tester.Frame()
	if t.state.GroupCount() != 1 {
		return t.fail(fmt.Errorf("Host Groups = %d, want 1", t.state.GroupCount()))
	}

	t.state.BeginCreate()
	form := t.state.Form()
	form.Label = "web-1"
	form.Hostname = "10.0.0.1"
	// The Port stays blank: the Host inherits the group's 2222 instead of
	// silently writing the protocol default onto the record.
	if err := t.state.Save(); err != nil {
		return t.fail(fmt.Errorf("save Host: %w", err))
	}
	t.tester.Frame()
	if t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("Hosts = %d, want 1", t.state.HostCount()))
	}
	if stored := t.state.Items()[0]; stored.Port != 0 {
		return t.fail(fmt.Errorf("stored Port = %d, want 0 so it inherits the group port",
			stored.Port))
	}

	// Drag the Host row onto the group row: this exercises the view's drag
	// payload and drop target, not just the state method they call.
	hostRect, ok := t.tester.Find("web-1")
	if !ok {
		return t.fail(fmt.Errorf("Host row not found: %v", t.tester.Texts()))
	}
	groupRect, ok := t.tester.Find("prod/web")
	if !ok {
		return t.fail(fmt.Errorf("group row not found: %v", t.tester.Texts()))
	}
	t.tester.Press(hostRect.X+hostRect.W/2, hostRect.Y+hostRect.H/2)
	t.tester.Move(groupRect.X+groupRect.W/2, groupRect.Y+groupRect.H/2)
	t.tester.Release(groupRect.X+groupRect.W/2, groupRect.Y+groupRect.H/2)
	t.tester.Frame()

	raw, err := t.service.List(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Hosts: %w", err))
	}
	if len(raw) != 1 || raw[0].Group != "prod/web" {
		return t.fail(fmt.Errorf("Host after drop = %+v, want group prod/web", raw))
	}
	if t.state.GroupMemberCount("prod/web") != 1 {
		return t.fail(fmt.Errorf("group member count = %d, want 1",
			t.state.GroupMemberCount("prod/web")))
	}

	// The Host has no username of its own; now that it is a member, the group
	// default is the effective value and is shown in the list.
	effective, ok := t.state.EffectiveHost(t.state.Items()[0].ID)
	if !ok || effective.Username != "deploy" {
		return t.fail(fmt.Errorf("effective Host username = %q, want the group default",
			effective.Username))
	}
	if effective.Port != 2222 {
		return t.fail(fmt.Errorf("effective Host port = %d, want the group port 2222",
			effective.Port))
	}
	if stored := t.state.Items()[0]; stored.Username != "" || stored.Port != 0 {
		return t.fail(fmt.Errorf("a group default was written onto the Host: %+v", stored))
	}
	if !textsContained(t.tester.Texts(), "deploy") {
		return t.fail(fmt.Errorf("the Host row does not show the group default: %v",
			t.tester.Texts()))
	}
	if !textsContained(t.tester.Texts(), "10.0.0.1:2222") {
		return t.fail(fmt.Errorf("the Host row does not show the inherited port: %v",
			t.tester.Texts()))
	}
	return selftest.Passed(
		"real store: group create, Host create, drag/drop membership, inherited username and port",
		"appvault.Service + store.Store + vaultui.View + ui.Tester")
}

func (t *selftest20) proxyStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	if err := t.tester.Click(m.VaultProxyNew()); err != nil {
		return t.fail(fmt.Errorf("open the Proxy Profile editor: %w", err))
	}
	t.tester.Frame()
	t.state.ProxyForm().Label = "office"
	t.state.ProxyForm().Type = string(domainvault.ProxySOCKS5)
	t.state.ProxyForm().Host = "socks.example.com"
	t.state.ProxyForm().Port = "1080"
	if err := t.state.SaveProxyProfile(); err != nil {
		return t.fail(fmt.Errorf("save Proxy Profile: %w", err))
	}
	t.tester.Frame()
	if t.state.ProxyProfileCount() != 1 {
		return t.fail(fmt.Errorf("Proxy Profiles = %d, want 1", t.state.ProxyProfileCount()))
	}
	profile := t.state.ProxyProfiles()[0]

	id := model.HostID(t.state.Items()[0].ID)
	t.state.BeginEdit(id)
	t.state.Form().ProxyProfileID = profile.ID
	if err := t.state.Save(); err != nil {
		return t.fail(fmt.Errorf("select the Proxy Profile on the Host: %w", err))
	}
	t.tester.Frame()

	effective, err := t.service.Get(context.Background(), id)
	if err != nil {
		return t.fail(fmt.Errorf("get effective Host: %w", err))
	}
	if effective.Proxy == nil || effective.Proxy.Host != "socks.example.com" ||
		effective.Proxy.Port != 1080 {
		return t.fail(fmt.Errorf("effective Proxy = %+v, want the selected profile",
			effective.Proxy))
	}

	// Deleting the profile clears the Host's soft reference and keeps the Host.
	t.state.RequestDeleteProxyProfile(profile.ID)
	t.tester.Frame()
	if !textsContained(t.tester.Texts(), "Delete") {
		return t.fail(fmt.Errorf("proxy delete confirmation missing: %v", t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm Proxy Profile delete: %w", err))
	}
	t.tester.Frame()
	if t.state.ProxyProfileCount() != 0 || t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("after profile delete: profiles = %d, Hosts = %d",
			t.state.ProxyProfileCount(), t.state.HostCount()))
	}
	raw, err := t.service.List(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Hosts: %w", err))
	}
	if raw[0].ProxyProfileID != "" {
		return t.fail(fmt.Errorf("Host kept the deleted profile reference %q",
			raw[0].ProxyProfileID))
	}
	return selftest.Passed(
		"real store: profile create, Host selection materialized, delete clears the reference",
		"appvault.Service + store.Store + vaultui.View + ui.Tester")
}

func (t *selftest20) knownHostStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	// Trust a host key through the real connection Manager and the real
	// SQLite store. The Manager notifies the Vault state, whose next frame
	// reloads the list: no manual Reload and no restart.
	key := domainconn.Key{
		Hostname: "known.example.com", Port: 2222, KeyType: "ssh-ed25519",
		PublicKey: "ssh-ed25519 AAAA", Fingerprint: "SHA256:selftest20",
	}
	manager := appconnection.NewManager(
		selftest20Dialer{key: key}, nil, t.store,
		appconnection.WithPrompter(selftest20Trust{}),
		appconnection.WithAuthSource(selftest20Auth{}),
		appconnection.WithKnownHostsChanged(t.state.InvalidateKnownHosts),
	)
	if err := manager.Test(context.Background(), appconnection.Request{
		HostID: "host-known", Hostname: key.Hostname, Port: key.Port, Username: "root",
	}); err != nil {
		return t.fail(fmt.Errorf("trust the host key: %w", err))
	}
	t.tester.Frame()

	if t.state.KnownHostCount() != 1 {
		return t.fail(fmt.Errorf("Known Hosts = %d, want 1 after the trust decision",
			t.state.KnownHostCount()))
	}
	if !t.tester.HasText("known.example.com:2222") ||
		!t.tester.HasText(m.VaultKnownHostTrusted()) ||
		!textsContained(t.tester.Texts(), "ssh-ed25519") {
		return t.fail(fmt.Errorf("Known Host row is missing trust state: %v", t.tester.Texts()))
	}

	if err := t.tester.Click(m.VaultKnownHostRemove()); err != nil {
		return t.fail(fmt.Errorf("click Remove trust: %w", err))
	}
	t.tester.Frame()
	if !textsContained(t.tester.Texts(), "known.example.com:2222") {
		return t.fail(fmt.Errorf("remove-trust confirmation is missing: %v", t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm remove trust: %w", err))
	}
	t.tester.Frame()
	if t.state.KnownHostCount() != 0 {
		return t.fail(fmt.Errorf("Known Hosts after remove = %d", t.state.KnownHostCount()))
	}
	if t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("removing trust deleted a Host"))
	}
	records, err := t.store.ListKnownHosts(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Known Hosts: %w", err))
	}
	if len(records) != 0 {
		return t.fail(fmt.Errorf("store still has %d Known Hosts", len(records)))
	}
	return selftest.Passed(
		"real store: a trusted host key appears without a reload and can be removed",
		"appconnection.Manager + store.Store + vaultui.View + ui.Tester")
}

// selftest20Dialer presents one host key and returns a transport whose shell is
// never opened: the trust path runs before authentication and terminal use.
type selftest20Dialer struct{ key domainconn.Key }

func (d selftest20Dialer) Dial(ctx context.Context, req appconnection.DialRequest) (appconnection.Client, error) {
	if req.VerifyHostKey != nil {
		if err := req.VerifyHostKey(ctx, d.key); err != nil {
			return nil, err
		}
	}
	return selftest20Client{}, nil
}

type selftest20Client struct{}

func (selftest20Client) OpenTerminal(context.Context, appconnection.TerminalSpec) (appconnection.Terminal, error) {
	return nil, fmt.Errorf("no terminal in the trust self-test")
}

func (selftest20Client) Close() error { return nil }

type selftest20Trust struct{}

func (selftest20Trust) Confirm(context.Context, domainconn.Key) (appconnection.PromptDecision, error) {
	return appconnection.PromptDecision{Trust: true, Persist: true}, nil
}

type selftest20Auth struct{}

func (selftest20Auth) AuthPlan(context.Context, appconnection.Request) (appconnection.AuthPlan, error) {
	return appconnection.AuthPlan{
		Username: "root",
		Password: "pw",
		Candidates: []appconnection.AuthCandidate{{
			Kind: appconnection.AuthKindPassword, ID: appconnection.CandidatePassword,
		}},
	}, nil
}

func (t *selftest20) deleteGroupStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("click group Delete: %w", err))
	}
	t.tester.Frame()
	if !textsContained(t.tester.Texts(), "Delete Host Group") {
		return t.fail(fmt.Errorf("group delete confirmation is missing: %v", t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm group Delete: %w", err))
	}
	t.tester.Frame()

	if t.state.GroupCount() != 0 {
		return t.fail(fmt.Errorf("Host Groups after delete = %d", t.state.GroupCount()))
	}
	if t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("Hosts after group delete = %d, want the Host kept",
			t.state.HostCount()))
	}
	raw, err := t.service.List(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Hosts: %w", err))
	}
	if raw[0].Group != "" {
		return t.fail(fmt.Errorf("Host group after delete = %q, want the Vault root",
			raw[0].Group))
	}
	return selftest.Passed(
		"real store: deleting a group keeps its Hosts and returns them to the Vault root",
		"store.Store + appvault.Service + vaultui.View + ui.Tester")
}

func (t *selftest20) layoutStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	// The Host lost its group above, so give it a username and tags of its
	// own: the summary then has the same token count as the reported
	// three-line case instead of only an endpoint and an auth method.
	host := t.state.Items()[0]
	t.state.BeginEdit(host.ID)
	t.state.Form().Username = "deploy"
	t.state.Form().Tags = "prod, web"
	if err := t.state.Save(); err != nil {
		return t.fail(fmt.Errorf("save the Host summary fields: %w", err))
	}
	effective, ok := t.state.EffectiveHost(host.ID)
	if !ok {
		return t.fail(fmt.Errorf("Host %q is missing after the edit", host.Label))
	}
	summary := []string{
		fmt.Sprintf("%s:%d", effective.Hostname, effective.Port),
		"deploy", m.VaultAuthSummaryKeyboard(), "prod, web",
	}

	// Render through the real shell, including a work Tab, so the Vault gets
	// the production content width and height: the window minus the fixed
	// sidebar, content padding, and tab strip. Rendering the surface alone at
	// 720 hid the layout bugs the real window showed.
	service := appsession.NewService(selftest20Factory{})
	defer service.CloseAll()
	shell := shellstate.NewState(
		shellstate.WithSessions(service, func(*appsession.Session) (shellstate.Surface, error) {
			return selftest20Surface{}, nil
		}),
		shellstate.WithVault(vaultui.View(t.state)),
	)
	if err := shell.NewTab(); err != nil {
		return t.fail(fmt.Errorf("open a work Tab for the production chrome: %w", err))
	}
	shell.ShowVault()
	view := shellui.View(shell)

	for _, size := range []struct {
		width, height int
	}{{1100, 720}, {720, 480}} {
		tester := ui.NewTester(view, size.width, size.height)
		tester.SetLocale("en")
		tester.Frame()

		for _, want := range []string{
			m.VaultHostNew(), m.VaultGroupNew(), m.VaultProxyNew(),
			m.VaultKnownHostsTitle(t.state.KnownHostCount()),
			m.VaultKeyListTitle(t.state.KeyCount()),
			m.VaultIdentityListTitle(t.state.IdentityCount()),
			"web-1",
		} {
			if !tester.HasText(want) {
				return t.fail(fmt.Errorf("%dx%d: %q is not rendered: %v",
					size.width, size.height, want, tester.Texts()))
			}
		}
		lines, err := selftest20SummaryLines(tester, summary)
		if err != nil {
			return t.fail(fmt.Errorf("%dx%d: %w", size.width, size.height, err))
		}
		if lines > 2 {
			return t.fail(fmt.Errorf("%dx%d: the Host summary uses %d lines, want at most 2",
				size.width, size.height, lines))
		}
		// Every laid-out text must stay inside the window. Texts the Scroll
		// has not reached have no geometry yet, so they are not checked.
		for _, text := range tester.Texts() {
			rect, ok := tester.Find(text)
			if !ok || rect.W <= 0 {
				continue
			}
			if rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 {
				return t.fail(fmt.Errorf("%dx%d: %q overflows horizontally: %+v",
					size.width, size.height, text, rect))
			}
		}
	}
	t.cleanup()
	return selftest.Passed(
		"the Vault renders inside the production shell and tab strip at 1100x720 "+
			"and 720x480 with a two-line Host summary and no horizontal overflow",
		"shellui.View + vaultui.View + ui.Tester")
}

// selftest20SummaryLines counts the visual lines the summary tokens occupy, including a
// token that wraps inside itself.
func selftest20SummaryLines(tester *ui.Tester, tokens []string) (int, error) {
	rects := make([]ui.Rect, 0, len(tokens))
	lineHeight := float32(0)
	for _, token := range tokens {
		rect, ok := tester.Find(token)
		if !ok || rect.W <= 0 {
			return 0, fmt.Errorf("summary token %q is not laid out", token)
		}
		rects = append(rects, rect)
		if lineHeight == 0 || (rect.H > 0 && rect.H < lineHeight) {
			lineHeight = rect.H
		}
	}
	type group struct {
		y     float32
		lines int
	}
	var groups []group
	for _, rect := range rects {
		lines := int((rect.H + lineHeight/2) / lineHeight)
		if lines < 1 {
			lines = 1
		}
		merged := false
		for i := range groups {
			if rect.Y-groups[i].y < 1 && groups[i].y-rect.Y < 1 {
				if lines > groups[i].lines {
					groups[i].lines = lines
				}
				merged = true
				break
			}
		}
		if !merged {
			groups = append(groups, group{y: rect.Y, lines: lines})
		}
	}
	total := 0
	for _, g := range groups {
		total += g.lines
	}
	return total, nil
}

// selftest20Transport keeps a fake Session alive so the layout step renders
// the same work Tab strip the production window shows over the Vault.
type selftest20Transport struct {
	output chan []byte
	closed chan struct{}
	once   sync.Once
}

func newSelftest20Transport() *selftest20Transport {
	return &selftest20Transport{output: make(chan []byte), closed: make(chan struct{})}
}

func (f *selftest20Transport) Read(p []byte) (int, error) {
	select {
	case data := <-f.output:
		return copy(p, data), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *selftest20Transport) Write(p []byte) (int, error) { return len(p), nil }
func (f *selftest20Transport) Resize(int, int) error       { return nil }
func (f *selftest20Transport) Wait() (int, error)          { return 0, nil }

func (f *selftest20Transport) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

type selftest20Factory struct{}

func (selftest20Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newSelftest20Transport(), nil
}

type selftest20Surface struct{}

func (selftest20Surface) View(c *ui.Context) ui.Element { return ui.Text(c, "terminal") }
func (selftest20Surface) Close()                        {}

func (t *selftest20) start() error {
	if t.state != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-20-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, "vault.db"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("open store: %w", err)
	}
	service := appvault.NewService(db,
		appvault.WithGroups(db),
		appvault.WithProxyProfiles(db),
		appvault.WithKnownHosts(db),
	)
	state := vaultstate.NewState(service,
		vaultstate.WithGroups(service),
		vaultstate.WithProxyProfiles(service),
		vaultstate.WithKnownHosts(service),
	)
	if err := state.Reload(); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("load the Vault: %w", err)
	}
	t.dir, t.store, t.service, t.state = dir, db, service, state
	t.tester = ui.NewTester(vaultui.View(state), 1100, 720)
	t.tester.SetLocale("en")
	t.tester.Frame()
	return nil
}

func (t *selftest20) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest20) cleanup() {
	if t.store != nil {
		_ = t.store.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.store, t.service, t.state, t.tester = "", nil, nil, nil, nil
}

func textsContained(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}
