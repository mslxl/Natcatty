package app

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/adapters/store"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/application/session/autologin"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// serialSecrets adapts the app's encrypted field-secret store to the serial
// dialog and the Session opener. The port path is the profile key, so the same
// device keeps its password across dialogs and workspace restores. Only the
// ciphertext lives in the store; no workspace payload or log sees the value.
type serialSecrets struct {
	store *store.SecretStore
}

// serialSecretRef names one serial profile's password. The Domain owns the
// entity type and field constants because they are part of the persisted key
// and the encryption AAD.
func serialSecretRef(profile string) appvault.SecretRef {
	return appvault.SecretRef{
		EntityType: domainserial.SecretEntityType,
		EntityID:   profile,
		Field:      domainserial.SecretFieldPassword,
	}
}

// GetPassword returns the stored password and whether one exists. A missing
// value is not an error: a profile may have been saved before auto-login.
func (s serialSecrets) GetPassword(ctx context.Context, profile string) (string, bool, error) {
	if s.store == nil {
		return "", false, nil
	}
	value, err := s.store.Get(ctx, serialSecretRef(profile))
	if errors.Is(err, appvault.ErrSecretNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetPassword stores or replaces a profile's password.
func (s serialSecrets) SetPassword(ctx context.Context, profile, password string) error {
	if s.store == nil {
		return errors.New("app: no secret store")
	}
	return s.store.Set(ctx, serialSecretRef(profile), password)
}

// DeletePassword removes a profile's password. A profile without one is a
// no-op.
func (s serialSecrets) DeletePassword(ctx context.Context, profile string) error {
	if s.store == nil {
		return nil
	}
	return s.store.Delete(ctx, serialSecretRef(profile))
}

// serialSecretReader is the read half serialAutoLoginOptions needs, so tests
// can supply a fake without the encrypted store.
type serialSecretReader interface {
	GetPassword(ctx context.Context, profile string) (string, bool, error)
}

// serialAutoLoginOptions builds an expect engine from a serial profile and its
// stored password. It returns nil when auto-login is off or the profile has
// nothing to send. A failed secret read leaves the password unset but keeps
// the username and custom replies usable.
func serialAutoLoginOptions(ctx context.Context, secrets serialSecretReader, cfg domainserial.Config) *autologin.Options {
	profile := cfg.AutoLogin
	if profile == nil || !profile.Enabled {
		return nil
	}
	password, passwordSet := "", false
	if profile.PasswordSet && secrets != nil {
		stored, found, err := secrets.GetPassword(ctx, cfg.SecretProfileID())
		if err == nil {
			password, passwordSet = stored, found
		}
	}
	return appsession.SerialAutoLoginOptions(profile, password, passwordSet)
}

// telnetAutoLoginOptions builds an expect engine from a Telnet Host's Vault
// login. password is the one-shot credential of a Quick Connect; when the Host
// is saved, its Vault credential wins, matching what Copy Credentials shows.
// A Host with no username and no password has nothing to replay.
func telnetAutoLoginOptions(ctx context.Context, vault *appvault.Service, host domainvault.Host, password string) *autologin.Options {
	username := host.Username
	passwordSet := password != ""
	if host.ID != "" && vault != nil {
		if credentials, err := vault.HostCredentials(ctx, host.ID); err == nil {
			if credentials.Username != "" {
				username = credentials.Username
			}
			if credentials.Password != "" {
				password, passwordSet = credentials.Password, true
			}
		}
	}
	if username == "" && !passwordSet {
		return nil
	}
	return &autologin.Options{Username: username, Password: password, PasswordSet: passwordSet}
}
