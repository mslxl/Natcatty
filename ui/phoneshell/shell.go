// Package phoneshell is natcatty's phone navigation: four sections —
// Sessions, Hosts, Vault and Settings — shown as the system's tab bar, each
// with its own back stack.
//
// The sections are navigation, not Workspace Tabs: a Workspace Tab is an open
// terminal Session, and the phone keeps one per Tab. The Sessions and Hosts
// sections draw their own roots and push one page per open Session or Host;
// the Vault and Settings pages read the same Vault store and the same settings
// documents the desktop uses.
package phoneshell

import (
	"context"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Section identifies one of the phone's four navigation sections.
type Section int

const (
	Sessions Section = iota
	Hosts
	Vault
	Settings
	sectionCount
)

// Info is a section's title and its SF Symbol. The symbol names the tab on
// iOS; Android needs an icon as well, which the Android shell supplies.
type Info struct {
	Title  string
	Symbol string
}

var infos = [sectionCount]Info{
	Sessions: {Title: "Sessions", Symbol: "rectangle.stack"},
	Hosts:    {Title: "Hosts", Symbol: "server.rack"},
	Vault:    {Title: "Vault", Symbol: "key"},
	Settings: {Title: "Settings", Symbol: "gearshape"},
}

// page is one page of a section.
type page struct {
	// path is the router location, as "/keys".
	path string
	// title is the page's navigation title.
	title string
	// text is what a placeholder page says. Real pages leave it empty.
	text string
	// summary is the root row's second line, such as a list count. It is
	// evaluated while the root renders, so it can reflect a fresh load.
	summary func() string
}

// section is one navigation section: its identity, its pages, and the router
// holding its back stack. The router survives section switches, which is what
// returns the user to where they left off.
type section struct {
	info   Info
	router *ui.Router
	pages  []page
	intro  string
	// draw renders a non-placeholder page. A nil draw falls back to text.
	draw func(c *ui.Context, p page)
	// rootDraw renders the section's root page instead of the list of pages.
	// Sessions and Hosts own their roots this way.
	rootDraw func(c *ui.Context)
	// route renders a dynamic page, such as /session/{id}. It reports whether
	// it matched the current path.
	route func(c *ui.Context, r *ui.Route) bool
	// before runs before the section draws a frame. Every section uses the
	// same hook — the Shell's lifecycle rule — so navigation cannot bypass it.
	before func()
}

// Shell is the four sections of the phone app. Construct it with New; its
// zero value is not usable.
type Shell struct {
	sections [sectionCount]*section
	selected Section
	windows  [sectionCount]*mygo.Window
	tabs     *mygo.Tabs

	vault    *vaultPages
	settings *settingsPages
	hosts    *hostPages
	sessions *sessionTabs
	// backgrounded suppresses the per-frame lifecycle rule while the app is
	// not in the foreground. Background applies the rule once (releasing the
	// view); a frame that still arrives must not reattach it before Foreground
	// resumes the Session deliberately.
	backgrounded bool
}

// Option customizes a Shell. The composition root passes the shared Vault
// and settings services; a Shell without them still navigates, and each page
// says the data is unavailable instead of drawing blank.
type Option func(*Shell)

// WithVault gives the Vault pages the shared SSH Key and Known Host store.
func WithVault(store VaultStore) Option {
	return func(s *Shell) { s.vault.store = store }
}

// WithSettings gives the Settings pages the shared preferences and terminal
// behavior services.
func WithSettings(service SettingsService) Option {
	return func(s *Shell) { s.settings.service = service }
}

// WithHistorySync gives the Settings pages the phone's Command History sync
// account and engine. Without it the History sync page says it is unavailable;
// no other page is affected.
func WithHistorySync(service HistorySyncService) Option {
	return func(s *Shell) { s.settings.history = service }
}

// WithHosts gives the Hosts section the shared Vault's Host list. Only remote
// Hosts are listed; Local and serial records stay on the desktop.
func WithHosts(store HostStore) Option {
	return func(s *Shell) { s.hosts.store = store }
}

// WithSessionOpener gives the Sessions section the application Session
// service. Without it, tapping a Host shows an actionable error instead of
// opening a Session.
func WithSessionOpener(opener SessionOpener) Option {
	return func(s *Shell) { s.sessions.opener = opener }
}

// WithPrompts gives the Sessions section the trust and credential prompt
// queues the application's Connection Manager blocks on.
func WithPrompts(prompts *Prompts) Option {
	return func(s *Shell) { s.sessions.prompts = prompts }
}

// WithScreenFactory replaces the libghostty-vt terminal screen. Tests inject a
// fake because ordinary `go test` runs have no native library.
func WithScreenFactory(factory ScreenFactory) Option {
	return func(s *Shell) { s.sessions.newScreen = factory }
}

// WithSessionStore persists the open Tabs in the shared Workspace restore
// format, so a relaunch lists the Tabs the previous run left open as dormant
// entries. Without it the phone still works but forgets its Tabs at exit.
func WithSessionStore(store SessionStore) Option {
	return func(s *Shell) { s.sessions.store = store }
}

// New returns the phone shell with every section at its root page.
func New(opts ...Option) *Shell {
	s := &Shell{
		vault:    newVaultPages(),
		settings: newSettingsPages(),
		hosts:    newHostPages(),
		sessions: &sessionTabs{newScreen: newTerminalScreen},
	}
	for _, opt := range opts {
		opt(s)
	}
	s.hosts.open = s.openHost
	s.hosts.details = func(host domainvault.Host) {
		s.sections[Hosts].router.Push("/host/" + string(host.ID))
	}
	s.sessions.launch = s.launchTab
	s.sessions.show = func(id string) {
		s.sections[Sessions].router.Push("/session/" + id)
	}
	s.sessions.close = s.closeSession
	s.sessions.confirmClose = s.confirmClosingSessions
	for i := range s.sections {
		s.sections[i] = s.newSection(Section(i))
	}
	return s
}

// confirmClosingSessions reads the Security preference live. Without a
// settings service the phone keeps the product default: confirm a close while
// the Session is live.
func (s *Shell) confirmClosingSessions() bool {
	if s.settings == nil || s.settings.service == nil {
		return true
	}
	return s.settings.service.Preferences().Security.ConfirmCloseSessions
}

// syncLiveView derives the visible Session from the current route and applies
// the lifecycle: that Tab is attached and resumed, every other Tab hibernates.
// It is the only place the rule is applied, so back-navigation, section
// switches, backgrounding, and deep links cannot each invent their own rule.
// It runs once per frame, before the section draws.
func (s *Shell) syncLiveView() {
	if s.backgrounded {
		return
	}
	s.sessions.syncVisible(s.visibleSession())
}

// visibleSession is the Session the current route shows: the top page behind
// /session/{id}, and only while the Sessions section is selected. A page in
// another section, or behind a pushed page in Sessions, shows no terminal.
func (s *Shell) visibleSession() string {
	if s.selected != Sessions {
		return ""
	}
	// A deep link may carry a trailing slash; the route still names the page.
	path := strings.TrimSuffix(s.sections[Sessions].router.Path(), "/")
	id, ok := strings.CutPrefix(path, "/session/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func (s *Shell) newSection(i Section) *section {
	sec := &section{info: infos[i], router: ui.NewRouter("/"), before: s.syncLiveView}
	switch i {
	case Sessions:
		sec.rootDraw = s.sessions.drawRoot
		sec.route = func(c *ui.Context, r *ui.Route) bool {
			if !r.Match("/session/{id}") {
				return false
			}
			id := r.Param("id")
			r.Title("Session")
			s.sessions.drawSession(c, id,
				func() { s.sessions.retry(id) },
				func() { s.leaveSession(id) })
			return true
		}
		s.pages = append(s.pages, smokePages(i)...)
	case Hosts:
		sec.rootDraw = s.hosts.drawRoot
		sec.route = func(c *ui.Context, r *ui.Route) bool {
			if !r.Match("/host/{id}") {
				return false
			}
			r.Title("Host")
			s.hosts.drawDetail(c, r.Param("id"))
			return true
		}
	case Vault:
		sec.intro = "Browse the Vault on the phone."
		sec.pages = []page{
			{path: "/keys", title: "SSH keys", summary: s.vault.keysSummary},
			{path: "/known-hosts", title: "Known hosts", summary: s.vault.knownHostsSummary},
		}
		sec.draw = s.vault.draw
	case Settings:
		sec.intro = "Settings for this phone."
		sec.pages = []page{
			{path: "/appearance", title: "Appearance", summary: staticSummary("Theme, interface, icons")},
			{path: "/terminal", title: "Terminal", summary: staticSummary("Clipboard and paste")},
			{path: "/input", title: "Input", summary: staticSummary("Completion and suggestions")},
			{path: "/security", title: "Security", summary: staticSummary("Confirmations")},
			{path: "/history", title: "History sync", summary: staticSummary("Atuin account and sync")},
		}
		sec.draw = s.settings.draw
	}
	return sec
}

func staticSummary(text string) func() string {
	return func() string { return text }
}

// pageContentWidth is the width a padded page body may use. Width is the
// content box, so the horizontal padding comes out of the window's width; a
// full-window column would otherwise lay its right edge past the screen.
func pageContentWidth(c *ui.Context) float32 {
	width, _ := c.Size()
	const padding = 32
	if width > padding {
		return width - padding
	}
	return width
}

// Info returns a section's title and symbol.
func (s *Shell) Info(sec Section) Info { return s.sections[sec].info }

// Router returns a section's router: its back stack.
func (s *Shell) Router(sec Section) *ui.Router { return s.sections[sec].router }

// Selected returns the section the tab bar shows.
func (s *Shell) Selected() Section { return s.selected }

// Select chooses a section, as the tab bar does when the user taps it. Each
// section keeps its own router, so selecting another leaves the first's stack
// where it was. Before Show it only records the choice; after it, it also
// chooses the native tab, so code that moves the user between sections (a
// connect entry point opening Sessions) changes what is on screen.
func (s *Shell) Select(sec Section) {
	if sec < 0 || sec >= sectionCount {
		return
	}
	s.selected = sec
	if s.tabs != nil {
		s.tabs.Select(int(sec))
	}
}

// Load reads the Vault lists the shell shows and the Hosts it can open, then
// adds the Tabs a previous launch left open as dormant entries. The phone root
// calls it before the first frame; a failure is recorded on the pages as their
// error state, so the shell still opens. It is safe to call again to refresh:
// the persisted Tabs are adopted once.
func (s *Shell) Load(ctx context.Context) error {
	vaultErr := s.vault.load(ctx)
	hostErr := s.hosts.load(ctx)
	// Restore even when the Host list failed: a dormant entry built from its
	// descriptor is still identifiable, and its restore reports the failure.
	s.restoreSessions(ctx)
	if hostErr != nil {
		return hostErr
	}
	return vaultErr
}

// restoreSessions turns the persisted Workspace payload into dormant Tabs. It
// runs once per Shell and ignores a storage failure: a broken row must not
// stop the app from starting, exactly as on the desktop.
func (s *Shell) restoreSessions(ctx context.Context) {
	if s.sessions.store == nil || s.sessions.restored {
		return
	}
	s.sessions.restored = true
	payload, err := s.sessions.store.Load(ctx)
	if err != nil {
		return
	}
	s.sessions.adoptRestored(FlattenRestored(payload), s.hosts.host)
}

// Background is the app-lifecycle hook for leaving the foreground. It releases
// the visible terminal view and persists the open Tabs, but never ends a
// Session: each transport and its application state stay alive, so Foreground
// can resume them. The screen is the only thing given up, which is what lets
// the OS suspend the app without the Sessions noticing.
func (s *Shell) Background() {
	s.backgrounded = true
	s.sessions.syncVisible("")
	s.sessions.persist()
}

// Foreground is the app-lifecycle hook for returning to the foreground. It
// resumes the Session the route makes visible, with no user action. A Session
// whose transport died while suspended is reopened under the same Tab; a Tab
// whose reconnect then fails stays reconnecting and retryable.
func (s *Shell) Foreground() {
	s.backgrounded = false
	visible := s.visibleSession()
	s.sessions.syncVisible(visible)
	s.sessions.reconnectVisible(visible)
}

// Close ends every open Session. The phone root calls it on quit.
func (s *Shell) Close() { s.sessions.closeAll() }

// openHost opens one Host as a Session Tab, switches to Sessions, and pushes
// the terminal page there. The Hosts section's router is untouched, so
// selecting Hosts again returns to the page the user left.
func (s *Shell) openHost(host domainvault.Host) {
	tab := s.sessions.begin(host)
	s.Select(Sessions)
	s.sections[Sessions].router.Push("/session/" + tab.id)
	s.launchTab(tab)
}

// launchTab opens one Tab off the UI thread. The Session service dials, so the
// frame that handled the tap is not held while a network connect runs. The
// dial context is tagged with the Tab's identity, so a host-key or credential
// prompt is queued for this Tab, and cancelling the Tab aborts the dial
// including any prompt it is waiting on.
func (s *Shell) launchTab(tab *phoneTab) {
	opener := s.sessions.opener
	if opener == nil {
		s.sessions.finish(tab.id, nil, errNoOpener)
		return
	}
	ctx, cancel := context.WithCancel(withPromptOwner(context.Background(), tab.id))
	s.sessions.attachCancel(tab.id, cancel)
	host := tab.host
	go func() {
		handle, err := opener.OpenHost(ctx, host)
		s.sessions.finish(tab.id, handle, err)
	}()
}

// closeSession ends one Tab and leaves its page if that page is showing.
func (s *Shell) closeSession(id string) {
	if s.visibleSession() == id {
		s.sections[Sessions].router.Back()
	}
	s.sessions.remove(id)
}

// leaveSession backs out of one Session's page without closing the Session.
// A Tab whose open failed is not a Session, so its error page is discarded; a
// reconnecting Tab stays and remains retryable.
func (s *Shell) leaveSession(id string) {
	if s.visibleSession() == id {
		s.sections[Sessions].router.Back()
	}
	s.sessions.discardFailed(id)
}

// View returns the view of one section, for the window of its tab. A section
// is its own view rather than the shell's so that each tab's window renders
// the section it holds.
func (s *Shell) View(sec Section) func(c *ui.Context) { return s.sections[sec].view }

// view draws the section: the system's navigation bar on phones, and a back
// bar of its own where there is no system stack (the desktop tester).
func (sec *section) view(c *ui.Context) {
	// Apply the Session lifecycle before anything draws: the route that is
	// about to render decides which Session owns the one live terminal view.
	if sec.before != nil {
		sec.before()
	}
	// A section's router is the window's first, so its history is the
	// system's stack of screens. On devices this draws the system bar; in
	// the tester it is a no-op and the view draws its own bar below.
	sec.router.SystemStack = true
	ui.Column(c).Fill().Children(func() {
		if !sec.router.InSystemStack(c) {
			t := c.Theme()
			bar := ui.Row(c).FillWidth().Shrink(0).Padding(8, 12).Gap(8).AlignItems(ui.Center)
			bar.Background(t.Background).Children(func() {
				ui.BackButton(c, sec.router)
				ui.Text(c, sec.router.Title()).Bold()
			})
		}
		sec.router.View(c, func(r *ui.Route) { sec.page(c, r) })
	})
}

// page routes the section's path. Route.Title names the system's navigation
// bar on phones; the back bar shows it elsewhere.
func (sec *section) page(c *ui.Context, r *ui.Route) {
	r.Title(sec.info.Title)
	// A smoke build adds the terminal diagnostic; every other build has
	// no such route.
	if smokeRoute(c, r) {
		return
	}
	switch {
	case r.Match("/"):
		sec.root(c)
	default:
		if sec.route != nil && sec.route(c, r) {
			return
		}
		for _, p := range sec.pages {
			if r.Match(p.path) {
				r.Title(p.title)
				sec.drawPage(c, p)
				return
			}
		}
		r.Title("Not found")
		ui.Text(c, "Not found").Padding(16)
	}
}

// drawPage renders a page: the Vault and Settings sections own their pages,
// while the sections whose tickets have not landed keep their placeholder.
func (sec *section) drawPage(c *ui.Context, p page) {
	if sec.draw != nil && p.text == "" {
		sec.draw(c, p)
		return
	}
	sec.placeholder(c, p)
}

// root lists the section's pages. Every row routes instead of connecting. The
// heading comes from the navigation bar (the system's on phones, the view's
// own elsewhere), so the page itself starts with the section's introduction.
func (sec *section) root(c *ui.Context) {
	if sec.rootDraw != nil {
		sec.rootDraw(c)
		return
	}
	t := c.Theme()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(10).Children(func() {
			ui.Text(c, sec.intro).TextColor(t.TextMuted)
			for _, p := range sec.pages {
				row := ui.Row(c.Key(p.path)).FillWidth().Padding(14, 16).Gap(12).
					AlignItems(ui.Center).Background(t.Surface).Radius(12).Label(p.title)
				row.Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, p.title)
						if p.summary != nil {
							if summary := p.summary(); summary != "" {
								ui.Text(c, summary).FontSize(12).TextColor(t.TextMuted).SingleLine()
							}
						}
					})
					ui.Text(c, "›").FontSize(20).TextColor(t.TextMuted)
				})
				if row.Clicked() {
					sec.router.Push(p.path)
				}
			}
		})
	})
}

// placeholder is a page not built yet. Its title is the navigation bar's.
func (sec *section) placeholder(c *ui.Context, p page) {
	t := c.Theme()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(10).Children(func() {
			ui.Text(c, p.text).TextColor(t.TextMuted)
		})
	})
}
