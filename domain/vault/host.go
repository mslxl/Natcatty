// Package vault holds the Vault's pure domain model: a Host and the rules
// that keep a Host record meaningful. It imports only the standard library
// and the shared domain/model vocabulary.
package vault

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mslxl/natcatty/domain/model"
)

// Protocol is the connection protocol a Host uses. Only the protocols this
// slice can manage are listed; later tickets add mosh, EternalTerminal,
// serial, and network-device behavior.
type Protocol string

const (
	// ProtocolSSH is a Host reached over SSH.
	ProtocolSSH Protocol = "ssh"
	// ProtocolTelnet is a Host reached over Telnet.
	ProtocolTelnet Protocol = "telnet"
	// ProtocolLocal is a shell that runs on this machine.
	ProtocolLocal Protocol = "local"
)

// Limits keep a malformed Host from entering the Vault. They are generous
// enough for real inventories and small enough to catch pasted garbage.
const (
	maxLabelLength    = 255
	maxHostnameLength = 255
	maxUsernameLength = 128
	maxGroupLength    = 1024
	maxTagLength      = 64
	maxTagCount       = 64
	maxNotesLength    = 64 << 10
	// maxKeyMaterialLength bounds a private key or certificate before it is
	// handed to the field-secret store.
	maxKeyMaterialLength = 256 << 10
)

// HostAuthMethod is the authentication choice stored on a Host. It is a
// selection, not credential material.
type HostAuthMethod string

const (
	// HostAuthAuto lets the connection choose from the configured material,
	// matching the original's automatic ordering.
	HostAuthAuto HostAuthMethod = "auto"
	// HostAuthPassword isolates password and keyboard-interactive auth; no key
	// or agent fallback is offered.
	HostAuthPassword HostAuthMethod = "password"
	// HostAuthKey uses the selected SSH Key and fails closed when it cannot be
	// used.
	HostAuthKey HostAuthMethod = "key"
	// HostAuthCertificate uses the selected SSH Key together with its signed
	// certificate and fails closed when it cannot be used.
	HostAuthCertificate HostAuthMethod = "certificate"
)

