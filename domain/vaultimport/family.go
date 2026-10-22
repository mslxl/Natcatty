package vaultimport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// NotesManifest is the versioned index written inside a Notes ZIP. It maps the
// human-readable archive path of each Note to its exact Note Group, title, and
// identity, so a title containing path-hostile characters or a duplicate title
// survives the round trip unambiguously.
type NotesManifest struct {
	Kind    string               `json:"kind"`
	Version int                  `json:"version"`
	Entries []NotesManifestEntry `json:"entries"`
	// NoteGroups lists every Note Group, including groups with no Note. It is
	// optional so version-1 archives written before it existed still import.
	NoteGroups []string `json:"noteGroups,omitempty"`
}

// NotesManifestEntry maps one archive entry to its Note.
type NotesManifestEntry struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Group string `json:"group,omitempty"`
	ID    string `json:"id,omitempty"`
}

// parseSnippetsJSON reads a portable Snippets document.
func parseSnippetsJSON(text string) Result {
	result := Result{Format: FormatSnippets}
	var document struct {
		Kind            string        `json:"kind"`
		Version         int           `json:"version"`
		SnippetPackages []string      `json:"snippetPackages"`
		Snippets        []jsonSnippet `json:"snippets"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "\xef\xbb\xbf")), &document); err != nil {
		result.Issues = append(result.Issues, errorIssue(IssueInvalidJSON, 0, err.Error()))
		return result
	}
	if document.Kind != "" && document.Kind != KindSnippets {
		result.Issues = append(result.Issues, errorIssue(IssueUnsupportedEntry, 0, document.Kind))
		return result
	}
	if document.Version != 0 && document.Version != 1 {
		result.Issues = append(result.Issues, errorIssue(IssueUnsupportedEntry, 0, "version"))
		return result
	}
	family := &Family{SnippetPackages: append([]string(nil), document.SnippetPackages...)}
	for _, snippet := range document.Snippets {
		family.Snippets = append(family.Snippets, FamilySnippet{
			ID: snippet.ID, Label: strings.TrimSpace(snippet.Label),
			Command: snippet.Command, Package: strings.TrimSpace(snippet.Package),
			Tags:            append([]string(nil), snippet.Tags...),
			Targets:         normalizeTargetIDs(snippet.Targets),
			TargetGroups:    append([]string(nil), snippet.TargetGroups...),
			TargetsAllHosts: snippet.TargetsAllHosts, NoAutoRun: snippet.NoAutoRun,
			MultiLineMode:  snippet.MultiLineMode,
			VariableValues: cloneStringMap(snippet.VariableValues),
		})
	}
	result.Family = family
	return result
}

// parseKeysJSON reads a portable SSH Keys document.
func parseKeysJSON(text string) Result {
	result := Result{Format: FormatKeys}
	var document struct {
		Kind    string    `json:"kind"`
		Version int       `json:"version"`
		Keys    []jsonKey `json:"keys"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "\xef\xbb\xbf")), &document); err != nil {
		result.Issues = append(result.Issues, errorIssue(IssueInvalidJSON, 0, err.Error()))
		return result
	}
	if document.Kind != "" && document.Kind != KindKeys {
		result.Issues = append(result.Issues, errorIssue(IssueUnsupportedEntry, 0, document.Kind))
		return result
	}
	if document.Version != 0 && document.Version != 1 {
		result.Issues = append(result.Issues, errorIssue(IssueUnsupportedEntry, 0, "version"))
		return result
	}
	family := &Family{}
	for _, key := range document.Keys {
		family.Keys = append(family.Keys, FamilyKey{
			ID: key.ID, Label: strings.TrimSpace(key.Label), Type: key.Type,
			PublicKey: key.PublicKey, Certificate: key.Certificate,
			FilePath:   strings.TrimSpace(key.FilePath),
			PrivateKey: key.PrivateKey, Passphrase: key.Passphrase,
		})
	}
	result.Family = family
	return result
}

