package vault

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/vaultimport"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

type fakeImports struct {
	preview appvault.ImportPreview
	err     error
	calls   []appvault.ImportCommitOptions
}

func (f *fakeImports) PreviewImport(context.Context, string, appvault.PreviewImportOptions) (appvault.ImportPreview, error) {
	if f.err != nil {
		return appvault.ImportPreview{}, f.err
	}
	return f.preview, nil
}

func (f *fakeImports) CommitImport(_ context.Context, preview appvault.ImportPreview,
	opts appvault.ImportCommitOptions) (appvault.ImportCommitResult, error) {

	if f.err != nil {
		return appvault.ImportCommitResult{}, f.err
	}
	f.calls = append(f.calls, opts)
	start := opts.Offset
	if start < 0 || start > len(preview.Rows) {
		start = len(preview.Rows)
	}
	end := len(preview.Rows)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}
	result := appvault.ImportCommitResult{Next: end, Done: end >= len(preview.Rows)}
	for index := start; index < end; index++ {
		selected := preview.Rows[index].Selected
		if index < len(opts.Selected) {
			selected = opts.Selected[index]
		}
		if !selected {
			continue
		}
		switch preview.Rows[index].Status {
		case appvault.ImportStatusNew:
			result.Imported = append(result.Imported, model.HostID(fmt.Sprintf("imported-%d", index)))
		case appvault.ImportStatusConflict:
			if opts.Merge == appvault.ImportMergeOverwrite {
				result.Overwritten = append(result.Overwritten, preview.Rows[index].Existing)
			} else {
				result.Skipped++
			}
		default:
			result.Skipped++
		}
	}
	return result, nil
}

type fakeImportPicker struct {
	name    string
	content []byte
	err     error
}

func (f fakeImportPicker) PickImportFile() (string, []byte, error) {
	return f.name, f.content, f.err
}

func importPreviewFixture() appvault.ImportPreview {
	rows := []appvault.ImportRow{
		{Index: 0, SourceRow: 2, Label: "web-1", Hostname: "web1.example.test",
			Port: 2222, Status: appvault.ImportStatusNew, Selected: true},
		{Index: 1, SourceRow: 3, Label: "web-2", Hostname: "web2.example.test",
			Port: 22, Status: appvault.ImportStatusConflict, Selected: true,
			Existing: "host-existing", ExistingLabel: "old web"},
		{Index: 2, SourceRow: 4, Label: "web-1 copy", Hostname: "web1.example.test",
			Port: 2222, Status: appvault.ImportStatusDuplicate},
		{Index: 3, SourceRow: 5, Label: "broken", Hostname: "bad host",
			Status: appvault.ImportStatusInvalid, Issues: []vaultimport.Issue{{
				Severity: vaultimport.SeverityWarning, Code: vaultimport.IssueInvalidHostname, Row: 5,
			}}},
	}
	return appvault.ImportPreview{
		Format: vaultimport.FormatCSV, FileName: "hosts.csv", Rows: rows,
		Counts: appvault.ImportCounts{
			Total: 4, New: 1, Conflicts: 1, Duplicates: 1, Invalid: 1, Selected: 2,
		},
		GroupsToCreate: []string{"Prod", "Prod/Web"},
		Issues: []vaultimport.Issue{
			{Severity: vaultimport.SeverityWarning, Code: vaultimport.IssueDuplicateInFile, Row: 4},
			{Severity: vaultimport.SeverityWarning, Code: vaultimport.IssueInvalidHostname, Row: 5},
		},
	}
}

