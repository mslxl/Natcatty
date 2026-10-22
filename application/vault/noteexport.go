package vault

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/application/vault/export"
	"github.com/mslxl/natcatty/domain/markdown"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// NoteExportFormat selects how a Vault Note is exported.
type NoteExportFormat string

const (
	// NoteExportMarkdown writes the original Markdown source unchanged.
	NoteExportMarkdown NoteExportFormat = "markdown"
	// NoteExportHTML writes one self-contained HTML document.
	NoteExportHTML NoteExportFormat = "html"
	// NoteExportZIP writes one archive entry per Note, preserving Note Group
	// paths as directories.
	NoteExportZIP NoteExportFormat = "zip"
)

// NoteExport is a generated export. Nothing here has touched the filesystem;
// the caller chooses where to save it.
type NoteExport struct {
	Format   NoteExportFormat
	Filename string
	Content  []byte
}

// ExportNote renders one Vault Note in format. Markdown returns the source
// byte for byte, so saving and reading it back preserves the Note exactly.
func (s *Service) ExportNote(ctx context.Context, id string, format NoteExportFormat) (NoteExport, error) {
	note, err := s.GetNote(ctx, id)
	if err != nil {
		return NoteExport{}, err
	}
	switch format {
	case NoteExportMarkdown:
		return NoteExport{
			Format:   format,
			Filename: exportFileName(note.Title, "md"),
			Content:  []byte(note.Content),
		}, nil
	case NoteExportHTML:
		return NoteExport{
			Format:   format,
			Filename: exportFileName(note.Title, "html"),
			Content:  []byte(markdown.HTMLPage(note.Title, note.Content)),
		}, nil
	case NoteExportZIP:
		content, err := notesArchive([]domainvault.Note{note})
		if err != nil {
			return NoteExport{}, internal(err)
		}
		return NoteExport{
			Format:   format,
			Filename: exportFileName(note.Title, "zip"),
			Content:  content,
		}, nil
	default:
		return NoteExport{}, unsupported(errors.New("unknown note export format"))
	}
}

// ExportNotes renders every Vault Note as a ZIP archive. Each entry is the
// Note's original Markdown under its Note Group path, so an import can restore
// both the content and the grouping. An empty Vault exports an empty archive.
func (s *Service) ExportNotes(ctx context.Context, format NoteExportFormat) (NoteExport, error) {
	if format != NoteExportZIP {
		return NoteExport{}, unsupported(errors.New("only ZIP exports multiple notes"))
	}
	notes, err := s.ListNotes(ctx)
	if err != nil {
		return NoteExport{}, err
	}
	content, err := notesArchive(notes)
	if err != nil {
		return NoteExport{}, internal(err)
	}
	return NoteExport{
		Format:   format,
		Filename: "netcatty-notes-all.zip",
		Content:  content,
	}, nil
}

// notesArchive writes one Markdown entry per Note plus the versioned manifest
// the Notes importer prefers. Group paths become directories; colliding file
// names get a numeric suffix rather than overwriting one another, while the
// manifest keeps the exact title.
func notesArchive(notes []domainvault.Note) ([]byte, error) {
	return vaultexport.NotesArchive(notes)
}

// exportFileName turns a Note title into a safe file name.
func exportFileName(title, extension string) string {
	return vaultexport.SanitizeName(title, "note") + "." + extension
}
