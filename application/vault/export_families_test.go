package vault

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// familySource is a Vault with one record in every family the JSON export
// carries, plus every link between them.
type familySource struct {
	service    *Service
	hosts      *fakeHosts
	groups     *fakeGroups
	keys       *fakeKeys
	identities *fakeIdentities
	proxies    *fakeProxies
	known      *fakeKnownHosts
	snippets   *fakeSnippets
	notes      *fakeNotes
	secrets    *fakeSecrets
}

func fixedFamilyTime() time.Time { return time.Unix(1700000000, 0).UTC() }

func newFamilySource(t *testing.T) *familySource {
	t.Helper()
	source := &familySource{
		hosts:      &fakeHosts{},
		groups:     &fakeGroups{},
		keys:       &fakeKeys{},
		identities: &fakeIdentities{},
		proxies:    &fakeProxies{},
		known:      &fakeKnownHosts{},
		snippets:   &fakeSnippets{},
		notes:      &fakeNotes{},
		secrets:    newFakeSecrets(),
	}
	store := &fakeImportStore{hosts: source.hosts, groups: source.groups}
	var hostIDs, keyIDs, identityIDs, proxyIDs, snippetIDs, noteIDs int
	source.service = NewService(source.hosts,
		WithGroups(source.groups),
		WithKeys(source.keys),
		WithIdentities(source.identities),
		WithProxyProfiles(source.proxies),
		WithKnownHosts(source.known),
		WithSnippets(source.snippets),
		WithNotes(source.notes),
		WithSecrets(source.secrets),
		WithImportStore(store),
		WithKeyInspector(&fakeInspector{}),
		WithIDGenerator(func() model.HostID {
			hostIDs++
			return model.HostID("host-" + strconv.Itoa(hostIDs))
		}),
		WithKeyIDGenerator(func() model.KeyID {
			keyIDs++
			return model.KeyID("key-" + strconv.Itoa(keyIDs))
		}),
		WithIdentityIDGenerator(func() model.IdentityID {
			identityIDs++
			return model.IdentityID("identity-" + strconv.Itoa(identityIDs))
		}),
		WithProxyProfileIDGenerator(func() string {
			proxyIDs++
			return "proxy-" + strconv.Itoa(proxyIDs)
		}),
		WithSnippetIDGenerator(func() string {
			snippetIDs++
			return "snippet-" + strconv.Itoa(snippetIDs)
		}),
		WithNoteIDGenerator(func() string {
			noteIDs++
			return "note-" + strconv.Itoa(noteIDs)
		}),
		WithClock(fixedFamilyTime),
	)
	ctx := context.Background()
	source.groups.items = []domainvault.Group{
		{Path: "Prod", OrderKey: 1000},
		{Path: "Prod/Web", OrderKey: 2000},
	}
	source.keys.keys = []domainvault.Key{{
		ID: "key-1", Label: "deploy key", Type: domainvault.KeyTypeED25519,
		PublicKey: "ssh-ed25519 AAAA deploy", Source: domainvault.KeySourceImported,
		HasPrivateKey: true, HasPassphrase: true, OrderKey: 1000,
	}}
	source.identities.identities = []domainvault.Identity{{
		ID: "ident-1", Label: "Deploy identity", Username: "deploy",
		AuthMethod: domainvault.IdentityAuthKey, KeyID: "key-1", HasPassword: true,
	}}
	source.proxies.items = []domainvault.ProxyProfile{{
		ID: "proxy-1", Label: "Bastion", OrderKey: 1000,
		Config: domainvault.ProxyConfig{
			Type: domainvault.ProxySOCKS5, Host: "proxy.example.test", Port: 1080,
			Username: "proxyuser", HasPassword: true,
		},
	}}
	source.hosts.hosts = []domainvault.Host{{
		ID: "host-1", Label: "Web One", Hostname: "web1.example.test", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolSSH, Group: "Prod/Web",
		Tags: []string{"web"}, Pinned: true, HasPassword: true,
		IdentityID: "ident-1", KeyID: "key-1", AuthMethod: domainvault.HostAuthKey,
		RequiresMFA: true, ProxyProfileID: "proxy-1", LegacyAlgorithms: true,
		SkipEcdsaHostKey: true, OrderKey: 1000,
	}}
	source.known.items = []domainconn.KnownHost{{
		ID: "known-1", Hostname: "web1.example.test", Port: 2222,
		KeyType: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA host",
		Fingerprint: "SHA256:abc", DiscoveredAt: fixedFamilyTime(),
		LastSeen: fixedFamilyTime(), ConvertedToHostID: "host-1",
	}}
	source.snippets.snippets = []domainvault.Snippet{
		{
			ID: "snippet-1", Label: "Deploy", Command: "deploy {{env}}",
			Package: "Ops/Daily", Tags: []string{"ops"},
			Targets: []model.HostID{"host-1"}, TargetGroups: []string{"Prod"},
			VariableValues: map[string]string{"env": "prod"}, OrderKey: 1000,
		},
		{
			ID: "snippet-2", Label: "Everyone", Command: "uptime",
			Package: "Ops", TargetsAllHosts: true, NoAutoRun: true,
			MultiLineRunMode: domainvault.SnippetRunPaste, OrderKey: 2000,
		},
	}
	source.notes.notes = []domainvault.Note{
		{ID: "note-1", Title: "Runbook", Content: "first\r\n", Group: "Ops", OrderKey: 1000},
		{ID: "note-2", Title: "Runbook", Content: "second", Group: "Ops", OrderKey: 2000},
		{ID: "note-3", Title: "Config / Draft", Content: "# Draft", Group: "Ops/Config", OrderKey: 3000},
	}
	source.notes.groups = []domainvault.NoteGroup{
		{Path: "Ops", OrderKey: 1000}, {Path: "Ops/Config", OrderKey: 2000},
	}
	// Credential plaintext lives only in the field-secret store.
	source.secrets.values[HostSecretRef("host-1", FieldPassword)] = "hostpass"
	source.secrets.values[KeySecretRef("key-1", FieldPrivateKey)] = "PRIVATE MATERIAL"
	source.secrets.values[KeySecretRef("key-1", FieldPassphrase)] = "keypass"
	source.secrets.values[IdentitySecretRef("ident-1", FieldPassword)] = "identp@ss"
	source.secrets.values[SecretRef{EntityType: "proxy_profile", EntityID: "proxy-1", Field: FieldProxyPassword}] = "proxyp@ss"
	if _, err := source.service.EffectiveHost(ctx, "host-1"); err != nil {
		t.Fatalf("seed EffectiveHost: %v", err)
	}
	return source
}

