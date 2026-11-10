package store

import (
	"context"
	"strings"
	"testing"

	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestRemoveNamespaceCanonicalizesTheLastNamespaceToAbsent pins the purge
// transform's empty-container representation. Removing the last namespace,
// or finding an already-empty container, must leave no pluginData property:
// the v2 envelope comparison treats {} and null as a different record from
// the absent property.
func TestRemoveNamespaceCanonicalizesTheLastNamespaceToAbsent(t *testing.T) {
	const want = `{"id":"h1","label":"keep"}`
	namespace := `{"schemaVersion":1,"fields":{"name":"acme"}}`
	cases := []struct {
		name    string
		record  string
		removed bool
		expect  string
	}{
		{
			name:    "last-namespace",
			record:  `{"id":"h1","label":"keep","pluginData":{"acme.one":` + namespace + `}}`,
			removed: true, expect: want,
		},
		{
			name:   "empty-object",
			record: `{"id":"h1","label":"keep","pluginData":{}}`,
			expect: want,
		},
		{
			name:   "null",
			record: `{"id":"h1","label":"keep","pluginData":null}`,
			expect: want,
		},
		{
			name:   "variant-null",
			record: `{"id":"h1","label":"keep","PluginData":null}`,
			expect: want,
		},
		{
			name:   "variant-empty",
			record: `{"id":"h1","label":"keep","PLUGINDATA":{}}`,
			expect: want,
		},
		{
			name:    "variant-last-namespace",
			record:  `{"id":"h1","label":"keep","PLUGINDATA":{"acme.one":` + namespace + `}}`,
			removed: true, expect: want,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clean, removed, err := removeNamespace(testCase.record, "acme.one")
			if err != nil {
				t.Fatalf("removeNamespace: %v", err)
			}
			if clean != testCase.expect {
				t.Fatalf("record = %s, want %s", clean, testCase.expect)
			}
			if removed != testCase.removed {
				t.Fatalf("removed = %v, want %v", removed, testCase.removed)
			}
		})
	}

	// A surviving namespace keeps a single canonical property.
	surviving := `{"id":"h1","label":"keep","PluginData":{` +
		`"acme.one":` + namespace + `,"other.two":` + namespace + `}}`
	clean, removed, err := removeNamespace(surviving, "acme.one")
	if err != nil || !removed {
		t.Fatalf("removeNamespace surviving = %v, %v", removed, err)
	}
	if strings.Contains(clean, "acme.one") ||
		strings.Contains(clean, `"PluginData"`) ||
		!strings.Contains(clean, `"other.two"`) {
		t.Fatalf("surviving namespace record = %s", clean)
	}
	interopAssertSingleCanonicalProperty(t, clean)
}

// TestPurgePluginDataDropsTheLastNamespaceContainer runs the purge through the
// real transaction for both record families.
func TestPurgePluginDataDropsTheLastNamespaceContainer(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h1", plugindata.Map{
		"acme.one": namespace(1, map[string]string{"name": `"acme"`}),
	})); err != nil {
		t.Fatalf("Save host: %v", err)
	}
	if err := NewDataProfiles(store).Save(ctx, pluginProfile("p1", plugindata.Map{
		"acme.one": namespace(1, map[string]string{"name": `"acme"`}),
	})); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	if err := store.PurgePluginData(ctx, "acme.one"); err != nil {
		t.Fatalf("PurgePluginData: %v", err)
	}
	for name, raw := range map[string]string{
		"host":    interopHostData(t, store, "h1"),
		"profile": interopProfileData(t, store, "p1"),
	} {
		properties, err := plugindata.ScanPluginDataProperties([]byte(raw))
		if err != nil || properties.Found {
			t.Fatalf("%s kept a container: %s, %v", name, raw, err)
		}
	}
	host, err := store.Get(ctx, "h1")
	if err != nil || len(host.PluginData) != 0 {
		t.Fatalf("purged host = %+v, %v", host.PluginData, err)
	}
	profile, err := NewDataProfiles(store).Get(ctx, "p1")
	if err != nil || len(profile.PluginData) != 0 {
		t.Fatalf("purged profile = %+v, %v", profile.PluginData, err)
	}
}

// TestQuarantineLookupBoundsThePluginIDSpelling is the N1 regression: save
// truncates an imported Plugin ID to MaxDiagnosticPluginIDBytes, so read and
// clear must truncate the lookup the same way or the row is unreachable.
func TestQuarantineLookupBoundsThePluginIDSpelling(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	hugePluginID := strings.Repeat("p", 200)
	payload := appsync.Payload{
		Hosts: []domainvault.Host{pluginHost("h1", nil)},
		PluginDataQuarantines: []appsync.RejectedPluginData{{
			Kind: plugindata.RecordHost, RecordID: "h1",
			Quarantine: plugindata.Quarantine{
				PluginID: hugePluginID,
				Reason:   plugindata.ReasonNamespace,
				Raw:      []byte(`{"schemaVersion":1}`),
			},
		}},
	}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	diagnostics, err := store.PluginDataDiagnostics(ctx)
	if err != nil {
		t.Fatalf("PluginDataDiagnostics: %v", err)
	}
	if len(diagnostics) != 1 ||
		diagnostics[0].PluginID != hugePluginID[:plugindata.MaxDiagnosticPluginIDBytes] {
		t.Fatalf("stored diagnostic = %+v", diagnostics)
	}
	raw, found, err := store.QuarantinedNamespace(ctx, plugindata.RecordHost,
		"h1", hugePluginID)
	if err != nil || !found || len(raw) == 0 {
		t.Fatalf("bounded Plugin ID lookup = %q, found = %v, err = %v",
			raw, found, err)
	}
	if err := store.ClearPluginDataQuarantine(ctx, plugindata.RecordHost,
		"h1", hugePluginID); err != nil {
		t.Fatalf("ClearPluginDataQuarantine: %v", err)
	}
	if diagnostics, err = store.PluginDataDiagnostics(ctx); err != nil {
		t.Fatalf("PluginDataDiagnostics after clear: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("bounded Plugin ID quarantine was not cleared: %+v", diagnostics)
	}
}
