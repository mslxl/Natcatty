package state

import (
	"context"
	"errors"
	"strings"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

// Imports is the external-inventory use-case seam. The application Service
// implements it; tests provide a fake.
type Imports interface {
	PreviewImport(ctx context.Context, text string, opts appvault.PreviewImportOptions) (appvault.ImportPreview, error)
	CommitImport(ctx context.Context, preview appvault.ImportPreview, opts appvault.ImportCommitOptions) (appvault.ImportCommitResult, error)
}

// ImportPicker opens the native file picker and reads the chosen file. The app
// wires a MyGo dialog and the filesystem; tests provide a fake.
type ImportPicker interface {
	PickImportFile() (name string, content []byte, err error)
}

// WithImports gives the Vault surface external inventory import.
func WithImports(imports Imports) Option {
	return func(s *State) { s.imports = imports }
}

// WithImportPicker gives the Vault surface the native file picker. It is
// called on the UI thread and returns name "", nil, nil when cancelled.
func WithImportPicker(picker ImportPicker) Option {
	return func(s *State) { s.importPicker = picker }
}

// ImportOutcome is the last successful import, shown as a dismissible banner
// on the list. It contains counts only, never imported credential material.
type ImportOutcome struct {
	Imported    int
	Overwritten int
	Skipped     int
	Groups      int
	Family      int
}

// ImportOpen reports whether the import panel is showing.
func (s *State) ImportOpen() bool { return s.importActive }

// ImportPreview returns the analyzed inventory, or nil before a file is
// chosen.
func (s *State) ImportPreview() *appvault.ImportPreview { return s.importPreview }

// ImportFileName returns the chosen file's base name.
func (s *State) ImportFileName() string { return s.importBaseName() }

// ImportMasterPasswordInput is the MobaXterm master-password input. The value
// is used only to decrypt credentials in memory and is never persisted.
func (s *State) ImportMasterPasswordInput() *string { return &s.importMaster }

// ImportFormatValue returns the selected format code, or "auto".
func (s *State) ImportFormatValue() string {
	if s.importFormat == "" {
		return "auto"
	}
	return s.importFormat
}

// SetImportFormat changes the parser and re-analyzes the chosen file. "auto"
// asks the parser to detect the format.
func (s *State) SetImportFormat(format string) bool {
	format = strings.TrimSpace(format)
	if format == "auto" {
		format = ""
	}
	if format != "" && !vaultimport.Format(format).Valid() {
		return false
	}
	if format == s.importFormat {
		return false
	}
	s.importFormat = format
	if s.importData == "" && s.importName == "" {
		return false
	}
	return s.RefreshImport()
}

// ImportMergeValue returns the conflict policy code.
func (s *State) ImportMergeValue() string {
	if s.importMerge == "" {
		return string(appvault.ImportMergeSkip)
	}
	return s.importMerge
}

// SetImportMerge changes how selected conflicts are handled.
func (s *State) SetImportMerge(value string) {
	if value != string(appvault.ImportMergeOverwrite) {
		value = string(appvault.ImportMergeSkip)
	}
	s.importMerge = value
	// A changed policy can affect rows in chunks that already committed, so
	// start from the first row. Stable preview IDs keep the retry idempotent.
	s.importOffset = 0
	s.redraw()
}

// ImportProblem reports a file or storage failure the panel must localize.
func (s *State) ImportProblem() Problem { return s.importProblem }

// ImportRowCount returns the parsed row count.
func (s *State) ImportRowCount() int {
	if s.importPreview == nil {
		return 0
	}
	return len(s.importPreview.Rows)
}

// ImportRow returns one preview row.
func (s *State) ImportRow(index int) (appvault.ImportRow, bool) {
	if s.importPreview == nil || index < 0 || index >= len(s.importPreview.Rows) {
		return appvault.ImportRow{}, false
	}
	return s.importPreview.Rows[index], true
}

// ImportRowSelected reports whether a row will be written.
func (s *State) ImportRowSelected(index int) bool {
	return index >= 0 && index < len(s.importSelected) && s.importSelected[index]
}

// ImportFamilyRows returns the non-Host records the document carries. They
// are restored together with the host rows and keep their source links.
func (s *State) ImportFamilyRows() []appvault.ImportFamilyRow {
	if s.importPreview == nil {
		return nil
	}
	return s.importPreview.FamilyRows
}

// ImportFamilyCounts summarizes the non-Host records.
func (s *State) ImportFamilyCounts() appvault.ImportFamilyCounts {
	if s.importPreview == nil {
		return appvault.ImportFamilyCounts{}
	}
	return s.importPreview.FamilyCounts
}

// ImportSelectedCount returns how many rows are selected.
func (s *State) ImportSelectedCount() int {
	count := 0
	for _, selected := range s.importSelected {
		if selected {
			count++
		}
	}
	return count
}

// BeginImport closes every editor and opens an empty import panel. Nothing is
// read until the user chooses a file.
func (s *State) BeginImport() {
	s.closeEditors()
	s.importActive = true
	s.importPreview = nil
	s.importName = ""
	s.importData = ""
	s.importFormat = ""
	s.importMaster = ""
	s.importSelected = nil
	s.importMerge = string(appvault.ImportMergeSkip)
	s.importProblem = ProblemNone
	s.redraw()
}

// CancelImport closes the import panel without writing anything. A summary
// from an earlier import is left for the list banner.
func (s *State) CancelImport() {
	s.closeImport()
	s.problem = ProblemNone
	s.redraw()
}

// ChooseImportFile opens the native picker, reads the file, and analyzes it.
// It reports false when the user cancels or the file cannot be analyzed.
func (s *State) ChooseImportFile() bool {
	if s.importPicker == nil {
		s.importProblem = ProblemImportUnavailable
		s.redraw()
		return false
	}
	name, content, err := s.importPicker.PickImportFile()
	if err != nil {
		s.importProblem = ProblemImportFile
		s.redraw()
		return false
	}
	if name == "" {
		return false
	}
	s.importName = name
	s.importData = string(content)
	s.importFormat = ""
	return s.RefreshImport()
}

// RefreshImport re-parses the chosen file with the current format and master
// password. It discards row selections, which is correct after the parsed rows
// change.
func (s *State) RefreshImport() bool {
	if s.importData == "" && s.importName == "" {
		return false
	}
	if s.imports == nil {
		s.importProblem = ProblemImportUnavailable
		s.redraw()
		return false
	}
	format := vaultimport.Format(strings.TrimSpace(s.importFormat))
	if format != "" && !format.Valid() {
		s.importProblem = ProblemImportParse
		s.redraw()
		return false
	}
	preview, err := s.imports.PreviewImport(context.Background(), s.importData,
		appvault.PreviewImportOptions{
			Format: format, FileName: s.importName,
			MasterPassword: s.importMaster,
		})
	if err != nil {
		s.importPreview = nil
		s.importSelected = nil
		s.importProblem = importProblemFor(err)
		s.redraw()
		return false
	}
	s.importPreview = &preview
	s.importSelected = make([]bool, len(preview.Rows))
	for index, row := range preview.Rows {
		s.importSelected[index] = row.Selected
	}
	s.importProblem = ProblemNone
	s.importOffset = 0
	s.redraw()
	return true
}

// ToggleImportRow flips one valid row's selection. Invalid and duplicate rows
// cannot be selected.
func (s *State) ToggleImportRow(index int) bool {
	if s.importPreview == nil || index < 0 || index >= len(s.importSelected) {
		return false
	}
	status := s.importPreview.Rows[index].Status
	if status == appvault.ImportStatusInvalid || status == appvault.ImportStatusDuplicate {
		return false
	}
	s.importSelected[index] = !s.importSelected[index]
	// Earlier chunks may have committed the previous selection; re-walk the
	// plan from the first row. Stable preview IDs keep the retry idempotent.
	s.importOffset = 0
	s.redraw()
	return true
}

// ConfirmImport writes the selected rows in bounded chunks until the plan is
// done, then reloads the Vault. A failure keeps the panel open at the failed
// chunk's offset; pressing Import again resumes without duplicating writes.
func (s *State) ConfirmImport() error {
	if s.importPreview == nil {
		return nil
	}
	if s.imports == nil {
		s.importProblem = ProblemImportUnavailable
		s.redraw()
		return nil
	}
	merge := appvault.ImportMerge(strings.TrimSpace(s.importMerge))
	if merge == "" {
		merge = appvault.ImportMergeSkip
	}
	outcome := ImportOutcome{}
	offset := s.importOffset
	for {
		if offset < 0 || offset > len(s.importPreview.Rows) {
			offset = 0
		}
		previous := offset
		result, err := s.imports.CommitImport(context.Background(), *s.importPreview,
			appvault.ImportCommitOptions{
				Selected: s.importSelected, Merge: merge,
				Offset: offset, Limit: importChunkSize,
			})
		if err != nil {
			// Resume at the failed chunk when the user retries without
			// changing the selection or merge policy.
			s.importOffset = result.Next
			s.importProblem = importProblemFor(err)
			s.redraw()
			return err
		}
		outcome.Imported += len(result.Imported) + result.FamilyCreated
		outcome.Overwritten += len(result.Overwritten)
		outcome.Skipped += result.Skipped + result.FamilySkipped
		outcome.Groups += len(result.GroupsCreated)
		outcome.Family += result.FamilyCreated
		offset = result.Next
		if result.Done {
			break
		}
		if offset <= previous {
			// A store that does not advance would loop forever. Treat it as a
			// storage failure rather than hanging the UI.
			s.importOffset = offset
			s.importProblem = ProblemImportStore
			s.redraw()
			return appvault.ErrImportStoreUnavailable
		}
	}
	s.importOutcome = &outcome
	s.importOffset = 0
	s.closeImport()
	_ = s.load()
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// importChunkSize bounds one transactional import batch.
const importChunkSize = 50

// ImportOutcome returns the last successful import's counts for the list
// banner.
func (s *State) ImportOutcome() (ImportOutcome, bool) {
	if s.importOutcome == nil {
		return ImportOutcome{}, false
	}
	return *s.importOutcome, true
}

// DismissImportOutcome hides the import summary banner.
func (s *State) DismissImportOutcome() {
	s.importOutcome = nil
	s.redraw()
}

// importBaseName strips directory components for display.
func (s *State) importBaseName() string {
	name := strings.ReplaceAll(s.importName, `\`, "/")
	if index := strings.LastIndexByte(name, '/'); index >= 0 {
		return name[index+1:]
	}
	return name
}

func (s *State) closeImport() {
	s.importActive = false
	s.importPreview = nil
	s.importName = ""
	s.importData = ""
	s.importSelected = nil
	s.importMaster = ""
	s.importOffset = 0
	s.importProblem = ProblemNone
}

func importProblemFor(err error) Problem {
	switch {
	case err == nil:
		return ProblemNone
	case errors.Is(err, appvault.ErrImportStoreUnavailable):
		return ProblemImportStore
	}
	var typed *appvault.Error
	if errors.As(err, &typed) && typed.Kind == appvault.KindValidation {
		return ProblemImportParse
	}
	return problemFor(err)
}
