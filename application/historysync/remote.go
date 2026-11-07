// Package historysync owns the Atuin account and the future record sync
// engine. It declares the single external sync seam, Remote, and speaks only
// domain sync records; HTTP, MessagePack, and PASETO belong to adapters.
//
// This package never touches the UI or a transport. Local Command History
// stays plaintext; the master key protects wire payloads and is held only for
// one remote operation.
package historysync

import (
	"context"
	"errors"
	"strings"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

// Pinned Atuin protocol version. The adapter sends this as Atuin-Version and
// the account service fails closed for any other server major version.
const (
	PinnedAtuinVersion = "18.24.5"
	PinnedAtuinMajor   = 18
)

// Capability identifiers the Atuin server advertises. Only the loose-record
// and page-size capabilities are required; packfile is never used.
const (
	CapabilityServer   = "sh.atuin.server/capabilities"
	CapabilityPageSize = "sh.atuin.server/records.page_size"
)

// RequiredCapabilities are the capabilities this client cannot work without.
var RequiredCapabilities = []string{CapabilityServer, CapabilityPageSize}

// Remote errors. Adapters wrap these sentinels; the account service maps them
// to a sync status and never exposes a server message, token, key, or record
// payload.
var (
	// ErrRemoteUnavailable reports that no Remote adapter is wired.
	ErrRemoteUnavailable = errors.New("historysync: remote adapter unavailable")
	// ErrAuthFailed reports a rejected login or token.
	ErrAuthFailed = errors.New("historysync: authentication failed")
	// ErrAuthExpired reports an expired session token.
	ErrAuthExpired = errors.New("historysync: authentication expired")
	// ErrIncompatibleServer reports an unknown major version or a capability
	// this client cannot satisfy.
	ErrIncompatibleServer = errors.New("historysync: incompatible Atuin server")
	// ErrKeyMismatch reports that the master key does not decrypt an existing
	// record.
	ErrKeyMismatch = errors.New("historysync: master key mismatch")
	// ErrOffline reports that the server could not be reached.
	ErrOffline = errors.New("historysync: server unreachable")
	// ErrRemote reports any other remote failure.
	ErrRemote = errors.New("historysync: remote request failed")
)

// Config is the credential scope of one remote operation. A factory creates a
// remote per operation so the token and master key live no longer than that
// operation. Wipe clears the byte slices and string references it can clear.
type Config struct {
	BaseURL   string
	AuthMode  atuinmodel.AuthMode
	Token     string
	Username  string
	Password  string
	MasterKey []byte
}

// Wipe drops the credentials held by the config. Go strings cannot be zeroed
// in place, but the references are cleared and the key bytes are wiped.
func (config *Config) Wipe() {
	config.Token = ""
	config.Password = ""
	domainhistory.WipeMasterKey(config.MasterKey)
	config.MasterKey = nil
}

// Account is the server identity returned by a login or token validation.
type Account struct {
	ServerURL string
	UserID    string
	Username  string
}

// AuthResult is the result of Authenticate. Token is the session token minted
// by a username/password login; it is empty when the remote validated a pasted
// token. The account service stores a non-empty token and never returns it.
// The name avoids the glossary's Session, which is a terminal instance.
type AuthResult struct {
	Token   string
	Account Account
}

// Capabilities is the server's protocol version and capability advertisement.
// Values maps a capability name to its version. Enforced lists capabilities
// the server requires the client to honor; an enforced capability this build
// does not know fails closed.
type Capabilities struct {
	ServerVersion string
	Major         int
	Minor         int
	Patch         int
	PageSize      int
	Values        map[string]int
	Enforced      []string
}

// Supports reports whether the server advertises name.
func (capabilities Capabilities) Supports(name string) bool {
	_, ok := capabilities.Values[name]
	return ok
}

// Check fails closed unless the server is the pinned major version, advertises
// the required capabilities, and enforces nothing this client ignores.
func (capabilities Capabilities) Check() error {
	if capabilities.Major != PinnedAtuinMajor {
		return ErrIncompatibleServer
	}
	for _, name := range RequiredCapabilities {
		if !capabilities.Supports(name) || capabilities.Values[name] < 1 {
			return ErrIncompatibleServer
		}
		if name == CapabilityPageSize && capabilities.PageSize <= 0 {
			return ErrIncompatibleServer
		}
	}
	known := make(map[string]bool, len(RequiredCapabilities))
	for _, name := range RequiredCapabilities {
		known[name] = true
	}
	for _, enforced := range capabilities.Enforced {
		if !known[enforced] {
			return ErrIncompatibleServer
		}
	}
	return nil
}

// Series identifies one append-only Atuin record series.
type Series struct {
	SeriesHost string
	Tag        string
}

// SeriesStatus is the server's highest index for one series. MaxIndex is -1
// for a series with no records; Atuin only reports series it has seen, so an
// empty list means an empty account.
type SeriesStatus struct {
	Series
	MaxIndex int64
}

// HasHistory reports whether any status is a non-empty history series. Other
// Atuin tags are out of scope and never downloaded.
func HasHistory(statuses []SeriesStatus) bool {
	for _, status := range statuses {
		if status.Tag == domainhistory.RecordTagHistory && status.MaxIndex >= 0 {
			return true
		}
	}
	return false
}

// Page is one downloaded batch. NextIndex is the index a following download
// starts at; it is zero when the page is empty.
type Page struct {
	Records   []SyncRecord
	NextIndex int64
}

// SyncRecord is one domain sync record exchanged with the Remote port. A create
// record carries Entry; a delete record carries DeletedAt. Payload is the
// domain record, never ciphertext or a wire type.
type SyncRecord struct {
	ID         string
	SeriesHost string
	Tag        string
	Index      int64
	RecordedAt time.Time
	Version    int
	Kind       string
	HistoryID  string
	Entry      *domainhistory.Entry
	DeletedAt  time.Time
}

// Valid reports whether a record carries the identity and kind the sync
// engine needs. A create record must carry its entry; a delete record must
// carry a deletion time.
func (record SyncRecord) Valid() bool {
	if record.ID == "" || record.SeriesHost == "" || record.Tag == "" || record.Index < 0 ||
		record.RecordedAt.IsZero() || record.HistoryID == "" {
		return false
	}
	switch record.Kind {
	case domainhistory.RecordKindCreate:
		return record.Entry != nil
	case domainhistory.RecordKindDelete:
		return !record.DeletedAt.IsZero()
	default:
		return false
	}
}

// KeyValidation is the result of decrypting one existing record with the
// configured master key.
type KeyValidation string

const (
	// KeyValidationVerified reports a key that decrypted a real record.
	KeyValidationVerified KeyValidation = "verified"
	// KeyValidationUnverified reports an empty account with nothing to
	// validate against.
	KeyValidationUnverified KeyValidation = "unverified"
	// KeyValidationMismatch reports a key that failed to decrypt a record.
	KeyValidationMismatch KeyValidation = "mismatch"
)

// Remote is the only external Atuin seam. The adapter owns HTTP, MessagePack,
// PASETO, and PASERK; callers see domain accounts, versions, series, and
// records. Ticket 13 uses Authenticate, Capabilities, RecordStatus, and
// ValidateMasterKey; Download and Upload are defined now for the sync engine
// but are not called before ticket 14.
type Remote interface {
	// Authenticate logs in with the configured username/password or validates
	// the configured pasted token. It returns the effective session token when
	// a login minted one.
	Authenticate(ctx context.Context) (AuthResult, error)
	// Capabilities reads the server protocol version and capability set.
	Capabilities(ctx context.Context) (Capabilities, error)
	// RecordStatus reads the highest index of every remote series.
	RecordStatus(ctx context.Context) ([]SeriesStatus, error)
	// Download reads one page of history records starting at start.
	Download(ctx context.Context, series Series, start int64, count int) (Page, error)
	// Upload sends one ordered batch of history records.
	Upload(ctx context.Context, records []SyncRecord) error
	// ValidateMasterKey decrypts one existing history record when the account
	// has any, using the key in the operation config.
	ValidateMasterKey(ctx context.Context) (KeyValidation, error)
	// Close wipes the operation credentials and releases adapter resources.
	Close() error
}

// Factory creates a Remote for one operation. Implementations must scope the
// Config to that operation and must not retain it.
type Factory interface {
	Open(ctx context.Context, config Config) (Remote, error)
}

// SanitizeServerURL trims a configured base URL for comparison and display.
// It only removes surrounding whitespace and trailing slashes; full policy
// validation belongs to the account service.
func SanitizeServerURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}
