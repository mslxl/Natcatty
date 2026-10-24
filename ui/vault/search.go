package vault

import (
	"strings"

	"github.com/egoist/mygo/ui"

	appsearch "github.com/mslxl/natcatty/application/search"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// newVaultSearch builds the Vault's inline search. Its records cover every
// entity the Vault holds, so one field finds a Host by address or tag, a
// Note by title, and a Note's body as Knowledge.
func newVaultSearch(s *vaultstate.State) *uisearch.State {
	// The source reads the state's own query: a Note whose body alone matches
	// joins the Knowledge section instead of the Notes one.
	var state *uisearch.State
	state = uisearch.New(
		uisearch.WithSource(func(_ uisearch.Mode, _ string) []appsearch.Record {
			return vaultRecords(s, state.Query())
		}),
		uisearch.WithOpen(func(_ uisearch.Mode, result appsearch.Result) bool {
			return openVaultResult(s, result)
		}),
	)
	return state
}

// vaultSnapshot is the searchable slice of the Vault at one frame. Keeping
// the record rules independent of the state makes every entity kind testable
// without building a service fake for each.
type vaultSnapshot struct {
	hosts           []domainvault.Host
	groups          []domainvault.Group
	keys            []domainvault.Key
	identities      []domainvault.Identity
	snippets        []domainvault.Snippet
	snippetPackages []domainvault.SnippetPackage
	notes           []domainvault.Note
	noteGroups      []domainvault.NoteGroup
	proxies         []domainvault.ProxyProfile
}

// vaultRecords reads the live Vault state and builds its search records.
func vaultRecords(s *vaultstate.State, query string) []appsearch.Record {
	if s == nil {
		return nil
	}
	return snapshotRecords(vaultSnapshot{
		hosts:           s.Items(),
		groups:          s.Groups(),
		keys:            s.Keys(),
		identities:      s.Identities(),
		snippets:        s.Snippets(),
		snippetPackages: s.SnippetPackages(),
		notes:           s.Notes(),
		noteGroups:      s.NoteGroups(),
		proxies:         s.ProxyProfiles(),
	}, query)
}

// snapshotRecords lists every searchable Vault entity in the order the list
// shows their sections. The current query decides whether a Note belongs to
// the Notes section or, when only its body matches, to Knowledge.
func snapshotRecords(snapshot vaultSnapshot, query string) []appsearch.Record {
	prepared := appsearch.Prepare(query)
	records := make([]appsearch.Record, 0, len(snapshot.hosts)+len(snapshot.notes)+16)
	// Knowledge comes last, after every entity the Vault lists directly.
	var knowledge []appsearch.Record

	for _, host := range snapshot.hosts {
		fields := []appsearch.Field{
			{Text: host.Label, Weight: appsearch.WeightLabel},
			{Text: host.Hostname, Weight: appsearch.WeightHostname},
			{Text: host.Username, Weight: appsearch.WeightUsername},
			{Text: host.Notes, Weight: appsearch.WeightNote, Body: true},
		}
		if host.Group != "" {
			fields = append(fields, appsearch.Field{Text: host.Group, Weight: appsearch.WeightGroup})
		}
		for _, tag := range host.Tags {
			fields = append(fields, appsearch.Field{Text: tag, Weight: appsearch.WeightTag})
		}
		records = append(records, appsearch.Record{
			Kind:     appsearch.KindHost,
			ID:       string(host.ID),
			Title:    host.Label,
			Subtitle: host.Hostname,
			Fields:   fields,
		})
	}

	for _, group := range snapshot.groups {
		records = append(records, appsearch.Record{
			Kind:  appsearch.KindHostGroup,
			ID:    group.Path,
			Title: group.Path,
			Fields: []appsearch.Field{
				{Text: group.Path, Weight: appsearch.WeightLabel},
				{Text: group.Notes, Weight: appsearch.WeightNote, Body: true},
			},
		})
	}

	for _, key := range snapshot.keys {
		records = append(records, appsearch.Record{
			Kind:  appsearch.KindKey,
			ID:    string(key.ID),
			Title: key.Label,
			Fields: []appsearch.Field{
				{Text: key.Label, Weight: appsearch.WeightLabel},
				{Text: string(key.Type), Weight: appsearch.WeightKeyword},
				{Text: key.PublicKey, Weight: appsearch.WeightNote, Body: true},
			},
		})
	}

	for _, identity := range snapshot.identities {
		records = append(records, appsearch.Record{
			Kind:  appsearch.KindIdentity,
			ID:    string(identity.ID),
			Title: identity.Label,
			Fields: []appsearch.Field{
				{Text: identity.Label, Weight: appsearch.WeightLabel},
				{Text: identity.Username, Weight: appsearch.WeightUsername},
			},
		})
	}

	for _, snippet := range snapshot.snippets {
		fields := []appsearch.Field{
			{Text: snippet.Label, Weight: appsearch.WeightLabel},
			{Text: snippet.Command, Weight: appsearch.WeightContent, Body: true},
		}
		if snippet.Package != "" {
			fields = append(fields, appsearch.Field{Text: snippet.Package, Weight: appsearch.WeightGroup})
		}
		for _, tag := range snippet.Tags {
			fields = append(fields, appsearch.Field{Text: tag, Weight: appsearch.WeightTag})
		}
		records = append(records, appsearch.Record{
			Kind:     appsearch.KindSnippet,
			ID:       snippet.ID,
			Title:    snippet.Label,
			Subtitle: snippet.Package,
			Fields:   fields,
		})
	}

	for _, snippetPackage := range snapshot.snippetPackages {
		records = append(records, appsearch.Record{
			Kind:  appsearch.KindSnippetPackage,
			ID:    snippetPackage.Path,
			Title: snippetPackage.Path,
			Fields: []appsearch.Field{
				{Text: snippetPackage.Path, Weight: appsearch.WeightLabel},
			},
		})
	}

	for _, note := range snapshot.notes {
		titleRecord := appsearch.Record{
			Kind:  appsearch.KindNote,
			ID:    note.ID,
			Title: note.Title,
			Fields: []appsearch.Field{
				{Text: note.Title, Weight: appsearch.WeightLabel},
				{Text: note.Group, Weight: appsearch.WeightGroup},
			},
		}
		if _, ok := appsearch.Match(prepared, titleRecord); ok {
			titleRecord.Subtitle = note.Group
			records = append(records, titleRecord)
			continue
		}
		// A body-only match is Knowledge: the note is worth finding for what
		// it says, not for its title.
		bodyRecord := appsearch.Record{
			Kind:  appsearch.KindKnowledge,
			ID:    note.ID,
			Title: note.Title,
			Fields: []appsearch.Field{
				{Text: note.Content, Weight: appsearch.WeightContent, Body: true},
			},
		}
		if _, ok := appsearch.Match(prepared, bodyRecord); ok {
			bodyRecord.Subtitle = note.Group
			knowledge = append(knowledge, bodyRecord)
		}
	}

	for _, group := range snapshot.noteGroups {
		records = append(records, appsearch.Record{
			Kind:  appsearch.KindNoteGroup,
			ID:    group.Path,
			Title: group.Path,
			Fields: []appsearch.Field{
				{Text: group.Path, Weight: appsearch.WeightLabel},
			},
		})
	}

	for _, proxy := range snapshot.proxies {
		records = append(records, appsearch.Record{
			Kind:     appsearch.KindProxyProfile,
			ID:       proxy.ID,
			Title:    proxy.Label,
			Subtitle: proxySubtitle(proxy.Config),
			Fields: []appsearch.Field{
				{Text: proxy.Label, Weight: appsearch.WeightLabel},
				{Text: string(proxy.Config.Type), Weight: appsearch.WeightKeyword},
				{Text: proxy.Config.Host, Weight: appsearch.WeightHostname},
				{Text: proxy.Config.Command, Weight: appsearch.WeightContent, Body: true},
			},
		})
	}

	return append(records, knowledge...)
}

// openVaultResult shows the editor or viewer for a search result.
func openVaultResult(s *vaultstate.State, result appsearch.Result) bool {
	if s == nil {
		return false
	}
	id := result.Record.ID
	switch result.Record.Kind {
	case appsearch.KindHost:
		return s.BeginEdit(model.HostID(id))
	case appsearch.KindHostGroup:
		return s.BeginEditGroup(id)
	case appsearch.KindKey:
		return s.BeginEditKey(model.KeyID(id))
	case appsearch.KindIdentity:
		return s.BeginEditIdentity(model.IdentityID(id))
	case appsearch.KindSnippet:
		return s.BeginEditSnippet(id)
	case appsearch.KindSnippetPackage:
		return s.BeginEditSnippetPackage(id)
	case appsearch.KindNote, appsearch.KindKnowledge:
		return s.BeginViewNote(id)
	case appsearch.KindNoteGroup:
		return s.BeginEditNoteGroup(id)
	case appsearch.KindProxyProfile:
		return s.BeginEditProxyProfile(id)
	default:
		return false
	}
}

// searchBar renders the Vault's one search field and offers a clear action
// once it holds text.
func searchBar(c *ui.Context, search *uisearch.State, m locales.Messages) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c, search.QueryPtr()).
			Label(m.VaultSearchPlaceholder()).
			Placeholder(m.VaultSearchPlaceholder()).
			Grow(1).
			OnTextKey(func(k ui.TextKey) bool {
				switch k.Key {
				case ui.KeyDown:
					if search.Move(1) {
						c.Invalidate()
					}
					return true
				case ui.KeyUp:
					if search.Move(-1) {
						c.Invalidate()
					}
					return true
				case ui.KeyEnter:
					if search.Activate() {
						c.Invalidate()
					}
					return true
				case ui.KeyEscape:
					clearVaultSearch(search)
					c.Invalidate()
					return true
				}
				return false
			})
		if strings.TrimSpace(search.Query()) != "" {
			if ui.Button(c, m.VaultSearchClear()).Clicked() {
				clearVaultSearch(search)
				c.Invalidate()
			}
		}
	})
}

