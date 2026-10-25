package preferences

import (
	"reflect"
	"slices"
	"strings"
)

// OpenerKind is what the SFTP surface runs when a file is opened.
type OpenerKind string

const (
	// OpenerInternal opens the file in the app's own editor.
	OpenerInternal OpenerKind = "internal-editor"
	// OpenerSystem hands the file to the operating system's associated
	// application.
	OpenerSystem OpenerKind = "system-editor"
	// OpenerExternal runs one chosen application with the file.
	OpenerExternal OpenerKind = "external-application"
)

// ExternalApplication is a chosen executable and the name shown for it. The
// name is a display convenience; the path is what runs.
type ExternalApplication struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

// Opener binds an opener kind to the application an external kind runs. The
// application is empty for the internal and system kinds.
type Opener struct {
	Kind        OpenerKind          `json:"openerType"`
	Application ExternalApplication `json:"application,omitempty"`
}

// FileAssociation binds one extension to an opener. Extension is stored
// without the dot and in lower case.
type FileAssociation struct {
	Extension   string              `json:"extension"`
	Kind        OpenerKind          `json:"openerType"`
	Application ExternalApplication `json:"application,omitempty"`
}

// ValidOpenerKind reports whether kind is one of the three supported openers.
func ValidOpenerKind(kind OpenerKind) bool {
	return kind == OpenerInternal || kind == OpenerSystem || kind == OpenerExternal
}

// NormalizeExtension canonicalizes a user- or file-supplied extension: the
// dot and a leading "*." are dropped, it is lower-cased, and anything that is
// not a single suffix is refused. A file without an extension has "".
func NormalizeExtension(extension string) string {
	extension = strings.TrimSpace(extension)
	extension = strings.TrimPrefix(extension, "*")
	extension = strings.TrimSpace(extension)
	extension = strings.TrimLeft(extension, ".")
	extension = strings.ToLower(extension)
	if extension == "" || len(extension) > 32 {
		return ""
	}
	for _, r := range extension {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '+':
		default:
			return ""
		}
	}
	return extension
}

// FileExtension returns the suffix an association is matched on: the text
// after the last dot of the file's base name, in lower case. A name with no
// dot, a dot at the start of the base name, or a trailing dot has no
// extension, so both hidden files and directories cannot claim one. A path is
// accepted, so callers may resolve an entry without splitting it first.
func FileExtension(fileName string) string {
	base := fileName
	if index := strings.LastIndexAny(base, `/\`); index >= 0 {
		base = base[index+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return ""
	}
	return NormalizeExtension(base[dot+1:])
}

// NormalizeOpener repairs a persisted or default opener. An invalid kind, and
// an external opener without an executable, becomes the system opener; the
// application is cleared for the kinds that do not use one.
func NormalizeOpener(opener Opener) Opener {
	switch opener.Kind {
	case OpenerInternal, OpenerSystem:
		return Opener{Kind: opener.Kind}
	case OpenerExternal:
		application := normalizeApplication(opener.Application)
		if application.Path == "" {
			return Opener{Kind: OpenerSystem}
		}
		return Opener{Kind: OpenerExternal, Application: application}
	default:
		return Opener{Kind: OpenerSystem}
	}
}

// NormalizeFileAssociations canonicalizes every entry, drops incomplete ones,
// and keeps one entry per extension. A later duplicate replaces the earlier
// entry in place, mirroring the original's one-association-per-extension map.
// An empty list is nil so a settings document with no associations compares
// equal to the defaults.
func NormalizeFileAssociations(list []FileAssociation) []FileAssociation {
	var out []FileAssociation
	index := make(map[string]int, len(list))
	for _, association := range list {
		normalized, ok := normalizeAssociation(association)
		if !ok {
			continue
		}
		if at, seen := index[normalized.Extension]; seen {
			out[at] = normalized
			continue
		}
		index[normalized.Extension] = len(out)
		out = append(out, normalized)
	}
	return out
}

func normalizeAssociation(association FileAssociation) (FileAssociation, bool) {
	extension := NormalizeExtension(association.Extension)
	if extension == "" || !ValidOpenerKind(association.Kind) {
		return FileAssociation{}, false
	}
	if association.Kind == OpenerExternal {
		application := normalizeApplication(association.Application)
		if application.Path == "" {
			return FileAssociation{}, false
		}
		return FileAssociation{Extension: extension, Kind: association.Kind,
			Application: application}, true
	}
	return FileAssociation{Extension: extension, Kind: association.Kind}, true
}

func normalizeApplication(application ExternalApplication) ExternalApplication {
	path := strings.TrimSpace(application.Path)
	name := strings.TrimSpace(application.Name)
	if path == "" {
		return ExternalApplication{}
	}
	if name == "" {
		name = applicationBase(path)
	}
	return ExternalApplication{Path: path, Name: name}
}

func applicationBase(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// AssociationFor returns the association for one extension, if one is
// configured. The extension is normalized, so callers may pass ".TXT".
func (e SFTPEditor) AssociationFor(extension string) (FileAssociation, bool) {
	extension = NormalizeExtension(extension)
	if extension == "" {
		return FileAssociation{}, false
	}
	for _, association := range e.FileAssociations {
		normalized, ok := normalizeAssociation(association)
		if ok && normalized.Extension == extension {
			return normalized, true
		}
	}
	return FileAssociation{}, false
}

// OpenerFor resolves which opener runs for a file: its extension's
// association when one exists, and the default opener otherwise. Unknown
// extensions and files without an extension therefore use the default.
func (e SFTPEditor) OpenerFor(fileName string) Opener {
	if association, ok := e.AssociationFor(FileExtension(fileName)); ok {
		return Opener{Kind: association.Kind, Application: association.Application}
	}
	return NormalizeOpener(e.DefaultOpener)
}

// SetAssociation adds or replaces the association for its extension. It
// returns a new SFTPEditor; the receiver's slice is never mutated. An
// incomplete association is refused instead of being stored.
func (e SFTPEditor) SetAssociation(association FileAssociation) (SFTPEditor, bool) {
	normalized, ok := normalizeAssociation(association)
	if !ok {
		return e, false
	}
	out := slices.Clone(e.FileAssociations)
	for index := range out {
		if NormalizeExtension(out[index].Extension) == normalized.Extension {
			out[index] = normalized
			e.FileAssociations = out
			return e, true
		}
	}
	e.FileAssociations = append(out, normalized)
	return e, true
}

// RemoveAssociation drops the association for one extension. Removing an
// extension that has none returns an equivalent value.
func (e SFTPEditor) RemoveAssociation(extension string) SFTPEditor {
	extension = NormalizeExtension(extension)
	if extension == "" {
		return e
	}
	out := make([]FileAssociation, 0, len(e.FileAssociations))
	for _, association := range e.FileAssociations {
		if NormalizeExtension(association.Extension) == extension {
			continue
		}
		out = append(out, association)
	}
	if len(out) == 0 {
		out = nil
	}
	e.FileAssociations = out
	return e
}

// Equal compares two settings documents structurally. Settings contains a
// slice, so the language's == operator no longer applies; the UI's reset
// control and the persistence tests need a value comparison.
func Equal(a, b Settings) bool {
	return reflect.DeepEqual(a, b)
}
