// Package phoneshell is natcatty's phone navigation: four sections —
// Sessions, Hosts, Vault and Settings — shown as the system's tab bar, each
// with its own back stack.
//
// The sections are navigation, not Workspace Tabs: a Workspace Tab is an open
// terminal Session, and the phone keeps one per Tab. Nothing here connects;
// the pages are placeholders until the Session and Vault pages land.
package phoneshell

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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

// page is a placeholder page of a section.
type page struct {
	// path is the router location, as "/keys".
	path string
	// title is the page's navigation title.
	title string
	// text is what the placeholder says.
	text string
}

// section is one navigation section: its identity, its pages, and the router
// holding its back stack. The router survives section switches, which is what
// returns the user to where they left off.
type section struct {
	info   Info
	router *ui.Router
	pages  []page
	intro  string
}

// Shell is the four sections of the phone app. Construct it with New; its
// zero value is not usable.
type Shell struct {
	sections [sectionCount]*section
	selected Section
	windows  [sectionCount]*mygo.Window
	tabs     *mygo.Tabs
}

// New returns the phone shell with every section at its root page.
func New() *Shell {
	s := &Shell{}
	for i := range s.sections {
		s.sections[i] = newSection(Section(i))
	}
	return s
}

func newSection(i Section) *section {
	s := &section{info: infos[i], router: ui.NewRouter("/")}
	switch i {
	case Sessions:
		s.intro = "No sessions yet."
		s.pages = []page{
			{path: "/new", title: "New session", text: "New session is a placeholder page."},
			{path: "/log", title: "Connection log", text: "Connection log is a placeholder page."},
		}
		s.pages = append(s.pages, smokePages(i)...)
	case Hosts:
		s.intro = "No hosts yet."
		s.pages = []page{
			{path: "/host/new", title: "New host", text: "New host is a placeholder page."},
			{path: "/browse", title: "Browse hosts", text: "Browse hosts is a placeholder page."},
		}
	case Vault:
		s.intro = "Browse the Vault on the phone."
		s.pages = []page{
			{path: "/keys", title: "SSH keys", text: "SSH keys is a placeholder page."},
			{path: "/known-hosts", title: "Known hosts", text: "Known hosts is a placeholder page."},
		}
	case Settings:
		s.intro = "Phone settings."
		s.pages = []page{
			{path: "/appearance", title: "Appearance", text: "Appearance is a placeholder page."},
			{path: "/terminal", title: "Terminal", text: "Terminal is a placeholder page."},
			{path: "/input", title: "Input", text: "Input is a placeholder page."},
			{path: "/security", title: "Security", text: "Security is a placeholder page."},
		}
	}
	return s
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

// View returns the view of one section, for the window of its tab. A section
// is its own view rather than the shell's so that each tab's window renders
// the section it holds.
func (s *Shell) View(sec Section) func(c *ui.Context) { return s.sections[sec].view }

// view draws the section: the system's navigation bar on phones, and a back
// bar of its own where there is no system stack (the desktop tester).
func (sec *section) view(c *ui.Context) {
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
		for _, p := range sec.pages {
			if r.Match(p.path) {
				r.Title(p.title)
				sec.placeholder(c, p)
				return
			}
		}
		r.Title("Not found")
		ui.Text(c, "Not found").Padding(16)
	}
}

// root lists the section's pages. Every row routes instead of connecting. The
// heading comes from the navigation bar (the system's on phones, the view's
// own elsewhere), so the page itself starts with the section's introduction.
func (sec *section) root(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(10).Children(func() {
			ui.Text(c, sec.intro).TextColor(t.TextMuted)
			for _, p := range sec.pages {
				row := ui.Row(c.Key(p.path)).FillWidth().Padding(14, 16).Gap(12).
					AlignItems(ui.Center).Background(t.Surface).Radius(12).Label(p.title)
				row.Children(func() {
					ui.Text(c, p.title).Grow(1)
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
