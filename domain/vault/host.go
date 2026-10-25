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

// Protocol is the connection protocol a Host uses. Serial and network-device
// behavior are settings on an SSH Host, not protocols of their own.
type Protocol string

const (
	// ProtocolSSH is a Host reached over SSH.
	ProtocolSSH Protocol = "ssh"
	// ProtocolTelnet is a Host reached over Telnet.
	ProtocolTelnet Protocol = "telnet"
	// ProtocolLocal is a shell that runs on this machine.
	ProtocolLocal Protocol = "local"
	// ProtocolMosh is an SSH Host that swaps to Mosh after the bootstrap.
	ProtocolMosh Protocol = "mosh"
	// ProtocolET is an SSH Host reached through the bundled EternalTerminal
	// client.
	ProtocolET Protocol = "et"
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
	// maxHostChainLength bounds a jump chain. It is deliberately generous for
	// real bastion topologies and small enough to reject accidental loops.
	maxHostChainLength = 10
	// maxProxyCommandLength bounds a ProxyCommand before it reaches a shell.
	maxProxyCommandLength = 8 << 10
	// maxMoshServerPathLength bounds the remote mosh-server override.
	maxMoshServerPathLength = 4 << 10
	// maxAlgorithmCount and maxAlgorithmNameLength bound an advanced algorithm
	// selection so a pasted list cannot balloon the Host record.
	maxAlgorithmCount      = 64
	maxAlgorithmNameLength = 128
)

// FileProtocol is a Host's file-transfer protocol preference for remote
// panes. It is independent of the connection Protocol: only SSH Hosts can use
// SFTP or SCP.
type FileProtocol string

const (
	// FileProtocolAuto tries SFTP and may fall back to SCP when the server has
	// no SFTP subsystem. A single-channel Host never falls back.
	FileProtocolAuto FileProtocol = "auto"
	// FileProtocolSFTP uses SFTP only and reports a failure instead of
	// silently changing protocol.
	FileProtocolSFTP FileProtocol = "sftp"
	// FileProtocolSCP uses scp and SSH exec channels, for servers without
	// SFTP.
	FileProtocolSCP FileProtocol = "scp"
)

// Valid reports whether p is one of the selectable file protocols.
func (p FileProtocol) Valid() bool {
	switch p {
	case FileProtocolAuto, FileProtocolSFTP, FileProtocolSCP:
		return true
	}
	return false
}

// NormalizeFileProtocol maps an empty or unknown value to automatic. It is the
// single normalization for records written before the preference existed and
// for form values that have not been validated yet.
func NormalizeFileProtocol(value FileProtocol) FileProtocol {
	switch value {
	case FileProtocolSFTP, FileProtocolSCP:
		return value
	default:
		return FileProtocolAuto
	}
}

// DeviceType marks a Host whose SSH peer runs a vendor CLI instead of a POSIX
// shell. Network-device mode sends commands as-is, without a shell wrapper or
// completion markers, and has no exit code to report.
type DeviceType string

const (
	// DeviceTypeDefault is an ordinary SSH Host.
	DeviceTypeDefault DeviceType = ""
	// DeviceTypeNetwork is network equipment (a switch, router, or firewall)
	// reached over SSH in raw execution mode.
	DeviceTypeNetwork DeviceType = "network"
)

// NormalizeDeviceType maps an empty or unknown value to the default. Network
// mode is meaningful only for SSH, so a Host of another protocol never keeps
// it.
func NormalizeDeviceType(value DeviceType) DeviceType {
	normalized := DeviceType(strings.ToLower(strings.TrimSpace(string(value))))
	if normalized == DeviceTypeNetwork {
		return DeviceTypeNetwork
	}
	return DeviceTypeDefault
}

// ProxyType is a transport used to reach the first endpoint of a route.
type ProxyType string

const (
	// ProxyHTTP is an HTTP CONNECT proxy.
	ProxyHTTP ProxyType = "http"
	// ProxySOCKS5 is a SOCKS5 proxy.
	ProxySOCKS5 ProxyType = "socks5"
	// ProxyCommand spawns a process and speaks SSH over its stdio.
	ProxyCommand ProxyType = "command"
)

