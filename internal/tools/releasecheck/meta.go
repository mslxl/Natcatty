package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CatalogTags are the languages the app ships, source first.
var CatalogTags = []string{"en", "es", "ru", "zh-CN", "zh-TW"}

// forbiddenMetadataName recognizes update feeds, update manifests, delta
// archives, and signing material. None belong in a release that does not
// update itself and is unsigned.
func forbiddenMetadataName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	switch {
	case strings.HasPrefix(base, "update-") && strings.HasSuffix(base, ".json"):
		return true
	case strings.HasPrefix(base, "latest") &&
		(strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
		return true
	case strings.HasPrefix(base, "appcast") && strings.HasSuffix(base, ".xml"):
		return true
	case strings.HasSuffix(base, ".delta"),
		strings.HasSuffix(base, ".p12"),
		strings.HasSuffix(base, ".pfx"),
		strings.HasSuffix(base, ".mobileprovision"):
		return true
	}
	return false
}

// checkNoUpdateMetadata rejects update/signing configuration in mygo.json and
// update artifacts in a directory tree. The macOS ad hoc signature is not
// signing metadata and leaves no file.
func checkNoUpdateMetadata(rootDir, manifestPath string) error {
	if manifestPath != "" {
		if err := checkManifest(manifestPath); err != nil {
			return err
		}
	}
	if rootDir == "" {
		return nil
	}
	var found []string
	err := filepath.WalkDir(rootDir, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if forbiddenMetadataName(entry.Name()) {
			relative, relErr := filepath.Rel(rootDir, filePath)
			if relErr != nil {
				relative = filePath
			}
			found = append(found, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(found) > 0 {
		sort.Strings(found)
		return fmt.Errorf("update or signing metadata found: %s", strings.Join(found, ", "))
	}
	return nil
}

// checkEmbeddedCatalogs proves from the executable itself that every catalog
// is embedded: Go's //go:embed stores the JSON verbatim, so a distinctive,
// catalog-only message from each language must appear in the binary.
func checkEmbeddedCatalogs(binaryPath, catalogDir string) error {
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("%s is empty", binaryPath)
	}
	catalogs := make(map[string][]string, len(CatalogTags))
	for _, tag := range CatalogTags {
		values, err := catalogStrings(filepath.Join(catalogDir, tag+".json"))
		if err != nil {
			return err
		}
		catalogs[tag] = values
	}
	seen := map[string]string{}
	for _, tag := range CatalogTags {
		marker, err := catalogMarker(tag, catalogs)
		if err != nil {
			return err
		}
		if other, ok := seen[marker]; ok {
			return fmt.Errorf("%s and %s share the marker %q", tag, other, marker)
		}
		seen[marker] = tag
		if !bytes.Contains(data, []byte(marker)) {
			return fmt.Errorf("%s does not embed the %s catalog: marker %q is missing", binaryPath, tag, marker)
		}
	}
	return nil
}

func catalogStrings(filePath string) ([]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	var values []string
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case string:
			values = append(values, value)
		case []any:
			for _, item := range value {
				walk(item)
			}
		case map[string]any:
			for _, item := range value {
				walk(item)
			}
		}
	}
	walk(root)
	if len(values) == 0 {
		return nil, fmt.Errorf("%s holds no messages", filePath)
	}
	return values, nil
}

// catalogMarker picks a deterministic message that only this catalog has: a
// marker that also appears in another language would prove nothing when found
// in a binary that embeds all five. Non-ASCII text is preferred, then length.
func catalogMarker(tag string, catalogs map[string][]string) (string, error) {
	others := make([]string, 0, len(catalogs))
	for other, values := range catalogs {
		if other == tag {
			continue
		}
		// One NUL-joined blob per other catalog makes the substring test a
		// single pass: a marker that another language contains, even inside a
		// longer message, would not prove this catalog is embedded.
		others = append(others, strings.Join(values, "\x00"))
	}
	var best string
	bestScore := -1
	for _, value := range catalogs[tag] {
		if utf8.RuneCountInString(value) < 4 {
			continue
		}
		if strings.ContainsAny(value, "\"\\\r\n\t\x00") {
			continue
		}
		unique := true
		for _, blob := range others {
			if strings.Contains(blob, value) {
				unique = false
				break
			}
		}
		if !unique {
			continue
		}
		score := utf8.RuneCountInString(value)
		if hasNonASCII(value) {
			score += 10000
		}
		if score > bestScore || (score == bestScore && value < best) {
			best = value
			bestScore = score
		}
	}
	if best == "" {
		return "", fmt.Errorf("the %s catalog has no message that every other catalog lacks", tag)
	}
	return best, nil
}

func hasNonASCII(value string) bool {
	for _, r := range value {
		if r > unicode.MaxASCII {
			return true
		}
	}
	return false
}
