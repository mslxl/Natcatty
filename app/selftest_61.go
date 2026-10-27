//go:build selftest

package app

// Ticket 61 self-test. Press F11, then 6, then 1.
//
// The check runs the production UI and domain seams headlessly, in one
// process, and proves the cross-cutting acceptance fixes:
//
//   - six Tabs at the 704x421 client reveal the active last Tab inside the
//     strip, every Tab is reachable by scrolling, and selecting a scrolled-out
//     Tab reveals it;
//   - a CRLF, lone CR, and mixed paste reach the send boundary with exactly
//     one LF per line, both directly and through the protected-paste gate,
//     which is the shared boundary for SSH, serial, and Telnet Sessions;
//   - the Host editor's Port hint follows SSH/Telnet (22/23) and a Telnet Host
//     renders no SSH-only Identity, Key, Method, agent, or MFA controls;
//   - the sidebar's five language options stay inside the 704x421 client;
//   - ICU plurals in all five locales use the singular for one container and
//     one GPU, and the monitoring list renders a dash for a Host without a
//     sample instead of a healthy zero;
//   - the Vault's not-found problem stays visible above pinned Save/Cancel at
//     both client sizes, and the forwarding editor's Save, Cancel, problem,
//     and last form row stay reachable above its pinned action bar;
//   - the self-test result directory is private to the process, and the
//     shared danger/warning text helpers clear 4.5:1 on every preset.
//
// Every wait observes layout or state with a hard deadline; nothing sleeps.
// A repeated F11,6,1 resets the run counter and rebuilds every fixture, so the
// check passes twice in one process.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-61.json.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	domainmodel "github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/forwarding"
	"github.com/mslxl/natcatty/ui/locales"
	uimonitoring "github.com/mslxl/natcatty/ui/monitoring"
	"github.com/mslxl/natcatty/ui/selftest"
	sessionui "github.com/mslxl/natcatty/ui/session"
	uishell "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	uivault "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const (
	selftest61Ticket = 61
	selftest61Wait   = 10 * time.Second
)

type selftest61 struct {
	// run counts completed entries. resetRunState starts a fresh run; every
	// step builds its fixture from scratch after it.
	run int
}

func (t *selftest61) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest61Ticket,
		Name:   "Cross-cutting UI defects: tabs, paste, editors, layout, contrast",
		Steps: []selftest.Step{
			{Name: "reset", Run: t.resetStep},
			{Name: "tab-strip", Run: t.tabStripStep},
			{Name: "paste-line-endings", Run: t.pasteStep},
			{Name: "host-editor", Run: t.hostEditorStep},
			{Name: "sidebar-languages", Run: t.sidebarStep},
			{Name: "plurals-and-no-sample", Run: t.pluralsAndDashStep},
			{Name: "pinned-editors", Run: t.pinnedEditorsStep},
			{Name: "process-isolation-and-contrast", Run: t.isolationAndContrastStep},
		},
	}
}

func (t *selftest61) resetRunState() {
	t.run++
}

func (t *selftest61) resetStep(*ui.Context) selftest.Outcome {
	t.resetRunState()
	return selftest.Passed(fmt.Sprintf("run %d starts from a fresh fixture", t.run), "")
}

// --- shell fixtures -------------------------------------------------------

type selftest61Transport struct {
	output chan []byte
	closed chan struct{}
	once   sync.Once
}

func newSelftest61Transport() *selftest61Transport {
	return &selftest61Transport{output: make(chan []byte), closed: make(chan struct{})}
}