func newImportView(t *testing.T, width, height int) (*vaultstate.State, *fakeImports, *ui.Tester) {
	t.Helper()
	hosts := &fakeHosts{}
	imports := &fakeImports{preview: importPreviewFixture()}
	state := vaultstate.NewState(hosts,
		vaultstate.WithImports(imports),
		vaultstate.WithImportPicker(fakeImportPicker{
			name: "hosts.csv", content: []byte("Hostname\nweb1.example.test\n"),
		}),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := newProductionTester(t, state, width, height)
	return state, imports, tt
}

func TestImportPanelPreviewsRowsAndIssuesAtBothSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range editorSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state, _, tt := newImportView(t, size.width, size.height)

			if err := tt.Click(m.VaultImportTitle()); err != nil {
				t.Fatalf("click Import: %v", err)
			}
			tt.Frame()
			if !state.ImportOpen() {
				t.Fatal("Import did not open")
			}
			if !tt.HasText(m.VaultImportIntro()) {
				t.Fatalf("import intro is missing, texts %q", tt.Texts())
			}
			if err := tt.Click(m.VaultImportChooseFile()); err != nil {
				t.Fatalf("click Choose File: %v", err)
			}
			tt.Frame()

			for _, want := range []string{
				"hosts.csv",
				m.VaultImportSummary(4, 1, 1, 1, 1),
				m.VaultImportGroups(2),
				m.VaultImportIssueLine(4, m.VaultImportIssueDuplicateInFile()),
				m.VaultImportIssueLine(5, m.VaultImportIssueInvalidHostname()),
				m.VaultImportStatusNew(), m.VaultImportStatusConflict(),
				m.VaultImportStatusDuplicate(), m.VaultImportStatusInvalid(),
				m.VaultImportConflictWith("old web"),
				m.VaultImportRowNumber(2), m.VaultImportRowNumber(5),
			} {
				if !tt.HasText(want) {
					t.Errorf("%q is missing, texts %q", want, tt.Texts())
				}
			}

			scrollToEnd(tt, size.width, size.height)
			assertVisible(t, tt, m.VaultImportImport(), size.width, size.height)
			assertVisible(t, tt, m.VaultHostCancel(), size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)

			if err := tt.Click(m.VaultHostCancel()); err != nil {
				t.Fatalf("click Cancel: %v", err)
			}
			tt.Frame()
			if state.ImportOpen() {
				t.Fatal("Cancel did not close the import panel")
			}
		})
	}
}

func TestImportPanelWritesSelectedRowsAndShowsSummary(t *testing.T) {
	m := locales.In("en")
	state, imports, tt := newImportView(t, 1100, 720)
	if err := tt.Click(m.VaultImportTitle()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(m.VaultImportChooseFile()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// The selected count is visible and a valid row can be deselected.
	if !tt.HasText(m.VaultImportSelected(2)) {
		t.Fatalf("selected count is missing, texts %q", tt.Texts())
	}
	if err := tt.Click("web-1"); err != nil {
		t.Fatalf("click row: %v", err)
	}
	tt.Frame()
	if state.ImportRowSelected(0) {
		t.Fatal("clicking the row did not deselect it")
	}
	if state.ImportRowSelected(2) || state.ImportRowSelected(3) {
		t.Fatal("duplicate and invalid rows must not be selectable")
	}
	if state.ToggleImportRow(3) {
		t.Fatal("invalid row accepted a selection")
	}
	if err := tt.Click("web-1"); err != nil {
		t.Fatalf("reselect row: %v", err)
	}
	tt.Frame()

	scrollToEnd(tt, 1100, 720)
	if err := tt.Click(m.VaultImportImport()); err != nil {
		t.Fatalf("click Import: %v", err)
	}
	tt.Frame()

	if state.ImportOpen() {
		t.Fatal("a completed import must close the panel")
	}
	outcome, ok := state.ImportOutcome()
	if !ok || outcome.Imported != 1 || outcome.Skipped != 1 {
		t.Fatalf("outcome = %+v, ok = %v; calls = %+v", outcome, ok, imports.calls)
	}
	if len(imports.calls) == 0 {
		t.Fatal("commit was not called")
	}
	if imports.calls[0].Merge != appvault.ImportMergeSkip {
		t.Fatalf("default merge = %q, want skip", imports.calls[0].Merge)
	}
	if !tt.HasText(m.VaultImportDone(1, 0, 1, 0)) {
		t.Fatalf("import summary banner is missing, texts %q", tt.Texts())
	}
	if err := tt.Click(m.VaultImportDismiss()); err != nil {
		t.Fatalf("click Dismiss: %v", err)
	}
	tt.Frame()
	if _, ok := state.ImportOutcome(); ok {
		t.Fatal("Dismiss did not clear the banner")
	}
}

func TestImportPanelReportsFileFailures(t *testing.T) {
	m := locales.In("en")
	hosts := &fakeHosts{}
	imports := &fakeImports{preview: importPreviewFixture()}
	state := vaultstate.NewState(hosts,
		vaultstate.WithImports(imports),
		vaultstate.WithImportPicker(fakeImportPicker{err: context.DeadlineExceeded}),
	)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	tt := newProductionTester(t, state, 720, 480)
	if err := tt.Click(m.VaultImportTitle()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(m.VaultImportChooseFile()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(m.VaultProblemImportFile()) {
		t.Fatalf("file failure is not localized in the panel, texts %q", tt.Texts())
	}
	if state.ImportPreview() != nil {
		t.Fatal("a failed pick left a preview")
	}
}