// ProxyConfig is a Host's proxy transport. Password is form input and is
// never stored on the Host: the Vault encrypts it into the field-secret store
// and the Host carries only HasPassword.
type ProxyConfig struct {
	Type     ProxyType `json:"type"`
	Host     string    `json:"host,omitempty"`
	Port     int       `json:"port,omitempty"`
	Command  string    `json:"command,omitempty"`
	Username string    `json:"username,omitempty"`
	// Password is credential plaintext in memory only.
	Password string `json:"-"`
	// HasPassword reports that an encrypted proxy password exists.
	HasPassword bool `json:"hasPassword,omitempty"`
}

// HostChain is a jump chain, ordered first = closest to the client. The final
// target is the Host that owns the chain, so it is not repeated here.
type HostChain struct {
	HostIDs []model.HostID `json:"hostIds"`
}

// AlgorithmOverrides is a Host's advanced SSH algorithm selection. A non-empty
// category replaces the transport's offer for that category. The JSON names
// mirror the original's algorithm shape.
type AlgorithmOverrides struct {
	KeyExchanges []string `json:"kex,omitempty"`
	Ciphers      []string `json:"cipher,omitempty"`
	MACs         []string `json:"hmac,omitempty"`
	HostKeys     []string `json:"serverHostKey,omitempty"`
}

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
	// X11Forwarding requests X11 forwarding for SSH Sessions on this Host. It
	// is off by default and requires X11WarningAcknowledged.
	X11Forwarding bool `json:"x11Forwarding,omitempty"`
	// X11WarningAcknowledged records that the user accepted the X11 security
	// warning. It is meaningful only while X11Forwarding is true.
	X11WarningAcknowledged bool `json:"x11WarningAcknowledged,omitempty"`
	// HostChain lists the jump Host references used before this Host. It is an
	// ordered reference list, not an existence guarantee: a deleted jump Host
	// remains dangling, as in the original's non-cascading Vault.
	HostChain *HostChain `json:"hostChain,omitempty"`
	// Proxy is the transport used to reach the first endpoint. It applies to
	// hop 1 only; later hops run over the nested SSH stream.
	Proxy *ProxyConfig `json:"proxy,omitempty"`
	// ProxyProfileID selects a saved Proxy Profile. A manual Proxy wins over a
	// profile; an empty reference means the Host has no proxy of its own.
	ProxyProfileID string `json:"proxyProfileId,omitempty"`
	// LegacyAlgorithms widens the offered algorithms for older servers. On a
	// jump Host it is additive with the target's setting.
	LegacyAlgorithms bool `json:"legacyAlgorithms,omitempty"`
	// SkipEcdsaHostKey removes every ECDSA host-key algorithm from the offer.
	// It is strictly per-Host and is never inherited by a jump.
	SkipEcdsaHostKey bool `json:"skipEcdsaHostKey,omitempty"`
	// Algorithms replaces individual algorithm categories when set.
	Algorithms *AlgorithmOverrides `json:"algorithms,omitempty"`
	// SFTPFileProtocol selects the remote file-transfer protocol. Empty means
	// automatic: try SFTP, then fall back to SCP when allowed.
	SFTPFileProtocol FileProtocol `json:"sftpFileProtocol,omitempty"`
	// SFTPSudo elevates the SFTP subsystem through sudo so root-owned files
	// can be edited. It requires SFTP or automatic protocol selection.
	SFTPSudo bool `json:"sftpSudo,omitempty"`
	// DeviceType selects raw network-device execution for an SSH Host. It is
	// the original's Host Details "Network Device Mode" setting and uses the
	// same persisted JSON name.
	DeviceType DeviceType `json:"deviceType,omitempty"`
	// MoshServerPath overrides the remote mosh-server binary for a Mosh Host.
	// Empty uses the remote PATH. It is kept only for ProtocolMosh.
	MoshServerPath string `json:"moshServerPath,omitempty"`
	// ETPort is the EternalTerminal server port for an ET Host. Zero means the
	// 2022 default. It is kept only for ProtocolET.
	ETPort int `json:"etPort,omitempty"`
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
	// X11Forwarding and X11WarningAcknowledged enable X11 forwarding. The
	// acknowledgement must be set before the preference can be saved.
	X11Forwarding          bool
	X11WarningAcknowledged bool
	// HostChain and Proxy configure the route to this Host. Proxy.Password is
	// plaintext form input; the Vault encrypts it into the field-secret store.
	HostChain *HostChain
	Proxy     *ProxyConfig
	// ProxyProfileID selects a saved Proxy Profile. It is a soft reference:
	// deleting a profile clears it on every Host and Host Group, but a record
	// written by an older version may still hold an unknown identifier.
	ProxyProfileID string
	// LegacyAlgorithms, SkipEcdsaHostKey, and Algorithms configure SSH
	// negotiation. Each jump Host carries its own copy.
	LegacyAlgorithms bool
	SkipEcdsaHostKey bool
	Algorithms       *AlgorithmOverrides
	// SFTPFileProtocol and SFTPSudo are the remote-pane protocol preference.
	SFTPFileProtocol FileProtocol
	SFTPSudo         bool
	// DeviceType selects raw network-device execution. It is kept only for
	// SSH Hosts.
	DeviceType DeviceType
	// MoshServerPath overrides the remote mosh-server binary. It is kept only
	// for ProtocolMosh.
	MoshServerPath string
	// ETPort is the EternalTerminal server port. Zero means the default and is
	// kept only for ProtocolET.
	ETPort int
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
	FieldChain       Field = "chain"
	FieldProxy       Field = "proxy"
	FieldAlgorithms  Field = "algorithms"
	FieldX11         Field = "x11"
	// FieldFileProtocol and FieldSFTPSudo name the remote-pane protocol
	// preference.
	FieldFileProtocol Field = "file_protocol"
	FieldSFTPSudo     Field = "sftp_sudo"
	// FieldMoshServerPath and FieldETPort name the Mosh and EternalTerminal
	// settings a validation error concerns.
	FieldMoshServerPath Field = "mosh_server_path"
	FieldETPort         Field = "et_port"
	// FieldSnippet, FieldSnippetCommand, and FieldSnippetPackage name the
	// parts of a Snippet a validation error concerns.
	FieldSnippet        Field = "snippet"
	FieldSnippetCommand Field = "snippet_command"
	FieldSnippetPackage Field = "snippet_package"
	// FieldNote, FieldNoteContent, and FieldNoteGroup name the parts of a
	// Vault Note a validation error concerns.
	FieldNote        Field = "note"
	FieldNoteContent Field = "note_content"
	FieldNoteGroup   Field = "note_group"
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
	// RuleAcknowledgementRequired reports a security-sensitive preference that
	// cannot be saved until the user explicitly accepts its warning.
	RuleAcknowledgementRequired Rule = "acknowledgement_required"
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
	draft.SFTPFileProtocol = FileProtocol(strings.ToLower(strings.TrimSpace(string(draft.SFTPFileProtocol))))
	if draft.SFTPFileProtocol == "" {
		draft.SFTPFileProtocol = FileProtocolAuto
	}
	draft.DeviceType = NormalizeDeviceType(draft.DeviceType)
	if draft.Protocol != ProtocolSSH {
		// Raw network-device execution needs an SSH PTY; a Host that switched
		// to another protocol must not keep a mode its transport cannot run.
		draft.DeviceType = DeviceTypeDefault
	}
	draft.MoshServerPath = strings.TrimSpace(draft.MoshServerPath)
	if draft.Protocol != ProtocolMosh {
		// A server path and an ET port belong to one protocol. Keeping them on
		// another Host would surface a setting its transport never reads.
		draft.MoshServerPath = ""
	}
	if draft.Protocol != ProtocolET {
		draft.ETPort = 0
	}
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Hostname = strings.TrimSpace(draft.Hostname)
	draft.Username = strings.TrimSpace(draft.Username)
	draft.Group = NormalizeGroup(draft.Group)
	draft.Notes = strings.TrimSpace(draft.Notes)
	draft.Tags = normalizeTags(draft.Tags)
	draft.HostChain = normalizeHostChain(draft.HostChain)
	draft.Proxy = normalizeProxy(draft.Proxy)
	draft.ProxyProfileID = strings.TrimSpace(draft.ProxyProfileID)
	draft.Algorithms = normalizeAlgorithms(draft.Algorithms)
	// Disabling X11 drops the acknowledgement, so re-enabling it always asks
	// the user to accept the warning again.
	if !draft.X11Forwarding {
		draft.X11WarningAcknowledged = false
	}
	if draft.Label == "" {
		draft.Label = draft.Hostname
	}
	// A zero Port is left empty on purpose: it means "inherit the Host Group's
	// port", and EffectivePort falls back to the protocol default only when no
	// group supplies one. Filling 22 here would make group defaults unreachable.
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
// the same protocol, normalized address, recorded port, username, and Host
// Group would connect to the same endpoint. The recorded port is compared, not
// the effective one: only the Host Group can resolve an empty port, so a blank
// port and an explicit protocol default are different endpoints when the group
// supplies its own port. A local Host additionally needs its label, because
// its address is the machine the app runs on.
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
		Label:                  h.Label,
		Hostname:               h.Hostname,
		Port:                   h.Port,
		Username:               h.Username,
		Protocol:               h.Protocol,
		Group:                  h.Group,
		Tags:                   h.Tags,
		Notes:                  h.Notes,
		AuthMethod:             h.AuthMethod,
		RequiresMFA:            h.RequiresMFA,
		UseSSHAgent:            h.UseSSHAgent,
		X11Forwarding:          h.X11Forwarding,
		X11WarningAcknowledged: h.X11WarningAcknowledged,
		HostChain:              h.HostChain,
		Proxy:                  h.Proxy,
		ProxyProfileID:         h.ProxyProfileID,
		Algorithms:             h.Algorithms,
		SFTPFileProtocol:       h.SFTPFileProtocol,
		SFTPSudo:               h.SFTPSudo,
		DeviceType:             h.DeviceType,
		MoshServerPath:         h.MoshServerPath,
		ETPort:                 h.ETPort,
	})
}

