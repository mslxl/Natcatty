package workspace

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

func validDescriptor() SessionDescriptor {
	return SessionDescriptor{Kind: SessionSSH, HostID: "h1", Hostname: "example.com", Port: 22, Username: "root", Label: "web"}
}

func restoredSessionPane(id string, descriptor SessionDescriptor) RestoredPane {
	return RestoredPane{ID: model.PaneID(id), PaneDescriptor: NewSessionPaneDescriptor(descriptor)}
}

func TestSanitizePayloadKeepsAValidWorkspace(t *testing.T) {
	root, err := SplitAt(NewPaneNode("p3"), "p3", "p2", "s1", SideRight)
	if err != nil {
		t.Fatal(err)
	}
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 1,
		Tabs: []RestoredTab{
			{ID: "t1", Number: 1, AutoTitle: true, Root: NewPaneNode("p1"), FocusPath: []int{}},
			{ID: "t2", Number: 2, Title: "work", GeneratedTitle: true, Copy: 1, Root: root, FocusPath: []int{1}},
		},
		Panes: []RestoredPane{
			restoredSessionPane("p1", validDescriptor()),
			restoredSessionPane("p2", SessionDescriptor{Kind: SessionLocal, Number: 3, Dir: "/tmp"}),
			restoredSessionPane("p3", validDescriptor()),
		},
	}

	got := SanitizePayload(payload)
	t.Logf("tab1 root %+v focus %v; tab2 root %+v focus %v", got.Tabs[0].Root, got.Tabs[0].FocusPath, got.Tabs[1].Root, got.Tabs[1].FocusPath)
	if got.Version != PayloadVersion || got.ActiveTab != 1 || len(got.Tabs) != 2 {
		t.Fatalf("payload = %+v, want two tabs with active 1", got)
	}
	if !reflect.DeepEqual(got.Tabs[1].FocusPath, []int{1}) {
		t.Fatalf("focus path = %v, want [1]", got.Tabs[1].FocusPath)
	}
	if got.Tabs[0].AutoTitle != true || got.Tabs[1].Title != "work" ||
		!got.Tabs[1].GeneratedTitle || got.Tabs[1].Copy != 1 {
		t.Fatalf("titles were not preserved: %+v", got.Tabs)
	}
}

func TestSanitizePayloadIsSafeWithACorruptPayload(t *testing.T) {
	good := validDescriptor()
	tests := []struct {
		name    string
		payload Payload
		want    int
	}{
		{
			name: "unsupported version",
			payload: Payload{
				Version: 99,
				Tabs:    []RestoredTab{{ID: "t", Root: NewPaneNode("p")}},
				Panes:   []RestoredPane{restoredSessionPane("p", good)},
			},
			want: 0,
		},
		{
			name: "pane without a descriptor is dropped",
			payload: Payload{
				Version: PayloadVersion,
				Tabs:    []RestoredTab{{ID: "t", Root: NewPaneNode("p")}},
			},
			want: 0,
		},
		{
			name: "duplicate pane is dropped and the split collapses",
			payload: Payload{
				Version: PayloadVersion,
				Tabs: []RestoredTab{{
					ID:   "t",
					Root: NewSplitNode("s", Vertical, 50, NewPaneNode("p"), NewPaneNode("p")),
				}},
				Panes: []RestoredPane{restoredSessionPane("p", good)},
			},
			want: 1,
		},
		{
			name: "a tab can appear only once",
			payload: Payload{
				Version: PayloadVersion,
				Tabs: []RestoredTab{
					{ID: "t", Root: NewPaneNode("p")},
					{ID: "t", Root: NewPaneNode("q")},
				},
				Panes: []RestoredPane{restoredSessionPane("p", good), restoredSessionPane("q", good)},
			},
			want: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SanitizePayload(test.payload); len(got.Tabs) != test.want {
				t.Fatalf("tabs = %d, want %d: %+v", len(got.Tabs), test.want, got)
			}
		})
	}
}

