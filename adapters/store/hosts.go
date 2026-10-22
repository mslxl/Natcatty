package store

import (
	"context"
	"encoding/json"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

// List returns every Host, pinned Hosts first, then each Host Group's order.
func (s *Store) List(ctx context.Context) ([]domainvault.Host, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM hosts
		ORDER BY pinned DESC, order_key ASC, label COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Hosts: %w", err)
	}

	hosts := make([]domainvault.Host, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Host data is %T", row[0])
		}
		var host domainvault.Host
		if err := json.Unmarshal([]byte(data), &host); err != nil {
			return nil, fmt.Errorf("store: decode Host: %w", err)
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// Get returns one Host.
func (s *Store) Get(ctx context.Context, id model.HostID) (domainvault.Host, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM hosts WHERE id = ?`, string(id))
	if err != nil {
		return domainvault.Host{}, fmt.Errorf("store: get Host: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.Host{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.Host{}, fmt.Errorf("store: Host data is %T", rows.Values[0][0])
	}
	var host domainvault.Host
	if err := json.Unmarshal([]byte(data), &host); err != nil {
		return domainvault.Host{}, fmt.Errorf("store: decode Host: %w", err)
	}
	return host, nil
}

// Save inserts or replaces one Host aggregate. The indexed columns are copies
// of the JSON fields and are written in the same statement.
func (s *Store) Save(ctx context.Context, host domainvault.Host) error {
	statement, err := hostSaveStatement(host)
	if err != nil {
		return err
	}
	_, err = s.db.Execute(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return fmt.Errorf("store: save Host: %w", err)
	}
	s.vaultChanged()
	return nil
}

// hostSaveStatement builds the insert-or-replace statement for one Host. The
// import batch uses it to put several Hosts in one transaction.
func hostSaveStatement(host domainvault.Host) (platformsqlite.Statement, error) {
	if err := host.Validate(); err != nil {
		return platformsqlite.Statement{}, fmt.Errorf("store: save Host: %w", err)
	}
	data, err := json.Marshal(host)
	if err != nil {
		return platformsqlite.Statement{}, fmt.Errorf("store: encode Host: %w", err)
	}

	return platformsqlite.Statement{
		SQL: `INSERT INTO hosts (
			id, label, hostname, port, username, protocol, group_path,
			pinned, order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label,
			hostname = excluded.hostname,
			port = excluded.port,
			username = excluded.username,
			protocol = excluded.protocol,
			group_path = excluded.group_path,
			pinned = excluded.pinned,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		Args: []any{
			string(host.ID),
			host.Label,
			host.Hostname,
			host.Port,
			host.Username,
			string(host.Protocol),
			host.Group,
			boolInt(host.Pinned),
			host.OrderKey,
			host.CreatedAt.UnixMilli(),
			host.UpdatedAt.UnixMilli(),
			string(data),
		},
	}, nil
}

// Delete removes one Host and nothing else. Vault references are soft, as in
// the original, so no related record is cascaded.
func (s *Store) Delete(ctx context.Context, id model.HostID) error {
	result, err := s.db.Execute(ctx, `DELETE FROM hosts WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("store: delete Host: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
