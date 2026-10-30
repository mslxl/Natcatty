package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListSnippets returns every Snippet in display order.
func (s *Store) ListSnippets(ctx context.Context) ([]domainvault.Snippet, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &snippetSchema{}, "order_key ASC", "label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Snippets: %w", err)
	}
	snippets, err := decodeDataList[domainvault.Snippet](data, "Snippet")
	if err != nil {
		return nil, err
	}
	return snippets, nil
}

// GetSnippet returns one Snippet by id.
func (s *Store) GetSnippet(ctx context.Context, id string) (domainvault.Snippet, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &snippetSchema{}, &snippetSchema{ID: id})
		return err
	}); err != nil {
		return domainvault.Snippet{}, fmt.Errorf("store: get Snippet: %w", err)
	}
	if !found {
		return domainvault.Snippet{}, appvault.ErrNotFound
	}
	var snippet domainvault.Snippet
	if err := decodeData(data, "Snippet", &snippet); err != nil {
		return domainvault.Snippet{}, err
	}
	return snippet, nil
}

// SaveSnippet inserts or replaces one Snippet.
func (s *Store) SaveSnippet(ctx context.Context, snippet domainvault.Snippet) error {
	model, err := snippetModel(snippet)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "label", "package_path", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Snippet: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteSnippet removes one Snippet.
func (s *Store) DeleteSnippet(ctx context.Context, id string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &snippetSchema{}, &snippetSchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Snippet: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}

// ListSnippetPackages returns every saved package in display order.
func (s *Store) ListSnippetPackages(ctx context.Context) ([]domainvault.SnippetPackage, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &snippetPackageSchema{}, "order_key ASC", "path COLLATE NOCASE ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Snippet packages: %w", err)
	}
	packages, err := decodeDataList[domainvault.SnippetPackage](data, "Snippet package")
	if err != nil {
		return nil, err
	}
	return packages, nil
}

// GetSnippetPackage returns one saved package by path.
func (s *Store) GetSnippetPackage(ctx context.Context, path string) (domainvault.SnippetPackage, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &snippetPackageSchema{}, &snippetPackageSchema{Path: path})
		return err
	}); err != nil {
		return domainvault.SnippetPackage{}, fmt.Errorf("store: get Snippet package: %w", err)
	}
	if !found {
		return domainvault.SnippetPackage{}, appvault.ErrNotFound
	}
	var pkg domainvault.SnippetPackage
	if err := decodeData(data, "Snippet package", &pkg); err != nil {
		return domainvault.SnippetPackage{}, err
	}
	return pkg, nil
}

// SaveSnippetPackage inserts or replaces one package.
func (s *Store) SaveSnippetPackage(ctx context.Context, pkg domainvault.SnippetPackage) error {
	model, err := snippetPackageModel(pkg)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "path", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Snippet package: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteSnippetPackage removes one package. Snippet references are cleared by
// the Vault service.
func (s *Store) DeleteSnippetPackage(ctx context.Context, path string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &snippetPackageSchema{}, &snippetPackageSchema{Path: path})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Snippet package: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
