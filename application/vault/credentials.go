package vault

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/domain/model"
)

// HostCredentials is the login a Host would authenticate with, for the Copy
// Credentials action. An Identity supplies its own username and password, so
// the copied value matches what a connection would actually send.
type HostCredentials struct {
	Username string
	Password string
}

// HostCredentials reads the effective login of a Host: group defaults fill an
// empty username, and a selected Identity supplies both its username and its
// stored password instead of the Host's own.
func (s *Service) HostCredentials(ctx context.Context, id model.HostID) (HostCredentials, error) {
	host, err := s.EffectiveHost(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return HostCredentials{}, ErrNotFound
	}
	if err != nil {
		return HostCredentials{}, err
	}

	credentials := HostCredentials{Username: host.Username}
	if host.IdentityID != "" && s.identities != nil {
		identity, identityErr := s.identities.GetIdentity(ctx, host.IdentityID)
		switch {
		case identityErr == nil:
			if identity.Username != "" {
				credentials.Username = identity.Username
			}
			if s.secrets != nil {
				password, secretErr := s.secretOrEmpty(ctx, IdentitySecretRef(identity.ID, FieldPassword))
				if secretErr != nil {
					return HostCredentials{}, secretErr
				}
				credentials.Password = password
			}
			return credentials, nil
		case errors.Is(identityErr, ErrNotFound):
			// A dangling Identity reference keeps the Host's own fields.
		default:
			return HostCredentials{}, internal(identityErr)
		}
	}
	if s.secrets != nil {
		password, secretErr := s.secretOrEmpty(ctx, HostSecretRef(host.ID, FieldPassword))
		if secretErr != nil {
			return HostCredentials{}, secretErr
		}
		credentials.Password = password
	}
	return credentials, nil
}
