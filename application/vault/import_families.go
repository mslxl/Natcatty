package vault

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// FamilyImportResult counts records a family import created.
type FamilyImportResult struct {
	Created int
	Skipped int
}

// ImportSnippetsJSON restores a Snippets document produced by the export. It
// is additive: labels and package paths are created as the document states.
func (s *Service) ImportSnippetsJSON(ctx context.Context, text string) (FamilyImportResult, error) {
	var document struct {
		Kind            string   `json:"kind"`
		Version         int      `json:"version"`
		SnippetPackages []string `json:"snippetPackages"`
		Snippets        []struct {
			Label           string   `json:"label"`
			Command         string   `json:"command"`
			Tags            []string `json:"tags"`
			Package         string   `json:"package"`
			TargetGroups    []string `json:"targetGroups"`
			TargetsAllHosts bool     `json:"targetsAllHosts"`
			NoAutoRun       bool     `json:"noAutoRun"`
			MultiLineMode   string   `json:"multiLineRunMode"`
		} `json:"snippets"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "\xef\xbb\xbf")), &document); err != nil {
		return FamilyImportResult{}, invalid(err)
	}
	if document.Kind != "" && document.Kind != "netcatty.snippets" {
		return FamilyImportResult{}, invalid(fmt.Errorf("vault: not a Snippets document"))
	}
	if document.Version != 0 && document.Version != 1 {
		return FamilyImportResult{}, invalid(fmt.Errorf("vault: unsupported Snippets version %d", document.Version))
	}

	result := FamilyImportResult{}
	for _, path := range document.SnippetPackages {
		if _, err := s.CreateSnippetPackage(ctx, domainvault.SnippetPackageDraft{Path: path}); err != nil {
			if typed, ok := err.(*Error); ok && typed.Kind == KindConflict {
				continue
			}
			return result, err
		}
	}
	for _, item := range document.Snippets {
		_, err := s.CreateSnippet(ctx, domainvault.SnippetDraft{
			Label:            item.Label,
			Command:          item.Command,
			Tags:             item.Tags,
			Package:          item.Package,
			TargetGroups:     item.TargetGroups,
			TargetsAllHosts:  item.TargetsAllHosts,
			NoAutoRun:        item.NoAutoRun,
			MultiLineRunMode: domainvault.SnippetMultiLineRunMode(item.MultiLineMode),
		})
		if err != nil {
			result.Skipped++
			continue
		}
		result.Created++
	}
	return result, nil
}

// ImportNotesZIP restores the Markdown archive the notes export writes. A
// Note's directory path becomes its Note Group; the base name becomes its
// title.
func (s *Service) ImportNotesZIP(ctx context.Context, archive []byte) (FamilyImportResult, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return FamilyImportResult{}, invalid(err)
	}
	result := FamilyImportResult{}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name), ".md") {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			result.Skipped++
			continue
		}
		var content bytes.Buffer
		_, readErr := content.ReadFrom(file)
		_ = file.Close()
		if readErr != nil {
			result.Skipped++
			continue
		}
		name := strings.ReplaceAll(entry.Name, `\`, "/")
		group := path.Dir(name)
		if group == "." {
			group = ""
		}
		title := strings.TrimSuffix(path.Base(name), path.Ext(name))
		if group != "" {
			if _, err := s.CreateNoteGroup(ctx, domainvault.NoteGroupDraft{Path: group}); err != nil {
				if typed, ok := err.(*Error); !ok || typed.Kind != KindConflict {
					return result, err
				}
			}
		}
		if _, err := s.CreateNote(ctx, domainvault.NoteDraft{
			Title: title, Content: content.String(), Group: group,
		}); err != nil {
			result.Skipped++
			continue
		}
		result.Created++
	}
	return result, nil
}

// ImportKeysJSON restores keys from a keys export. Keys without private
// material produce no Key because an imported Key needs material to be usable.
func (s *Service) ImportKeysJSON(ctx context.Context, text string) (FamilyImportResult, error) {
	var document struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
		Keys    []struct {
			Label       string `json:"label"`
			Type        string `json:"type"`
			PublicKey   string `json:"publicKey"`
			Certificate string `json:"certificate"`
			FilePath    string `json:"filePath"`
			PrivateKey  string `json:"privateKey"`
			Passphrase  string `json:"passphrase"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "\xef\xbb\xbf")), &document); err != nil {
		return FamilyImportResult{}, invalid(err)
	}
	if document.Kind != "" && document.Kind != "netcatty.keys" {
		return FamilyImportResult{}, invalid(fmt.Errorf("vault: not a Keys document"))
	}
	result := FamilyImportResult{}
	for _, item := range document.Keys {
		if item.PrivateKey == "" {
			result.Skipped++
			continue
		}
		_, err := s.CreateKey(ctx, domainvault.KeyDraft{
			Label:          item.Label,
			PrivateKey:     item.PrivateKey,
			Passphrase:     item.Passphrase,
			SavePassphrase: item.Passphrase != "",
			Certificate:    item.Certificate,
			PublicKey:      item.PublicKey,
			Type:           domainvault.KeyType(item.Type),
			FilePath:       item.FilePath,
			Source:         domainvault.KeySourceImported,
		})
		if err != nil {
			result.Skipped++
			continue
		}
		result.Created++
	}
	return result, nil
}
