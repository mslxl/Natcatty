package store

import (
	"context"
	"encoding/json"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ListSnippets returns every Snippet in display order.
func (s *Store) ListSnippets(ctx context.Context) ([]domainvault.Snippet, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM snippets
		ORDER BY order_key ASC, label COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Snippets: %w", err)
	}
	snippets := make([]domainvault.Snippet, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Snippet data is %T", row[0])
		}
		var snippet domainvault.Snippet
		if err := json.Unmarshal([]byte(data), &snippet); err != nil {
			return nil, fmt.Errorf("store: decode Snippet: %w", err)
		}
		snippets = append(snippets, snippet)
	}
	return snippets, nil
}

// GetSnippet returns one Snippet by id.
func (s *Store) GetSnippet(ctx context.Context, id string) (domainvault.Snippet, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM snippets WHERE id = ?`, id)
	if err != nil {
		return domainvault.Snippet{}, fmt.Errorf("store: get Snippet: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.Snippet{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.Snippet{}, fmt.Errorf("store: Snippet data is %T", rows.Values[0][0])
	}
	var snippet domainvault.Snippet
	if err := json.Unmarshal([]byte(data), &snippet); err != nil {
		return domainvault.Snippet{}, fmt.Errorf("store: decode Snippet: %w", err)
	}
	return snippet, nil
}

// SaveSnippet inserts or replaces one Snippet.
func (s *Store) SaveSnippet(ctx context.Context, snippet domainvault.Snippet) error {
	if snippet.ID == "" {
		return fmt.Errorf("store: Snippet has no id")
	}
	data, err := json.Marshal(snippet)
	if err != nil {
		return fmt.Errorf("store: encode Snippet: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO snippets (
			id, label, package_path, order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label,
			package_path = excluded.package_path,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		snippet.ID,
		snippet.Label,
		snippet.Package,
		snippet.OrderKey,
		snippet.CreatedAt.UnixMilli(),
		snippet.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Snippet: %w", err)
	}
	return nil
}

// DeleteSnippet removes one Snippet.
func (s *Store) DeleteSnippet(ctx context.Context, id string) error {
	result, err := s.db.Execute(ctx, `DELETE FROM snippets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete Snippet: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	return nil
}

// ListSnippetPackages returns every saved package in display order.
func (s *Store) ListSnippetPackages(ctx context.Context) ([]domainvault.SnippetPackage, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM snippet_packages
		ORDER BY order_key ASC, path COLLATE NOCASE ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Snippet packages: %w", err)
	}
	packages := make([]domainvault.SnippetPackage, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Snippet package data is %T", row[0])
		}
		var pkg domainvault.SnippetPackage
		if err := json.Unmarshal([]byte(data), &pkg); err != nil {
			return nil, fmt.Errorf("store: decode Snippet package: %w", err)
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

// GetSnippetPackage returns one saved package by path.
func (s *Store) GetSnippetPackage(ctx context.Context, path string) (domainvault.SnippetPackage, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM snippet_packages WHERE path = ?`, path)
	if err != nil {
		return domainvault.SnippetPackage{}, fmt.Errorf("store: get Snippet package: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.SnippetPackage{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.SnippetPackage{}, fmt.Errorf("store: Snippet package data is %T", rows.Values[0][0])
	}
	var pkg domainvault.SnippetPackage
	if err := json.Unmarshal([]byte(data), &pkg); err != nil {
		return domainvault.SnippetPackage{}, fmt.Errorf("store: decode Snippet package: %w", err)
	}
	return pkg, nil
}

// SaveSnippetPackage inserts or replaces one package.
func (s *Store) SaveSnippetPackage(ctx context.Context, pkg domainvault.SnippetPackage) error {
	if pkg.Path == "" {
		return fmt.Errorf("store: Snippet package has no path")
	}
	data, err := json.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("store: encode Snippet package: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO snippet_packages (
			path, order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		pkg.Path,
		pkg.OrderKey,
		pkg.CreatedAt.UnixMilli(),
		pkg.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Snippet package: %w", err)
	}
	return nil
}

// DeleteSnippetPackage removes one package. Snippet references are cleared by
// the Vault service, which owns the non-cascading rule; the adapter never
// touches another table.
func (s *Store) DeleteSnippetPackage(ctx context.Context, path string) error {
	result, err := s.db.Execute(ctx, `DELETE FROM snippet_packages WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("store: delete Snippet package: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	return nil
}