func (f *selftest61Transport) Read(p []byte) (int, error) {
	select {
	case data := <-f.output:
		return copy(p, data), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *selftest61Transport) Write(p []byte) (int, error) { return len(p), nil }
func (f *selftest61Transport) Resize(int, int) error       { return nil }
func (f *selftest61Transport) Wait() (int, error)          { return 6, nil }

func (f *selftest61Transport) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

type selftest61Factory struct{ transport *selftest61Transport }

func (f *selftest61Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

type selftest61Surface struct{}

func (selftest61Surface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "ticket-61-terminal-surface").Fill()
}

func (selftest61Surface) Close() {}

type selftest61Shell struct {
	state   *shellstate.State
	service *appsession.Service
}

func (s *selftest61Shell) close() {
	if s.state != nil {
		s.state.Shutdown()
	}
	if s.service != nil {
		s.service.CloseAll()
	}
}

func newSelftest61Shell(tabs int, opts ...shellstate.Option) (*selftest61Shell, error) {
	transport := newSelftest61Transport()
	service := appsession.NewService(&selftest61Factory{transport: transport})
	options := append([]shellstate.Option{
		shellstate.WithSessions(service, func(*appsession.Session) (shellstate.Surface, error) {
			return selftest61Surface{}, nil
		}),
	}, opts...)
	shell := &selftest61Shell{state: shellstate.NewState(options...), service: service}
	for index := 0; index < tabs; index++ {
		if err := shell.state.NewTab(); err != nil {
			shell.close()
			return nil, err
		}
	}
	deadline := time.Now().Add(selftest61Wait)
	for {
		shell.state.Flush()
		if shell.state.TabCount() == tabs {
			break
		}
		if time.Now().After(deadline) {
			shell.close()
			return nil, fmt.Errorf("timed out after %s waiting for %d Tabs", selftest61Wait, tabs)
		}
	}
	return shell, nil
}

func selftest61Inside(tester *ui.Tester, width, height int, text string) (ui.Rect, error) {
	rect, ok := tester.Find(text)
	if !ok {
		return ui.Rect{}, fmt.Errorf("%dx%d does not render %q: %q", width, height, text, tester.Texts())
	}
	if rect.X < -0.5 || rect.Y < -0.5 ||
		rect.X+rect.W > float32(width)+0.5 || rect.Y+rect.H > float32(height)+0.5 {
		return rect, fmt.Errorf("%dx%d clips %q: %+v", width, height, text, rect)
	}
	return rect, nil
}

// --- tab strip ------------------------------------------------------------

func (t *selftest61) tabStripStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	shell, err := newSelftest61Shell(6)
	if err != nil {
		return selftest.Failed(err)
	}
	defer shell.close()

	width, height := 704, 421
	tester := ui.NewTester(uishell.View(shell.state), width, height)
	tester.SetLocale("en")
	tester.Frame()

	strip, err := selftest61Inside(tester, width, height, m.WorkTabsLabel())
	if err != nil {
		return selftest.Failed(err)
	}
	last, err := selftest61Inside(tester, width, height, "Local Session 6")
	if err != nil {
		return selftest.Failed(err)
	}
	if last.W < 40 {
		return selftest.Failed(fmt.Errorf("the active last Tab is clipped at rest: %+v", last))
	}
	if last.X < strip.X-0.5 || last.X+last.W > strip.X+strip.W+0.5 {
		return selftest.Failed(fmt.Errorf("the active last Tab is outside the strip at rest: tab %+v strip %+v", last, strip))
	}
	if first, ok := tester.Find("Local Session 1"); ok &&
		first.X < strip.X+strip.W && first.X+first.W > strip.X {
		return selftest.Failed(fmt.Errorf("the first Tab is half cut at rest: tab %+v strip %+v", first, strip))
	}

	tester.Scroll(strip.X+strip.W/2, strip.Y+strip.H/2, -2000, 0)
	tester.Frame()
	first, err := selftest61Inside(tester, width, height, "Local Session 1")
	if err != nil {
		return selftest.Failed(err)
	}
	if first.W < 40 || first.X < strip.X-0.5 || first.X+first.W > strip.X+strip.W+0.5 {
		return selftest.Failed(fmt.Errorf("the first Tab is unreachable after scrolling: tab %+v strip %+v", first, strip))
	}

	tester.Scroll(strip.X+strip.W/2, strip.Y+strip.H/2, 2000, 0)
	tester.Frame()
	last, err = selftest61Inside(tester, width, height, "Local Session 6")
	if err != nil {
		return selftest.Failed(err)
	}
	if last.W < 40 || last.X < strip.X-0.5 || last.X+last.W > strip.X+strip.W+0.5 {
		return selftest.Failed(fmt.Errorf("the last Tab is unreachable after scrolling: tab %+v strip %+v", last, strip))
	}

	shell.state.SelectTab(0)
	tester.Frame()
	first, err = selftest61Inside(tester, width, height, "Local Session 1")
	if err != nil {
		return selftest.Failed(err)
	}
	if first.W < 40 || first.X < strip.X-0.5 || first.X+first.W > strip.X+strip.W+0.5 {
		return selftest.Failed(fmt.Errorf("selecting the first Tab did not reveal it: tab %+v strip %+v", first, strip))
	}
	return selftest.Passed(
		"six Tabs at 704x421 keep the active last Tab whole, scroll to every Tab, and reveal a selected Tab",
		"ui/shell.View + ui.Tester")
}