func clearVaultSearch(search *uisearch.State) {
	*search.QueryPtr() = ""
	search.Sync()
}

// vaultSearchResults renders the ranked, sectioned results in place of the
// list. The Vault shows the same rows as the Quick Switcher.
func vaultSearchResults(c *ui.Context, search *uisearch.State, m locales.Messages, t *ui.Theme) {
	sections := search.Sections()
	count := 0
	for _, section := range sections {
		count += len(section.Results)
	}
	search.Clamp(count)
	reveal := search.TakeReveal()

	ui.Text(c, m.VaultSearchResults(count)).FontSize(11).TextColor(t.TextMuted)
	if count == 0 {
		ui.Column(c).FillWidth().Padding(16).Gap(4).Children(func() {
			ui.Text(c, m.VaultSearchNoResults()).Bold()
			ui.Text(c, m.VaultSearchNoResultsBody()).FontSize(12).TextColor(t.TextMuted)
		})
		return
	}
	uisearch.ListAt(c, sections, m, t, uisearch.ListOptions{
		Selected:  search.Selected(),
		Reveal:    reveal,
		KeyPrefix: "vault-search-row",
		Activate: func(index int) {
			if search.Select(index) {
				c.Invalidate()
			}
			if search.Activate() {
				c.Invalidate()
			}
		},
	})
}

// proxySubtitle names a Proxy Profile's endpoint in the result row.
func proxySubtitle(config domainvault.ProxyConfig) string {
	switch {
	case config.Host != "":
		return config.Host
	case config.Command != "":
		return config.Command
	default:
		return string(config.Type)
	}
}
