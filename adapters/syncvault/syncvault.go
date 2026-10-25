// Package syncvault adapts the SQLite Vault and the field-secret store to the
// sync application's Vault port. It is the only place that knows which local
// tables carry synced data and how credentials are decrypted for the payload.
package syncvault

import (
	"context"
	"errors"
	"fmt"

	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Records is the read/write surface of *store.Store that the adapter uses.
type Records interface {
	List(ctx context.Context) ([]domainvault.Host, error)
	ListGroups(ctx context.Context) ([]domainvault.Group, error)
	ListKeys(ctx context.Context) ([]domainvault.Key, error)
	ListIdentities(ctx context.Context) ([]domainvault.Identity, error)
	ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error)
	ListForwardingRules(ctx context.Context) ([]domainforward.Rule, error)
	ListSnippets(ctx context.Context) ([]domainvault.Snippet, error)
	ListSnippetPackages(ctx context.Context) ([]domainvault.SnippetPackage, error)
	ListNotes(ctx context.Context) ([]domainvault.Note, error)
	ListNoteGroups(ctx context.Context) ([]domainvault.NoteGroup, error)
	ApplySync(ctx context.Context, payload appsync.Payload) error
}

// Secrets is the field-secret surface of *store.SecretStore.
type Secrets interface {
	Mode() appvault.SecretMode
	Get(ctx context.Context, ref appvault.SecretRef) (string, error)
	Set(ctx context.Context, ref appvault.SecretRef, value string) error
	Delete(ctx context.Context, ref appvault.SecretRef) error
}

// Vault implements appsync.Vault over the local records and credentials.
type Vault struct {
	records Records
	secrets Secrets
}

var _ appsync.Vault = (*Vault)(nil)

// New returns a Vault adapter.
func New(records Records, secrets Secrets) *Vault {
	return &Vault{records: records, secrets: secrets}
}

