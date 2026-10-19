// Package workspace owns Workspace persistence use cases: loading the
// validated restore payload at startup and saving it whenever the layout
// changes. The pure layout rules live in domain/workspace; this package only
// declares the storage port it consumes.
package workspace

import (
	"context"

	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// Repository is the persistence port the Workspace consumes.
type Repository interface {
	// Load returns the stored payload. A missing row is the empty payload.
	Load(ctx context.Context) (domainworkspace.Payload, error)
	// Save replaces the one stored payload.
	Save(ctx context.Context, payload domainworkspace.Payload) error
	// Clear removes the stored payload.
	Clear(ctx context.Context) error
}

// Service loads and saves the Workspace restore payload.
type Service struct {
	repo Repository
}

// NewService returns a Service backed by repo. A nil repository makes every
// operation a no-op, which is what tests and headless builds use.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Load returns a sanitized payload. A storage failure is treated like an
// absent layout: restoring an empty Workspace is always safe, and a broken
// database row must not stop the app from starting.
func (s *Service) Load(ctx context.Context) domainworkspace.Payload {
	if s == nil || s.repo == nil {
		return domainworkspace.EmptyPayload()
	}
	payload, err := s.repo.Load(ctx)
	if err != nil {
		return domainworkspace.EmptyPayload()
	}
	return domainworkspace.SanitizePayload(payload)
}

// Save validates and stores payload. An empty Workspace is stored as an empty
// payload so the next launch starts clean.
func (s *Service) Save(ctx context.Context, payload domainworkspace.Payload) error {
	if s == nil || s.repo == nil {
		return nil
	}
	return s.repo.Save(ctx, domainworkspace.SanitizePayload(payload))
}

// Clear removes the stored payload.
func (s *Service) Clear(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	return s.repo.Clear(ctx)
}