func TestSanitizePayloadRepairsFocusAndSplitIdentity(t *testing.T) {
	root := NewSplitNode("", Vertical, 0,
		NewPaneNode("p1"),
		NewSplitNode("", Horizontal, 140, NewPaneNode("p2"), NewPaneNode("p3")),
	)
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 9,
		Tabs: []RestoredTab{
			{ID: "t", Root: root, FocusPath: []int{0, 0}},
		},
		Panes: []RestoredPane{
			restoredSessionPane("p1", validDescriptor()),
			restoredSessionPane("p2", validDescriptor()),
			restoredSessionPane("p3", validDescriptor()),
		},
	}

	got := SanitizePayload(payload)
	if got.ActiveTab != 0 {
		t.Fatalf("active tab = %d, want 0", got.ActiveTab)
	}
	tab := got.Tabs[0]
	if !reflect.DeepEqual(tab.FocusPath, []int{0}) {
		t.Fatalf("focus path = %v, want the first leaf [0]", tab.FocusPath)
	}
	if !tab.Root.IsPane() && (tab.Root.SplitID == "" || tab.Root.Ratio != DefaultRatio) {
		t.Fatalf("root split was not repaired: %+v", tab.Root)
	}
	if !tab.Root.Second.IsPane() && tab.Root.Second.Ratio != DefaultRatio {
		t.Fatalf("nested split ratio = %v, want 50", tab.Root.Second.Ratio)
	}
	if !tab.Root.Second.IsPane() && tab.Root.Second.SplitID == "" {
		t.Fatal("nested split id was not repaired")
	}
}

func TestSanitizePayloadDropsInvalidSessionKinds(t *testing.T) {
	payload := Payload{
		Version: PayloadVersion,
		Tabs: []RestoredTab{
			{ID: "t", Root: NewSplitNode("s", Vertical, 50, NewPaneNode("ssh"), NewPaneNode("bad"))},
		},
		Panes: []RestoredPane{
			restoredSessionPane("ssh", validDescriptor()),
			restoredSessionPane("bad", SessionDescriptor{Kind: "telnet"}),
		},
	}
	got := SanitizePayload(payload)
	if len(got.Tabs) != 1 || got.Tabs[0].Root.PaneID != "ssh" {
		t.Fatalf("payload = %+v, want the split collapsed to the valid pane", got)
	}
}

func TestSerialDescriptorRequiresAValidConfig(t *testing.T) {
	descriptor := SessionDescriptor{
		Kind: SessionSerial,
		Serial: &domainserial.Config{
			Path: "/dev/ttyUSB0", BaudRate: 57600, DataBits: 7,
			StopBits: domainserial.StopBitsTwo, Parity: domainserial.ParityEven,
			FlowControl: domainserial.FlowRTSCTS,
		},
	}
	if !descriptor.Valid() {
		t.Fatal("a complete serial descriptor is invalid")
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionDescriptor
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, descriptor) {
		t.Fatalf("serial descriptor round trip = %+v, want %+v", decoded, descriptor)
	}

	for _, invalid := range []SessionDescriptor{
		{Kind: SessionSerial},
		{Kind: SessionSerial, Serial: &domainserial.Config{Path: "COM1", BaudRate: 115200, DataBits: 9}},
		{Kind: SessionSerial, Serial: &domainserial.Config{Path: "", BaudRate: 115200, DataBits: 8}},
	} {
		if invalid.Valid() {
			t.Fatalf("descriptor %+v is valid, want invalid", invalid)
		}
	}
}

func TestTelnetDescriptorRequiresTheSavedHostAndAddress(t *testing.T) {
	descriptor := SessionDescriptor{
		Kind: SessionTelnet, HostID: "host-telnet", Hostname: "console.local", Port: 23,
		Label: "console", Username: "admin",
	}
	if !descriptor.Valid() {
		t.Fatal("a complete Telnet descriptor is invalid")
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionDescriptor
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, descriptor) {
		t.Fatalf("Telnet descriptor round trip = %+v, want %+v", decoded, descriptor)
	}
	for _, invalid := range []SessionDescriptor{
		{Kind: SessionTelnet},
		{Kind: SessionTelnet, Hostname: "console.local"},
		{Kind: SessionTelnet, HostID: "host-telnet"},
	} {
		if invalid.Valid() {
			t.Fatalf("descriptor %+v is valid, want invalid", invalid)
		}
	}
}

