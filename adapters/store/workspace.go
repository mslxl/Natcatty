package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appworkspace "github.com/mslxl/natcatty/application/workspace"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

// WorkspaceRepository is the Workspace persistence view of the Store. It is a
// separate type because Store already has a vault-oriented Save method.
type WorkspaceRepository struct {
	db *platformsqlite.DB
}

var _ appworkspace.Repository = (*WorkspaceRepository)(nil)

// Workspace returns the Workspace repository backed by the same connection.
func (s *Store) Workspace() *WorkspaceRepository { return &WorkspaceRepository{db: s.db} }

// Load returns the stored Workspace restore payload. A missing row is the
// empty Workspace. A row that cannot be decoded is also treated as empty:
// a corrupt layout must not prevent the app from starting.
func (s *WorkspaceRepository) Load(ctx context.Context) (domainworkspace.Payload, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM workspace_layout WHERE id = 1`)
	if err != nil {
		return domainworkspace.EmptyPayload(), fmt.Errorf("store: load Workspace: %w", err)
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return domainworkspace.EmptyPayload(), nil
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainworkspace.EmptyPayload(), nil
	}
	var payload domainworkspace.Payload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return domainworkspace.EmptyPayload(), nil
	}
	return domainworkspace.SanitizePayload(payload), nil
}

// Save replaces the single Workspace row.
func (s *WorkspaceRepository) Save(ctx context.Context, payload domainworkspace.Payload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("store: encode Workspace: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO workspace_layout (id, version, data, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			data = excluded.data,
			updated_at = excluded.updated_at`,
		payload.Version,
		string(data),
		time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("store: save Workspace: %w", err)
	}
	return nil
}

// Clear removes the stored Workspace.
func (s *WorkspaceRepository) Clear(ctx context.Context) error {
	if _, err := s.db.Execute(ctx, `DELETE FROM workspace_layout WHERE id = 1`); err != nil {
		return fmt.Errorf("store: clear Workspace: %w", err)
	}
	return nil
}