// newDestination builds an empty Vault with fresh stores so every imported id
// is remapped.
func newDestination(t *testing.T) *familySource {
	t.Helper()
	destination := &familySource{
		hosts:      &fakeHosts{},
		groups:     &fakeGroups{},
		keys:       &fakeKeys{},
		identities: &fakeIdentities{},
		proxies:    &fakeProxies{},
		known:      &fakeKnownHosts{},
		snippets:   &fakeSnippets{},
		notes:      &fakeNotes{},
		secrets:    newFakeSecrets(),
	}
	store := &fakeImportStore{hosts: destination.hosts, groups: destination.groups}
	var hostIDs, keyIDs, identityIDs, proxyIDs, snippetIDs, noteIDs int
	destination.service = NewService(destination.hosts,
		WithGroups(destination.groups),
		WithKeys(destination.keys),
		WithIdentities(destination.identities),
		WithProxyProfiles(destination.proxies),
		WithKnownHosts(destination.known),
		WithSnippets(destination.snippets),
		WithNotes(destination.notes),
		WithSecrets(destination.secrets),
		WithImportStore(store),
		WithKeyInspector(&fakeInspector{}),
		WithIDGenerator(func() model.HostID {
			hostIDs++
			return model.HostID("new-host-" + strconv.Itoa(hostIDs))
		}),
		WithKeyIDGenerator(func() model.KeyID {
			keyIDs++
			return model.KeyID("new-key-" + strconv.Itoa(keyIDs))
		}),
		WithIdentityIDGenerator(func() model.IdentityID {
			identityIDs++
			return model.IdentityID("new-identity-" + strconv.Itoa(identityIDs))
		}),
		WithProxyProfileIDGenerator(func() string {
			proxyIDs++
			return "new-proxy-" + strconv.Itoa(proxyIDs)
		}),
		WithSnippetIDGenerator(func() string {
			snippetIDs++
			return "new-snippet-" + strconv.Itoa(snippetIDs)
		}),
		WithNoteIDGenerator(func() string {
			noteIDs++
			return "new-note-" + strconv.Itoa(noteIDs)
		}),
		WithClock(fixedFamilyTime),
	)
	return destination
}