// --- paste line endings ---------------------------------------------------

func (t *selftest61) pasteStep(*ui.Context) selftest.Outcome {
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{"crlf", "first\r\nsecond", "first\nsecond"},
		{"lone-cr", "first\rsecond", "first\nsecond"},
		{"mixed", "a\r\nb\rc", "a\nb\nc"},
		{"already-lf", "a\nb", "a\nb"},
	} {
		if got := domainterminal.NormalizePasteText(test.in); got != test.want {
			return selftest.Failed(fmt.Errorf("%s: normalized %q, want %q", test.name, got, test.want))
		}
		if lines := domainterminal.PasteLineCount(test.in); lines != 2 && test.name != "mixed" {
			return selftest.Failed(fmt.Errorf("%s: line count = %d, want 2", test.name, lines))
		}
	}

	var direct string
	gate := sessionui.NewPasteGate(
		func() (bool, int) { return false, domainterminal.DefaultPasteMinLines },
		func(text string) { direct = text },
	)
	if !gate.Request("first\r\nsecond\rmiddle") {
		return selftest.Failed(errors.New("an unprotected CRLF paste was held for confirmation"))
	}
	if direct != "first\nsecond\nmiddle" {
		return selftest.Failed(fmt.Errorf("direct paste sent %q, want one LF per line", direct))
	}

	var guarded string
	protected := sessionui.NewPasteGate(
		func() (bool, int) { return true, 2 },
		func(text string) { guarded = text },
	)
	if protected.Request("one\r\ntwo\rthree") {
		return selftest.Failed(errors.New("a protected multiline paste was sent without confirmation"))
	}
	pending, ok := protected.Pending()
	if !ok {
		return selftest.Failed(errors.New("the protected paste is not waiting"))
	}
	if pending.Text != "one\ntwo\nthree" || pending.Lines != 3 {
		return selftest.Failed(fmt.Errorf("waiting paste = %q (%d lines), want normalized text and 3 lines",
			pending.Text, pending.Lines))
	}
	if !protected.Accept() {
		return selftest.Failed(errors.New("Accept sent nothing"))
	}
	if strings.Count(guarded, "\r") != 0 || strings.Count(guarded, "\n") != 2 {
		return selftest.Failed(fmt.Errorf("accepted paste sent %q, want one LF per line and no CR", guarded))
	}
	return selftest.Passed(
		"CRLF, lone CR, and mixed line endings normalize once at the PasteGate boundary for direct and protected pastes",
		"ui/session.PasteGate")
}

// --- Host editor ----------------------------------------------------------

type selftest61Hosts struct {
	mu      sync.Mutex
	items   []domainvault.Host
	removed map[domainmodel.HostID]bool
	next    int
}

func newSelftest61Hosts(items ...domainvault.Host) *selftest61Hosts {
	return &selftest61Hosts{items: items, removed: make(map[domainmodel.HostID]bool)}
}

func (f *selftest61Hosts) List(context.Context) ([]domainvault.Host, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := make([]domainvault.Host, 0, len(f.items))
	for _, host := range f.items {
		if !f.removed[host.ID] {
			items = append(items, host)
		}
	}
	return items, nil
}

func (f *selftest61Hosts) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	host := domainvault.Host{ID: domainmodel.HostID(fmt.Sprintf("ticket-61-host-%d", f.next))}
	host.Label = draft.Label
	host.Hostname = draft.Hostname
	host.Port = draft.Port
	host.Protocol = draft.Protocol
	f.items = append(f.items, host)
	return host, nil
}