// parseNotesZIP reads a Notes Markdown archive. The manifest wins when
// present; a foreign archive without one falls back to deriving the group from
// the directory and the title from the file name.
func parseNotesZIP(text string) Result {
	result := Result{Format: FormatNotesZIP}
	reader, err := zip.NewReader(bytes.NewReader([]byte(text)), int64(len(text)))
	if err != nil {
		result.Issues = append(result.Issues, errorIssue(IssueInvalidArchive, 0, err.Error()))
		return result
	}

	family := &Family{}
	manifest, manifestErr := readNotesManifest(reader)
	if manifestErr != nil {
		result.Issues = append(result.Issues, Issue{
			Severity: SeverityWarning, Code: IssueInvalidManifest, Detail: manifestErr.Error(),
		})
	}
	entries := make(map[string]NotesManifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.Path != "" {
			entries[entry.Path] = entry
		}
	}
	seenPaths := make(map[string]bool, len(entries))
	groups := make(map[string]bool)
	for _, group := range manifest.NoteGroups {
		for _, path := range noteGroupAncestors(group) {
			groups[path] = true
		}
	}

	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(file.Name, `\`, "/")
		if name == NotesManifestName {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		content, readErr := readZipEntry(file)
		if readErr != nil {
			result.Issues = append(result.Issues, Issue{
				Severity: SeverityWarning, Code: IssueMissingNoteEntry,
				Value: name, Detail: readErr.Error(),
			})
			continue
		}
		note := FamilyNote{Content: content}
		if entry, ok := entries[name]; ok {
			note.Title = entry.Title
			note.Group = normalizeGroup(entry.Group)
			note.ID = entry.ID
			seenPaths[name] = true
		} else {
			group := path.Dir(name)
			if group == "." {
				group = ""
			}
			note.Group = normalizeGroup(group)
			note.Title = strings.TrimSuffix(path.Base(name), path.Ext(name))
		}
		// Derive the Note Group list from the entries as well, so an older
		// manifest without noteGroups still restores its groups.
		for _, groupPath := range noteGroupAncestors(note.Group) {
			groups[groupPath] = true
		}
		family.Notes = append(family.Notes, note)
	}
	// A manifest entry whose file is missing is reported instead of silently
	// disappearing from the round trip.
	for entryPath, entry := range entries {
		if seenPaths[entryPath] {
			continue
		}
		result.Issues = append(result.Issues, Issue{
			Severity: SeverityWarning, Code: IssueMissingNoteEntry,
			Value: entryPath, Detail: entry.Title,
		})
	}
	if len(groups) > 0 {
		paths := make([]string, 0, len(groups))
		for groupPath := range groups {
			paths = append(paths, groupPath)
		}
		sort.Strings(paths)
		family.NoteGroups = paths
	}
	result.Family = family
	return result
}

// noteGroupAncestors expands one Note Group path, root first. It mirrors the
// Host Group hierarchy so an importer creates both "Ops" and "Ops/Config".
func noteGroupAncestors(path string) []string {
	path = normalizeGroup(path)
	if path == "" {
		return nil
	}
	segments := strings.Split(path, "/")
	out := make([]string, 0, len(segments))
	for index := range segments {
		out = append(out, strings.Join(segments[:index+1], "/"))
	}
	return out
}

func readNotesManifest(reader *zip.Reader) (NotesManifest, error) {
	for _, file := range reader.File {
		if strings.ReplaceAll(file.Name, `\`, "/") != NotesManifestName {
			continue
		}
		content, err := readZipEntry(file)
		if err != nil {
			return NotesManifest{}, err
		}
		var manifest NotesManifest
		if err := json.Unmarshal([]byte(strings.TrimPrefix(content, "\xef\xbb\xbf")), &manifest); err != nil {
			return NotesManifest{}, err
		}
		if manifest.Kind != "" && manifest.Kind != KindNotes {
			return NotesManifest{}, errUnsupportedManifest
		}
		if manifest.Version != 0 && manifest.Version != 1 {
			return NotesManifest{}, errUnsupportedManifest
		}
		return manifest, nil
	}
	return NotesManifest{}, nil
}

// errUnsupportedManifest reports a manifest this importer does not understand.
// The caller falls back to the directory layout with a warning.
type manifestError string

func (e manifestError) Error() string { return string(e) }

const errUnsupportedManifest = manifestError("unsupported notes manifest")

func readZipEntry(file *zip.File) (string, error) {
	reader, err := file.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	var content bytes.Buffer
	if _, err := content.ReadFrom(reader); err != nil {
		return "", err
	}
	return content.String(), nil
}
