package store

import (
	"context"
	"fmt"
	"time"

	appworkspace "github.com/mslxl/natcatty/application/workspace"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"gorm.io/gorm"
)

// WorkspaceRepository is the Workspace persistence view of the Store.
type WorkspaceRepository struct{ db *Store }

var _ appworkspace.Repository = (*WorkspaceRepository)(nil)

// Workspace returns the Workspace repository backed by the same connection.
func (s *Store) Workspace() *WorkspaceRepository { return &WorkspaceRepository{db: s} }

// Load returns the stored Workspace restore payload. Corrupt data is empty.
func (s *WorkspaceRepository) Load(ctx context.Context) (domainworkspace.Payload, error) {
	var row workspaceLayoutSchema
	if err := s.db.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Where(&workspaceLayoutSchema{ID: 1}).First(&row).Error
	}); err != nil {
		if err == gorm.ErrRecordNotFound {
			return domainworkspace.EmptyPayload(), nil
		}
		return domainworkspace.EmptyPayload(), fmt.Errorf("store: load Workspace: %w", err)
	}
	var payload domainworkspace.Payload
	if err := decodeData(row.Data, "Workspace", &payload); err != nil {
		return domainworkspace.EmptyPayload(), nil
	}
	return domainworkspace.SanitizePayload(payload), nil
}

// Save replaces the single Workspace row.
func (s *WorkspaceRepository) Save(ctx context.Context, payload domainworkspace.Payload) error {
	data, err := marshalData(payload, "Workspace")
	if err != nil {
		return err
	}
	row := workspaceLayoutSchema{ID: 1, Version: int64(payload.Version), Data: data, UpdatedAt: time.Now().UnixMilli()}
	if err := s.db.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &row, "id", "version", "data", "updated_at")
	}); err != nil {
		return fmt.Errorf("store: save Workspace: %w", err)
	}
	return nil
}

// Clear removes the stored Workspace.
func (s *WorkspaceRepository) Clear(ctx context.Context) error {
	if err := s.db.db.ORM(ctx, func(tx *gorm.DB) error {
		_, err := deleteModel(tx, &workspaceLayoutSchema{}, &workspaceLayoutSchema{ID: 1})
		return err
	}); err != nil {
		return fmt.Errorf("store: clear Workspace: %w", err)
	}
	return nil
}
