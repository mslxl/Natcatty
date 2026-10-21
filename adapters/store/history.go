package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apphistory "github.com/mslxl/natcatty/application/history"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	platformsqlite "github.com/mslxl/natcatty/platform/sqlite"
)

var _ apphistory.Store = (*Store)(nil)

// SaveConnectionLog inserts or replaces one connection log.
func (s *Store) SaveConnectionLog(ctx context.Context, log domainhistory.ConnectionLog) error {
	if log.ID == "" {
		return fmt.Errorf("store: connection log has no id")
	}
	data, err := json.Marshal(log)
	if err != nil {
		return fmt.Errorf("store: encode connection log: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO connection_logs (
			id, session_id, host_id, host_label, hostname, username, kind, status,
			error, started_at, ended_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			host_id = excluded.host_id,
			host_label = excluded.host_label,
			hostname = excluded.hostname,
			username = excluded.username,
			kind = excluded.kind,
			status = excluded.status,
			error = excluded.error,
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			data = excluded.data`,
		log.ID,
		string(log.SessionID),
		string(log.HostID),
		log.HostLabel,
		log.Hostname,
		log.Username,
		log.Kind,
		string(log.Status),
		log.Error,
		log.StartedAt.UnixMilli(),
		log.EndedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save connection log: %w", err)
	}
	return nil
}

// ListConnectionLogs returns every connection log newest first.
func (s *Store) ListConnectionLogs(ctx context.Context) ([]domainhistory.ConnectionLog, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM connection_logs
		ORDER BY started_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list connection logs: %w", err)
	}
	logs := make([]domainhistory.ConnectionLog, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: connection log data is %T", row[0])
		}
		var log domainhistory.ConnectionLog
		if err := json.Unmarshal([]byte(data), &log); err != nil {
			return nil, fmt.Errorf("store: decode connection log: %w", err)
		}
		logs = append(logs, log)
	}
	return logs, nil
}

// DeleteConnectionLog removes one log. A missing id is not an error, so a
// stale row from another view deletes idempotently.
func (s *Store) DeleteConnectionLog(ctx context.Context, id string) error {
	if _, err := s.db.Execute(ctx, `DELETE FROM connection_logs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete connection log: %w", err)
	}
	return nil
}

// PruneConnectionLogs deletes logs outside retention and reports how many it
// removed. Age is applied first, then the newest Count rows are kept.
func (s *Store) PruneConnectionLogs(ctx context.Context, retention domainhistory.Retention, now time.Time) (int, error) {
	statements := make([]platformsqlite.Statement, 0, 2)
	if retention.MaxAge > 0 {
		statements = append(statements, platformsqlite.Statement{
			SQL:  `DELETE FROM connection_logs WHERE started_at < ?`,
			Args: []any{now.Add(-retention.MaxAge).UnixMilli()},
		})
	}
	if retention.Count > 0 {
		statements = append(statements, platformsqlite.Statement{
			SQL: `DELETE FROM connection_logs WHERE id NOT IN (
				SELECT id FROM connection_logs ORDER BY started_at DESC, id DESC LIMIT ?
			)`,
			Args: []any{retention.Count},
		})
	}
	return s.runPrune(ctx, statements, "connection logs")
}

// ClearConnectionLogs removes every connection log and reports the count.
func (s *Store) ClearConnectionLogs(ctx context.Context) (int, error) {
	result, err := s.db.Execute(ctx, `DELETE FROM connection_logs`)
	if err != nil {
		return 0, fmt.Errorf("store: clear connection logs: %w", err)
	}
	return int(result.Changes), nil
}

// SaveHistoryEntry inserts or replaces one recorded command.
func (s *Store) SaveHistoryEntry(ctx context.Context, entry domainhistory.Entry) error {
	if entry.ID == "" {
		return fmt.Errorf("store: history entry has no id")
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: encode history entry: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO history_entries (
			id, session_id, host_id, host_label, scope, command, source,
			recorded_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			host_id = excluded.host_id,
			host_label = excluded.host_label,
			scope = excluded.scope,
			command = excluded.command,
			source = excluded.source,
			recorded_at = excluded.recorded_at,
			data = excluded.data`,
		entry.ID,
		string(entry.SessionID),
		string(entry.HostID),
		entry.HostLabel,
		entry.Scope,
		entry.Command,
		entry.Source,
		entry.RecordedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save history entry: %w", err)
	}
	return nil
}

// ListHistoryEntries returns every recorded command newest first.
func (s *Store) ListHistoryEntries(ctx context.Context) ([]domainhistory.Entry, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM history_entries
		ORDER BY recorded_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list history entries: %w", err)
	}
	entries := make([]domainhistory.Entry, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: history entry data is %T", row[0])
		}
		var entry domainhistory.Entry
		if err := json.Unmarshal([]byte(data), &entry); err != nil {
			return nil, fmt.Errorf("store: decode history entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// DeleteHistoryEntry removes one recorded command.
func (s *Store) DeleteHistoryEntry(ctx context.Context, id string) error {
	if _, err := s.db.Execute(ctx, `DELETE FROM history_entries WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete history entry: %w", err)
	}
	return nil
}

// ClearHistoryEntries removes every recorded command and reports the count.
func (s *Store) ClearHistoryEntries(ctx context.Context) (int, error) {
	result, err := s.db.Execute(ctx, `DELETE FROM history_entries`)
	if err != nil {
		return 0, fmt.Errorf("store: clear history entries: %w", err)
	}
	return int(result.Changes), nil
}

// PruneHistoryEntries deletes commands outside retention and reports how many
// it removed. Age is applied first, then the newest Count rows are kept.
func (s *Store) PruneHistoryEntries(ctx context.Context, retention domainhistory.Retention, now time.Time) (int, error) {
	statements := make([]platformsqlite.Statement, 0, 2)
	if retention.MaxAge > 0 {
		statements = append(statements, platformsqlite.Statement{
			SQL:  `DELETE FROM history_entries WHERE recorded_at < ?`,
			Args: []any{now.Add(-retention.MaxAge).UnixMilli()},
		})
	}
	if retention.Count > 0 {
		statements = append(statements, platformsqlite.Statement{
			SQL: `DELETE FROM history_entries WHERE id NOT IN (
				SELECT id FROM history_entries ORDER BY recorded_at DESC, id DESC LIMIT ?
			)`,
			Args: []any{retention.Count},
		})
	}
	return s.runPrune(ctx, statements, "history entries")
}

// runPrune executes the deletes in one transaction and sums their changes.
func (s *Store) runPrune(ctx context.Context, statements []platformsqlite.Statement, label string) (int, error) {
	if len(statements) == 0 {
		return 0, nil
	}
	results, err := s.db.Transaction(ctx, statements)
	if err != nil {
		return 0, fmt.Errorf("store: prune %s: %w", label, err)
	}
	removed := 0
	for _, result := range results {
		removed += int(result.Changes)
	}
	return removed, nil
}
