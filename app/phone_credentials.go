// Phone credential-store lifecycle. The field key lives in the platform
// credential store; on a phone that store can be temporarily unavailable (for
// example before the first unlock). The Vault must still open and serve its
// non-secret families, and the UI must say why credentials and Vault Sync are
// paused instead of silently substituting an insecure store. Returning to the
// foreground retries the open, and a success swaps the usable field-secret
// store in behind the same Vault service.

package app

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

// phoneCredentialUnavailable is the sentence the Vault home shows while the
// field key cannot be read from the system credential store. It names the
// consequence (credentials locked, Vault Sync paused) and the recovery action
// instead of leaving a silent downgrade.
const phoneCredentialUnavailable = "The system credential store is unavailable, so saved credentials are locked and Vault Sync is paused. Unlock the device, then return to the app to retry."

// phoneCredentials owns the phone's field-secret store across a credential
// outage. It is the appvault.FieldSecrets the Vault and Session runtime hold,
// so a store opened by a later retry is used by those already-constructed
// services without rebuilding them.
//
// A store returned in SecretModeUnavailable is kept: reading and writing
// credentials still fails closed, but deleting one needs no key and must keep
// acting on the same Vault rows. The store is never replaced by a session-only
// or plaintext store.
type phoneCredentials struct {
	db   *store.Store
	keys platformsecrets.Store

	// retryMu serializes OpenSecretStore attempts so a foreground retry cannot
	// race a startup attempt.
	retryMu sync.Mutex
	mu      sync.Mutex
	store   *store.SecretStore
	// available is true only when the field key was read (or created) in
	// os/passphrase mode. A locked passphrase store is available, an
	// unavailable store is not.
	available bool
	reason    string
	// notified records that onAvailable already ran, so concurrent retries
	// cannot build Vault Sync twice.
	notified    bool
	onAvailable func(*store.SecretStore)
}

var _ appvault.FieldSecrets = (*phoneCredentials)(nil)

// newPhoneCredentials returns a credential owner in the unavailable state.
// Call Retry to attempt the first open; the phone root does this before the
// shell is built, and a failure never blocks startup.
func newPhoneCredentials(db *store.Store, keys platformsecrets.Store) *phoneCredentials {
	return &phoneCredentials{
		db:     db,
		keys:   keys,
		reason: phoneCredentialUnavailable,
	}
}

// SetOnAvailable registers the hook that wires Vault Sync after a retry
// succeeds. It runs on the goroutine that called Retry, which is the UI thread
// for a foreground transition.
func (c *phoneCredentials) SetOnAvailable(fn func(*store.SecretStore)) {
	c.mu.Lock()
	c.onAvailable = fn
	c.notified = false
	c.mu.Unlock()
}

// Status reports whether the credential service is usable and, when it is
// not, the sentence the Vault home shows.
func (c *phoneCredentials) Status() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.available, c.reason
}

// Store returns the open field-secret store, which may carry ciphertext
// deletion while its mode is unavailable. It is nil only when the platform
// store could not be opened at all (a transient read failure).
func (c *phoneCredentials) Store() *store.SecretStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store
}

// Retry opens the credential service again and reports whether it is usable.
// It is safe to call on every foreground transition: once available, later
// calls are a no-op.
func (c *phoneCredentials) Retry(ctx context.Context) bool {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()

	c.mu.Lock()
	if c.available {
		c.mu.Unlock()
		return true
	}
	keys := c.keys
	c.mu.Unlock()

	opened, err := store.OpenSecretStore(ctx, c.db, keys)

	c.mu.Lock()
	if err != nil {
		c.available = false
		c.reason = phoneCredentialUnavailable
		c.mu.Unlock()
		return false
	}
	c.store = opened
	if opened.Mode() == appvault.SecretModeUnavailable {
		c.available = false
		c.reason = phoneCredentialUnavailable
		c.mu.Unlock()
		return false
	}

	c.available = true
	c.reason = ""
	fire := c.onAvailable != nil && !c.notified
	if fire {
		c.notified = true
	}
	hook := c.onAvailable
	c.mu.Unlock()

	if fire {
		hook(opened)
	}
	return true
}

// Mode delegates to the open store. Without one the credential service is
// unavailable; this is the value the Vault and editors read.
func (c *phoneCredentials) Mode() appvault.SecretMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return appvault.SecretModeUnavailable
	}
	return c.store.Mode()
}

func (c *phoneCredentials) Set(ctx context.Context, ref appvault.SecretRef, value string) error {
	store, err := c.current()
	if err != nil {
		return err
	}
	return store.Set(ctx, ref, value)
}

func (c *phoneCredentials) Get(ctx context.Context, ref appvault.SecretRef) (string, error) {
	store, err := c.current()
	if err != nil {
		return "", err
	}
	return store.Get(ctx, ref)
}

// Delete removes one field's ciphertext. Unlike Set and Get it does not need
// the field key, so it also works when Retry could not open the credential
// service at all: iOS and Android surface a locked keychain/keystore as an
// arbitrary platform error, not ErrUnsupported, and OpenSecretStore returns
// that as an error, leaving no store to carry the keyless delete. Deleting the
// exact row through the adapter keeps an explicit Key/secret deletion acting on
// the same Vault instead of orphaning ciphertext that would decrypt again after
// the store recovers.
func (c *phoneCredentials) Delete(ctx context.Context, ref appvault.SecretRef) error {
	c.mu.Lock()
	opened, db := c.store, c.db
	c.mu.Unlock()
	if opened != nil {
		return opened.Delete(ctx, ref)
	}
	if db == nil {
		return appvault.ErrSecretsUnavailable
	}
	return store.DeleteSecretValue(ctx, db, ref)
}

func (c *phoneCredentials) SetPassphrase(ctx context.Context, passphrase string) error {
	store, err := c.current()
	if err != nil {
		return err
	}
	return store.SetPassphrase(ctx, passphrase)
}

func (c *phoneCredentials) UnlockWithPassphrase(ctx context.Context, passphrase string) error {
	store, err := c.current()
	if err != nil {
		return err
	}
	return store.UnlockWithPassphrase(ctx, passphrase)
}

func (c *phoneCredentials) UseSessionOnly(ctx context.Context) error {
	store, err := c.current()
	if err != nil {
		return err
	}
	return store.UseSessionOnly(ctx)
}

// current returns the open store or the unavailable sentinel. The caller must
// not hold c.mu.
func (c *phoneCredentials) current() (*store.SecretStore, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil, appvault.ErrSecretsUnavailable
	}
	return c.store, nil
}

// phoneRuntime is the phone composition root's handle on the Vault Sync engine
// that a credential retry may attach after startup. The atomic pointer lets
// the database change hook and the lifecycle callbacks read it without holding
// a lock.
type phoneRuntime struct {
	sync atomic.Pointer[phoneSync]
}

func (r *phoneRuntime) currentSync() *phoneSync { return r.sync.Load() }
func (r *phoneRuntime) setSync(sync *phoneSync) { r.sync.Store(sync) }

// Stop ends a Vault Sync engine that was attached at startup or by a retry.
func (r *phoneRuntime) Stop() {
	if sync := r.sync.Load(); sync != nil {
		sync.Stop()
	}
}
