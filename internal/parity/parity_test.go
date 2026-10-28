package parity

import (
	"strings"
	"testing"
)

const sampleMatrix = `# Parity matrix: comments are ignored.
- id: shell.launch
  area: shell
  feature: App launches a native window
  original_paths:
    - electron/main.cjs
    - electron/launch.cjs
  module: app
  tickets: [01, 60]
  acceptance_tests:
    - app.TestLaunchSmoke
    - internal/release.TestParseTagAcceptsStrictSemVer
  status: verified
  notes: "Quoted \"notes\" stay one scalar."

- id: terminal.search
  area: terminal
  feature: "Terminal search"
  original_paths: [electron/terminalWorker/terminalWorker.js]
  module: application/session
  tickets: [18]
  automation_exception: renderer-only-visual
  manual_qa:
    - QA-001
  status: excluded
  notes: 'Single-quoted scalar.'
`

func TestParseMatrixReadsEveryField(t *testing.T) {
	m, err := ParseMatrix([]byte(sampleMatrix))
	if err != nil {
		t.Fatalf("ParseMatrix: %v", err)
	}
	if len(m.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(m.Rows))
	}
	launch := m.Rows[0]
	if launch.ID != "shell.launch" || launch.Area != "shell" || launch.Module != "app" {
		t.Errorf("launch row = %+v", launch)
	}
	if got := strings.Join(launch.OriginalPaths, ","); got != "electron/main.cjs,electron/launch.cjs" {
		t.Errorf("original paths = %q", got)
	}
	if got := strings.Join(launch.Tickets, ","); got != "01,60" {
		t.Errorf("tickets = %q", got)
	}
	if got := strings.Join(launch.AcceptanceTests, ","); got != "app.TestLaunchSmoke,internal/release.TestParseTagAcceptsStrictSemVer" {
		t.Errorf("acceptance tests = %q", got)
	}
	if launch.Notes != `Quoted "notes" stay one scalar.` {
		t.Errorf("notes = %q", launch.Notes)
	}
	if launch.Line != 2 {
		t.Errorf("line = %d, want 2", launch.Line)
	}

	search := m.Rows[1]
	if search.Feature != "Terminal search" || search.Status != "excluded" {
		t.Errorf("search row = %+v", search)
	}
	if search.AutomationException != "renderer-only-visual" {
		t.Errorf("automation exception = %q", search.AutomationException)
	}
	if got := strings.Join(search.ManualQA, ","); got != "QA-001" {
		t.Errorf("manual qa = %q", got)
	}
	if search.Notes != "Single-quoted scalar." {
		t.Errorf("notes = %q", search.Notes)
	}
}

func TestParseMatrixRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "unknown field",
			input: "- id: ui.foo\n  area: shell\n  feature: Foo\n  original_paths: [x]\n  module: app\n  tickets: [01]\n  status: verified\n  owner: nobody\n",
			want:  "unknown field",
		},
		{
			name:  "row must start with id",
			input: "- area: shell\n  id: ui.foo\n",
			want:  "must start with id",
		},
		{
			name:  "field outside a row",
			input: "area: shell\n",
			want:  "expected a row",
		},
		{
			name:  "list item without a field",
			input: "- id: ui.foo\n  area: shell\n    - stray\n",
			want:  "list item without a list field",
		},
		{
			name:  "inline list must be bracketed",
			input: "- id: ui.foo\n  area: shell\n  tickets: 01\n",
			want:  "expected an inline list",
		},
		{
			name:  "unterminated quote",
			input: "- id: ui.foo\n  area: shell\n  feature: \"unterminated\n",
			want:  "invalid quoted value",
		},
		{
			name:  "missing colon",
			input: "- id: ui.foo\n  area: shell\n  feature Foo\n",
			want:  "expected \"key: value\"",
		},
		{
			name:  "empty matrix",
			input: "# only a comment\n",
			want:  "has no rows",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseMatrix([]byte(test.input))
			if err == nil {
				t.Fatal("ParseMatrix accepted malformed input")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestParseInlineListKeepsCommasInsideQuotes(t *testing.T) {
	got, err := parseInlineList(`["a, b", 'c', plain]`)
	if err != nil {
		t.Fatalf("parseInlineList: %v", err)
	}
	want := []string{"a, b", "c", "plain"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("items = %q, want %q", got, want)
	}
}

func TestParseLimitationsReadsSections(t *testing.T) {
	data := `# Parity limitations

## Out of scope

### ` + "`plugins.runtime`" + `
Plugins are removed.

Decision: excluded by the spec.
Ticket: 01

### ` + "`mcp.blocklist-threat-model`" + `
A textual safety net.

Decision: accepted.
Ticket: 58.
`
	limitations := ParseLimitations([]byte(data))
	if len(limitations) != 2 {
		t.Fatalf("limitations = %d, want 2", len(limitations))
	}
	if limitations[0].ID != "plugins.runtime" || !strings.Contains(limitations[0].Body, "Plugins are removed.") {
		t.Errorf("first limitation = %+v", limitations[0])
	}
	if limitations[1].ID != "mcp.blocklist-threat-model" || !strings.Contains(limitations[1].Body, "Ticket: 58.") {
		t.Errorf("second limitation = %+v", limitations[1])
	}
}

func TestAuditAcceptsAReleaseReadyMatrix(t *testing.T) {
	m, err := ParseMatrix([]byte(sampleMatrix))
	if err != nil {
		t.Fatalf("ParseMatrix: %v", err)
	}
	limitations := ParseLimitations([]byte("### `terminal.search`\nGap.\n\nDecision: accepted.\nTicket: 18\n"))
	if problems := Audit(m, limitations); len(problems) != 0 {
		t.Fatalf("Audit reported %v, want none", problems)
	}
}

func TestAuditReportsReleaseBlockers(t *testing.T) {
	matrix := `- id: shell.launch
  area: shell
  feature: Launch
  original_paths: [electron/main.cjs]
  module: app
  tickets: [01]
  acceptance_tests: []
  status: implemented
  notes: ""
- id: terminal.search
  area: terminal
  feature: Search
  original_paths: [electron/terminalWorker/terminalWorker.js]
  module: application/session
  tickets: [18]
  status: excluded
  notes: ""
- id: sync.crdt
  area: sync
  feature: CRDT
  original_paths: [domain/sync.ts]
  module: domain/sync
  tickets: [55]
  acceptance_tests: [domain/sync.TestMergeLaws]
  status: verified
  notes: ""
`
	m, err := ParseMatrix([]byte(matrix))
	if err != nil {
		t.Fatalf("ParseMatrix: %v", err)
	}
	limitations := ParseLimitations([]byte("### `terminal.search`\nGap with no sign-off.\n"))
	problems := strings.Join(Audit(m, limitations), "\n")
	for _, want := range []string{
		`row "shell.launch" is implemented`,
		`excluded row "terminal.search" has no "Decision:"`,
		`excluded row "terminal.search" has no "Ticket:"`,
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("Audit problems = %q, want %q", problems, want)
		}
	}

	orphan := ParseLimitations([]byte("### `ui.missing`\nGap.\n\nDecision: accepted.\nTicket: 60\n"))
	problems = strings.Join(Audit(m, orphan), "\n")
	if !strings.Contains(problems, `no matrix row uses that id`) {
		t.Errorf("Audit problems = %q, want the orphan limitation", problems)
	}

	notExcluded := ParseLimitations([]byte("### `sync.crdt`\nGap.\n\nDecision: accepted.\nTicket: 60\n"))
	problems = strings.Join(Audit(m, notExcluded), "\n")
	if !strings.Contains(problems, `row is not excluded`) {
		t.Errorf("Audit problems = %q, want the non-excluded limitation", problems)
	}

	duplicate := ParseLimitations([]byte("### `terminal.search`\nA.\n\nDecision: d.\nTicket: 18\n\n### `terminal.search`\nB.\n\nDecision: d.\nTicket: 18\n"))
	problems = strings.Join(Audit(m, duplicate), "\n")
	if !strings.Contains(problems, `defines "terminal.search" more than once`) {
		t.Errorf("Audit problems = %q, want the duplicate limitation", problems)
	}
}

func TestFindByID(t *testing.T) {
	m, err := ParseMatrix([]byte(sampleMatrix))
	if err != nil {
		t.Fatalf("ParseMatrix: %v", err)
	}
	if row := FindByID(m.Rows, "shell.launch"); row == nil || row.Area != "shell" {
		t.Errorf("FindByID returned %+v", row)
	}
	if row := FindByID(m.Rows, "missing"); row != nil {
		t.Errorf("FindByID returned %+v for a missing id", row)
	}
}
