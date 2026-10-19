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
