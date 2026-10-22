package vault

import (
	"context"

	"github.com/mslxl/natcatty/domain/vaultimport"
)

// FamilyImportResult counts records a family import created.
type FamilyImportResult struct {
	Created int
	Skipped int
}

// ImportSnippetsJSON restores a Snippets document produced by the export. It
// uses the same preview and commit path as the Import screen, so explicit
// Host targets, variable values, and source ids survive the round trip.
func (s *Service) ImportSnippetsJSON(ctx context.Context, text string) (FamilyImportResult, error) {
	return s.importFamilyDocument(ctx, text, vaultimport.FormatSnippets)
}

// ImportNotesZIP restores the notes export archive. The archive's versioned
// manifest is authoritative for title and Note Group; the human-readable
// Markdown layout is only a fallback.
func (s *Service) ImportNotesZIP(ctx context.Context, archive []byte) (FamilyImportResult, error) {
	return s.importFamilyDocument(ctx, string(archive), vaultimport.FormatNotesZIP)
}

// ImportKeysJSON restores Keys from a keys export. A Key without private
// material produces no Key because an imported Key needs material to be
// usable.
func (s *Service) ImportKeysJSON(ctx context.Context, text string) (FamilyImportResult, error) {
	return s.importFamilyDocument(ctx, text, vaultimport.FormatKeys)
}

func (s *Service) importFamilyDocument(ctx context.Context, text string,
	format vaultimport.Format) (FamilyImportResult, error) {

	preview, err := s.PreviewImport(ctx, text, PreviewImportOptions{Format: format})
	if err != nil {
		return FamilyImportResult{}, err
	}
	result, err := s.CommitImport(ctx, preview, ImportCommitOptions{})
	if err != nil {
		return FamilyImportResult{}, err
	}
	created := 0
	switch format {
	case vaultimport.FormatSnippets:
		created = preview.FamilyCounts.Snippets
	case vaultimport.FormatNotesZIP:
		created = preview.FamilyCounts.Notes
	case vaultimport.FormatKeys:
		created = preview.FamilyCounts.Keys
	}
	return FamilyImportResult{Created: created, Skipped: result.FamilySkipped}, nil
}