func (f *selftest61Hosts) Update(_ context.Context, id domainmodel.HostID, draft domainvault.Draft) (domainvault.Host, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed[id] {
		return domainvault.Host{}, appvault.ErrNotFound
	}
	for index := range f.items {
		if f.items[index].ID != id {
			continue
		}
		f.items[index].Label = draft.Label
		f.items[index].Hostname = draft.Hostname
		f.items[index].Port = draft.Port
		f.items[index].Protocol = draft.Protocol
		return f.items[index], nil
	}
	return domainvault.Host{}, appvault.ErrNotFound
}

func (f *selftest61Hosts) Delete(_ context.Context, id domainmodel.HostID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed[id] = true
	return nil
}

func (f *selftest61Hosts) remove(id domainmodel.HostID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed[id] = true
}

func (t *selftest61) hostEditorStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	hosts := newSelftest61Hosts(domainvault.Host{
		ID: "ticket-61-host", Label: "web-1", Hostname: "10.0.0.1",
		Port: 22, Protocol: domainvault.ProtocolSSH,
	})
	state := vaultstate.NewState(hosts)
	if err := state.Reload(); err != nil {
		return selftest.Failed(fmt.Errorf("reload the Vault: %w", err))
	}
	state.BeginCreate()
	tester := ui.NewTester(uivault.View(state), 1084, 661)
	tester.SetLocale("en")
	tester.Frame()

	if got := state.HostPortPlaceholder(); got != "22" {
		return selftest.Failed(fmt.Errorf("SSH Port hint = %q, want 22", got))
	}
	if !tester.HasText(m.VaultHostPortHint("22")) {
		return selftest.Failed(fmt.Errorf("the SSH Port hint is not rendered: %q", tester.Texts()))
	}
	for _, label := range []string{
		m.VaultHostAuthSection(), m.VaultHostIdentity(), m.VaultHostKey(),
		m.VaultHostAuthMethod(), m.VaultHostUseAgent(), m.VaultHostRequiresMFA(),
	} {
		if !tester.HasText(label) {
			return selftest.Failed(fmt.Errorf("an SSH Host does not render %q: %q", label, tester.Texts()))
		}
	}

	state.SetHostProtocol(string(domainvault.ProtocolTelnet))
	tester.Frame()
	if got := state.HostPortPlaceholder(); got != "23" {
		return selftest.Failed(fmt.Errorf("Telnet Port hint = %q, want 23", got))
	}
	if !tester.HasText(m.VaultHostPortHint("23")) {
		return selftest.Failed(fmt.Errorf("the Telnet Port hint is not rendered: %q", tester.Texts()))
	}
	for _, label := range []string{
		m.VaultHostAuthSection(), m.VaultHostIdentity(), m.VaultHostKey(),
		m.VaultHostAuthMethod(), m.VaultHostUseAgent(), m.VaultHostRequiresMFA(),
	} {
		if tester.HasText(label) {
			return selftest.Failed(fmt.Errorf("a Telnet Host still renders the SSH-only control %q: %q",
				label, tester.Texts()))
		}
	}
	return selftest.Passed(
		"the Host editor shows the effective SSH/Telnet Port hint and hides every SSH-only authentication control for Telnet",
		"ui/vault.View + vaultstate.SetHostProtocol")
}

// --- sidebar languages ----------------------------------------------------

func (t *selftest61) sidebarStep(*ui.Context) selftest.Outcome {
	shell := shellstate.NewState()
	defer shell.Shutdown()
	width, height := 704, 421
	tester := ui.NewTester(uishell.View(shell), width, height)
	tester.SetLocale("en")
	tester.Frame()
	for _, tag := range locales.Tags() {
		autonym := i18n.Autonym(tag)
		if _, err := selftest61Inside(tester, width, height, autonym); err != nil {
			return selftest.Failed(err)
		}
	}
	return selftest.Passed(
		"all five language options stay inside the 704x421 client",
		"ui/shell.View + ui.Tester")
}

// --- plurals and no-sample dash ------------------------------------------

