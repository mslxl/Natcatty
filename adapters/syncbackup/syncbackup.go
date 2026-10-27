// Package syncbackup adapts the local Vault backup service to the sync
// application's backup port. Sync needs protective restore points before it
// overwrites the local Vault and a list the conflict/guard UI can show; it
// must not import the sibling application/vaultbackup package itself.
package syncbackup

import (
	"context"

	appsync "github.com/mslxl/natcatty/application/sync"
	appvaultbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

// Service is the backup use case surface this adapter consumes.
type Service interface {
	CreateProtective(ctx context.Context) (appvaultbackup.CreateResult, error)
	List(ctx context.Context) ([]domainbackup.Summary, error)
	Restore(ctx context.Context, id string) error
}

// Adapter implements appsync.Backups.
type Adapter struct {
	service Service
}

var _ appsync.Backups = (*Adapter)(nil)

// New returns an adapter over the local backup service.
func New(service Service) *Adapter {
	return &Adapter{service: service}
}

// CreateProtective writes the mandatory restore point. The sync service fails
// closed when this returns an error.
func (a *Adapter) CreateProtective(ctx context.Context) error {
	_, err := a.service.CreateProtective(ctx)
	return err
}

// List maps the local summaries onto the sync surface, newest first. The sync
// service sets Source; the adapter never exposes a backup payload or key.
func (a *Adapter) List(ctx context.Context) ([]appsync.BackupInfo, error) {
	summaries, err := a.service.List(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]appsync.BackupInfo, 0, len(summaries))
	for _, summary := range summaries {
		infos = append(infos, appsync.BackupInfo{
			ID:        summary.ID,
			Source:    appsync.BackupSourceLocal,
			CreatedAt: summary.CreatedAt,
			Reason:    string(summary.Reason),
			SizeBytes: summary.SizeBytes,
		})
	}
	return infos, nil
}

// Restore applies one local restore point. The backup service writes its own
// before-restore protective backup first.
func (a *Adapter) Restore(ctx context.Context, id string) error {
	return a.service.Restore(ctx, id)
}
