package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

var _ appvault.ImportStore = (*Store)(nil)

// SaveImportBatch writes Host Groups and Hosts in one transaction. Building
// every statement before opening it means an invalid record fails the chunk
// without touching the database; any SQL error rolls the whole chunk back.
func (s *Store) SaveImportBatch(ctx context.Context, batch appvault.ImportBatch) error {
	if len(batch.Groups) == 0 && len(batch.Hosts) == 0 {
		return nil
	}
	statements := make([]platformsqlite.Statement, 0, len(batch.Groups)+len(batch.Hosts))
	for _, group := range batch.Groups {
		statement, err := groupSaveStatement(group)
		if err != nil {
			return err
		}
		statements = append(statements, statement)
	}
	for _, host := range batch.Hosts {
		statement, err := hostSaveStatement(host)
		if err != nil {
			return err
		}
		statements = append(statements, statement)
	}
	if _, err := s.db.Transaction(ctx, statements); err != nil {
		return fmt.Errorf("store: save import batch: %w", err)
	}
	s.vaultChanged()
	return nil
}