// X11Enabled reports whether a saved Host may request X11 forwarding. Both the
// preference and the security acknowledgement must be present, so a record
// created before the warning existed never enables forwarding silently.
func (h Host) X11Enabled() bool {
	return h.X11Forwarding && h.X11WarningAcknowledged
}

// IsNetworkDevice reports whether the Host runs a vendor CLI over SSH and
// therefore needs raw execution. A value written without SSH (an old or
// hand-edited record) is not enough on its own.
func (h Host) IsNetworkDevice() bool {
	return h.Protocol == ProtocolSSH && h.DeviceType == DeviceTypeNetwork
}

func validateFields(draft Draft) error {
	switch draft.Protocol {
	case ProtocolSSH, ProtocolTelnet, ProtocolLocal, ProtocolMosh, ProtocolET:
	default:
		return validation(FieldProtocol, RuleUnsupported)
	}

	switch draft.AuthMethod {
	case "", HostAuthAuto, HostAuthPassword, HostAuthKey, HostAuthCertificate:
	default:
		return validation(FieldAuthMethod, RuleUnsupported)
	}

	// An empty value is the automatic default for records written before the
	// protocol preference existed.
	fileProtocol := draft.SFTPFileProtocol
	if fileProtocol == "" {
		fileProtocol = FileProtocolAuto
	}
	if !fileProtocol.Valid() {
		return validation(FieldFileProtocol, RuleInvalid)
	}
	if draft.Protocol != ProtocolSSH {
		if fileProtocol != FileProtocolAuto {
			return validation(FieldFileProtocol, RuleUnsupported)
		}
		if draft.SFTPSudo {
			return validation(FieldSFTPSudo, RuleUnsupported)
		}
	}
	if draft.SFTPSudo && fileProtocol == FileProtocolSCP {
		return validation(FieldSFTPSudo, RuleUnsupported)
	}

	if draft.X11Forwarding {
		if draft.Protocol != ProtocolSSH {
			return validation(FieldX11, RuleUnsupported)
		}
		if !draft.X11WarningAcknowledged {
			return validation(FieldX11, RuleAcknowledgementRequired)
		}
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
		// Zero means the Host inherits its Host Group's port, or the protocol
		// default when the group sets none.
		if draft.Port != 0 && (draft.Port < 1 || draft.Port > 65535) {
			return validation(FieldPort, RuleOutOfRange)
		}
	}

	if err := validateText(FieldUsername, draft.Username, maxUsernameLength, false); err != nil {
		return err
	}

	if draft.Protocol == ProtocolMosh {
		if err := validateText(FieldMoshServerPath, draft.MoshServerPath, maxMoshServerPathLength, false); err != nil {
			return err
		}
	}
	if draft.Protocol == ProtocolET && draft.ETPort != 0 && (draft.ETPort < 1 || draft.ETPort > 65535) {
		return validation(FieldETPort, RuleOutOfRange)
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

	if draft.HostChain != nil {
		if len(draft.HostChain.HostIDs) > maxHostChainLength {
			return validation(FieldChain, RuleTooLong)
		}
		seen := make(map[model.HostID]bool, len(draft.HostChain.HostIDs))
		for _, id := range draft.HostChain.HostIDs {
			if strings.TrimSpace(string(id)) == "" || seen[id] {
				return validation(FieldChain, RuleInvalid)
			}
			seen[id] = true
		}
	}

	if err := validateProxy(draft.Proxy); err != nil {
		return err
	}
	if err := validateText(FieldProxy, draft.ProxyProfileID, maxLabelLength, false); err != nil {
		return err
	}
	return validateAlgorithms(draft.Algorithms)
}

// validateAlgorithms checks the advanced algorithm selection. The values are
// opaque to the domain; the transport reports an unknown name as an
// unsupported-algorithm error during negotiation.
func validateAlgorithms(overrides *AlgorithmOverrides) error {
	if overrides == nil {
		return nil
	}
	for _, list := range [][]string{
		overrides.KeyExchanges, overrides.Ciphers, overrides.MACs, overrides.HostKeys,
	} {
		if len(list) > maxAlgorithmCount {
			return validation(FieldAlgorithms, RuleTooLong)
		}
		for _, value := range list {
			if value == "" {
				return validation(FieldAlgorithms, RuleRequired)
			}
			if err := validateText(FieldAlgorithms, value, maxAlgorithmNameLength, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateProxy checks a normalized proxy configuration. A nil proxy means a
// direct connection and is always valid.
func validateProxy(proxy *ProxyConfig) error {
	if proxy == nil {
		return nil
	}
	switch proxy.Type {
	case ProxyHTTP, ProxySOCKS5:
		if proxy.Host == "" {
			return validation(FieldProxy, RuleRequired)
		}
		if err := validateText(FieldProxy, proxy.Host, maxHostnameLength, true); err != nil {
			return err
		}
		if proxy.Port < 1 || proxy.Port > 65535 {
			return validation(FieldProxy, RuleOutOfRange)
		}
		if err := validateText(FieldProxy, proxy.Username, maxUsernameLength, false); err != nil {
			return err
		}
		if proxy.Command != "" {
			return validation(FieldProxy, RuleInvalid)
		}
	case ProxyCommand:
		if proxy.Command == "" {
			return validation(FieldProxy, RuleRequired)
		}
		if len(proxy.Command) > maxProxyCommandLength {
			return validation(FieldProxy, RuleTooLong)
		}
		if strings.ContainsRune(proxy.Command, 0) {
			return validation(FieldProxy, RuleInvalid)
		}
	default:
		return validation(FieldProxy, RuleUnsupported)
	}
	return nil
}

// normalizeHostChain trims and copies a chain, dropping an empty chain so a
// Host without jumps serializes exactly like a Host that never had one.
func normalizeHostChain(chain *HostChain) *HostChain {
	if chain == nil || len(chain.HostIDs) == 0 {
		return nil
	}
	normalized := &HostChain{HostIDs: make([]model.HostID, 0, len(chain.HostIDs))}
	for _, id := range chain.HostIDs {
		normalized.HostIDs = append(normalized.HostIDs, model.HostID(strings.TrimSpace(string(id))))
	}
	return normalized
}

// normalizeAlgorithms trims, drops empty and duplicate names, and collapses an
// empty selection to nil so it serializes like a Host that never set one.
func normalizeAlgorithms(overrides *AlgorithmOverrides) *AlgorithmOverrides {
	if overrides == nil {
		return nil
	}
	normalized := &AlgorithmOverrides{
		KeyExchanges: normalizeAlgorithmNames(overrides.KeyExchanges),
		Ciphers:      normalizeAlgorithmNames(overrides.Ciphers),
		MACs:         normalizeAlgorithmNames(overrides.MACs),
		HostKeys:     normalizeAlgorithmNames(overrides.HostKeys),
	}
	if len(normalized.KeyExchanges) == 0 && len(normalized.Ciphers) == 0 &&
		len(normalized.MACs) == 0 && len(normalized.HostKeys) == 0 {
		return nil
	}
	return normalized
}

func normalizeAlgorithmNames(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeProxy(proxy *ProxyConfig) *ProxyConfig {
	if proxy == nil {
		return nil
	}
	normalized := &ProxyConfig{
		Type:        ProxyType(strings.ToLower(strings.TrimSpace(string(proxy.Type)))),
		Host:        strings.TrimSpace(proxy.Host),
		Port:        proxy.Port,
		Command:     strings.TrimSpace(proxy.Command),
		Username:    strings.TrimSpace(proxy.Username),
		Password:    proxy.Password,
		HasPassword: proxy.HasPassword,
	}
	if normalized.Type == "" && normalized.Host == "" && normalized.Port == 0 &&
		normalized.Command == "" && normalized.Username == "" && normalized.Password == "" {
		return nil
	}
	return normalized
}

// CloneProxy returns a copy safe to store on a Host. Credential plaintext is
// never copied: only the HasPassword flag survives.
func CloneProxy(proxy *ProxyConfig) *ProxyConfig {
	if proxy == nil {
		return nil
	}
	copy := *proxy
	copy.Password = ""
	return &copy
}

// CloneHostChain returns a copy of a jump chain.
func CloneHostChain(chain *HostChain) *HostChain {
	if chain == nil {
		return nil
	}
	copy := HostChain{HostIDs: append([]model.HostID(nil), chain.HostIDs...)}
	return &copy
}

// CloneAlgorithms returns a deep copy of an algorithm selection.
func CloneAlgorithms(overrides *AlgorithmOverrides) *AlgorithmOverrides {
	if overrides == nil {
		return nil
	}
	return &AlgorithmOverrides{
		KeyExchanges: append([]string(nil), overrides.KeyExchanges...),
		Ciphers:      append([]string(nil), overrides.Ciphers...),
		MACs:         append([]string(nil), overrides.MACs...),
		HostKeys:     append([]string(nil), overrides.HostKeys...),
	}
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

// EffectivePort returns the port a connection uses. A Host with a port of its
// own wins; a zero port means it inherits its Host Group's port, and when no
// group sets one the protocol's default applies (22 for SSH, 23 for Telnet).
// The Vault stores the zero so the inheritance survives a group change.
func EffectivePort(protocol Protocol, port int) int {
	if port != 0 {
		return port
	}
	return defaultPort(protocol)
}

func validation(field Field, rule Rule) error {
	return &ValidationError{Field: field, Rule: rule}
}
