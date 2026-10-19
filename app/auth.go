package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// authResolver is the slice of the Vault service the connection layer needs.
// The application Service implements it; tests provide a fake.
type authResolver interface {
	ResolveAuth(ctx context.Context, hostID model.HostID) (appvault.ResolvedAuth, error)
}

// vaultAuthSource resolves a Host's saved Identity, SSH Key, and password into
// the application's authentication plan. It is the bridge between the Vault
// and the connection layer; credential plaintext stays in memory.
type vaultAuthSource struct {
	vault authResolver
	// defaults supplies the unencrypted ~/.ssh identity files tried after the
	// selected Key in automatic mode. It is nil in tests that do not exercise
	// default-key discovery.
	defaults defaultKeySource
}

var _ appconnection.AuthSource = vaultAuthSource{}

func (a vaultAuthSource) AuthPlan(ctx context.Context, req appconnection.Request) (appconnection.AuthPlan, error) {
	resolved, err := a.vault.ResolveAuth(ctx, req.HostID)
	if err != nil {
		return appconnection.AuthPlan{}, authErrorFor(err)
	}

	keyMode := resolved.Selection.Method == domainvault.HostAuthKey ||
		resolved.Selection.Method == domainvault.HostAuthCertificate
	if keyMode && (resolved.MissingKey || resolved.Key == nil) {
		return appconnection.AuthPlan{}, appconnection.AuthFailure(fmt.Errorf(
			"%w: Host %s selects an SSH Key that is missing", appconnection.ErrKeyUnavailable, req.HostID))
	}
	// Certificate mode is explicit and fail-closed: without a signed
	// certificate there is nothing to offer, and falling back to the password
	// would silently change the Host's configured method.
	if resolved.Selection.Method == domainvault.HostAuthCertificate &&
		strings.TrimSpace(resolved.Key.Certificate) == "" {
		return appconnection.AuthPlan{}, appconnection.AuthFailure(fmt.Errorf(
			"%w: Host %s selects certificate authentication without a signed certificate",
			appconnection.ErrKeyUnavailable, req.HostID))
	}

	username := resolved.Selection.Username
	if username == "" {
		username = req.Username
	}
	spec := appconnection.AuthSpec{
		Username:    username,
		Mode:        authMode(resolved.Selection.Method),
		Password:    resolved.Password,
		UseAgent:    resolved.Selection.UseSSHAgent,
		RequiresMFA: resolved.Selection.RequiresMFA,
	}
	if spec.Mode == appconnection.AuthModeAuto && a.defaults != nil {
		spec.DefaultKeys = a.defaults.DefaultKeys()
	}
	if resolved.Key != nil {
		// A resolvable Key is an explicit selection in every mode; automatic
		// mode places it ahead of any default keys.
		spec.SelectedKey = &appconnection.KeyMaterial{
			ID:          resolved.Key.ID,
			Label:       resolved.Key.Label,
			PrivateKey:  resolved.Key.PrivateKey,
			Passphrase:  resolved.Key.Passphrase,
			Certificate: resolved.Key.Certificate,
		}
	}
	return appconnection.BuildAuthPlan(spec), nil
}

func authMode(method domainvault.HostAuthMethod) appconnection.AuthMode {
	switch method {
	case domainvault.HostAuthPassword:
		return appconnection.AuthModePassword
	case domainvault.HostAuthKey:
		return appconnection.AuthModeKey
	case domainvault.HostAuthCertificate:
		return appconnection.AuthModeCertificate
	default:
		return appconnection.AuthModeAuto
	}
}

// authErrorFor maps Vault failures onto connection failures. A missing Host or
// an absent credential is an auth failure the user can fix; a locked or broken
// secret store stays internal so the UI never claims the password is wrong.
func authErrorFor(err error) error {
	if errors.Is(err, appvault.ErrNotFound) {
		return appconnection.AuthFailure(appconnection.ErrNoCredentials)
	}
	var typed *appvault.Error
	if errors.As(err, &typed) {
		switch typed.Kind {
		case appvault.KindValidation, appvault.KindUnsupported:
			return appconnection.AuthFailure(err)
		case appvault.KindPermission:
			return &appconnection.Error{Kind: appconnection.KindInternal, Cause: err}
		default:
			return &appconnection.Error{Kind: appconnection.KindInternal, Cause: err}
		}
	}
	return &appconnection.Error{Kind: appconnection.KindInternal, Cause: err}
}