// TestETDescriptorRoundTripsOnlyTheResumeLocator: the persisted descriptor
// stores an opaque locator for the secret-store entry, never the ET session id
// or passkey themselves.
func TestETDescriptorRoundTripsOnlyTheResumeLocator(t *testing.T) {
	descriptor := SessionDescriptor{
		Kind: SessionET, HostID: "host-et", Hostname: "et.example.com", Port: 2022,
		Username: "alice", ResumeKey: "et-resume-0123456789abcdef0123456789abcdef",
	}
	if !descriptor.Valid() {
		t.Fatal("a complete ET descriptor is invalid")
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionDescriptor
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, descriptor) {
		t.Fatalf("ET descriptor round trip = %+v, want %+v", decoded, descriptor)
	}
	if !bytes.Contains(data, []byte(`"resumeKey":"et-resume-`)) {
		t.Fatalf("ET descriptor JSON = %s, want the resume locator", data)
	}
}

func TestPayloadJSONRoundTrips(t *testing.T) {
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 0,
		Tabs: []RestoredTab{
			{ID: "t", Number: 1, AutoTitle: true, Root: NewPaneNode("p1"), FocusPath: []int{}},
		},
		Panes: []RestoredPane{restoredSessionPane("p1", validDescriptor())},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"kind":"session"`)) {
		t.Fatalf("payload JSON = %s, want an explicit Session content kind", data)
	}
	var decoded Payload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := SanitizePayload(decoded); !reflect.DeepEqual(got, SanitizePayload(payload)) {
		t.Fatalf("round trip = %+v, want %+v", got, payload)
	}
}

// TestSanitizePayloadTreatsLegacyPanesAsSessions: a payload written before
// content kinds existed has no kind field; it must restore as Session content
// with the kind made explicit.
func TestSanitizePayloadTreatsLegacyPanesAsSessions(t *testing.T) {
	const legacy = `{
		"version": 1,
		"activeTab": 0,
		"tabs": [{"id": "t", "number": 1, "root": {"type": "pane", "paneId": "p"}}],
		"panes": [{"id": "p", "session": {"kind": "ssh", "hostId": "h1",
			"hostname": "example.com", "port": 22, "username": "root"}}]
	}`
	var payload Payload
	if err := json.Unmarshal([]byte(legacy), &payload); err != nil {
		t.Fatal(err)
	}
	got := SanitizePayload(payload)
	if len(got.Tabs) != 1 || len(got.Panes) != 1 {
		t.Fatalf("sanitized legacy payload = %+v, want one tab and one pane", got)
	}
	pane := got.Panes[0]
	if pane.Kind != PaneKindSession {
		t.Fatalf("legacy pane kind = %q, want %q", pane.Kind, PaneKindSession)
	}
	session, ok := pane.SessionContent()
	if !ok || !session.Valid() || session.Kind != SessionSSH {
		t.Fatalf("legacy pane session = %+v, %v; want a valid SSH Session", session, ok)
	}
}

// TestSanitizePayloadKeepsUnsupportedKindsAsUnavailable: a View or an unknown
// content kind never drops its Pane or blocks the Workspace; it becomes an
// unavailable placeholder.
func TestSanitizePayloadKeepsUnsupportedKindsAsUnavailable(t *testing.T) {
	root := NewSplitNode("s", Vertical, 50,
		NewPaneNode("session"), NewPaneNode("view"))
	root = NewSplitNode("s2", Vertical, 50, root, NewPaneNode("unknown"))
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 0,
		Tabs:      []RestoredTab{{ID: "t", Root: root, FocusPath: []int{0}}},
		Panes: []RestoredPane{
			restoredSessionPane("session", validDescriptor()),
			{ID: "view", PaneDescriptor: PaneDescriptor{
				Kind: PaneKindView,
				View: &ViewPaneDescriptor{ViewID: "acme.notes", ContextKey: "global"},
			}},
			{ID: "unknown", PaneDescriptor: PaneDescriptor{Kind: "future-kind"}},
		},
	}
	got := SanitizePayload(payload)
	if len(got.Tabs) != 1 || len(got.Panes) != 3 {
		t.Fatalf("sanitized payload = %+v, want the full split layout", got)
	}
	if ids := CollectPaneIDs(got.Tabs[0].Root); !reflect.DeepEqual(ids,
		[]model.PaneID{"session", "view", "unknown"}) {
		t.Fatalf("panes = %v, want all three leaves preserved", ids)
	}
	byID := make(map[model.PaneID]PaneDescriptor, len(got.Panes))
	for _, pane := range got.Panes {
		byID[pane.ID] = pane.PaneDescriptor
	}
	if byID["session"].Available() != true || byID["session"].Kind != PaneKindSession {
		t.Fatalf("session descriptor = %+v, want available Session content", byID["session"])
	}
	if byID["view"].Kind != PaneKindView || byID["view"].Available() ||
		byID["view"].View == nil || byID["view"].View.ViewID != "acme.notes" {
		t.Fatalf("view descriptor = %+v, want the reserved View shape as unavailable", byID["view"])
	}
	if byID["unknown"].Kind != "future-kind" || byID["unknown"].Available() {
		t.Fatalf("unknown descriptor = %+v, want an unavailable placeholder", byID["unknown"])
	}
}

// TestSanitizePayloadCapsViewState keeps an oversized View state blob from
// entering the Workspace while preserving the placeholder.
func TestSanitizePayloadCapsViewState(t *testing.T) {
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 0,
		Tabs:      []RestoredTab{{ID: "t", Root: NewPaneNode("view")}},
		Panes: []RestoredPane{{ID: "view", PaneDescriptor: PaneDescriptor{
			Kind: PaneKindView,
			View: &ViewPaneDescriptor{ViewID: "acme.notes", State: bytes.Repeat([]byte("x"), MaxViewStateBytes+1)},
		}}},
	}
	got := SanitizePayload(payload)
	if len(got.Panes) != 1 || got.Panes[0].View == nil || len(got.Panes[0].View.State) != 0 {
		t.Fatalf("view descriptor = %+v, want the placeholder with state dropped", got.Panes)
	}
}

func TestEmptyPayloadHasNoTabsAndAVaultFocus(t *testing.T) {
	empty := EmptyPayload()
	if empty.Version != PayloadVersion || empty.ActiveTab != -1 || len(empty.Tabs) != 0 {
		t.Fatalf("empty payload = %+v", empty)
	}
	if got := SanitizePayload(empty); len(got.Tabs) != 0 || got.ActiveTab != -1 {
		t.Fatalf("sanitized empty payload = %+v", got)
	}
}

func TestFocusPathTo(t *testing.T) {
	root := NewSplitNode("s", Vertical, 40,
		NewPaneNode("p1"),
		NewSplitNode("s2", Horizontal, 60, NewPaneNode("p2"), NewPaneNode("p3")),
	)
	if got := FocusPath(root, "p3"); !reflect.DeepEqual(got, []int{1, 1}) {
		t.Fatalf("focus path = %v, want [1 1]", got)
	}
	if got := FocusPath(root, "missing"); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("missing focus path = %v, want the first leaf", got)
	}
	if got := FocusPath(NewPaneNode("only"), "only"); len(got) != 0 {
		t.Fatalf("root pane focus path = %v, want empty", got)
	}
}

func TestFocusPathIgnoresAMalformedPath(t *testing.T) {
	root := NewSplitNode("s", Vertical, 50, NewPaneNode("p1"), NewPaneNode("p2"))
	if got := SanitizeFocusPath(root, []int{5, 0}); !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("focus path = %v, want the first leaf", got)
	}
	if got := SanitizeFocusPath(root, []int{1}); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("valid focus path = %v, want [1]", got)
	}
}

func TestSanitizePayloadMapsActiveTabPastDroppedTabs(t *testing.T) {
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 1,
		Tabs: []RestoredTab{
			{ID: "dropped", Root: NewPaneNode("missing")},
			{ID: "a", Root: NewPaneNode("a1")},
			{ID: "b", Root: NewPaneNode("b1")},
		},
		Panes: []RestoredPane{
			restoredSessionPane("a1", validDescriptor()),
			restoredSessionPane("b1", validDescriptor()),
		},
	}
	got := SanitizePayload(payload)
	if len(got.Tabs) != 2 || got.ActiveTab != 0 {
		t.Fatalf("tabs = %d, active = %d; want 2 and 0", len(got.Tabs), got.ActiveTab)
	}
	if got.Tabs[got.ActiveTab].ID != "a" {
		t.Fatalf("active Tab = %q, want a", got.Tabs[got.ActiveTab].ID)
	}
}

func TestPluginSessionDescriptorRoundTripsBoundedRestoreState(t *testing.T) {
	descriptor := SessionDescriptor{
		Kind:     SessionPlugin,
		Label:    "Loopback",
		HostID:   "host-loop",
		Hostname: "loopback",
		Protocol: "x-acme-loopback",
		Provider: "acme.loopback",
		Restore:  []byte(`{"token":"opaque"}`),
	}
	if !descriptor.Valid() {
		t.Fatal("plugin descriptor is not valid")
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"restore":"eyJ0b2tlbiI6Im9wYXF1ZSJ9"`)) {
		t.Fatalf("restore is not bounded base64 JSON: %s", encoded)
	}
	var decoded SessionDescriptor
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, descriptor) {
		t.Fatalf("round trip = %+v, want %+v", decoded, descriptor)
	}
	payload := Payload{
		Version: PayloadVersion,
		Tabs:    []RestoredTab{{ID: "t", Root: NewPaneNode("p")}},
		Panes:   []RestoredPane{restoredSessionPane("p", descriptor)},
	}
	got := SanitizePayload(payload)
	if len(got.Panes) != 1 || got.Panes[0].Session == nil {
		t.Fatalf("sanitized plugin pane = %+v", got.Panes)
	}
	if got.Panes[0].Session.Kind != SessionPlugin ||
		got.Panes[0].Session.Provider != "acme.loopback" ||
		string(got.Panes[0].Session.Restore) != `{"token":"opaque"}` {
		t.Fatalf("restored plugin descriptor = %+v", got.Panes[0].Session)
	}
}

