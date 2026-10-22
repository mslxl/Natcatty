package store

import (
	"context"
	"encoding/json"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ListProxyProfiles returns every Proxy Profile in display order.
func (s *Store) ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM proxy_profiles
		ORDER BY order_key ASC, label COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Proxy Profiles: %w", err)
	}

	profiles := make([]domainvault.ProxyProfile, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Proxy Profile data is %T", row[0])
		}
		var profile domainvault.ProxyProfile
		if err := json.Unmarshal([]byte(data), &profile); err != nil {
			return nil, fmt.Errorf("store: decode Proxy Profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

// GetProxyProfile returns one Proxy Profile by id.
func (s *Store) GetProxyProfile(ctx context.Context, id string) (domainvault.ProxyProfile, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM proxy_profiles WHERE id = ?`, id)
	if err != nil {
		return domainvault.ProxyProfile{}, fmt.Errorf("store: get Proxy Profile: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.ProxyProfile{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.ProxyProfile{}, fmt.Errorf("store: Proxy Profile data is %T", rows.Values[0][0])
	}
	var profile domainvault.ProxyProfile
	if err := json.Unmarshal([]byte(data), &profile); err != nil {
		return domainvault.ProxyProfile{}, fmt.Errorf("store: decode Proxy Profile: %w", err)
	}
	return profile, nil
}

// SaveProxyProfile inserts or replaces one Proxy Profile.
func (s *Store) SaveProxyProfile(ctx context.Context, profile domainvault.ProxyProfile) error {
	if profile.ID == "" {
		return fmt.Errorf("store: Proxy Profile has no id")
	}
	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("store: encode Proxy Profile: %w", err)
	}

	_, err = s.db.Execute(ctx, `INSERT INTO proxy_profiles (
			id, label, type, host, port, order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label,
			type = excluded.type,
			host = excluded.host,
			port = excluded.port,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		profile.ID,
		profile.Label,
		string(profile.Config.Type),
		profile.Config.Host,
		profile.Config.Port,
		profile.OrderKey,
		profile.CreatedAt.UnixMilli(),
		profile.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Proxy Profile: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteProxyProfile removes one Proxy Profile. Host and Host Group references
// are cleared by the Vault service, which owns the non-cascading rule; the
// adapter never touches another table.
func (s *Store) DeleteProxyProfile(ctx context.Context, id string) error {
	result, err := s.db.Execute(ctx, `DELETE FROM proxy_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete Proxy Profile: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