func (t *selftest61) pluralsAndDashStep(*ui.Context) selftest.Outcome {
	type pluralCase struct {
		tag  string
		one  string
		many string
	}
	for _, test := range []pluralCase{
		{"en", "1 container", "2 containers"},
		{"es", "1 contenedor", "2 contenedores"},
		{"ru", "1 контейнер", "2 контейнера"},
		{"zh-CN", "1 个容器", "2 个容器"},
		{"zh-TW", "1 個容器", "2 個容器"},
	} {
		m := locales.In(test.tag)
		if got := m.SysManagerContainersCount(1); got != test.one {
			return selftest.Failed(fmt.Errorf("%s one container = %q, want %q", test.tag, got, test.one))
		}
		if got := m.SysManagerContainersCount(2); got != test.many {
			return selftest.Failed(fmt.Errorf("%s two containers = %q, want %q", test.tag, got, test.many))
		}
	}
	for _, test := range []struct {
		tag  string
		one  string
		many string
	}{
		{"en", "1 GPU", "2 GPUs"},
		{"es", "1 GPU", "2 GPU"},
		{"ru", "1 GPU", "2 GPU"},
		{"zh-CN", "1 块 GPU", "2 块 GPU"},
		{"zh-TW", "1 張 GPU", "2 張 GPU"},
	} {
		m := locales.In(test.tag)
		if got := m.SysManagerGPUCount(1); got != test.one {
			return selftest.Failed(fmt.Errorf("%s one GPU = %q, want %q", test.tag, got, test.one))
		}
		if got := m.SysManagerGPUCount(2); got != test.many {
			return selftest.Failed(fmt.Errorf("%s two GPUs = %q, want %q", test.tag, got, test.many))
		}
	}

	// The monitoring list draws a dash for every headline metric of a Host
	// whose first sample has not arrived, never a zero that would look
	// healthy.
	m := locales.In("en")
	now := time.Now()
	hosts := &selftest61MonitorHosts{items: []domainvault.Host{
		{ID: "ticket-61-nosample", Label: "Beta", Hostname: "beta.selftest"},
	}}
	prefs := &selftest61Prefs{}
	prefs.settings.Monitor = domainpreferences.NormalizeMonitor(domainpreferences.Monitor{
		Panels: []domainpreferences.MonitorPanel{{
			ID: "ticket-61", Name: "Operations",
			HostIDs:  []string{"ticket-61-nosample"},
			View:     domainpreferences.MonitorViewList,
			Interval: 5, Range: 30, Sort: domainpreferences.MonitorSortManual,
		}},
	})
	service := &selftest61MonitorService{snapshot: domainmonitor.Snapshot{
		TakenAt: now,
		Hosts: map[string]domainmonitor.HostState{
			"ticket-61-nosample": {
				HostID: "ticket-61-nosample", Status: domainmonitor.StatusUnsupported,
				Reason: domainmonitor.ReasonOS, At: now,
			},
		},
		Running:   map[string]bool{"ticket-61": true},
		StartedAt: map[string]time.Time{"ticket-61": now.Add(-time.Minute)},
	}}
	monitor := uimonitoring.NewState(service, hosts, prefs, nil,
		uimonitoring.WithClock(func() time.Time { return now }))
	defer monitor.Close()
	monitor.Activate()
	defer monitor.Deactivate()
	shell := shellstate.NewState(shellstate.WithMonitor(uimonitoring.View(monitor), nil, nil))
	shell.ShowMonitor()
	defer shell.Shutdown()

	for _, size := range [][2]int{{704, 421}, {1084, 661}} {
		// The shell pressure proves the typed status stays reachable beside
		// the Host at the minimum client, where the optional metric columns
		// sit horizontally off-screen by design.
		tester := ui.NewTester(uishell.View(shell), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		for _, text := range []string{
			m.MonitoringTitle(), m.MonitoringStatusUnsupported(), m.MonitoringReasonOS(),
		} {
			if _, err := selftest61Inside(tester, size[0], size[1], text); err != nil {
				return selftest.Failed(err)
			}
		}

		// A full-width rendering makes the metric columns reachable without
		// horizontal scrolling, so the dash and the absence of healthy zeros
		// are read directly.
		metrics := ui.NewTester(uimonitoring.View(monitor), size[0], size[1])
		metrics.SetLocale("en")
		metrics.Frame()
		for _, text := range []string{
			m.MonitoringCpu(),
			hosts.items[0].Hostname + " · " + m.MonitoringGpu() + " —",
		} {
			if _, err := selftest61Inside(metrics, size[0], size[1], text); err != nil {
				return selftest.Failed(err)
			}
		}
		for _, zero := range []string{"0%", "0 ms", "0 B/s"} {
			if metrics.HasText(zero) {
				return selftest.Failed(fmt.Errorf("%dx%d renders %q for a Host with no sample: %q",
					size[0], size[1], zero, metrics.Texts()))
			}
		}
	}
	return selftest.Passed(
		"every locale uses the singular for one container/GPU, and a Host without a sample shows dashes at both client sizes",
		"ui/locales ICU catalogs + ui/monitoring.View")
}

type selftest61MonitorHosts struct{ items []domainvault.Host }

func (h *selftest61MonitorHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), h.items...), nil
}