// TestVaultJSONRoundTripRestoresEveryFamilyAndLink is the acceptance test for
// ticket 25's round trip. It exports in the production format, imports through
// PreviewImport/CommitImport, and checks every family and cross-reference.
func TestVaultJSONRoundTripRestoresEveryFamilyAndLink(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatalf("ExportVault: %v", err)
	}

	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
		PreviewImportOptions{FileName: "netcatty-vault.json"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.Format != "netcatty_json" {
		t.Fatalf("detected format = %q", preview.Format)
	}
	result, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{})
	if err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if len(result.Imported) != 1 || len(destination.hosts.hosts) != 1 {
		t.Fatalf("imported hosts = %v, stored = %d", result.Imported, len(destination.hosts.hosts))
	}
	host := destination.hosts.hosts[0]
	if host.Label != "Web One" || host.Hostname != "web1.example.test" ||
		host.Port != 2222 || host.Group != "Prod/Web" || !host.Pinned {
		t.Fatalf("Host = %+v", host)
	}
	if host.IdentityID == "" || host.IdentityID == "ident-1" {
		t.Fatalf("Identity link was not remapped: %q", host.IdentityID)
	}
	if host.ProxyProfileID == "" || host.ProxyProfileID == "proxy-1" {
		t.Fatalf("Proxy Profile link was not remapped: %q", host.ProxyProfileID)
	}
	if host.AuthMethod != domainvault.HostAuthKey || !host.RequiresMFA ||
		!host.LegacyAlgorithms || !host.SkipEcdsaHostKey {
		t.Fatalf("Host auth/options = %+v", host)
	}

	if len(destination.keys.keys) != 1 {
		t.Fatalf("Keys = %+v", destination.keys.keys)
	}
	key := destination.keys.keys[0]
	if key.Label != "deploy key" {
		t.Fatalf("Key = %+v", key)
	}
	if host.KeyID != key.ID {
		t.Fatalf("Host KeyID = %q, Key = %q", host.KeyID, key.ID)
	}
	if got := destination.secrets.values[KeySecretRef(key.ID, FieldPrivateKey)]; got != "PRIVATE MATERIAL" {
		t.Fatalf("Key material = %q", got)
	}
	if got := destination.secrets.values[KeySecretRef(key.ID, FieldPassphrase)]; got != "keypass" {
		t.Fatalf("Key passphrase = %q", got)
	}

	if len(destination.identities.identities) != 1 {
		t.Fatalf("Identities = %+v", destination.identities.identities)
	}
	identity := destination.identities.identities[0]
	if identity.Username != "deploy" || identity.AuthMethod != domainvault.IdentityAuthKey {
		t.Fatalf("Identity = %+v", identity)
	}
	if identity.KeyID != key.ID {
		t.Fatalf("Identity KeyID = %q, want %q", identity.KeyID, key.ID)
	}
	if !identity.HasPassword {
		t.Fatal("Identity password flag was not restored")
	}
	if got := destination.secrets.values[IdentitySecretRef(identity.ID, FieldPassword)]; got != "identp@ss" {
		t.Fatalf("Identity password = %q", got)
	}

	if len(destination.proxies.items) != 1 {
		t.Fatalf("Proxy Profiles = %+v", destination.proxies.items)
	}
	proxy := destination.proxies.items[0]
	if proxy.Label != "Bastion" || proxy.Config.Host != "proxy.example.test" ||
		proxy.Config.Port != 1080 || proxy.Config.Username != "proxyuser" {
		t.Fatalf("Proxy Profile = %+v", proxy)
	}
	if !proxy.Config.HasPassword {
		t.Fatal("Proxy Profile password flag was not restored")
	}
	if got := destination.secrets.values[SecretRef{EntityType: "proxy_profile", EntityID: proxy.ID, Field: FieldProxyPassword}]; got != "proxyp@ss" {
		t.Fatalf("Proxy Profile password = %q", got)
	}
	effective, err := destination.service.EffectiveHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("EffectiveHost: %v", err)
	}
	if effective.Proxy == nil || effective.Proxy.Password != "proxyp@ss" {
		t.Fatalf("effective proxy = %+v", effective.Proxy)
	}

	if len(destination.known.items) != 1 {
		t.Fatalf("Known Hosts = %+v", destination.known.items)
	}
	if destination.known.items[0].Fingerprint != "SHA256:abc" {
		t.Fatalf("Known Host = %+v", destination.known.items[0])
	}
	if destination.known.items[0].ConvertedToHostID != string(host.ID) {
		t.Fatalf("Known Host converted link = %q, want %q",
			destination.known.items[0].ConvertedToHostID, host.ID)
	}

	if len(destination.snippets.snippets) != 2 {
		t.Fatalf("Snippets = %+v", destination.snippets.snippets)
	}
	snippets := append([]domainvault.Snippet(nil), destination.snippets.snippets...)
	sort.Slice(snippets, func(i, j int) bool { return snippets[i].Label < snippets[j].Label })
	deploy := snippets[0]
	if deploy.Label != "Deploy" || deploy.Package != "Ops/Daily" ||
		len(deploy.Targets) != 1 || deploy.Targets[0] != host.ID {
		t.Fatalf("Deploy Snippet = %+v", deploy)
	}
	if len(deploy.TargetGroups) != 1 || deploy.TargetGroups[0] != "Prod" {
		t.Fatalf("Deploy target groups = %v", deploy.TargetGroups)
	}
	if deploy.VariableValues["env"] != "prod" {
		t.Fatalf("Deploy variable values = %v", deploy.VariableValues)
	}
	everyone := snippets[1]
	if !everyone.TargetsAllHosts || !everyone.NoAutoRun ||
		everyone.MultiLineRunMode != domainvault.SnippetRunPaste {
		t.Fatalf("Everyone Snippet = %+v", everyone)
	}
	if len(everyone.Targets) != 0 {
		t.Fatalf("all-hosts Snippet kept explicit targets: %v", everyone.Targets)
	}

	if len(destination.notes.notes) != 3 {
		t.Fatalf("Notes = %+v", destination.notes.notes)
	}
	titles := make(map[string]int)
	for _, note := range destination.notes.notes {
		titles[note.Title]++
	}
	if titles["Runbook"] != 2 {
		t.Fatalf("duplicate Note titles collapsed: %v", titles)
	}
	if titles["Config / Draft"] != 1 {
		t.Fatalf("slash Note title was renamed: %v", titles)
	}
	for _, note := range destination.notes.notes {
		switch note.Title {
		case "Runbook":
			if note.Group != "Ops" || (note.Content != "first\r\n" && note.Content != "second") {
				t.Fatalf("Runbook = %+v", note)
			}
		case "Config / Draft":
			if note.Group != "Ops/Config" || note.Content != "# Draft" {
				t.Fatalf("Config Note = %+v", note)
			}
		}
	}

	if !containsGroup(destination.groups.items, "Prod") ||
		!containsGroup(destination.groups.items, "Prod/Web") {
		t.Fatalf("Host Groups = %+v", destination.groups.items)
	}
	if !containsNoteGroup(destination.notes.groups, "Ops") ||
		!containsNoteGroup(destination.notes.groups, "Ops/Config") {
		t.Fatalf("Note Groups = %+v", destination.notes.groups)
	}
}

