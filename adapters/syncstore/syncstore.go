// Package syncstore persists the cloud-sync profile: a JSON file for
// non-secret settings beside the Vault and the provider token in the OS
// credential store. It never writes a token or the master password to the JSON
// file.
package syncstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

// tokenKey is the credential-store entry. The versioned name is part of the
// on-disk contract.
const tokenKey = "cloud-sync-github-token-v1"

// ConfigStore is the sync-settings JSON file.
type ConfigStore struct {
	path string
	mu   sync.Mutex
}

var _ appsync.ConfigStore = (*ConfigStore)(nil)

// OpenConfig returns a store for path. The file need not exist.
func OpenConfig(path string) *ConfigStore { return &ConfigStore{path: path} }

// Load reads the profile. A missing or malformed file yields the zero config,
// which is the unconfigured state; it must never stop the app from starting.
func (s *ConfigStore) Load(ctx context.Context) (appsync.Config, error) {
	if err := ctx.Err(); err != nil {
		return appsync.Config{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return appsync.Config{}, nil
	}
	if err != nil {
		return appsync.Config{}, fmt.Errorf("syncstore: read config: %w", err)
	}
	var config appsync.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return appsync.Config{}, nil
	}
	return config, nil
}

// Save atomically replaces the profile. The file is owner-only.
func (s *ConfigStore) Save(ctx context.Context, config appsync.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("syncstore: encode config: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("syncstore: create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".cloud-sync-*.tmp")
	if err != nil {
		return fmt.Errorf("syncstore: create temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncstore: set config mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncstore: write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncstore: sync config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("syncstore: close config: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("syncstore: replace config: %w", err)
	}
	tmpPath = ""
	return nil
}

// TokenStore keeps the provider token in the OS credential store.
type TokenStore struct {
	secrets platformsecrets.Store
}

var _ appsync.TokenStore = (*TokenStore)(nil)

// NewTokenStore wraps the platform credential store.
func NewTokenStore(secrets platformsecrets.Store) *TokenStore {
	return &TokenStore{secrets: secrets}
}

// Load reads the token. A missing entry is appsync.ErrNoToken.
func (s *TokenStore) Load(ctx context.Context) (domainsync.Token, error) {
	if err := ctx.Err(); err != nil {
		return domainsync.Token{}, err
	}
	data, err := s.secrets.Get(tokenKey)
	if errors.Is(err, platformsecrets.ErrNotFound) {
		return domainsync.Token{}, appsync.ErrNoToken
	}
	if err != nil {
		return domainsync.Token{}, fmt.Errorf("syncstore: read token: %w", err)
	}
	var token domainsync.Token
	if err := json.Unmarshal(data, &token); err != nil || !token.HasCredential() {
		// A WebDAV basic/digest profile has no access token but is a valid
		// credential; anything else with no credential is corrupt or empty and
		// keeps the historical "no stored token" meaning.
		return domainsync.Token{}, appsync.ErrNoToken
	}
	return token, nil
}

// Save writes the token, replacing an existing entry.
func (s *TokenStore) Save(ctx context.Context, token domainsync.Token) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("syncstore: encode token: %w", err)
	}
	if err := s.secrets.Set(tokenKey, data); err != nil {
		return fmt.Errorf("syncstore: save token: %w", err)
	}
	return nil
}

// Clear removes the token. A missing entry is not an error.
func (s *TokenStore) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.secrets.Delete(tokenKey)
	if err != nil && !errors.Is(err, platformsecrets.ErrNotFound) {
		return fmt.Errorf("syncstore: clear token: %w", err)
	}
	return nil
}
