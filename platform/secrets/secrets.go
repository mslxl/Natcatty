// Package secrets is the platform seam over MyGo's OS credential store.
// Adapters consume Store without importing MyGo; only platform may do that.
package secrets

import (
	"errors"

	"github.com/egoist/mygo"
)

// Store keeps small secrets in the operating system's credential store.
type Store interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// ErrNotFound reports that no secret is stored under the key.
var ErrNotFound = errors.New("secrets: not found")

// ErrUnsupported reports that the platform has no credential store.
var ErrUnsupported = errors.New("secrets: unsupported")

// System is the operating system's credential store.
type System struct{}

// Scoped is the OS credential store under another service name. It lets an
// isolated process keep its entries in the platform store without reading or
// writing the app's real entries. The packaged smoke uses a per-profile
// service so a temporary artifact never touches the user's saved keys.
type Scoped struct{ Service string }

var _ Store = System{}

// Get reads a secret.
func (System) Get(key string) ([]byte, error) {
	value, err := mygo.Secrets.Get(key)
	if err != nil {
		return nil, mapError(err)
	}
	return value, nil
}

// Set writes a secret, replacing an existing value.
func (System) Set(key string, value []byte) error {
	return mapError(mygo.Secrets.Set(key, value))
}

// Delete removes a secret. Deleting a missing key is not an error.
func (System) Delete(key string) error {
	return mapError(mygo.Secrets.Delete(key))
}

var _ Store = Scoped{}

// Get reads a secret from the scoped service.
func (s Scoped) Get(key string) ([]byte, error) {
	value, err := mygo.Secrets.Service(s.Service).Get(key)
	if err != nil {
		return nil, mapError(err)
	}
	return value, nil
}

// Set writes a secret to the scoped service.
func (s Scoped) Set(key string, value []byte) error {
	return mapError(mygo.Secrets.Service(s.Service).Set(key, value))
}

// Delete removes a secret from the scoped service.
func (s Scoped) Delete(key string) error {
	return mapError(mygo.Secrets.Service(s.Service).Delete(key))
}

// mapError names the two states the field-secret adapter must distinguish.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mygo.ErrSecretNotFound):
		return ErrNotFound
	case errors.Is(err, errors.ErrUnsupported):
		return ErrUnsupported
	default:
		return err
	}
}