type selftest61Prefs struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
}

func (p *selftest61Prefs) Settings() domainpreferences.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *selftest61Prefs) Update(change func(*domainpreferences.Settings)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(&p.settings)
	p.settings.Monitor = domainpreferences.NormalizeMonitor(p.settings.Monitor)
	return nil
}

type selftest61MonitorService struct {
	mu       sync.Mutex
	snapshot domainmonitor.Snapshot
}

func (s *selftest61MonitorService) Start(string, []domainvault.Host, time.Duration) error {
	return nil
}
func (s *selftest61MonitorService) Stop(string) {}
func (s *selftest61MonitorService) StopAll()    {}
func (s *selftest61MonitorService) Subscribe(func()) func() {
	return func() {}
}
func (s *selftest61MonitorService) Snapshot() domainmonitor.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snapshot
	out.Hosts = make(map[string]domainmonitor.HostState, len(s.snapshot.Hosts))
	for id, host := range s.snapshot.Hosts {
		out.Hosts[id] = host
	}
	return out
}

// --- pinned editors -------------------------------------------------------

func (t *selftest61) pinnedEditorsStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	hosts := newSelftest61Hosts(domainvault.Host{
		ID: "ticket-61-host", Label: "web-1", Hostname: "10.0.0.1",
		Port: 22, Protocol: domainvault.ProtocolSSH,
	})
	vault := vaultstate.NewState(hosts)
	if err := vault.Reload(); err != nil {
		return selftest.Failed(fmt.Errorf("reload the Vault: %w", err))
	}
	ruleHosts := make([]domainvault.Host, 0, 12)
	for index := 0; index < 12; index++ {
		ruleHosts = append(ruleHosts, domainvault.Host{
			ID:    domainmodel.HostID(fmt.Sprintf("ticket-61-bastion-%02d", index)),
			Label: fmt.Sprintf("bastion-%02d", index),
		})
	}
	fwdService := &selftest61ForwardingService{}
	fwdHosts := &selftest61MonitorHosts{items: ruleHosts}
	forwardingState := forwarding.NewState(fwdService, fwdHosts)
	defer forwardingState.Close()
	if err := forwardingState.Reload(); err != nil {
		return selftest.Failed(fmt.Errorf("reload forwarding: %w", err))
	}

	shell, err := newSelftest61Shell(1,
		shellstate.WithVault(uivault.View(vault, uivault.WithForwarding(forwardingState))))
	if err != nil {
		return selftest.Failed(err)
	}
	defer shell.close()
	shell.state.ShowVault()

	// The Vault's save failure is pinned above the actions. It is shown for a
	// Host deleted from storage while its editor is open.
	if !vault.BeginEdit("ticket-61-host") {
		return selftest.Failed(errors.New("the Host editor did not open"))
	}
	hosts.remove("ticket-61-host")
	if err := vault.Save(); err != nil {
		return selftest.Failed(fmt.Errorf("save the deleted Host: %w", err))
	}
	for _, size := range [][2]int{{704, 421}, {1084, 661}} {
		tester := ui.NewTester(uishell.View(shell.state), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		save, err := selftest61Inside(tester, size[0], size[1], m.VaultHostSave())
		if err != nil {
			return selftest.Failed(err)
		}
		if _, err := selftest61Inside(tester, size[0], size[1], m.VaultHostCancel()); err != nil {
			return selftest.Failed(err)
		}
		problem, err := selftest61Inside(tester, size[0], size[1], m.VaultProblemNotFound())
		if err != nil {
			return selftest.Failed(err)
		}
		if problem.Y+problem.H > save.Y+0.5 {
			return selftest.Failed(fmt.Errorf(
				"%dx%d: the not-found problem is below the pinned actions: problem %+v save %+v",
				size[0], size[1], problem, save))
		}
	}
	vault.CancelEdit()

	// The forwarding editor keeps Save, Cancel, and its problem pinned; its
	// last form row scrolls fully above them at the minimum client.
	forwardingState.BeginCreate()
	form := forwardingState.Form()
	form.Type = string(domainforward.TypeLocal)
	form.Label = "web"
	form.HostID = string(ruleHosts[0].ID)
	form.BindAddress = "127.0.0.1"
	form.LocalPort = "not-a-port"
	form.RemoteHost = "localhost"
	form.RemotePort = "80"
	form.Enabled = true
	if err := forwardingState.Save(); err != nil {
		return selftest.Failed(fmt.Errorf("save the invalid forwarding rule: %w", err))
	}
	tester := ui.NewTester(uishell.View(shell.state), 704, 421)
	tester.SetLocale("en")
	tester.Frame()
	save, err := selftest61Inside(tester, 704, 421, m.VaultHostSave())
	if err != nil {
		return selftest.Failed(err)
	}
	if _, err := selftest61Inside(tester, 704, 421, m.VaultHostCancel()); err != nil {
		return selftest.Failed(err)
	}
	problem, err := selftest61Inside(tester, 704, 421, m.VaultForwardingProblemLocalPortInvalid())
	if err != nil {
		return selftest.Failed(err)
	}
	if problem.Y+problem.H > save.Y+0.5 {
		return selftest.Failed(fmt.Errorf(
			"the forwarding problem is below the pinned actions: problem %+v save %+v", problem, save))
	}
	tester.Scroll(352, 180, 0, 4000)
	tester.Frame()
	enabled, ok := tester.Find(m.VaultForwardingEnabled())
	if !ok || enabled.W <= 0 || enabled.H <= 0 {
		return selftest.Failed(fmt.Errorf("the last forwarding row is unreachable by scrolling: %+v (%v)",
			enabled, ok))
	}
	if enabled.Y < 0 || enabled.Y+enabled.H > save.Y+0.5 {
		return selftest.Failed(fmt.Errorf(
			"the last forwarding row did not clear the pinned actions: row %+v save %+v", enabled, save))
	}
	problem, err = selftest61Inside(tester, 704, 421, m.VaultForwardingProblemLocalPortInvalid())
	if err != nil {
		return selftest.Failed(err)
	}
	if problem.Y+problem.H > save.Y+0.5 {
		return selftest.Failed(fmt.Errorf(
			"scrolling moved the forwarding problem under the actions: problem %+v save %+v", problem, save))
	}
	return selftest.Passed(
		"the Vault not-found problem and the forwarding problem stay above pinned Save/Cancel at 704x421 and 1084x661; the last forwarding row scrolls above the bar",
		"ui/shell.View + ui/vault.View + ui/forwarding.View")
}

