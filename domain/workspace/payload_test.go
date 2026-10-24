package workspace

import (
	"encoding/json"
	"reflect"
	"testing"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

func validDescriptor() SessionDescriptor {
	return SessionDescriptor{Kind: SessionSSH, HostID: "h1", Hostname: "example.com", Port: 22, Username: "root", Label: "web"}
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
			{ID: "t2", Number: 2, Title: "work", Copy: 1, Root: root, FocusPath: []int{1}},
		},
		Panes: []RestoredPane{
			{ID: "p1", Session: validDescriptor()},
			{ID: "p2", Session: SessionDescriptor{Kind: SessionLocal, Number: 3, Dir: "/tmp"}},
			{ID: "p3", Session: validDescriptor()},
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
	if got.Tabs[0].AutoTitle != true || got.Tabs[1].Title != "work" || got.Tabs[1].Copy != 1 {
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
				Panes:   []RestoredPane{{ID: "p", Session: good}},
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
				Panes: []RestoredPane{{ID: "p", Session: good}},
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
				Panes: []RestoredPane{{ID: "p", Session: good}, {ID: "q", Session: good}},
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
			{ID: "p1", Session: validDescriptor()},
			{ID: "p2", Session: validDescriptor()},
			{ID: "p3", Session: validDescriptor()},
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
			{ID: "ssh", Session: validDescriptor()},
			{ID: "bad", Session: SessionDescriptor{Kind: "telnet"}},
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

func TestPayloadJSONRoundTrips(t *testing.T) {
	payload := Payload{
		Version:   PayloadVersion,
		ActiveTab: 0,
		Tabs: []RestoredTab{
			{ID: "t", Number: 1, AutoTitle: true, Root: NewPaneNode("p1"), FocusPath: []int{}},
		},
		Panes: []RestoredPane{{ID: "p1", Session: validDescriptor()}},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Payload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := SanitizePayload(decoded); !reflect.DeepEqual(got, SanitizePayload(payload)) {
		t.Fatalf("round trip = %+v, want %+v", got, payload)
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
			{ID: "a1", Session: validDescriptor()},
			{ID: "b1", Session: validDescriptor()},
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
