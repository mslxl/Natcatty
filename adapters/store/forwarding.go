package store

import (
	"context"
	"fmt"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	"gorm.io/gorm"
)

var _ appforwarding.RuleRepository = (*Store)(nil)

// ListForwardingRules returns every usable rule in display order. A row that
// cannot be decoded, or that normalizes to an invalid rule, is dropped.
func (s *Store) ListForwardingRules(ctx context.Context) ([]domainforward.Rule, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &forwardingRuleSchema{}, "order_key ASC", "label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Port Forwarding Rules: %w", err)
	}
	rules := make([]domainforward.Rule, 0, len(data))
	for _, item := range data {
		var candidate domainforward.Rule
		if decodeData(item, "Port Forwarding Rule", &candidate) != nil {
			continue
		}
		rule, ok := domainforward.SanitizeRule(candidate)
		if ok {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

// GetForwardingRule returns one rule. Malformed records are treated as missing.
func (s *Store) GetForwardingRule(ctx context.Context, id model.ForwardingRuleID) (domainforward.Rule, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &forwardingRuleSchema{}, &forwardingRuleSchema{ID: string(id)})
		return err
	}); err != nil {
		return domainforward.Rule{}, fmt.Errorf("store: get Port Forwarding Rule: %w", err)
	}
	if !found {
		return domainforward.Rule{}, appforwarding.ErrNotFound
	}
	var candidate domainforward.Rule
	if decodeData(data, "Port Forwarding Rule", &candidate) != nil {
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
	model, err := forwardingRuleModel(rule)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "label", "type", "host_id", "bind_address", "local_port", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Port Forwarding Rule: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteForwardingRule removes one rule and nothing else.
func (s *Store) DeleteForwardingRule(ctx context.Context, id model.ForwardingRuleID) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &forwardingRuleSchema{}, &forwardingRuleSchema{ID: string(id)})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Port Forwarding Rule: %w", err)
	}
	if changed == 0 {
		return appforwarding.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
