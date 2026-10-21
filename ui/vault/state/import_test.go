package state

import (
	"context"
	"errors"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
)

type fakeImports struct {
	preview   appvault.ImportPreview
	commits   []appvault.ImportCommitOptions
	failNext  int
	failError error
}

func (f *fakeImports) PreviewImport(context.Context, string, appvault.PreviewImportOptions) (appvault.ImportPreview, error) {
	return f.preview, nil
}

func (f *fakeImports) CommitImport(_ context.Context, preview appvault.ImportPreview,
	opts appvault.ImportCommitOptions) (appvault.ImportCommitResult, error) {

	f.commits = append(f.commits, opts)
	if f.failError != nil {
		err := f.failError
		f.failError = nil
		return appvault.ImportCommitResult{Next: f.failNext}, err
	}
	end := opts.Offset + opts.Limit
	if end > len(preview.Rows) || opts.Limit <= 0 {
		end = len(preview.Rows)
	}
	result := appvault.ImportCommitResult{Next: end, Done: end >= len(preview.Rows)}
	result.Imported = append(result.Imported, model.HostID("imported"))
	return result, nil
}

type fakeImportPicker struct {
	name    string
	content string
}

func (f fakeImportPicker) PickImportFile() (string, []byte, error) {
	return f.name, []byte(f.content), nil
}

func importPreview(rows int) appvault.ImportPreview {
	preview := appvault.ImportPreview{FileName: "hosts.csv"}
	for index := 0; index < rows; index++ {
		preview.Rows = append(preview.Rows, appvault.ImportRow{
			Index: index, Hostname: "host.example.test", Selected: true,
		})
	}
	return preview
}

func importState(t *testing.T, rows int) (*State, *fakeImports) {
	t.Helper()
	imports := &fakeImports{preview: importPreview(rows)}
	state := NewState(&fakeHosts{},
		WithImports(imports),
		WithImportPicker(fakeImportPicker{name: "hosts.csv", content: "Hostname\nhost.example.test\n"}),
		WithRedrawRequest(func() {}),
	)
	state.BeginImport()
	if !state.ChooseImportFile() {
		t.Fatal("ChooseImportFile did not analyze the chosen file")
	}
	return state, imports
}

func TestConfirmImportResumesAtTheFailedChunk(t *testing.T) {
	state, imports := importState(t, 60)
	imports.failNext = 50
	imports.failError = errors.New("database is busy")

	if err := state.ConfirmImport(); err == nil {
		t.Fatal("ConfirmImport hid the first-chunk failure")
	}
	if !state.ImportOpen() {
		t.Fatal("a failed import must keep the panel open")
	}
	if state.importOffset != 50 {
		t.Fatalf("offset after failure = %d, want 50", state.importOffset)
	}
	if len(imports.commits) != 1 || imports.commits[0].Offset != 0 {
		t.Fatalf("commits = %+v", imports.commits)
	}

	if err := state.ConfirmImport(); err != nil {
		t.Fatalf("resumed ConfirmImport: %v", err)
	}
	if len(imports.commits) != 2 || imports.commits[1].Offset != 50 {
		t.Fatalf("resume offset = %+v, want 50", imports.commits)
	}
	if state.ImportOpen() {
		t.Fatal("the successful import did not close the panel")
	}
	if state.importOffset != 0 {
		t.Fatalf("offset after success = %d, want 0", state.importOffset)
	}
	if outcome, ok := state.ImportOutcome(); !ok || outcome.Imported != 1 {
		t.Fatalf("outcome = %+v, ok = %v", outcome, ok)
	}
}

func TestImportOffsetResetsWhenThePlanChanges(t *testing.T) {
	state, _ := importState(t, 120)

	state.importOffset = 50
	if !state.ToggleImportRow(0) {
		t.Fatal("ToggleImportRow rejected a new row")
	}
	if state.importOffset != 0 {
		t.Fatalf("selection change kept offset %d", state.importOffset)
	}

	state.importOffset = 50
	state.SetImportMerge(string(appvault.ImportMergeOverwrite))
	if state.importOffset != 0 {
		t.Fatalf("merge change kept offset %d", state.importOffset)
	}

	state.importOffset = 50
	if !state.RefreshImport() {
		t.Fatal("RefreshImport failed")
	}
	if state.importOffset != 0 {
		t.Fatalf("refresh kept offset %d", state.importOffset)
	}
}

func TestConfirmImportStopsWhenTheStoreDoesNotAdvance(t *testing.T) {
	state, _ := importState(t, 120)

	// A store that returns the same Next as the requested offset must fail
	// instead of looping forever.
	state.imports = stuckImports{}
	if err := state.ConfirmImport(); err == nil {
		t.Fatal("ConfirmImport accepted a non-advancing store")
	}
	if state.ImportProblem() != ProblemImportStore {
		t.Fatalf("problem = %v, want ProblemImportStore", state.ImportProblem())
	}
	if !state.ImportOpen() {
		t.Fatal("a stalled import closed the panel")
	}
}

type stuckImports struct{}

func (stuckImports) PreviewImport(context.Context, string,
	appvault.PreviewImportOptions) (appvault.ImportPreview, error) {
	return importPreview(120), nil
}

func (stuckImports) CommitImport(context.Context, appvault.ImportPreview,
	appvault.ImportCommitOptions) (appvault.ImportCommitResult, error) {
	return appvault.ImportCommitResult{Next: 0, Done: false}, nil
}