type selftest61ForwardingService struct{}

func (selftest61ForwardingService) List(context.Context) ([]appforwarding.View, error) {
	return nil, nil
}

func (selftest61ForwardingService) Create(context.Context, domainforward.Draft) (domainforward.Rule, error) {
	return domainforward.Rule{}, nil
}

func (selftest61ForwardingService) Update(context.Context, domainmodel.ForwardingRuleID, domainforward.Draft) (domainforward.Rule, error) {
	return domainforward.Rule{}, nil
}

func (selftest61ForwardingService) Delete(context.Context, domainmodel.ForwardingRuleID) error {
	return nil
}

func (selftest61ForwardingService) Start(context.Context, domainmodel.ForwardingRuleID) error {
	return nil
}

func (selftest61ForwardingService) Stop(context.Context, domainmodel.ForwardingRuleID) error {
	return nil
}

func (selftest61ForwardingService) Snapshot() appforwarding.Snapshot {
	return appforwarding.Snapshot{}
}

func (selftest61ForwardingService) Subscribe(func(appforwarding.Event)) func() {
	return func() {}
}

// --- process isolation and contrast --------------------------------------

func (t *selftest61) isolationAndContrastStep(*ui.Context) selftest.Outcome {
	dir := selftest.DefaultResultDir()
	wantSuffix := fmt.Sprintf("natcatty-selftest-%d", os.Getpid())
	if !strings.HasSuffix(dir, wantSuffix) {
		return selftest.Failed(fmt.Errorf(
			"the default result directory %q does not end in the process id suffix %q", dir, wantSuffix))
	}
	if selftestResultDir != dir {
		return selftest.Failed(fmt.Errorf(
			"the app result directory %q is not the process-private default %q", selftestResultDir, dir))
	}

	snow, ok := domaintheme.LookupPreset(false, "snow")
	if !ok {
		return selftest.Failed(errors.New("the Snow preset is missing"))
	}
	midnight, ok := domaintheme.LookupPreset(true, "midnight")
	if !ok {
		return selftest.Failed(errors.New("the Midnight preset is missing"))
	}
	light := uitheme.UI(false, snow, uitheme.Options{})
	dark := uitheme.UI(true, midnight, uitheme.Options{})
	if got := uitheme.DangerText(light); got != ui.Hex("#b51616") {
		return selftest.Failed(fmt.Errorf("light danger text = %+v, want #b51616", got))
	}
	if got := uitheme.DangerText(dark); got != ui.Hex("#ff6b6b") {
		return selftest.Failed(fmt.Errorf("dark danger text = %+v, want #ff6b6b", got))
	}
	if ratio := selftest61Contrast(ui.Hex("#b51616"), ui.Hex("#f1f4f8")); ratio < 6.15 || ratio > 6.17 {
		return selftest.Failed(fmt.Errorf("light danger on Snow = %.3f:1, want the pinned 6.158:1", ratio))
	}
	if ratio := selftest61Contrast(ui.Hex("#ff6b6b"), dark.Background); ratio < 6.45 || ratio > 6.49 {
		return selftest.Failed(fmt.Errorf("dark danger on Midnight = %.3f:1, want the pinned 6.47:1", ratio))
	}

	for _, appearance := range []struct {
		name    string
		dark    bool
		presets []domaintheme.Preset
	}{
		{"light", false, domaintheme.LightPresets()},
		{"dark", true, domaintheme.DarkPresets()},
	} {
		for _, preset := range appearance.presets {
			theme := uitheme.UI(appearance.dark, preset, uitheme.Options{})
			for _, test := range []struct {
				name  string
				color ui.Color
			}{
				{"danger", uitheme.DangerText(theme)},
				{"warning", uitheme.WarningText(theme)},
			} {
				for _, background := range []struct {
					name  string
					color ui.Color
				}{
					{"background", theme.Background},
					{"surface", theme.Surface},
				} {
					if ratio := selftest61Contrast(test.color, background.color); ratio < uitheme.MinimumTextContrast {
						return selftest.Failed(fmt.Errorf(
							"%s preset %s: %s text on %s = %.2f:1, want at least %.1f:1",
							appearance.name, preset.ID, test.name, background.name,
							ratio, uitheme.MinimumTextContrast))
					}
				}
			}
		}
	}
	return selftest.Passed(
		fmt.Sprintf("the result directory is private to PID %d and danger/warning text clears %.1f:1 on every preset",
			os.Getpid(), uitheme.MinimumTextContrast),
		"selftest.DefaultResultDir + ui/theme helpers")
}

func selftest61Contrast(a, b ui.Color) float64 {
	la, ok := domaintheme.HexLuminance(fmt.Sprintf("#%02x%02x%02x", a.R, a.G, a.B))
	if !ok {
		return 0
	}
	lb, ok := domaintheme.HexLuminance(fmt.Sprintf("#%02x%02x%02x", b.R, b.G, b.B))
	if !ok {
		return 0
	}
	return domaintheme.ContrastRatio(la, lb)
}
