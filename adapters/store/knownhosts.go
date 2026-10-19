package store

import (
	"context"
	"encoding/json"
	"fmt"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

var _ appconnection.KnownHostRepository = (*Store)(nil)

// ListKnownHosts returns every Known Host. A record whose JSON tail cannot be
// decoded is skipped rather than failing the whole trust store, so one
// corrupted row cannot make every connection prompt on a changed key.
func (s *Store) ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM known_hosts
		ORDER BY hostname COLLATE NOCASE ASC, port ASC, key_type ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Known Hosts: %w", err)
	}

	records := make([]domainconn.KnownHost, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Known Host data is %T", row[0])
		}
		var record domainconn.KnownHost
		if err := json.Unmarshal([]byte(data), &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

// UpsertKnownHost inserts a trust record or updates the record with the same
// id or endpoint selector. Identity and discovery time are preserved on a
// rotation, matching the original upsertKnownHost.
func (s *Store) UpsertKnownHost(ctx context.Context, record domainconn.KnownHost) error {
	if record.ID == "" {
		return fmt.Errorf("store: Known Host has no id")
	}
	if record.Port == 0 {
		record.Port = 22
	}
	if record.LastSeen.IsZero() {
		record.LastSeen = record.DiscoveredAt
	}
	if record.DiscoveredAt.IsZero() {
		record.DiscoveredAt = record.LastSeen
	}

	rows, err := s.db.Query(ctx, `SELECT data FROM known_hosts
		WHERE id = ? OR (hostname = ? COLLATE NOCASE AND port = ? AND key_type = ?)
		LIMIT 1`,
		record.ID, record.Hostname, record.Port, record.KeyType)
	if err != nil {
		return fmt.Errorf("store: find Known Host: %w", err)
	}
	if len(rows.Values) > 0 {
		if data, ok := rows.Values[0][0].(string); ok {
			var existing domainconn.KnownHost
			if err := json.Unmarshal([]byte(data), &existing); err == nil {
				record.ID = existing.ID
				record.DiscoveredAt = existing.DiscoveredAt
				if record.ConvertedToHostID == "" {
					record.ConvertedToHostID = existing.ConvertedToHostID
				}
			}
		}
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("store: encode Known Host: %w", err)
	}

	_, err = s.db.Execute(ctx, `INSERT INTO known_hosts (
			id, hostname, port, key_type, fingerprint, public_key,
			discovered_at, last_seen, converted_to_host_id, order_key, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname,
			port = excluded.port,
			key_type = excluded.key_type,
			fingerprint = excluded.fingerprint,
			public_key = excluded.public_key,
			last_seen = excluded.last_seen,
			converted_to_host_id = excluded.converted_to_host_id,
			order_key = excluded.order_key,
			data = excluded.data`,
		record.ID,
		record.Hostname,
		record.Port,
		record.KeyType,
		record.Fingerprint,
		record.PublicKey,
		record.DiscoveredAt.UnixMilli(),
		record.LastSeen.UnixMilli(),
		record.ConvertedToHostID,
		record.Order,
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Known Host: %w", err)
	}
	return nil
}
