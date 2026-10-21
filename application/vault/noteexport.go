package vault

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode"

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

// notesArchive writes one Markdown entry per Note. Group paths become
// directories; colliding file names get a numeric suffix rather than
// overwriting one another.
func notesArchive(notes []domainvault.Note) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	used := make(map[string]int, len(notes))
	for _, note := range notes {
		name := noteArchiveName(note)
		key := strings.ToLower(name)
		used[key]++
		if used[key] > 1 {
			name = addNameSuffix(name, used[key])
		}
		writer, err := archive.Create(name)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if _, err := writer.Write([]byte(note.Content)); err != nil {
			_ = archive.Close()
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// noteArchiveName builds a group-preserving, filesystem-safe entry name.
func noteArchiveName(note domainvault.Note) string {
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(domainvault.NormalizeGroup(note.Group), "/") {
		if segment == "" {
			continue
		}
		segments = append(segments, sanitizeExportName(segment, "group"))
	}
	segments = append(segments, exportFileName(note.Title, "md"))
	return strings.Join(segments, "/")
}

// exportFileName turns a Note title into a safe file name.
func exportFileName(title, extension string) string {
	return sanitizeExportName(title, "note") + "." + extension
}

// reservedWindowsNames are names Windows refuses as a file stem.
var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// sanitizeExportName matches the original Notes export file-name rules:
// control and Windows-unsafe characters become dashes, whitespace collapses to
// one space, trailing dots and spaces are stripped, a reserved Windows stem
// gains an underscore, and the result is capped at 120 runes. An empty or
// unusable title falls back to fallback.
func sanitizeExportName(name, fallback string) string {
	name = strings.TrimSpace(name)
	var builder strings.Builder
	lastSpace := false
	count := 0
	for _, r := range name {
		if count >= 120 {
			break
		}
		switch {
		case unicode.IsControl(r), r == '<', r == '>', r == ':', r == '"',
			r == '/', r == '\\', r == '|', r == '?', r == '*':
			builder.WriteByte('-')
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace && builder.Len() > 0 {
				builder.WriteByte(' ')
				lastSpace = true
			}
		default:
			builder.WriteRune(r)
			lastSpace = false
		}
		count++
	}
	result := strings.TrimRight(builder.String(), ". ")
	if result == "" || result == "." || result == ".." {
		return fallback
	}
	stem := result
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	if reservedWindowsNames[strings.ToLower(stem)] {
		result += "_"
	}
	return result
}

// addNameSuffix adds "-2" before the extension of name.
func addNameSuffix(name string, count int) string {
	dot := strings.LastIndexByte(name, '.')
	suffix := "-" + strconv.Itoa(count)
	if dot < 0 {
		return name + suffix
	}
	return name[:dot] + suffix + name[dot:]
}