func TestPluginSessionDescriptorFailsClosedOnBadProtocolAndOversizedRestore(t *testing.T) {
	base := SessionDescriptor{
		Kind: SessionPlugin, HostID: "h", Hostname: "loopback",
		Protocol: "x-acme-loopback", Provider: "acme.loopback",
	}
	tests := []SessionDescriptor{
		{Kind: SessionPlugin, HostID: "h", Hostname: "loopback", Protocol: "ssh"},
		{Kind: SessionPlugin, HostID: "h", Hostname: "loopback", Protocol: "acme-loopback"},
		{Kind: SessionPlugin, HostID: "h", Protocol: "x-acme-loopback"},
		func() SessionDescriptor {
			value := base
			value.Restore = make([]byte, MaxSessionRestoreBytes+1)
			return value
		}(),
	}
	for i, descriptor := range tests {
		if descriptor.Valid() {
			t.Fatalf("descriptor %d unexpectedly valid: %+v", i, descriptor)
		}
	}
	oversized := base
	oversized.Restore = make([]byte, MaxSessionRestoreBytes+1)
	payload := Payload{
		Version: PayloadVersion,
		Tabs:    []RestoredTab{{ID: "t", Root: NewPaneNode("p")}},
		Panes:   []RestoredPane{restoredSessionPane("p", oversized)},
	}
	got := SanitizePayload(payload)
	if len(got.Panes) != 1 || got.Panes[0].Session == nil || got.Panes[0].Session.Restore != nil {
		t.Fatalf("oversized restore was not dropped while keeping the pane: %+v", got.Panes)
	}
}

