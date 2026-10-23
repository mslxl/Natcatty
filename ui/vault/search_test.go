package vault

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appsearch "github.com/mslxl/natcatty/application/search"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

func searchFixture() vaultSnapshot {
	return vaultSnapshot{
		hosts: []domainvault.Host{
			{ID: "h1", Label: "prod-db", Hostname: "10.0.0.1", Username: "postgres",
				Group: "prod", Tags: []string{"database"}, Notes: "primary"},
			{ID: "h2", Label: "staging", Hostname: "prod-db.example.com"},
		},
		groups:     []domainvault.Group{{Path: "prod", Notes: "production"}},
		keys:       []domainvault.Key{{ID: "k1", Label: "deploy key", Type: domainvault.KeyTypeED25519, PublicKey: "ssh-ed25519 AAAA"}},
		identities: []domainvault.Identity{{ID: "i1", Label: "deployer", Username: "deploy"}},
		snippets: []domainvault.Snippet{
			{ID: "sn1", Label: "restart nginx", Command: "systemctl restart nginx", Package: "ops", Tags: []string{"web"}},
		},
		snippetPackages: []domainvault.SnippetPackage{{Path: "ops"}},
		notes: []domainvault.Note{
			{ID: "n1", Title: "Runbook", Content: "Open the firewall before the deploy.", Group: "ops"},
			{ID: "n2", Title: "Onboarding", Content: "Read the Runbook first."},
		},
		noteGroups: []domainvault.NoteGroup{{Path: "ops"}},
		proxies: []domainvault.ProxyProfile{
			{ID: "p1", Label: "corp proxy", Config: domainvault.ProxyConfig{Type: "socks5", Host: "proxy.local"}},
		},
	}
}

func hasRecord(records []appsearch.Result, kind appsearch.Kind, id string) bool {
	for _, record := range records {
		if record.Record.Kind == kind && record.Record.ID == id {
			return true
		}
	}
	return false
}

// TestVaultSearchCoversEveryEntity proves one field reaches every kind the
// acceptance list names.
func TestVaultSearchCoversEveryEntity(t *testing.T) {
	snapshot := searchFixture()
	cases := []struct {
		query string
		kind  appsearch.Kind
		id    string
	}{
		{"prod-db", appsearch.KindHost, "h1"},
		{"10.0.0.1", appsearch.KindHost, "h1"},
		{"database", appsearch.KindHost, "h1"},
		{"postgres", appsearch.KindHost, "h1"},
		{"prod", appsearch.KindHostGroup, "prod"},
		{"deploy key", appsearch.KindKey, "k1"},
		{"deployer", appsearch.KindIdentity, "i1"},
		{"nginx", appsearch.KindSnippet, "sn1"},
		{"ops", appsearch.KindSnippetPackage, "ops"},
		{"Runbook", appsearch.KindNote, "n1"},
		{"firewall", appsearch.KindKnowledge, "n1"},
		{"ops", appsearch.KindNoteGroup, "ops"},
		{"proxy.local", appsearch.KindProxyProfile, "p1"},
	}
	for _, testCase := range cases {
		results := appsearch.Rank(testCase.query, snapshotRecords(snapshot, testCase.query))
		if !hasRecord(results, testCase.kind, testCase.id) {
			t.Errorf("%q did not find %s %q: %v", testCase.query, testCase.kind, testCase.id, recordNames(results))
		}
	}
}

// TestVaultSearchRanksLabelsAboveHostnames keeps the original matcher's
// weights: the Host named for the query leads the Host whose address matches.
func TestVaultSearchRanksLabelsAboveHostnames(t *testing.T) {
	snapshot := searchFixture()
	results := appsearch.Rank("prod-db", snapshotRecords(snapshot, "prod-db"))
	if len(results) < 2 {
		t.Fatalf("results = %v, want both Hosts", recordNames(results))
	}
	if results[0].Record.Kind != appsearch.KindHost || results[0].Record.ID != "h1" {
		t.Fatalf("first result = %s %s, want the label match",
			results[0].Record.Kind, results[0].Record.ID)
	}
}