// Host is one saved target in the Vault. Credential material is deliberately
// absent: the Vault application encrypts a submitted password into the
// field-secret store, and the Host carries only the non-secret HasPassword
// flag. Nothing on this struct is ever credential plaintext.
type Host struct {
	ID       model.HostID `json:"id"`
	Label    string       `json:"label"`
	Hostname string       `json:"hostname"`
	Port     int          `json:"port"`
	Username string       `json:"username"`
	Protocol Protocol     `json:"protocol"`
	// Group is a slash-separated Host Group path, "" for the Vault root.
	// It is a path, not an id: a Host may reference a group that no longer
	// exists, matching the original's non-cascading behavior.
	Group  string   `json:"group"`
	Tags   []string `json:"tags"`
	Notes  string   `json:"notes,omitempty"`
	Pinned bool     `json:"pinned"`
	// HasPassword reports that an encrypted password exists for this Host.
	// The ciphertext lives in secret_values, never in this record.
	HasPassword bool `json:"hasPassword,omitempty"`
	// IdentityID selects a reusable Identity. An empty value means the Host
	// authenticates on its own.
	IdentityID model.IdentityID `json:"identityId,omitempty"`
	// KeyID selects an SSH Key directly. IdentityID and KeyID are references,
	// not existence guarantees: a deleted Key leaves a dangling reference, as
	// in the original's non-cascading Vault.
	KeyID model.KeyID `json:"keyId,omitempty"`
	// AuthMethod is the explicit authentication choice. Empty means auto.
	AuthMethod HostAuthMethod `json:"authMethod,omitempty"`
	// UseSSHAgent offers the local SSH agent as the first automatic method.
	UseSSHAgent bool `json:"useSshAgent,omitempty"`
	// RequiresMFA prefers keyboard-interactive before password or key probing.
	RequiresMFA bool `json:"requiresMfa,omitempty"`
	// OrderKey orders Hosts within their Host Group, in steps of 1000.
	OrderKey  int64     `json:"orderKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Draft is the editable part of a Host: what a create or edit form supplies.
// The application layer owns the identity and timestamps.
type Draft struct {
	Label    string
	Hostname string
	Port     int
	Username string
	Protocol Protocol
	Group    string
	Tags     []string
	Notes    string
	Pinned   bool
	// IdentityID, KeyID, AuthMethod, UseSSHAgent, and RequiresMFA select how
	// the Host authenticates. They carry references only, never material.
	IdentityID  model.IdentityID
	KeyID       model.KeyID
	AuthMethod  HostAuthMethod
	UseSSHAgent bool
	RequiresMFA bool
	// Password is plaintext form input. The Vault application encrypts it
	// into the field-secret store; it is never copied onto Host or written
	// to the JSON tail. An empty value on Update keeps the stored password.
	Password string
}

// Field names the part of a Host a validation error concerns.
type Field string

const (
	FieldLabel       Field = "label"
	FieldHostname    Field = "hostname"
	FieldPort        Field = "port"
	FieldUsername    Field = "username"
	FieldProtocol    Field = "protocol"
	FieldGroup       Field = "group"
	FieldTags        Field = "tags"
	FieldNotes       Field = "notes"
	FieldAuthMethod  Field = "auth_method"
	FieldIdentity    Field = "identity"
	FieldKey         Field = "key"
	FieldKeyType     Field = "key_type"
	FieldKeyMaterial Field = "key_material"
	FieldPrivateKey  Field = "private_key"
	FieldPassphrase  Field = "passphrase"
	FieldCertificate Field = "certificate"
)

// Rule names the validation rule a value broke. The UI maps Field and Rule to
// a localized message; domain and application code never contain user-visible
// text.
type Rule string

const (
	RuleRequired    Rule = "required"
	RuleInvalid     Rule = "invalid"
	RuleOutOfRange  Rule = "out_of_range"
	RuleTooLong     Rule = "too_long"
	RuleUnsupported Rule = "unsupported"
)

// ValidationError reports the first rule a Host value broke.
type ValidationError struct {
	Field Field
	Rule  Rule
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("vault: %s is %s", e.Field, e.Rule)
}

// NormalizeDraft trims and canonicalizes user input and fills the defaults a
// blank form implies. It returns a *ValidationError when the input cannot be
// saved.
func NormalizeDraft(draft Draft) (Draft, error) {
	draft.Protocol = Protocol(strings.ToLower(strings.TrimSpace(string(draft.Protocol))))
	if draft.Protocol == "" {
		draft.Protocol = ProtocolSSH
	}
	draft.AuthMethod = HostAuthMethod(strings.ToLower(strings.TrimSpace(string(draft.AuthMethod))))
	if draft.AuthMethod == "" {
		draft.AuthMethod = HostAuthAuto
	}
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Hostname = strings.TrimSpace(draft.Hostname)
	draft.Username = strings.TrimSpace(draft.Username)
	draft.Group = NormalizeGroup(draft.Group)
	draft.Notes = strings.TrimSpace(draft.Notes)
	draft.Tags = normalizeTags(draft.Tags)
	if draft.Label == "" {
		draft.Label = draft.Hostname
	}
	if draft.Port == 0 {
		draft.Port = defaultPort(draft.Protocol)
	}
	if err := validateFields(draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

// NormalizeGroup canonicalizes a Host Group path: slash-separated segments,
// no leading, trailing, or repeated slashes, and each segment trimmed.
func NormalizeGroup(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	segments := strings.Split(path, "/")
	cleaned := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		cleaned = append(cleaned, segment)
	}
	return strings.Join(cleaned, "/")
}

// MergeKey is the identity the Vault treats as a duplicate: two Hosts with
// the same protocol, normalized address, port, username, and Host Group would
// connect to the same endpoint. A local Host additionally needs its label,
// because its address is the machine the app runs on.
func (h Host) MergeKey() string {
	parts := []string{
		string(h.Protocol),
		strings.ToLower(h.Hostname),
		strconv.Itoa(h.Port),
		strings.ToLower(h.Username),
		NormalizeGroup(h.Group),
	}
	if h.Protocol == ProtocolLocal {
		parts = append(parts, strings.ToLower(strings.TrimSpace(h.Label)))
	}
	return strings.Join(parts, "\x1f")
}

// Validate checks that a Host already in canonical form is saveable.
func (h Host) Validate() error {
	return validateFields(Draft{
		Label:       h.Label,
		Hostname:    h.Hostname,
		Port:        h.Port,
		Username:    h.Username,
		Protocol:    h.Protocol,
		Group:       h.Group,
		Tags:        h.Tags,
		Notes:       h.Notes,
		AuthMethod:  h.AuthMethod,
		RequiresMFA: h.RequiresMFA,
		UseSSHAgent: h.UseSSHAgent,
	})
}

func validateFields(draft Draft) error {
	switch draft.Protocol {
	case ProtocolSSH, ProtocolTelnet, ProtocolLocal:
	default:
		return validation(FieldProtocol, RuleUnsupported)
	}

	switch draft.AuthMethod {
	case "", HostAuthAuto, HostAuthPassword, HostAuthKey, HostAuthCertificate:
	default:
		return validation(FieldAuthMethod, RuleUnsupported)
	}

	if draft.Label == "" {
		return validation(FieldLabel, RuleRequired)
	}
	if err := validateText(FieldLabel, draft.Label, maxLabelLength, false); err != nil {
		return err
	}

	if draft.Protocol == ProtocolLocal {
		if draft.Hostname != "" {
			if err := validateText(FieldHostname, draft.Hostname, maxHostnameLength, true); err != nil {
				return err
			}
		}
		if draft.Port != 0 {
			return validation(FieldPort, RuleOutOfRange)
		}
	} else {
		if draft.Hostname == "" {
			return validation(FieldHostname, RuleRequired)
		}
		if err := validateText(FieldHostname, draft.Hostname, maxHostnameLength, true); err != nil {
			return err
		}
		if draft.Port < 1 || draft.Port > 65535 {
			return validation(FieldPort, RuleOutOfRange)
		}
	}

	if err := validateText(FieldUsername, draft.Username, maxUsernameLength, false); err != nil {
		return err
	}

	if len(draft.Group) > maxGroupLength {
		return validation(FieldGroup, RuleTooLong)
	}
	if hasControl(draft.Group) {
		return validation(FieldGroup, RuleInvalid)
	}

	if len(draft.Tags) > maxTagCount {
		return validation(FieldTags, RuleTooLong)
	}
	for _, tag := range draft.Tags {
		if err := validateText(FieldTags, tag, maxTagLength, false); err != nil {
			return err
		}
	}

	if len(draft.Notes) > maxNotesLength {
		return validation(FieldNotes, RuleTooLong)
	}
	if strings.ContainsRune(draft.Notes, 0) {
		return validation(FieldNotes, RuleInvalid)
	}
	return nil
}

// validateText rejects control characters, and when noSpace is true, any
// whitespace inside the value. Hostnames and usernames must be single tokens.
func validateText(field Field, value string, max int, noSpace bool) error {
	if len(value) > max {
		return validation(field, RuleTooLong)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return validation(field, RuleInvalid)
		}
		if noSpace && unicode.IsSpace(r) {
			return validation(field, RuleInvalid)
		}
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func defaultPort(protocol Protocol) int {
	switch protocol {
	case ProtocolTelnet:
		return 23
	case ProtocolLocal:
		return 0
	default:
		return 22
	}
}

func validation(field Field, rule Rule) error {
	return &ValidationError{Field: field, Rule: rule}
}
