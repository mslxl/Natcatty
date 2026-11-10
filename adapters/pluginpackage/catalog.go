package pluginpackage

import (
	"errors"

	"github.com/mslxl/natcatty/adapters/plugini18n"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Catalog limits from the plugin i18n contract. They are aliases of the
// domain contract, so install validation and runtime loading cannot drift.
const (
	// MaxLocales bounds the catalogs a package may ship.
	MaxLocales = domainplugin.MaxLocales
	// MaxCatalogKeys bounds one locale.
	MaxCatalogKeys = domainplugin.MaxCatalogKeys
	// MaxMessageBytes bounds one message.
	MaxMessageBytes = domainplugin.MaxMessageBytes
	// MaxCatalogsBytes bounds every catalog together.
	MaxCatalogsBytes = domainplugin.MaxCatalogsBytes
)

// validateCatalogs checks the required i18n/<bcp47>.toml tree with the same
// parser activation uses. The default locale's catalog must exist, every TOML
// and locale tag must be valid, and the manifest's *_key references must name
// strings the default catalog defines.
func validateCatalogs(files map[string][]byte, manifest domainplugin.Manifest) error {
	catalogs, err := plugini18n.Parse(files, manifest.DefaultLocale)
	if err != nil {
		return catalogValidationError(err)
	}
	if err := plugini18n.ValidateReferencedKeys(catalogs, manifest.ReferencedMessageKeys()); err != nil {
		return catalogValidationError(err)
	}
	return nil
}

// catalogValidationError preserves the package-relative path the catalog
// parser reported.
func catalogValidationError(err error) error {
	var catalogErr *domainplugin.CatalogError
	if errors.As(err, &catalogErr) {
		return &InvalidPackageError{Path: catalogErr.Path, Problem: catalogErr.Problem}
	}
	return err
}