// TestVaultSearchSeparatesTitleAndBodyMatches proves a Note appears once: in
// Notes when its title matches, in Knowledge when only its body does.
func TestVaultSearchSeparatesTitleAndBodyMatches(t *testing.T) {
	snapshot := searchFixture()

	titleMatches := appsearch.Rank("Runbook", snapshotRecords(snapshot, "Runbook"))
	if !hasRecord(titleMatches, appsearch.KindNote, "n1") {
		t.Fatalf("title match missing from Notes: %v", recordNames(titleMatches))
	}
	if hasRecord(titleMatches, appsearch.KindKnowledge, "n1") {
		t.Fatal("a title match also appeared as Knowledge")
	}

	bodyMatches := appsearch.Rank("firewall", snapshotRecords(snapshot, "firewall"))
	if !hasRecord(bodyMatches, appsearch.KindKnowledge, "n1") {
		t.Fatalf("body match missing from Knowledge: %v", recordNames(bodyMatches))
	}
	if hasRecord(bodyMatches, appsearch.KindNote, "n1") {
		t.Fatal("a body match also appeared as a Note title")
	}
}

// TestVaultSearchEmptyQueryListsTheWholeVault keeps the list's own order
// meaningful before anything is typed.
func TestVaultSearchEmptyQueryListsTheWholeVault(t *testing.T) {
	records := snapshotRecords(searchFixture(), "")
	for _, kind := range []appsearch.Kind{
		appsearch.KindHost, appsearch.KindHostGroup, appsearch.KindKey,
		appsearch.KindIdentity, appsearch.KindSnippet, appsearch.KindSnippetPackage,
		appsearch.KindNote, appsearch.KindNoteGroup, appsearch.KindProxyProfile,
	} {
		found := false
		for _, record := range records {
			if record.Kind == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the empty list has no %s record", kind)
		}
	}
	if hasKind(records, appsearch.KindKnowledge) {
		t.Fatal("Knowledge appears before a query is typed")
	}
}

func hasKind(records []appsearch.Record, kind appsearch.Kind) bool {
	for _, record := range records {
		if record.Kind == kind {
			return true
		}
	}
	return false
}

func recordNames(results []appsearch.Result) []string {
	out := make([]string, len(results))
	for i, result := range results {
		out[i] = string(result.Record.Kind) + ":" + result.Record.ID
	}
	return out
}

func newSearchVault(t *testing.T) (*vaultstate.State, *noteTestRepo) {
	t.Helper()
	repo := &noteTestRepo{
		notes: []domainvault.Note{
			{ID: "n1", Title: "Firewall Runbook", Content: "Open the firewall before deploying.", Group: "ops"},
			{ID: "n2", Title: "Onboarding", Content: "Read the Runbook first."},
		},
		groups: []domainvault.NoteGroup{{Path: "ops"}},
	}
	service := appvault.NewService(nil, appvault.WithNotes(repo))
	state := vaultstate.NewState(&entityHosts{items: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
			Username: "root", Protocol: domainvault.ProtocolSSH},
	}}, vaultstate.WithNotes(service))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state, repo
}

// TestVaultSearchFieldFiltersAndOpensTheMatch drives the real field: click it,
// type, then Enter opens the Note viewer.
func TestVaultSearchFieldFiltersAndOpensTheMatch(t *testing.T) {
	state, _ := newSearchVault(t)
	search := newVaultSearch(state)
	tester := ui.NewTester(View(state, withSearch(search)), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	m := locales.In("en")
	if err := tester.Click(m.VaultSearchPlaceholder()); err != nil {
		t.Fatalf("the search field has no accessible name: %v", err)
	}
	tester.Type("deploying")
	tester.Frame()

	if !tester.HasText(m.SearchSectionKnowledge()) {
		t.Fatalf("a body match did not render Knowledge: %v", tester.Texts())
	}
	if !tester.HasText("Firewall Runbook") {
		t.Fatalf("the matching Note is missing: %v", tester.Texts())
	}

	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if !state.ViewingNote() {
		t.Fatalf("Enter did not open the Note viewer: %v", tester.Texts())
	}
}

// TestVaultSearchNoResultsAndEscapeClear checks both empty outcomes and the
// way back to the unfiltered list.
func TestVaultSearchNoResultsAndEscapeClear(t *testing.T) {
	state, _ := newSearchVault(t)
	search := newVaultSearch(state)
	tester := ui.NewTester(View(state, withSearch(search)), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.VaultSearchPlaceholder()); err != nil {
		t.Fatalf("the search field has no accessible name: %v", err)
	}
	tester.Type("zzz")
	tester.Frame()
	if !tester.HasText(m.VaultSearchNoResults()) {
		t.Fatalf("missing the no-result state: %v", tester.Texts())
	}

	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if search.Query() != "" {
		t.Fatalf("Escape left the query %q", search.Query())
	}
	if !tester.HasText(m.VaultHostListTitle(1)) {
		t.Fatalf("Escape did not restore the list: %v", tester.Texts())
	}
}
