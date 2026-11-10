// Package plugini18n parses the plugin i18n/<bcp47>.toml catalog assets. The
// same parser serves .natplug install validation, installed and development
// package activation, and first-party plugins that embed their catalogs, so
// every plugin follows one catalog format.
package plugini18n

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Directory is the package-relative catalog directory.
const Directory = "i18n"

// Source loads catalogs from an unpacked package or development directory.
// It implements the application layer's CatalogSource port.
type Source struct{}

var _ appplugin.CatalogSource = Source{}

// LoadCatalogs implements application/plugin.CatalogSource.
func (Source) LoadCatalogs(directory, defaultLocale string) (domainplugin.Catalogs, error) {
	return LoadDirectory(directory, defaultLocale)
}

// LoadDirectory reads the i18n/*.toml assets under directory.
func LoadDirectory(directory, defaultLocale string) (domainplugin.Catalogs, error) {
	return LoadFS(os.DirFS(directory), defaultLocale)
}

// LoadFS reads the i18n/*.toml assets under root in fsys. A first-party
// plugin embeds the same tree and calls this with its embed.FS, so compiled-in
// plugins use the identical format and validation.
func LoadFS(fsys fs.FS, defaultLocale string) (domainplugin.Catalogs, error) {
	files := make(map[string][]byte)
	entries, err := fs.ReadDir(fsys, Directory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domainplugin.Catalogs{}, missingDefault(defaultLocale)
		}
		return domainplugin.Catalogs{}, &domainplugin.CatalogError{
			Path: Directory, Problem: fmt.Sprintf("read catalog directory: %v", err)}
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		data, err := fs.ReadFile(fsys, Directory+"/"+name)
		if err != nil {
			return domainplugin.Catalogs{}, &domainplugin.CatalogError{
				Path: Directory + "/" + name, Problem: fmt.Sprintf("read catalog: %v", err)}
		}
		files[Directory+"/"+name] = data
	}
	return Parse(files, defaultLocale)
}

// Parse decodes the i18n/*.toml entries of an in-memory package file set.
// Other package files are ignored.
func Parse(files map[string][]byte, defaultLocale string) (domainplugin.Catalogs, error) {
	names := catalogNames(files)
	if len(names) == 0 {
		return domainplugin.Catalogs{}, missingDefault(defaultLocale)
	}
	total := 0
	for _, name := range names {
		total += len(files[name])
	}
	if total > domainplugin.MaxCatalogsBytes {
		return domainplugin.Catalogs{}, &domainplugin.CatalogError{
			Path: Directory, Problem: fmt.Sprintf(
				"catalogs total %d bytes, above the %d byte limit", total, domainplugin.MaxCatalogsBytes)}
	}
	if len(names) > domainplugin.MaxLocales {
		return domainplugin.Catalogs{}, &domainplugin.CatalogError{
			Path: Directory, Problem: fmt.Sprintf(
				"package ships %d catalogs, above the %d locale limit", len(names), domainplugin.MaxLocales)}
	}
	locales := make(map[string]domainplugin.Catalog, len(names))
	for _, name := range names {
		tag := strings.TrimSuffix(strings.TrimPrefix(name, Directory+"/"), ".toml")
		if strings.Contains(tag, "/") {
			return domainplugin.Catalogs{}, &domainplugin.CatalogError{
				Path: name, Problem: "catalog must sit directly under i18n/"}
		}
		catalog, err := parseCatalog(name, files[name])
		if err != nil {
			return domainplugin.Catalogs{}, err
		}
		locales[tag] = catalog
	}
	catalogs := domainplugin.Catalogs{Default: defaultLocale, Locales: locales}
	if err := catalogs.Validate(); err != nil {
		return domainplugin.Catalogs{}, err
	}
	return catalogs, nil
}

func catalogNames(files map[string][]byte) []string {
	names := make([]string, 0)
	for name := range files {
		if strings.HasPrefix(name, Directory+"/") && strings.HasSuffix(name, ".toml") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// parseCatalog decodes one flat TOML catalog. The optional meta table carries
// documentation only; every other top-level key is a message.
func parseCatalog(name string, data []byte) (domainplugin.Catalog, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, &domainplugin.CatalogError{Path: name, Problem: fmt.Sprintf("parse TOML: %v", err)}
	}
	catalog := make(domainplugin.Catalog, len(raw))
	for key, value := range raw {
		if key == "meta" {
			meta, ok := value.(map[string]any)
			if !ok {
				return nil, &domainplugin.CatalogError{Path: name, Problem: "meta must be a table"}
			}
			for metaKey, metaValue := range meta {
				if _, ok := metaValue.(string); !ok {
					return nil, &domainplugin.CatalogError{
						Path: name, Problem: fmt.Sprintf("meta.%s must be a string", metaKey)}
				}
			}
			continue
		}
		message, ok := value.(string)
		if !ok {
			return nil, &domainplugin.CatalogError{
				Path: name, Problem: fmt.Sprintf("key %q is %T, want a string", key, value)}
		}
		if !domainplugin.ValidMessageKey(key) {
			return nil, &domainplugin.CatalogError{
				Path: name, Problem: fmt.Sprintf("key %q does not match [a-z0-9_.-]{1,128}", key)}
		}
		if len(message) > domainplugin.MaxMessageBytes {
			return nil, &domainplugin.CatalogError{
				Path: name, Problem: fmt.Sprintf(
					"message %q is %d bytes, above the %d byte limit",
					key, len(message), domainplugin.MaxMessageBytes)}
		}
		catalog[key] = message
	}
	if len(catalog) > domainplugin.MaxCatalogKeys {
		return nil, &domainplugin.CatalogError{
			Path: name, Problem: fmt.Sprintf(
				"catalog has %d keys, above the %d key limit", len(catalog), domainplugin.MaxCatalogKeys)}
	}
	return catalog, nil
}

func missingDefault(defaultLocale string) error {
	return &domainplugin.CatalogError{
		Path: Directory, Problem: fmt.Sprintf("default locale %q has no catalog", defaultLocale)}
}

// ValidateReferencedKeys reports the first manifest-referenced message key that
// the default catalog does not define. Install validation runs this so a
// host-rendered contribution cannot point at a missing translation.
func ValidateReferencedKeys(catalogs domainplugin.Catalogs,
	refs []domainplugin.MessageKeyRef) error {
	defaultCatalog, _, ok := catalogs.Locale(catalogs.Default)
	if !ok {
		return missingDefault(catalogs.Default)
	}
	for _, ref := range refs {
		if _, exists := defaultCatalog[ref.Key]; !exists {
			return &domainplugin.CatalogError{
				Path: Directory,
				Problem: fmt.Sprintf(
					"manifest key %q (%s) is not in the default locale %q catalog",
					ref.Key, ref.Field, catalogs.Default),
			}
		}
	}
	return nil
}
