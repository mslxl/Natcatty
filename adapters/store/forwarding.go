package store

import (
	"context"
	"encoding/json"
	"fmt"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

var _ appforwarding.RuleRepository = (*Store)(nil)

// ListForwardingRules returns every usable rule in display order. A row that
// cannot be decoded, or that normalizes to an invalid rule, is dropped rather
// than failing the whole list: a single malformed record must not hide the
// rest of the Vault.
func (s *Store) ListForwardingRules(ctx context.Context) ([]domainforward.Rule, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM forwarding_rules
		ORDER BY order_key ASC, label COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Port Forwarding Rules: %w", err)
	}

	rules := make([]domainforward.Rule, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			continue
		}
		var candidate domainforward.Rule
		if err := json.Unmarshal([]byte(data), &candidate); err != nil {
			continue
		}
		rule, ok := domainforward.SanitizeRule(candidate)
		if !ok {
			continue
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// GetForwardingRule returns one rule. A malformed stored record is reported
// as not found, matching the list's decision to drop unusable records.
func (s *Store) GetForwardingRule(ctx context.Context, id model.ForwardingRuleID) (domainforward.Rule, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM forwarding_rules WHERE id = ?`, string(id))
	if err != nil {
		return domainforward.Rule{}, fmt.Errorf("store: get Port Forwarding Rule: %w", err)
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return domainforward.Rule{}, appforwarding.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainforward.Rule{}, appforwarding.ErrNotFound
	}
	var candidate domainforward.Rule
	if err := json.Unmarshal([]byte(data), &candidate); err != nil {
		return domainforward.Rule{}, appforwarding.ErrNotFound
	}
	rule, ok := domainforward.SanitizeRule(candidate)
	if !ok {
		return domainforward.Rule{}, appforwarding.ErrNotFound
	}
	return rule, nil
}

// SaveForwardingRule inserts or replaces one rule aggregate.
func (s *Store) SaveForwardingRule(ctx context.Context, rule domainforward.Rule) error {
	if rule.ID == "" {
		return fmt.Errorf("store: save Port Forwarding Rule: empty id")
	}
	if err := rule.Validate(); err != nil {
		return fmt.Errorf("store: save Port Forwarding Rule: %w", err)
	}
	data, err := json.Marshal(rule)
	if err != nil {
		return fmt.Errorf("store: encode Port Forwarding Rule: %w", err)
	}

	_, err = s.db.Execute(ctx, `INSERT INTO forwarding_rules (
			id, label, type, host_id, bind_address, local_port,
			order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label,
			type = excluded.type,
			host_id = excluded.host_id,
			bind_address = excluded.bind_address,
			local_port = excluded.local_port,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		string(rule.ID),
		rule.Label,
		string(rule.Type),
		string(rule.HostID),
		rule.BindAddress,
		rule.LocalPort,
		rule.OrderKey,
		rule.CreatedAt.UnixMilli(),
		rule.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Port Forwarding Rule: %w", err)
	}
	return nil
}

// DeleteForwardingRule removes one rule and nothing else.
func (s *Store) DeleteForwardingRule(ctx context.Context, id model.ForwardingRuleID) error {
	result, err := s.db.Execute(ctx, `DELETE FROM forwarding_rules WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("store: delete Port Forwarding Rule: %w", err)
	}
	if result.Changes == 0 {
		return appforwarding.ErrNotFound
	}
	return nil
}
