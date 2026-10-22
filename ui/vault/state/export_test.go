package state

import (
	"context"
	"errors"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeExports struct {
	options []appvault.ExportOptions
	result  appvault.Export
	err     error
}

func (f *fakeExports) ExportVault(_ context.Context, opts appvault.ExportOptions) (appvault.Export, error) {
	f.options = append(f.options, opts)
	if f.err != nil {
		return appvault.Export{}, f.err
	}
	result := f.result
	result.Format = opts.Format
	if result.Content == nil {
		result.Content = []byte("content")
	}
	if result.Filename == "" {
		result.Filename = "export." + opts.Format.Extension()
	}
	return result, nil
}

type fakeExportWriter struct {
	name    string
	content []byte
	path    string
	err     error
	calls   int
}

func (f *fakeExportWriter) Save(name string, content []byte) (string, error) {
	f.calls++
	f.name = name
	f.content = append([]byte(nil), content...)
	if f.err != nil {
		return "", f.err
	}
	if f.path != "" {
		return f.path, nil
	}
	return "C:/Exports/" + name, nil
}

func exportState(t *testing.T, selected bool) (*State, *fakeExports, *fakeExportWriter) {
	t.Helper()
	hosts := &fakeHosts{items: []domainvault.Host{{
		ID: "h1", Label: "Web", Hostname: "web.example.test", Port: 22,
	}}}
	exports := &fakeExports{}
	writer := &fakeExportWriter{}
	state := NewState(hosts,
		WithExports(exports),
		WithExportWriter(writer),
		WithRedrawRequest(func() {}),
	)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	if selected {
		state.Select("h1")
	}
	return state, exports, writer
}

func TestBeginExportScopesToTheSelectedHost(t *testing.T) {
	state, _, _ := exportState(t, true)
	state.BeginExport()
	if !state.ExportOpen() || state.ExportFormatValue() != string(appvault.ExportJSON) {
		t.Fatalf("panel = %v, format = %q", state.ExportOpen(), state.ExportFormatValue())
	}
	if state.ExportScopeValue() != ExportScopeSelected {
		t.Fatalf("scope = %q", state.ExportScopeValue())
	}
	if state.ExportIncludesSecrets() || state.ExportSecretsConfirmed() {
		t.Fatal("secrets started enabled")
	}
}

func TestBeginExportWithoutSelectionUsesEverything(t *testing.T) {
	state, _, _ := exportState(t, false)
	state.BeginExport()
	if state.ExportScopeValue() != ExportScopeAll {
		t.Fatalf("scope = %q", state.ExportScopeValue())
	}
}

func TestExportRequiresThePlaintextConfirmation(t *testing.T) {
	state, exports, writer := exportState(t, true)
	state.BeginExport()
	state.SetExportIncludeSecrets(true)
	if err := state.Export(""); err != nil {
		t.Fatal(err)
	}
	if len(exports.options) != 0 || writer.calls != 0 {
		t.Fatalf("unconfirmed export called through: options %d, writes %d",
			len(exports.options), writer.calls)
	}
	if problem := state.ExportProblem(); problem != ProblemExportUnconfirmed {
		t.Fatalf("problem = %q", problem)
	}

	state.ConfirmExportSecrets()
	if err := state.Export(""); err != nil {
		t.Fatal(err)
	}
	if len(exports.options) != 1 || !exports.options[0].IncludeSecrets {
		t.Fatalf("options = %+v", exports.options)
	}
	if len(exports.options[0].HostIDs) != 1 || exports.options[0].HostIDs[0] != model.HostID("h1") {
		t.Fatalf("hosts = %v", exports.options[0].HostIDs)
	}
	outcome, ok := state.ExportOutcome()
	if !ok || outcome.Path == "" || outcome.Bytes != len("content") {
		t.Fatalf("outcome = %+v, %v", outcome, ok)
	}
	if state.ExportOpen() {
		t.Fatal("a saved export left the panel open")
	}
}

func TestTogglingSecretsOffAndOnRequiresConfirmationAgain(t *testing.T) {
	state, _, _ := exportState(t, false)
	state.BeginExport()
	state.SetExportIncludeSecrets(true)
	state.ConfirmExportSecrets()
	state.SetExportIncludeSecrets(false)
	if state.ExportSecretsConfirmed() {
		t.Fatal("disabling secrets kept the confirmation")
	}
	state.SetExportIncludeSecrets(true)
	if state.ExportSecretsConfirmed() {
		t.Fatal("re-enabling secrets kept the confirmation")
	}
}

func TestCancelledSaveDialogKeepsThePanelOpen(t *testing.T) {
	// A success with an empty path is the save dialog's Cancel response.
	state := NewState(&fakeHosts{}, WithExports(&fakeExports{}),
		WithExportWriter(cancelPathWriter{}), WithRedrawRequest(func() {}))
	state.BeginExport()
	if err := state.Export(""); err != nil {
		t.Fatal(err)
	}
	if !state.ExportOpen() || state.ExportProblem() != ProblemNone {
		t.Fatalf("cancel closed or failed the panel: open %v, problem %q",
			state.ExportOpen(), state.ExportProblem())
	}
	if _, ok := state.ExportOutcome(); ok {
		t.Fatal("a cancelled save recorded an outcome")
	}
}

type cancelPathWriter struct{}

func (cancelPathWriter) Save(string, []byte) (string, error) { return "", nil }

func TestExportUnavailableAndWriterFailuresAreReported(t *testing.T) {
	state := NewState(&fakeHosts{}, WithRedrawRequest(func() {}))
	state.BeginExport()
	if err := state.Export(""); err != nil {
		t.Fatal(err)
	}
	if state.ExportProblem() != ProblemExportUnavailable {
		t.Fatalf("unavailable problem = %q", state.ExportProblem())
	}

	state, _, writer := exportState(t, false)
	state.BeginExport()
	writer.err = errors.New("disk full")
	if err := state.Export(""); err == nil {
		t.Fatal("writer failure was swallowed")
	}
	if state.ExportProblem() != ProblemExportWriter || !state.ExportOpen() {
		t.Fatalf("problem = %q, open = %v", state.ExportProblem(), state.ExportOpen())
	}
}

func TestExportValidationReportsTheFormatAndTheMissingHost(t *testing.T) {
	state, _, _ := exportState(t, false)
	state.BeginExport()
	state.SetExportFormat(string(appvault.ExportCSV))
	if state.ExportFormatValue() != string(appvault.ExportCSV) {
		t.Fatalf("format = %q", state.ExportFormatValue())
	}
	if state.SetExportFormat("nope") {
		t.Fatal("an unknown format was accepted")
	}
	state.SetExportScope(ExportScopeSelected)
	if err := state.Export(""); err != nil {
		t.Fatal(err)
	}
	if state.ExportProblem() != ProblemExportNoHost {
		t.Fatalf("problem = %q", state.ExportProblem())
	}
}

func TestExportAPIErrorIsLocalizedAndKeepsThePanel(t *testing.T) {
	state, exports, _ := exportState(t, false)
	state.BeginExport()
	exports.err = &appvault.Error{Kind: appvault.KindValidation}
	if err := state.Export(""); err == nil {
		t.Fatal("export error was swallowed")
	}
	if state.ExportProblem() != ProblemExportFormat || !state.ExportOpen() {
		t.Fatalf("problem = %q, open = %v", state.ExportProblem(), state.ExportOpen())
	}
}

func TestDismissExportOutcome(t *testing.T) {
	state, _, _ := exportState(t, false)
	state.BeginExport()
	if err := state.Export(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.ExportOutcome(); !ok {
		t.Fatal("no outcome")
	}
	state.DismissExportOutcome()
	if _, ok := state.ExportOutcome(); ok {
		t.Fatal("outcome was not dismissed")
	}
}