func TestPayloadVersionTwoReadsVersionOne(t *testing.T) {
	payload := Payload{
		Version: 1,
		Tabs:    []RestoredTab{{ID: "t", Root: NewPaneNode("p")}},
		Panes:   []RestoredPane{restoredSessionPane("p", validDescriptor())},
	}
	got := SanitizePayload(payload)
	if got.Version != PayloadVersion || len(got.Tabs) != 1 || len(got.Panes) != 1 {
		t.Fatalf("version 1 payload = %+v", got)
	}
}

// TestSanitizePayloadKeepsPerPlacementViewDescriptors proves a View stored
// outside a work Tab (page, window, aside) survives restore while a diagram
// placement inside a Tab is left to that Tab's Pane descriptor.
func TestSanitizePayloadKeepsPerPlacementViewDescriptors(t *testing.T) {
	payload := Payload{
		Version: PayloadVersion,
		Views: []RestoredView{
			{Placement: ViewPlacementPage, ViewID: "acme.notes", ContextKey: "global", Title: " Notes ", State: []byte("a"), Order: 1},
			{Placement: ViewPlacementWindow, ViewID: "acme.logs", ContextKey: "host-1"},
			{Placement: ViewPlacementAside, ViewID: "acme.tools", ContextKey: "global", Order: 2},
			{Placement: ViewPlacementPane, ViewID: "acme.ignored", ContextKey: "global"},
			{Placement: ViewPlacementTab, ViewID: "acme.ignored", ContextKey: "global"},
			{Placement: "shell", ViewID: "acme.bad"},
			{Placement: ViewPlacementPage, ViewID: " "},
		},
	}
	got := SanitizePayload(payload)
	if len(got.Views) != 3 {
		t.Fatalf("views = %+v, want three placement descriptors", got.Views)
	}
	if got.Views[0].Placement != ViewPlacementPage || got.Views[0].Title != "Notes" ||
		string(got.Views[0].State) != "a" || got.Views[0].Order != 1 {
		t.Fatalf("page descriptor = %+v", got.Views[0])
	}
	if got.Views[1].Placement != ViewPlacementWindow || got.Views[1].ViewID != "acme.logs" {
		t.Fatalf("window descriptor = %+v", got.Views[1])
	}
	if got.Views[2].Placement != ViewPlacementAside || got.Views[2].ViewID != "acme.tools" {
		t.Fatalf("aside descriptor = %+v", got.Views[2])
	}
}