// TestVaultJSONRoundTripWithoutSecretsLeavesNoPlaintext verifies the default
// is still non-destructive: credentials are absent from the document, and the
// import restores structure without inventing passwords.
func TestVaultJSONRoundTripWithoutSecretsLeavesNoPlaintext(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportJSON, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hostpass", "PRIVATE MATERIAL", "keypass", "identp@ss", "proxyp@ss"} {
		if containsText(exported.Content, secret) {
			t.Fatalf("export without IncludeSecrets contains %q", secret)
		}
	}
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, string(exported.Content), PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(destination.identities.identities) != 1 ||
		len(destination.proxies.items) != 1 ||
		len(destination.keys.keys) != 0 {
		t.Fatalf("families = identities %d, proxies %d, keys %d",
			len(destination.identities.identities), len(destination.proxies.items),
			len(destination.keys.keys))
	}
	if len(destination.secrets.values) != 0 {
		t.Fatalf("import without exported secrets wrote credentials: %v", destination.secrets.values)
	}
}

func containsGroup(groups []domainvault.Group, path string) bool {
	for _, group := range groups {
		if group.Path == path {
			return true
		}
	}
	return false
}

func containsText(haystack []byte, needle string) bool {
	return len(needle) > 0 && strings.Contains(string(haystack), needle)
}

// UpsertKnownHost is the import write path. The read-only Vault surface does
// not expose it, so the fake implements the optional capability the importer
// detects.
func (f *fakeKnownHosts) UpsertKnownHost(_ context.Context, record domainconn.KnownHost) error {
	if f.err != nil {
		return f.err
	}
	for i, existing := range f.items {
		if existing.Hostname == record.Hostname && existing.Port == record.Port &&
			existing.KeyType == record.KeyType {
			f.items[i] = record
			return nil
		}
	}
	f.items = append(f.items, record)
	return nil
}

