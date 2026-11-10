package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"gorm.io/gorm"
)

// DataProfiles persists plugin data-profile records. It is a DataProfileStore
// port for the data-provider host; secret values live in the field-secret
// adapter and only their opaque references appear in the record.
type DataProfiles struct {
	store *Store
}

// NewDataProfiles wraps one store as a data-profile repository.
func NewDataProfiles(store *Store) *DataProfiles {
	return &DataProfiles{store: store}
}

var _ appplugin.DataProfileStore = (*DataProfiles)(nil)

// ListDataProfiles returns every profile record. It lets the sync adapter use
// the Store directly along with the other Vault families.
func (s *Store) ListDataProfiles(ctx context.Context) ([]domainplugin.DataProfile, error) {
	return NewDataProfiles(s).List(ctx)
}

// ListProfiles implements the Vault export's read-only profile source.
func (p *DataProfiles) ListProfiles(ctx context.Context) ([]domainplugin.DataProfile, error) {
	return p.List(ctx)
}

// List returns every profile in display order.
func (p *DataProfiles) List(ctx context.Context) ([]domainplugin.DataProfile, error) {
	if p == nil || p.store == nil {
		return nil, appplugin.ErrDataUnavailable
	}
	var data []string
	if err := p.store.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &dataProfileSchema{},
			"label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list data profiles: %w", err)
	}
	profiles, err := decodeDataList[domainplugin.DataProfile](data, "data profile")
	if err != nil {
		return nil, err
	}
	return profiles, nil
}

// Get returns one profile by id.
func (p *DataProfiles) Get(ctx context.Context, id string) (domainplugin.DataProfile, error) {
	if p == nil || p.store == nil {
		return domainplugin.DataProfile{}, appplugin.ErrDataUnavailable
	}
	var data string
	var found bool
	if err := p.store.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &dataProfileSchema{},
			&dataProfileSchema{ID: id})
		return err
	}); err != nil {
		return domainplugin.DataProfile{}, fmt.Errorf("store: get data profile: %w", err)
	}
	if !found {
		return domainplugin.DataProfile{}, appplugin.ErrDataProfileNotFound
	}
	var profile domainplugin.DataProfile
	if err := decodeData(data, "data profile", &profile); err != nil {
		return domainplugin.DataProfile{}, err
	}
	return profile, nil
}

// Save inserts or replaces one profile record.
func (p *DataProfiles) Save(ctx context.Context,
	profile domainplugin.DataProfile) error {

	if p == nil || p.store == nil {
		return appplugin.ErrDataUnavailable
	}
	profile, quarantined, err := sanitizeDataProfilePluginData(profile)
	if err != nil {
		return err
	}
	model, err := dataProfileModel(profile)
	if err != nil {
		return err
	}
	if err := p.store.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if err := upsertModel(tx, &model, "id", "provider", "label", "mode",
			"updated_at", "data"); err != nil {
			return err
		}
		return saveQuarantines(tx, plugindata.RecordDataProfile, profile.ID, profile.PluginData, quarantined)
	}); err != nil {
		return fmt.Errorf("store: save data profile: %w", err)
	}
	return nil
}

// Delete removes one profile record. Secret references are removed by the
// host before this call.
func (p *DataProfiles) Delete(ctx context.Context, id string) error {
	if p == nil || p.store == nil {
		return appplugin.ErrDataUnavailable
	}
	var changed int64
	if err := p.store.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &dataProfileSchema{},
			&dataProfileSchema{ID: id})
		if err != nil {
			return err
		}
		return tx.Where("record_kind = ? AND record_id = ?",
			string(plugindata.RecordDataProfile), id).
			Delete(&pluginDataQuarantineSchema{}).Error
	}); err != nil {
		return fmt.Errorf("store: delete data profile: %w", err)
	}
	if changed == 0 {
		return appplugin.ErrDataProfileNotFound
	}
	return nil
}

// DataProfileSecrets stores provider-defined secret values in the Vault field
// secret store. The reference is opaque to the host and the provider; it
// names only the profile, the field, and no value.
type DataProfileSecrets struct {
	secrets appvault.FieldSecrets
}

// NewDataProfileSecrets wraps the Vault field-secret port.
func NewDataProfileSecrets(secrets appvault.FieldSecrets) *DataProfileSecrets {
	return &DataProfileSecrets{secrets: secrets}
}

var _ appplugin.DataProfileSecrets = (*DataProfileSecrets)(nil)

// Put writes one secret field and returns its opaque reference. Rewriting a
// field replaces the ciphertext under the same reference.
func (s *DataProfileSecrets) Put(ctx context.Context, profileID, field,
	value string) (string, error) {

	if s == nil || s.secrets == nil {
		return "", appplugin.ErrDataUnavailable
	}
	if strings.ContainsAny(profileID+field, ":") {
		return "", errors.New("store: data profile secret field is invalid")
	}
	ref := appvault.SecretRef{
		EntityType: appvault.EntityDataProfile,
		EntityID:   profileID,
		Field:      field,
	}
	if err := s.secrets.Set(ctx, ref, value); err != nil {
		return "", err
	}
	return appvault.DataProfileSecretRef(profileID, field), nil
}

// Get resolves one opaque reference to its plaintext. The caller must not
// persist, log, or return the result.
func (s *DataProfileSecrets) Get(ctx context.Context, raw string) (string, error) {
	if s == nil || s.secrets == nil {
		return "", appplugin.ErrDataUnavailable
	}
	ref, err := appvault.ParseDataProfileSecretRef(raw)
	if err != nil {
		return "", err
	}
	value, err := s.secrets.Get(ctx, ref)
	if err != nil {
		if errors.Is(err, appvault.ErrSecretNotFound) {
			return "", appplugin.ErrDataSecretNotFound
		}
		return "", err
	}
	return value, nil
}

// Delete removes one secret field. Deleting a missing value is not an error.
func (s *DataProfileSecrets) Delete(ctx context.Context, raw string) error {
	if s == nil || s.secrets == nil {
		return appplugin.ErrDataUnavailable
	}
	ref, err := appvault.ParseDataProfileSecretRef(raw)
	if err != nil {
		return err
	}
	err = s.secrets.Delete(ctx, ref)
	if err != nil && errors.Is(err, appvault.ErrSecretNotFound) {
		return nil
	}
	return err
}