// TestSanitizePayloadKeepsOnlyTheFirstPagePerView proves the page singleton
// rule is repaired on load instead of letting two instances claim the slot.
func TestSanitizePayloadKeepsOnlyTheFirstPagePerView(t *testing.T) {
	payload := Payload{
		Version: PayloadVersion,
		Views: []RestoredView{
			{Placement: ViewPlacementPage, ViewID: "acme.notes", ContextKey: "one"},
			{Placement: ViewPlacementPage, ViewID: "acme.notes", ContextKey: "two"},
			{Placement: ViewPlacementPage, ViewID: "acme.other"},
		},
	}
	got := SanitizePayload(payload)
	if len(got.Views) != 2 {
		t.Fatalf("views = %+v, want the first page per view", got.Views)
	}
	if got.Views[0].ContextKey != "one" || got.Views[1].ViewID != "acme.other" {
		t.Fatalf("views = %+v", got.Views)
	}
}

// TestSanitizePayloadCapsPlacementViewState drops oversized blob state but
// keeps the placement, so the View still restores as an unavailable surface.
func TestSanitizePayloadCapsPlacementViewState(t *testing.T) {
	payload := Payload{
		Version: PayloadVersion,
		Views: []RestoredView{{
			Placement: ViewPlacementAside,
			ViewID:    "acme.notes",
			State:     bytes.Repeat([]byte("x"), MaxViewStateBytes+1),
		}},
	}
	got := SanitizePayload(payload)
	if len(got.Views) != 1 || len(got.Views[0].State) != 0 {
		t.Fatalf("views = %+v, want the placement with dropped state", got.Views)
	}
}

// TestViewPaneDescriptorPlacementDefaultsToPane keeps payloads written before
// View Tab placement existed restoring as ordinary view Panes.
func TestViewPaneDescriptorPlacementDefaultsToPane(t *testing.T) {
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 0,
		Tabs:      []RestoredTab{{ID: "tab-1", Root: NewPaneNode("pane-view")}},
		Panes: []RestoredPane{{
			ID: "pane-view",
			PaneDescriptor: PaneDescriptor{
				Kind: PaneKindView,
				View: &ViewPaneDescriptor{ViewID: "acme.notes"},
			},
		}},
	}
	got := SanitizePayload(payload)
	if len(got.Panes) != 1 || got.Panes[0].View == nil ||
		got.Panes[0].View.PlacementOrDefault() != ViewPlacementPane {
		t.Fatalf("pane descriptor = %+v", got.Panes)
	}
}
