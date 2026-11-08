package historysync

import (
	"errors"
	"maps"
	"reflect"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

var testRecordedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func validCapabilities() Capabilities {
	return Capabilities{
		ServerVersion: PinnedAtuinVersion,
		Major:         PinnedAtuinMajor,
		Minor:         24,
		Patch:         5,
		PageSize:      100,
		Values: map[string]int{
			CapabilityServer:   1,
			CapabilityPageSize: 1,
		},
	}
}

func TestCapabilitiesFailClosed(t *testing.T) {
	valid := validCapabilities()
	if err := valid.Check(); err != nil {
		t.Fatalf("valid capabilities: %v", err)
	}

	tests := []struct {
		name  string
		apply func(*Capabilities)
	}{
		{"unknown major", func(c *Capabilities) { c.Major = 17 }},
		{"future major", func(c *Capabilities) { c.Major = 19 }},
		{"missing capabilities capability", func(c *Capabilities) { delete(c.Values, CapabilityServer) }},
		{"missing page size capability", func(c *Capabilities) { delete(c.Values, CapabilityPageSize) }},
		{"zero page size", func(c *Capabilities) { c.PageSize = 0 }},
		{"unknown enforced capability", func(c *Capabilities) {
			c.Enforced = []string{"sh.atuin.server/records.packfile"}
		}},
	}
	for _, test := range tests {
		capabilities := validCapabilities()
		capabilities.Values = maps.Clone(valid.Values)
		test.apply(&capabilities)
		if err := capabilities.Check(); !errors.Is(err, ErrIncompatibleServer) {
			t.Errorf("%s: Check = %v, want ErrIncompatibleServer", test.name, err)
		}
	}
}

func TestCapabilitiesIgnoreAnUnenforcedUnknownCapability(t *testing.T) {
	capabilities := validCapabilities()
	capabilities.Values = maps.Clone(capabilities.Values)
	capabilities.Values["sh.atuin.server/records.packfile"] = 1
	if err := capabilities.Check(); err != nil {
		t.Fatalf("advertised but unenforced packfile: %v", err)
	}
}

func TestHasHistoryIgnoresOtherTagsAndEmptySeries(t *testing.T) {
	if HasHistory(nil) {
		t.Fatal("nil status reported history")
	}
	if HasHistory([]SeriesStatus{{Series: Series{SeriesHost: "w", Tag: "kv"}, MaxIndex: 4}}) {
		t.Fatal("a kv series reported history")
	}
	if HasHistory([]SeriesStatus{{Series: Series{SeriesHost: "w", Tag: domainhistory.RecordTagHistory}, MaxIndex: -1}}) {
		t.Fatal("an empty history series reported history")
	}
	if !HasHistory([]SeriesStatus{{Series: Series{SeriesHost: "w", Tag: domainhistory.RecordTagHistory}, MaxIndex: 0}}) {
		t.Fatal("a history series at index 0 was not reported")
	}
}

func TestSyncRecordValid(t *testing.T) {
	entry := &domainhistory.Entry{ID: "id", Scope: "local", Command: "ls"}
	base := SyncRecord{
		ID: "record", SeriesHost: "writer", Tag: domainhistory.RecordTagHistory,
		Index: 0, RecordedAt: testRecordedAt, Version: 2,
		Kind: domainhistory.RecordKindCreate, HistoryID: "id", Entry: entry,
	}
	if !base.Valid() {
		t.Fatal("a valid create record was rejected")
	}
	withoutEntry := base
	withoutEntry.Entry = nil
	if withoutEntry.Valid() {
		t.Fatal("a create record without an entry was accepted")
	}
	deletion := base
	deletion.Kind = domainhistory.RecordKindDelete
	deletion.Entry = nil
	if deletion.Valid() {
		t.Fatal("a delete record without a deletion time was accepted")
	}
	deletion.DeletedAt = testRecordedAt
	if !deletion.Valid() {
		t.Fatal("a valid delete record was rejected")
	}
}

// TestRemotePortHasNoStoreDeleteOperation pins the sync port to the
// non-destructive operations it implements. Deletion travels as ordinary
// delete records through Upload; the delete-all-store endpoint is never
// reachable from the application.
func TestRemotePortHasNoStoreDeleteOperation(t *testing.T) {
	remoteType := reflect.TypeOf((*Remote)(nil)).Elem()
	expected := map[string]bool{
		"Authenticate":      true,
		"Capabilities":      true,
		"RecordStatus":      true,
		"Download":          true,
		"Upload":            true,
		"ValidateMasterKey": true,
		"Close":             true,
	}
	for index := 0; index < remoteType.NumMethod(); index++ {
		name := remoteType.Method(index).Name
		if !expected[name] {
			t.Errorf("Remote exposes %q; the delete-all-store endpoint must not be reachable", name)
		}
		delete(expected, name)
	}
	for name := range expected {
		t.Errorf("Remote is missing expected operation %q", name)
	}
}
