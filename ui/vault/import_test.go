package vault

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/vaultimport"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

type fakeImports struct {
	preview       appvault.ImportPreview
	err           error
	calls         []appvault.ImportCommitOptions
	familyCreated int
	familySkipped int
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
	// A family-only document commits in one chunk: the non-Host records are
	// restored once, at offset zero.
	if opts.Offset == 0 {
		result.FamilyCreated = f.familyCreated
		result.FamilySkipped = f.familySkipped
	}
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

			if err := chooseVaultAction(tt, m, m.VaultImportTitle()); err != nil {
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

// TestImportMasterPasswordClearsTheScrollbar types a long masked password and
// asserts the field's laid-out right edge stays left of the scroll bar's
// track at both supported window sizes. The bar overlays the content, so a
// full-width grow column would put the field's border, and text, under it.
func TestImportMasterPasswordClearsTheScrollbar(t *testing.T) {
	longPassword := strings.Repeat("correct-horse-battery-", 8)
	for _, size := range editorSizes {
		for _, locale := range []string{"en", "zh-CN"} {
			name := fmt.Sprintf("%dx%d/%s", size.width, size.height, locale)
			t.Run(name, func(t *testing.T) {
				m := locales.In(locale)
				state, _, tt := newImportView(t, size.width, size.height)
				tt.SetLocale(locale)

				if err := chooseVaultAction(tt, m, m.VaultImportTitle()); err != nil {
					t.Fatalf("click Import: %v", err)
				}
				tt.Frame()
				if err := tt.Click(m.VaultImportChooseFile()); err != nil {
					t.Fatalf("click Choose File: %v", err)
				}
				tt.Frame()

				// A long value makes the masked text reach the field's right
				// padding, where the scroll bar used to cover it.
				*state.ImportMasterPasswordInput() = longPassword
				var layout importFieldLayout
				restore := watchImportLayout(func(found importFieldLayout) { layout = found })
				defer restore()
				// The probe reports the previous frame's boxes, so a second
				// frame records the layout built with the long value.
				tt.Frame()
				tt.Frame()

				master, viewport := layout.master, layout.viewport
				if master.W <= 0 || master.H <= 0 {
					t.Fatalf("the master-password field was not laid out: %+v", master)
				}
				if viewport.W <= 0 || viewport.H <= 0 {
					t.Fatalf("the import scroll viewport was not laid out: %+v", viewport)
				}
				// MyGo paints the vertical track from the theme's thumb width
				// (6 DIPs by default) plus 6 DIPs of inset and padding. Assert
				// against that independently of the padding the panel applies.
				const mygoScrollbarTrack = 12
				if layout.gutter != mygoScrollbarTrack {
					t.Errorf("the panel reserved %.1f DIPs for the scroll bar, want the %.1f-DIP track",
						layout.gutter, float32(mygoScrollbarTrack))
				}
				const epsilon = 0.5
				if master.X < viewport.X-epsilon {
					t.Errorf("the master-password field starts before the scroll viewport: field %+v, viewport %+v",
						master, viewport)
				}
				if right := master.X + master.W; right > viewport.X+viewport.W-mygoScrollbarTrack+epsilon {
					t.Errorf("the master-password field runs under the scroll bar: field right %.1f, usable right %.1f (field %+v, viewport %+v)",
						right, viewport.X+viewport.W-mygoScrollbarTrack, master, viewport)
				}
				// The rest of the panel must still render at both sizes.
				for _, want := range []string{
					m.VaultImportFormat(), m.VaultImportFormatAuto(),
					m.VaultImportMasterHint(), m.VaultImportMerge(), m.VaultImportMergeSkip(),
					m.VaultImportImport(), m.VaultHostCancel(),
				} {
					if !tt.HasText(want) {
						t.Errorf("%q is missing after the gutter change, texts %q", want, tt.Texts())
					}
				}
			})
		}
	}
}

// watchImportLayout sets the import panel's layout probe and returns a
// function that restores the previous one.
func watchImportLayout(fn func(importFieldLayout)) (restore func()) {
	previous := importLayoutProbe
	importLayoutProbe = fn
	return func() { importLayoutProbe = previous }
}

func TestImportPanelWritesSelectedRowsAndShowsSummary(t *testing.T) {
	m := locales.In("en")
	state, imports, tt := newImportView(t, 1100, 720)
	if err := chooseVaultAction(tt, m, m.VaultImportTitle()); err != nil {
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

// TestFamilyOnlyImportCountsRestoredRowsAndNamesIssues covers the banner and
// warning contract for documents with no Host rows: the restored records count
// as imported, and a degraded Key or dangling Snippet target is named in the
// localized preview.
func TestFamilyOnlyImportCountsRestoredRowsAndNamesIssues(t *testing.T) {
	m := locales.In("en")
	hosts := &fakeHosts{}
	imports := &fakeImports{
		preview: appvault.ImportPreview{
			Format: vaultimport.FormatKeys, FileName: "keys.json",
			FamilyCounts: appvault.ImportFamilyCounts{Keys: 1},
			FamilyRows: []appvault.ImportFamilyRow{{
				Kind: appvault.FamilyKindKey, Label: "deploy key",
			}},
			Issues: []vaultimport.Issue{
				{
					Severity: vaultimport.SeverityWarning,
					Code:     vaultimport.IssueKeyWithoutMaterial,
					Value:    "deploy key",
				},
				{
					Severity: vaultimport.SeverityWarning,
					Code:     vaultimport.IssueDanglingSnippetTarget,
					Value:    "host-1", Detail: "Deploy",
				},
			},
		},
		familyCreated: 1,
	}
	state := vaultstate.NewState(hosts,
		vaultstate.WithImports(imports),
		vaultstate.WithImportPicker(fakeImportPicker{
			name: "keys.json", content: []byte(`{"kind":"netcatty.keys"}`),
		}),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := newProductionTester(t, state, 1100, 720)
	if err := chooseVaultAction(tt, m, m.VaultImportTitle()); err != nil {
		t.Fatalf("click Import: %v", err)
	}
	tt.Frame()
	if err := tt.Click(m.VaultImportChooseFile()); err != nil {
		t.Fatalf("click Choose File: %v", err)
	}
	tt.Frame()

	for _, want := range []string{
		m.VaultImportIssueKeyWithoutMaterial("deploy key"),
		m.VaultImportIssueDanglingTarget("Deploy", "host-1"),
	} {
		if !tt.HasText(want) {
			t.Fatalf("%q is missing from the preview, texts %q", want, tt.Texts())
		}
	}

	scrollToEnd(tt, 1100, 720)
	if err := tt.Click(m.VaultImportImport()); err != nil {
		t.Fatalf("click Import: %v", err)
	}
	tt.Frame()
	outcome, ok := state.ImportOutcome()
	if !ok || outcome.Imported != 1 || outcome.Overwritten != 0 ||
		outcome.Skipped != 0 || outcome.Family != 1 {
		t.Fatalf("outcome = %+v, ok = %v", outcome, ok)
	}
	if !tt.HasText(m.VaultImportDone(1, 0, 0, 0)) ||
		!tt.HasText(m.VaultImportDoneFamily(1)) {
		t.Fatalf("banner does not count the restored record, texts %q", tt.Texts())
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
	if err := chooseVaultAction(tt, m, m.VaultImportTitle()); err != nil {
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

func familyImportPreviewFixture() appvault.ImportPreview {
	return appvault.ImportPreview{
		Format:   vaultimport.FormatNotesZIP,
		FileName: "netcatty-notes.zip",
		FamilyCounts: appvault.ImportFamilyCounts{
			NoteGroups: 1, Notes: 3,
		},
		FamilyRows: []appvault.ImportFamilyRow{
			{Kind: appvault.FamilyKindNoteGroup, Label: "Ops"},
			{Kind: appvault.FamilyKindNote, Label: "Runbook", Detail: "Ops"},
			{Kind: appvault.FamilyKindNote, Label: "Config / Draft", Detail: "Ops/Config"},
		},
	}
}

// TestImportPanelAcceptsFamilyDocumentsAtBothSizes covers the Import screen's
// family formats: a Notes ZIP is detected, previewed, and committed without a
// Host row. The panel keeps the same fixed action bar and gutter behavior.
func TestImportPanelAcceptsFamilyDocumentsAtBothSizes(t *testing.T) {
	m := locales.In("en")
	codes, labels := importFormatOptions(m)
	for _, want := range []struct{ code, label string }{
		{string(vaultimport.FormatSnippets), m.VaultImportFormatSnippets()},
		{string(vaultimport.FormatKeys), m.VaultImportFormatKeys()},
		{string(vaultimport.FormatNotesZIP), m.VaultImportFormatNotesZIP()},
		{string(vaultimport.FormatNetcattyJSON), m.VaultImportFormatNetcattyJSON()},
	} {
		found := false
		for index, code := range codes {
			if code == want.code {
				found = true
				if labels[index] != want.label {
					t.Fatalf("%s label = %q, want %q", code, labels[index], want.label)
				}
			}
		}
		if !found {
			t.Fatalf("format %s is not offered: %v", want.code, codes)
		}
	}

	for _, size := range editorSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			hosts := &fakeHosts{}
			imports := &fakeImports{preview: familyImportPreviewFixture()}
			state := vaultstate.NewState(hosts,
				vaultstate.WithImports(imports),
				vaultstate.WithImportPicker(fakeImportPicker{
					name: "netcatty-notes.zip", content: []byte("PK\x03\x04"),
				}),
			)
			if err := state.Reload(); err != nil {
				t.Fatal(err)
			}
			tt := newProductionTester(t, state, size.width, size.height)
			if err := chooseVaultAction(tt, m, m.VaultImportTitle()); err != nil {
				t.Fatalf("click Import: %v", err)
			}
			tt.Frame()
			if err := tt.Click(m.VaultImportChooseFile()); err != nil {
				t.Fatalf("click Choose File: %v", err)
			}
			tt.Frame()

			if got := state.ImportFormatValue(); got != "auto" {
				t.Fatalf("a detected file left format = %q, want auto", got)
			}
			for _, want := range []string{
				m.VaultImportFamily(4),
				m.VaultImportFamilySelected(4),
				m.VaultImportFamilyRows(),
				m.VaultImportFamilyNoteGroup(), m.VaultImportFamilyNote(),
				"Runbook", "Config / Draft", "Ops/Config",
			} {
				if !tt.HasText(want) {
					t.Errorf("%q is missing, texts %q", want, tt.Texts())
				}
			}
			scrollToEnd(tt, size.width, size.height)
			assertVisible(t, tt, m.VaultImportImport(), size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)
			if err := tt.Click(m.VaultImportImport()); err != nil {
				t.Fatalf("click Import: %v", err)
			}
			tt.Frame()
			if state.ImportOpen() {
				t.Fatal("a family-only import must complete and close the panel")
			}
			if len(imports.calls) != 1 {
				t.Fatalf("commit calls = %d, want 1", len(imports.calls))
			}
		})
	}
}
