package store

import (
	"context"
	"encoding/json"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

// ListGroups returns every Host Group in display order.
func (s *Store) ListGroups(ctx context.Context) ([]domainvault.Group, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM host_groups
		ORDER BY order_key ASC, path COLLATE NOCASE ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Host Groups: %w", err)
	}

	groups := make([]domainvault.Group, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Host Group data is %T", row[0])
		}
		var group domainvault.Group
		if err := json.Unmarshal([]byte(data), &group); err != nil {
			return nil, fmt.Errorf("store: decode Host Group: %w", err)
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// GetGroup returns one Host Group by path.
func (s *Store) GetGroup(ctx context.Context, path string) (domainvault.Group, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM host_groups WHERE path = ?`, path)
	if err != nil {
		return domainvault.Group{}, fmt.Errorf("store: get Host Group: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.Group{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.Group{}, fmt.Errorf("store: Host Group data is %T", rows.Values[0][0])
	}
	var group domainvault.Group
	if err := json.Unmarshal([]byte(data), &group); err != nil {
		return domainvault.Group{}, fmt.Errorf("store: decode Host Group: %w", err)
	}
	return group, nil
}

// SaveGroup inserts or replaces one Host Group. The indexed columns are copies
// of the JSON fields and are written in the same statement.
func (s *Store) SaveGroup(ctx context.Context, group domainvault.Group) error {
	statement, err := groupSaveStatement(group)
	if err != nil {
		return err
	}
	_, err = s.db.Execute(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return fmt.Errorf("store: save Host Group: %w", err)
	}
	return nil
}

// groupSaveStatement builds the insert-or-replace statement for one Host
// Group. The import batch uses it to put groups and Hosts in one transaction.
func groupSaveStatement(group domainvault.Group) (platformsqlite.Statement, error) {
	if group.Path == "" {
		return platformsqlite.Statement{}, fmt.Errorf("store: Host Group has no path")
	}
	data, err := json.Marshal(group)
	if err != nil {
		return platformsqlite.Statement{}, fmt.Errorf("store: encode Host Group: %w", err)
	}

	return platformsqlite.Statement{
		SQL: `INSERT INTO host_groups (
			path, notes, username, port, protocol, auth_method,
			identity_id, key_id, use_ssh_agent, requires_mfa,
			legacy_algorithms, skip_ecdsa_host_key, proxy_profile_id,
			order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			notes = excluded.notes,
			username = excluded.username,
			port = excluded.port,
			protocol = excluded.protocol,
			auth_method = excluded.auth_method,
			identity_id = excluded.identity_id,
			key_id = excluded.key_id,
			use_ssh_agent = excluded.use_ssh_agent,
			requires_mfa = excluded.requires_mfa,
			legacy_algorithms = excluded.legacy_algorithms,
			skip_ecdsa_host_key = excluded.skip_ecdsa_host_key,
			proxy_profile_id = excluded.proxy_profile_id,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		Args: []any{
			group.Path,
			group.Notes,
			group.Username,
			group.Port,
			string(group.Protocol),
			string(group.AuthMethod),
			string(group.IdentityID),
			string(group.KeyID),
			boolInt(group.UseSSHAgent),
			boolInt(group.RequiresMFA),
			boolInt(group.LegacyAlgorithms),
			boolInt(group.SkipEcdsaHostKey),
			group.ProxyProfileID,
			group.OrderKey,
			group.CreatedAt.UnixMilli(),
			group.UpdatedAt.UnixMilli(),
			string(data),
		},
	}, nil
}

// DeleteGroup removes one Host Group. Hosts and descendant groups are not
// cascaded: the Vault service decides what happens to them.
func (s *Store) DeleteGroup(ctx context.Context, path string) error {
	result, err := s.db.Execute(ctx, `DELETE FROM host_groups WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("store: delete Host Group: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	return nil
}