func containsNoteGroup(groups []domainvault.NoteGroup, path string) bool {
	for _, group := range groups {
		if group.Path == path {
			return true
		}
	}
	return false
}

// TestNotesZIPRoundTripPreservesExactTitles covers the manifest fix: two
// Notes may share a title, and a title may contain a slash, without the
// archive layout renaming either on import.
func TestNotesZIPRoundTripPreservesExactTitles(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	archive, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportNotesZIP, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatalf("ExportNotesZIP: %v", err)
	}
	destination := newDestination(t)
	result, err := destination.service.ImportNotesZIP(ctx, archive.Content)
	if err != nil {
		t.Fatalf("ImportNotesZIP: %v", err)
	}
	if result.Created != 3 {
		t.Fatalf("created = %+v", result)
	}
	if len(destination.notes.notes) != 3 {
		t.Fatalf("Notes = %+v", destination.notes.notes)
	}
	titles := map[string]int{}
	for _, note := range destination.notes.notes {
		titles[note.Title]++
	}
	if titles["Runbook"] != 2 {
		t.Fatalf("duplicate titles = %v", titles)
	}
	if titles["Config / Draft"] != 1 {
		t.Fatalf("slash title = %v", titles)
	}
}

// TestSnippetsJSONImportKeepsExplicitTargets covers the scope-loss defect: a
// Snippet's explicit Host ids are references, so an import without those
// Hosts must keep them dangling rather than silently widening the Snippet.
func TestSnippetsJSONImportKeepsExplicitTargets(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportSnippets, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	result, err := destination.service.ImportSnippetsJSON(ctx, string(exported.Content))
	if err != nil || result.Created != 2 {
		t.Fatalf("ImportSnippetsJSON = %+v, %v", result, err)
	}
	var deploy domainvault.Snippet
	for _, snippet := range destination.snippets.snippets {
		if snippet.Label == "Deploy" {
			deploy = snippet
		}
	}
	if len(deploy.Targets) != 1 || deploy.Targets[0] != "host-1" {
		t.Fatalf("explicit targets = %v, want the dangling host-1 reference", deploy.Targets)
	}
	if deploy.VariableValues["env"] != "prod" {
		t.Fatalf("variable values = %v", deploy.VariableValues)
	}
	if len(deploy.TargetGroups) != 1 || deploy.TargetGroups[0] != "Prod" {
		t.Fatalf("target groups = %v", deploy.TargetGroups)
	}
}

// TestFamilyDocumentsImportThroughPreviewCommitWithoutAFormatOverride is the
// Import-screen contract: detection, preview, and commit, with no format
// override. It intentionally uses only the public import API so it runs
// against the pre-fix code as well.
func TestFamilyDocumentsImportThroughPreviewCommitWithoutAFormatOverride(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	cases := []struct {
		format ExportFormat
		check  func(*familySource) error
	}{
		{ExportSnippets, func(d *familySource) error {
			if len(d.snippets.snippets) != 2 {
				return fmt.Errorf("Snippets = %d", len(d.snippets.snippets))
			}
			return nil
		}},
		{ExportKeys, func(d *familySource) error {
			if len(d.keys.keys) != 1 {
				return fmt.Errorf("Keys = %d", len(d.keys.keys))
			}
			return nil
		}},
		{ExportNotesZIP, func(d *familySource) error {
			if len(d.notes.notes) != 3 {
				return fmt.Errorf("Notes = %d", len(d.notes.notes))
			}
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			exported, err := source.service.ExportVault(ctx, ExportOptions{
				Format: tc.format, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
			})
			if err != nil {
				t.Fatal(err)
			}
			destination := newDestination(t)
			preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
				PreviewImportOptions{FileName: "export" + exported.Filename})
			if err != nil {
				t.Fatalf("PreviewImport: %v", err)
			}
			if _, err := destination.service.CommitImport(ctx, preview,
				ImportCommitOptions{}); err != nil {
				t.Fatalf("CommitImport: %v", err)
			}
			if err := tc.check(destination); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestExportWithSecretsIncludesIdentityAndProxyPasswords is the old-API form
// of the secret round trip: both passwords must be in the document when the
// user confirms IncludeSecrets.
func TestExportWithSecretsIncludesIdentityAndProxyPasswords(t *testing.T) {
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"identp@ss", "proxyp@ss"} {
		if !containsText(exported.Content, want) {
			t.Fatalf("export with IncludeSecrets lacks %q", want)
		}
	}
}