// Snapshot reads every synced family plus the plaintext credentials the
// envelope must carry. A credential that cannot be decrypted fails the
// snapshot: uploading a silently incomplete Vault would let another device
// wipe the missing fields on its next download.
func (v *Vault) Snapshot(ctx context.Context) (appsync.Payload, error) {
	var payload appsync.Payload
	var newest int64
	add := func(when int64) {
		if when > newest {
			newest = when
		}
	}

	var err error
	if payload.Hosts, err = v.records.List(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, host := range payload.Hosts {
		add(host.UpdatedAt.UnixMilli())
		if host.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.HostSecretRef(host.ID, appvault.FieldPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
		if host.Proxy != nil && host.Proxy.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.HostSecretRef(host.ID, appvault.FieldProxyPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.Groups, err = v.records.ListGroups(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, group := range payload.Groups {
		add(group.UpdatedAt.UnixMilli())
	}
	if payload.Keys, err = v.records.ListKeys(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, key := range payload.Keys {
		add(key.UpdatedAt.UnixMilli())
		if key.HasPrivateKey {
			if err := v.addSecret(ctx, &payload, appvault.KeySecretRef(key.ID, appvault.FieldPrivateKey)); err != nil {
				return appsync.Payload{}, err
			}
		}
		if key.HasPassphrase {
			if err := v.addSecret(ctx, &payload, appvault.KeySecretRef(key.ID, appvault.FieldPassphrase)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.Identities, err = v.records.ListIdentities(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, identity := range payload.Identities {
		add(identity.UpdatedAt.UnixMilli())
		if identity.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.IdentitySecretRef(identity.ID, appvault.FieldPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.ProxyProfiles, err = v.records.ListProxyProfiles(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, profile := range payload.ProxyProfiles {
		add(profile.UpdatedAt.UnixMilli())
		if profile.Config.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.ProxyProfileSecretRef(profile.ID, appvault.FieldProxyPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.PortForwardingRules, err = v.records.ListForwardingRules(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, rule := range payload.PortForwardingRules {
		add(rule.UpdatedAt.UnixMilli())
	}
	if payload.Snippets, err = v.records.ListSnippets(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, snippet := range payload.Snippets {
		add(snippet.UpdatedAt.UnixMilli())
	}
	if payload.SnippetPackages, err = v.records.ListSnippetPackages(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, pkg := range payload.SnippetPackages {
		add(pkg.UpdatedAt.UnixMilli())
	}
	if payload.Notes, err = v.records.ListNotes(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, note := range payload.Notes {
		add(note.UpdatedAt.UnixMilli())
	}
	if payload.NoteGroups, err = v.records.ListNoteGroups(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, group := range payload.NoteGroups {
		add(group.UpdatedAt.UnixMilli())
	}
	payload.SyncedAt = newest
	return payload, nil
}

func (v *Vault) addSecret(ctx context.Context, payload *appsync.Payload, ref appvault.SecretRef) error {
	value, err := v.secrets.Get(ctx, ref)
	if errors.Is(err, appvault.ErrSecretNotFound) {
		// The entity advertises a credential that is no longer stored. Omit it
		// rather than failing; the local record's flag is stale.
		return nil
	}
	if err != nil {
		return fmt.Errorf("syncvault: read %s %s: %w", ref.EntityType, ref.Field, err)
	}
	payload.Secrets = append(payload.Secrets, appsync.SecretValue{
		EntityType: ref.EntityType,
		EntityID:   ref.EntityID,
		Field:      ref.Field,
		Value:      value,
	})
	return nil
}

// Apply replaces the synced records, then writes the payload's credentials
// under this device's field key and removes stale credentials for entities the
// payload still contains. Local-only tables and the local key material are
// untouched.
func (v *Vault) Apply(ctx context.Context, payload appsync.Payload) error {
	mode := v.secrets.Mode()
	if mode == appvault.SecretModeUnavailable || mode == appvault.SecretModePassphraseLocked {
		return fmt.Errorf("syncvault: cannot apply credentials in %s mode", mode)
	}
	if err := v.records.ApplySync(ctx, payload); err != nil {
		return err
	}
	byRef := make(map[appvault.SecretRef]bool, len(payload.Secrets))
	for _, secret := range payload.Secrets {
		ref := appvault.SecretRef{EntityType: secret.EntityType, EntityID: secret.EntityID, Field: secret.Field}
		if err := v.secrets.Set(ctx, ref, secret.Value); err != nil {
			return fmt.Errorf("syncvault: store %s %s: %w", ref.EntityType, ref.Field, err)
		}
		byRef[ref] = true
	}
	return v.clearStale(ctx, payload, byRef)
}

// clearStale deletes credential fields that an incoming record no longer
// advertises. Orphan rows for entities absent from the payload are left for a
// later cleanup: they are ciphertext and cannot be addressed without listing
// the secret store.
func (v *Vault) clearStale(ctx context.Context, payload appsync.Payload, byRef map[appvault.SecretRef]bool) error {
	clear := func(ref appvault.SecretRef) error {
		if byRef[ref] {
			return nil
		}
		err := v.secrets.Delete(ctx, ref)
		if errors.Is(err, appvault.ErrSecretNotFound) {
			return nil
		}
		return err
	}
	for _, host := range payload.Hosts {
		if err := clear(appvault.HostSecretRef(host.ID, appvault.FieldPassword)); err != nil {
			return err
		}
		if err := clear(appvault.HostSecretRef(host.ID, appvault.FieldProxyPassword)); err != nil {
			return err
		}
	}
	for _, key := range payload.Keys {
		if err := clear(appvault.KeySecretRef(key.ID, appvault.FieldPrivateKey)); err != nil {
			return err
		}
		if err := clear(appvault.KeySecretRef(key.ID, appvault.FieldPassphrase)); err != nil {
			return err
		}
	}
	for _, identity := range payload.Identities {
		if err := clear(appvault.IdentitySecretRef(identity.ID, appvault.FieldPassword)); err != nil {
			return err
		}
	}
	for _, profile := range payload.ProxyProfiles {
		if err := clear(appvault.ProxyProfileSecretRef(profile.ID, appvault.FieldProxyPassword)); err != nil {
			return err
		}
	}
	return nil
}
