package phoneshell

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func texts(t *testing.T, tt *ui.Tester) []string {
	t.Helper()
	return tt.Texts()
}

func hasText(tt *ui.Tester, want string) bool {
	return slices.Contains(tt.Texts(), want)
}

// TestSectionsRouteAndPop drives every section through its pages with the
// framework's tester: a row routes to the page, the page renders, and the back
// button returns to the section's root with its state intact. A section whose
// real page has landed is checked for its own content; a section still on
// placeholders is checked for the placeholder text.
func TestSectionsRouteAndPop(t *testing.T) {
	for _, sec := range []Section{Sessions, Hosts, Vault, Settings} {
		shell := New()
		// The Vault pages start in their loading state; Load turns a shell
		// without a store into the unavailable state this test checks.
		_ = shell.Load(context.Background())
		tt := ui.NewTester(shell.View(sec), 390, 844)
		if !hasText(tt, shell.Info(sec).Title) {
			t.Fatalf("section %d: root does not show %q; texts: %v", sec, shell.Info(sec).Title, texts(t, tt))
		}
		pages := shell.sections[sec].pages
		if len(pages) == 0 {
			// Sessions and Hosts draw their roots directly instead of listing
			// placeholder pages; each has its own test for the real surface.
			switch sec {
			case Sessions:
				if !hasText(tt, "No sessions yet. Open a Host from the Hosts tab.") {
					t.Fatalf("Sessions root lacks its empty state; texts: %v", texts(t, tt))
				}
			case Hosts:
				if !hasText(tt, "The Vault is not available on this device.") {
					t.Fatalf("Hosts root lacks its unavailable state; texts: %v", texts(t, tt))
				}
			}
			continue
		}
		for _, p := range pages {
			if err := tt.Click(p.title); err != nil {
				t.Fatalf("section %d: click %q: %v", sec, p.title, err)
			}
			if got := shell.Router(sec).Path(); got != p.path {
				t.Fatalf("section %d: path after clicking %q = %q, want %q", sec, p.title, got, p.path)
			}
			if p.text != "" {
				if !hasText(tt, p.text) {
					t.Fatalf("section %d: %q does not show %q; texts: %v", sec, p.path, p.text, texts(t, tt))
				}
			} else if !tt.HasText("not available on this device") {
				// The Vault and Settings pages, without their services,
				// render their own unavailable state rather than blank.
				t.Fatalf("section %d: %q rendered blank or without its state; texts: %v", sec, p.path, texts(t, tt))
			}
			if err := tt.Click("Back"); err != nil {
				t.Fatalf("section %d: click Back from %q: %v", sec, p.path, err)
			}
			tt.Advance(600 * time.Millisecond)
			if got := shell.Router(sec).Path(); got != "/" {
				t.Fatalf("section %d: path after Back = %q, want /", sec, got)
			}
			if !hasText(tt, shell.Info(sec).Title) {
				t.Fatalf("section %d: root is not shown after Back; texts: %v", sec, texts(t, tt))
			}
		}
	}
}

// TestSwitchingSectionsReturnsWhereTheUserLeftOff uses one tester over the
// shell's selected section, as the tab bar switches it: a deep page in Hosts
// stays deep through a detour to Sessions, while Sessions is untouched.
func TestSwitchingSectionsReturnsWhereTheUserLeftOff(t *testing.T) {
	shell := New(WithHosts(&fakeHostStore{hosts: []domainvault.Host{{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}}}))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)

	shell.Select(Hosts)
	tt.Frame()
	if err := tt.Click("Details for prod box"); err != nil {
		t.Fatalf("open the Host's detail page: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
	if got := shell.Router(Hosts).Path(); got != "/host/prod" {
		t.Fatalf("Hosts path = %q, want /host/prod", got)
	}

	shell.Select(Sessions)
	tt.Frame()
	if !hasText(tt, shell.Info(Sessions).Title) {
		t.Fatalf("Sessions root not shown after switching; texts: %v", texts(t, tt))
	}
	if got := shell.Router(Sessions).Path(); got != "/" {
		t.Fatalf("Sessions path = %q, want /", got)
	}

	shell.Select(Hosts)
	tt.Frame()
	if !hasText(tt, "Endpoint") {
		t.Fatalf("Hosts did not return to /host/prod; texts: %v", texts(t, tt))
	}
	if got := shell.Router(Hosts).Path(); got != "/host/prod" {
		t.Fatalf("Hosts path after returning = %q, want /host/prod", got)
	}
}

// TestSectionsKeepSeparateRouters is the state half of the same rule: every
// section has its own router, so a push in one cannot move another.
func TestSectionsKeepSeparateRouters(t *testing.T) {
	shell := New()
	before := make([]*ui.Router, sectionCount)
	for i := range before {
		before[i] = shell.Router(Section(i))
	}
	shell.Router(Hosts).Push("/browse")
	shell.Router(Vault).Push("/keys")
	for i := range before {
		if shell.Router(Section(i)) != before[i] {
			t.Fatalf("section %d does not keep the router it started with", i)
		}
	}
	if got := shell.Router(Hosts).Path(); got != "/browse" {
		t.Fatalf("Hosts path = %q, want /browse", got)
	}
	if got := shell.Router(Vault).Path(); got != "/keys" {
		t.Fatalf("Vault path = %q, want /keys", got)
	}
	for _, sec := range []Section{Sessions, Settings} {
		if got := shell.Router(sec).Path(); got != "/" {
			t.Fatalf("section %d path = %q, want / (another section's push leaked)", sec, got)
		}
	}
}

// TestSelectIgnoresUnknownSection pins that a stray tab index cannot move the
// shell outside its four sections.
func TestSelectIgnoresUnknownSection(t *testing.T) {
	shell := New()
	shell.Select(Hosts)
	shell.Select(Section(99))
	if got := shell.Selected(); got != Hosts {
		t.Fatalf("Selected = %d after an out-of-range Select, want %d", got, Hosts)
	}
}
