package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testPanelSection() PanelSection {
	return PanelSection{
		ID:      "health",
		Title:   "Host health",
		Order:   10,
		Refresh: PanelRefreshFast,
		Compact: true,
		Actions: []PanelAction{
			{ID: "restart", Label: "Restart", Destructive: true},
			{ID: "copy", Label: "Copy"},
		},
	}
}

func TestDecodePanelPayloadAcceptsTheClosedDescriptorSet(t *testing.T) {
	enabled := true
	payload := PanelPayload{Items: []PanelDescriptor{
		{Kind: PanelDescriptorMetric, Label: "Load", Value: "0.42", Unit: "%",
			Level: PanelLevelOK},
		{Kind: PanelDescriptorSection, Label: "Details", Collapsible: true,
			Rows: []PanelRow{{Label: "Kernel", Value: "6.1"}}},
		{Kind: PanelDescriptorTable, Columns: []string{"Name", "State"},
			TableRows: []PanelTableRow{{Key: "web", Cells: []string{"web", "up"}}}},
		{Kind: PanelDescriptorStatus, Label: "Daemon", Detail: "running",
			Level: PanelLevelWarning},
		{Kind: PanelDescriptorAction, Action: "restart", RowKey: "web",
			Enabled: &enabled},
		{Kind: PanelDescriptorTimeseries, Label: "RPS",
			Points: []float64{1, 2, 3}, Scale: 10},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := DecodePanelPayload(raw, testPanelSection())
	if err != nil {
		t.Fatalf("DecodePanelPayload: %v", err)
	}
	if len(decoded.Items) != len(payload.Items) {
		t.Fatalf("decoded %d descriptors, want %d", len(decoded.Items), len(payload.Items))
	}
	for i, item := range decoded.Items {
		if item.Kind != payload.Items[i].Kind {
			t.Errorf("descriptor %d kind = %q, want %q", i, item.Kind, payload.Items[i].Kind)
		}
	}
}

func TestDecodePanelPayloadRejectsANativeUITree(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"unknown node field", `{"items":[{"kind":"metric","label":"load","children":[]}]}`},
		{"native tree kind", `{"items":[{"kind":"tree","label":"root"}]}`},
		{"markup field", `{"items":[{"kind":"status","label":"ok","html":"<b>ok</b>"}]}`},
		{"colour field", `{"items":[{"kind":"metric","label":"load","color":"#ff0000"}]}`},
		{"drawing field", `{"items":[{"kind":"timeseries","label":"x","svg":"<svg/>"}]}`},
		{"unknown top-level field", `{"items":[],"tree":{}}`},
		{"trailing value", `{"items":[]}{"items":[]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodePanelPayload([]byte(test.raw), testPanelSection()); err == nil {
				t.Fatalf("payload %s was accepted", test.raw)
			}
		})
	}
}

func TestDecodePanelPayloadEnforcesDescriptorBounds(t *testing.T) {
	section := testPanelSection()
	section.Refresh = PanelRefreshPush
	tooManyRows := make([]PanelRow, MaxPanelRows+1)
	for i := range tooManyRows {
		tooManyRows[i] = PanelRow{Label: "l", Value: "v"}
	}
	tooManyColumns := make([]string, MaxPanelColumns+1)
	for i := range tooManyColumns {
		tooManyColumns[i] = "c"
	}
	tooManyPoints := make([]float64, MaxPanelPoints+1)
	tooManyItems := make([]PanelDescriptor, MaxPanelItems+1)
	for i := range tooManyItems {
		tooManyItems[i] = PanelDescriptor{Kind: PanelDescriptorMetric, Label: "load"}
	}
	tests := []struct {
		name    string
		section PanelSection
		payload PanelPayload
	}{
		{"too many descriptors", section, PanelPayload{Items: tooManyItems}},
		{"too many rows", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorSection, Label: "s", Rows: tooManyRows}}}},
		{"too many columns", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorTable, Columns: tooManyColumns}}}},
		{"too many points", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorTimeseries, Points: tooManyPoints}}}},
		{"undeclared action", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorAction, Action: "builtin-kill"}}}},
		{"duplicate row key", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorTable, Columns: []string{"c"}, TableRows: []PanelTableRow{
				{Key: "web", Cells: []string{"1"}},
				{Key: "web", Cells: []string{"2"}},
			}}}}},
		{"cell count mismatch", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorTable, Columns: []string{"a", "b"},
				TableRows: []PanelTableRow{{Key: "r", Cells: []string{"1"}}}}}}},
		{"unknown level", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorStatus, Label: "s", Level: "purple"}}}},
		{"oversize string", section, PanelPayload{Items: []PanelDescriptor{
			{Kind: PanelDescriptorMetric, Label: strings.Repeat("x", MaxPanelStringBytes+1)}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := DecodePanelPayload(raw, test.section); err == nil {
				t.Fatal("an over-limit payload was accepted")
			}
		})
	}

	// A push is capped at 4 KiB; a fast section has the 256 KiB payload budget.
	value := strings.Repeat("y", MaxPanelStringBytes-100)
	big := PanelPayload{Items: []PanelDescriptor{{
		Kind: PanelDescriptorSection, Label: "big",
		Rows: []PanelRow{
			{Label: "a", Value: value},
			{Label: "b", Value: value},
			{Label: "c", Value: value},
		},
	}}}
	raw, _ := json.Marshal(big)
	push := testPanelSection()
	push.Refresh = PanelRefreshPush
	if _, err := DecodePanelPayload(raw, push); err == nil {
		t.Fatal("a push payload above 4 KiB was accepted")
	}
	fast := testPanelSection()
	fast.Refresh = PanelRefreshFast
	if _, err := DecodePanelPayload(raw, fast); err != nil {
		t.Fatalf("a fast payload within 256 KiB was rejected: %v", err)
	}
}

func TestPanelRefreshCadenceFloorsAndDefaults(t *testing.T) {
	push := PanelSection{ID: "p", Refresh: PanelRefreshPush}
	if got := IntervalFor(push); got != 0 {
		t.Errorf("push interval = %v, want no host polling", got)
	}
	fast := PanelSection{ID: "f", Refresh: PanelRefreshFast}
	if got := IntervalFor(fast); got != PanelFastDefault {
		t.Errorf("fast default = %v, want %v", got, PanelFastDefault)
	}
	if got := IntervalFor(fast); got < PanelFastFloor {
		t.Errorf("fast interval %v is below the %v floor", got, PanelFastFloor)
	}
	fast.Interval = 250
	if got := IntervalFor(fast); got < PanelFastFloor {
		t.Errorf("fast interval %v is below the %v floor", got, PanelFastFloor)
	}
	fast.Interval = 0
	fast.MinInterval = 10000
	if got := IntervalFor(fast); got != 10*time.Second {
		t.Errorf("fast interval = %v, want the declared 10s", got)
	}
	fast.MinInterval = 0
	fast.Interval = 5000
	if got := IntervalFor(fast); got != 5*time.Second {
		t.Errorf("fast interval = %v, want 5s", got)
	}
	snapshot := PanelSection{ID: "s", Refresh: PanelRefreshSnapshot}
	if got := IntervalFor(snapshot); got != PanelSnapshotCeiling {
		t.Errorf("snapshot default = %v, want %v", got, PanelSnapshotCeiling)
	}
	snapshot.Interval = 60000
	if got := IntervalFor(snapshot); got != PanelSnapshotCeiling {
		t.Errorf("snapshot interval was not capped: %v", got)
	}
	if got := PanelPushFloor; got != 100*time.Millisecond {
		t.Errorf("push floor = %v, want 100ms", got)
	}
}

func TestPanelSectionIntervalsUseTheDeclaredVocabulary(t *testing.T) {
	for _, milliseconds := range PanelIntervals {
		section := testPanelSection()
		section.Interval = milliseconds
		section.MinInterval = milliseconds
		if err := ValidatePanelSection(section); err != nil {
			t.Fatalf("declared interval %d ms was rejected: %v", milliseconds, err)
		}
	}
	section := testPanelSection()
	section.Interval = 1234
	if err := ValidatePanelSection(section); err == nil {
		t.Fatal("an undeclared interval validated")
	}
	section = testPanelSection()
	section.MinInterval = 750
	if err := ValidatePanelSection(section); err == nil {
		t.Fatal("an undeclared min_interval validated")
	}

	snapshot := testPanelSection()
	snapshot.Refresh = PanelRefreshSnapshot
	if err := ValidatePanelSection(snapshot); err != nil {
		t.Fatalf("a snapshot section with default intervals was rejected: %v", err)
	}
	snapshot.Interval = 500
	if err := ValidatePanelSection(snapshot); err == nil {
		t.Fatal("a snapshot faster than one second validated")
	}
	snapshot.Interval = 2000
	snapshot.MinInterval = 3000
	if err := ValidatePanelSection(snapshot); err != nil {
		t.Fatalf("a snapshot within the collector cadence was rejected: %v", err)
	}
	snapshot.MinInterval = 5000
	if err := ValidatePanelSection(snapshot); err == nil {
		t.Fatal("a snapshot min_interval beyond the collector cadence validated")
	}
}

func TestManifestSystemPanelContributionsRequireThePermission(t *testing.T) {
	manifest := validManifest()
	manifest.Contributions.SystemPanel = []PanelSection{testPanelSection()}
	err := manifest.Validate()
	if err == nil || !strings.Contains(err.Error(), "system_panel") {
		t.Fatalf("a section without system.panel validated: %v", err)
	}
	manifest.Permissions.Required = []string{string(PermissionSystemPanel)}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate with system.panel: %v", err)
	}
	manifest.Contributions.SystemPanel = append(manifest.Contributions.SystemPanel,
		testPanelSection())
	if err := manifest.Validate(); err == nil {
		t.Fatal("a duplicate section id validated")
	}
	manifest.Contributions.SystemPanel = []PanelSection{{
		ID: "caps", Title: "Caps", Refresh: PanelRefreshPush,
	}}
	manifest.Permissions.Required = []string{
		string(PermissionSystemPanel), string(PermissionSessionsRead),
	}
	manifest.Permissions.Scopes = map[string]Scope{
		string(PermissionSessionsRead): {Host: "host-1"},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("a Host-scoped sessions.read grant was rejected: %v", err)
	}
	manifest.Permissions.Scopes = map[string]Scope{
		string(PermissionSessionsRead): {Session: "session-1"},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("a Session-scoped sessions.read grant was rejected: %v", err)
	}

	// Only sessions.read may be Host-wide; the Host-scope exception must not
	// widen a control grant.
	manifest.Permissions.Required = []string{
		string(PermissionSystemPanel), string(PermissionSessionsControl),
	}
	manifest.Permissions.Scopes = map[string]Scope{
		string(PermissionSessionsControl): {Host: "host-1"},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("a Host-wide sessions.control grant validated")
	}
	manifest.Permissions.Scopes = map[string]Scope{
		string(PermissionSessionsControl): {Session: "session-1"},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("a Session-scoped sessions.control grant was rejected: %v", err)
	}
	if err := ValidatePermissionRequests(nil, []PermissionRequest{{
		Permission: PermissionSessionsRead,
		Scope:      Scope{Kind: ScopeSession, Host: "host-1", Session: "session-1"},
	}}); err == nil {
		t.Fatal("a session scope with both a Session and a Host validated")
	}
}

func TestSystemPanelCommandEncodingIsReservedAndRoundTrips(t *testing.T) {
	request := PanelRefreshRequest{
		Surface: PanelSurfaceSide, HostID: "host-1", SectionID: "health",
	}
	encoded := EncodePanelRefreshCommand(request)
	decoded, ok := ParsePanelRefreshCommand(encoded)
	if !ok || decoded != request {
		t.Fatalf("refresh command round trip = %+v, %v", decoded, ok)
	}
	action := PanelActionCommand{SectionID: "health", ActionID: "restart", RowKey: "web · 1"}
	decodedAction, ok := ParsePanelActionCommand(EncodePanelActionCommand(action))
	if !ok || decodedAction != action {
		t.Fatalf("action command round trip = %+v, %v", decodedAction, ok)
	}
	// A colon is not allowed in a declared command id, so the reserved prefix
	// can never collide with a plugin command.
	if !strings.HasPrefix(panelActionCommandPrefix, "systempanel.") ||
		!strings.HasSuffix(panelActionCommandPrefix, ":") {
		t.Fatalf("reserved prefix = %q", panelActionCommandPrefix)
	}
	if commandIDPattern.MatchString(panelActionCommandPrefix + "x") {
		t.Fatalf("a declared command id may contain a colon: %q", panelActionCommandPrefix)
	}
	if _, ok := ParsePanelActionCommand("plugin.acme.hello.greet"); ok {
		t.Fatal("a normal command parsed as a reserved action")
	}
}

func TestSystemPanelPermissionVocabularyHasNoCommandExecution(t *testing.T) {
	for _, info := range PermissionVocabulary() {
		if info.Permission == "system.exec" || strings.Contains(string(info.Permission), "exec") {
			t.Fatalf("the v1 vocabulary gained command execution: %q", info.Permission)
		}
	}
	info, ok := PermissionByName(string(PermissionSystemPanel))
	if !ok || info.Scope != ScopeNone {
		t.Fatalf("system.panel = %+v, %v", info, ok)
	}
	if _, ok := PermissionByName("system.snapshot"); ok {
		t.Fatal("a separate snapshot permission was added")
	}
}
