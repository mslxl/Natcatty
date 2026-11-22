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
	domainprotocol "github.com/mslxl/natcatty/domain/model/protocol"
	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

// Protocol is the connection protocol a Host uses. Serial and network-device
// behavior are settings on an SSH Host, not protocols of their own.
type Protocol string

const (
	// ProtocolSSH is a Host reached over SSH.
	ProtocolSSH Protocol = "ssh"
	// ProtocolTelnet is a Host reached over Telnet.
	ProtocolTelnet Protocol = "telnet"
	// ProtocolVNC is a Host reached over the RFB protocol.
	ProtocolVNC Protocol = "vnc"
)

// Transport names the Session transport an SSH Host uses. It is meaningful
// only when Protocol is ProtocolSSH; every other protocol clears it. The zero
// value is plain SSH, so the field stays out of the JSON of the common Host.
type Transport = domainprotocol.Transport

const (
	// TransportPlain is plain SSH. It is the zero value and is never persisted;
	// the transport input spelling "ssh" canonicalizes to it.
	TransportPlain Transport = ""
	// TransportMosh is the Mosh transport bootstrapped over SSH.
	TransportMosh = domainprotocol.TransportMosh
	// TransportET is the EternalTerminal transport bootstrapped over SSH.
	TransportET = domainprotocol.TransportET
)

// NormalizeTransport maps an input transport onto its canonical stored value.
// The empty value and the transport input spelling "ssh" mean plain SSH. Mosh
// and ET are recognized; any other value is returned lowercased and trimmed so
// validation can reject it. A protocol other than SSH never keeps a transport.
func NormalizeTransport(protocol Protocol, transport Transport) Transport {
	if protocol != ProtocolSSH {
		return TransportPlain
	}
	value := Transport(strings.ToLower(strings.TrimSpace(string(transport))))
	switch value {
	case TransportPlain, domainprotocol.TransportSSH:
		return TransportPlain
	case TransportMosh, TransportET:
		return value
	default:
		return value
	}
}

// normalizeProtocolTransport canonicalizes a (protocol, transport) pair. An
// empty protocol is the SSH default; the transport is meaningful only for SSH.
// It does not validate: an unknown protocol or transport is preserved so the
// validation step rejects it.
func normalizeProtocolTransport(protocol Protocol, transport Transport) (Protocol, Transport) {
	protocol = Protocol(strings.ToLower(strings.TrimSpace(string(protocol))))
	if protocol == "" {
		protocol = ProtocolSSH
	}
	return protocol, NormalizeTransport(protocol, transport)
}

// HostCapabilities is the capability set resolved for one Host or Draft. It is
// a value type so call sites never compare protocol names.
type HostCapabilities struct {
	SSHAuth      bool
	FileTransfer bool
	Forwarding   bool
	Monitoring   bool
	Proxy        bool
	X11          bool
	Plaintext    bool
}

func capabilitiesFor(protocol Protocol, transport Transport) HostCapabilities {
	protocol, transport = normalizeProtocolTransport(protocol, transport)
	descriptor, ok := descriptorFor(protocol, transport)
	if !ok {
		return HostCapabilities{}
	}
	return HostCapabilities{
		SSHAuth:      descriptor.SSHAuth,
		FileTransfer: descriptor.FileTransfer,
		Forwarding:   descriptor.Forwarding,
		Monitoring:   descriptor.Monitoring,
		Proxy:        descriptor.Proxy,
		X11:          descriptor.X11,
		Plaintext:    descriptor.Plaintext,
	}
}

// descriptorFor resolves the facts for a canonical protocol and transport.
func descriptorFor(protocol Protocol, transport Transport) (domainprotocol.Descriptor, bool) {
	if protocol != ProtocolSSH {
		return protocol.descriptor()
	}
	if transport == TransportPlain {
		return domainprotocol.Resolve(domainprotocol.SSH)
	}
	return domainprotocol.DescriptorForTransport(domainprotocol.Transport(transport))
}

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
	// maxStartupCommandLength bounds the command typed into every new
	// connection. It is generous for a small multi-line setup script.
	maxStartupCommandLength = 16 << 10
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

// The VNC endpoint and viewer vocabulary lives in domain/model/vnc so the
// Session start spec, this persisted record, and the RFB adapter share one
// definition. The names below stay on the Vault package because the Host record
// and its editor already use them.
const (
	// VNCDisplayBasePort is the TCP port of VNC display 0.
	VNCDisplayBasePort = modelvnc.DisplayBasePort
	// VNCDisplayMax is the highest display number the editor accepts.
	VNCDisplayMax = modelvnc.DisplayMax
)

// VNCDisplayMode selects how the VNC Host editor accepts the endpoint. It is
// presentation state: the Host record always stores the resolved TCP port.
type VNCDisplayMode = modelvnc.DisplayMode

const (
	// VNCDisplayNumber enters the endpoint as a display number ("1" or ":1").
	VNCDisplayNumber = modelvnc.DisplayNumber
	// VNCPort enters the endpoint as a raw TCP port.
	VNCPort = modelvnc.Port
)

// NormalizeDisplayMode maps an empty or unknown value to display-number input,
// the default the editor presents.
func NormalizeDisplayMode(mode VNCDisplayMode) VNCDisplayMode {
	return modelvnc.NormalizeDisplayMode(mode)
}

// VNCFitMode selects how the viewer scales the remote desktop.
type VNCFitMode = modelvnc.FitMode

const (
	// VNCFit scales the desktop to fit the pane, preserving the aspect ratio.
	VNCFit = modelvnc.Fit
	// VNCOneToOne shows one remote pixel per pane pixel.
	VNCOneToOne = modelvnc.OneToOne
	// VNCClip keeps one-to-one pixels and pans a clipped viewport.
	VNCClip = modelvnc.Clip
)

// NormalizeFitMode maps an empty or unknown value to fit-to-window, the
// default the viewer starts with.
func NormalizeFitMode(mode VNCFitMode) VNCFitMode {
	return modelvnc.NormalizeFitMode(mode)
}

// VNCCursorMode selects whether the local pointer or the server-drawn cursor
// is visible over the framebuffer.
type VNCCursorMode = modelvnc.CursorMode

const (
	// VNCCursorLocal draws the local pointer from the server's cursor shape.
	VNCCursorLocal = modelvnc.CursorLocal
	// VNCCursorServer lets the server paint the cursor into the framebuffer.
	VNCCursorServer = modelvnc.CursorServer
)

// NormalizeCursorMode maps an empty or unknown value to the local cursor, the
// default every other remote-desktop client uses.
func NormalizeCursorMode(mode VNCCursorMode) VNCCursorMode {
	return modelvnc.NormalizeCursorMode(mode)
}

// VNCConfig is a VNC Host's persisted viewer configuration. The endpoint is
// not stored here: the Host's Port is always the real TCP port, and the
// display-number input mode only decides how the editor presents it.
type VNCConfig = modelvnc.Config

// DefaultVNCConfig is the configuration a new VNC Host starts from and the
// baseline NormalizeVNCConfig fills missing enum values from.
func DefaultVNCConfig() VNCConfig {
	return modelvnc.DefaultConfig()
}

// NormalizeVNCConfig canonicalizes a VNC configuration. A nil value means a
// new or older record, so it starts from the defaults; unknown enum values
// fall back to their defaults. A non-nil Shared=false is an explicit choice
// and is preserved.
func NormalizeVNCConfig(config *VNCConfig) *VNCConfig {
	return modelvnc.NormalizeConfig(config)
}

// CloneVNCConfig returns a copy safe to store on a Host or draft. It keeps a
// nil value nil so a non-VNC Host serializes exactly as before.
func CloneVNCConfig(config *VNCConfig) *VNCConfig {
	return modelvnc.CloneConfig(config)
}

// VNCPortForDisplay returns the TCP port for a VNC display number. The
// boolean is false for a display outside 0-99.
func VNCPortForDisplay(display int) (int, bool) {
	return modelvnc.PortForDisplay(display)
}

// VNCDisplayForPort returns the display number for a TCP port. The boolean is
// false when the port is outside the 5900-5999 display range.
func VNCDisplayForPort(port int) (int, bool) {
	return modelvnc.DisplayForPort(port)
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
	// ProfileID marks a config materialized from a saved Proxy Profile. It is
	// resolved state, never persisted: the Host stores the reference in
	// ProxyProfileID instead.
	ProfileID string `json:"-"`
}

// HostChain is a jump chain, ordered first = closest to the client. The final
// target is the Host that owns the chain, so it is not repeated here.
type HostChain struct {
	HostIDs []model.HostID `json:"hostIds"`
}

// IDs returns a copy of the chain's hop IDs, or nil when the chain is nil or
// empty. It makes an optional chain safe to read without a nil check.
func (c *HostChain) IDs() []model.HostID {
	if c == nil || len(c.HostIDs) == 0 {
		return nil
	}
	return append([]model.HostID(nil), c.HostIDs...)
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
	// Transport selects the Session transport for an SSH Host: empty is plain
	// SSH, "mosh" and "et" are the SSH-based transports. It is empty for every
	// other protocol. Mosh and ET were never Host protocol IDs, so no record
	// written by an older build is mapped onto this field.
	Transport Transport `json:"transport,omitempty"`
	// Group is the Host Group path, "" for the Vault root. It is derived from
	// the group tree when a Host is read or written to a portable payload;
	// storage keeps only GroupID. A legacy record written before IDs existed
	// may still carry a path without one.
	Group string `json:"group"`
	// GroupID is the Host's only stored membership: the stable identity of the
	// group it belongs to. It is empty for the Vault root. A rename or a move
	// keeps the ID, so the member follows the group without being rewritten.
	GroupID string   `json:"groupId,omitempty"`
	Tags    []string `json:"tags"`
	Notes   string   `json:"notes,omitempty"`
	Pinned  bool     `json:"pinned"`
	// StartupCommand is typed once after a Session to this Host is ready and
	// replayed after every reconnect. Empty means none.
	StartupCommand string `json:"startupCommand,omitempty"`
	// HasPassword reports that an encrypted password exists for this Host.
	// The ciphertext lives in secret_values, never in this record.
	HasPassword bool `json:"hasPassword,omitempty"`
	// HasVNCPassword reports that a VNC password exists in its own secret
	// slot. It is independent of HasPassword: VNC never reads or writes the
	// SSH password slot.
	HasVNCPassword bool `json:"hasVncPassword,omitempty"`
	// VNC is the viewer configuration of a VNC Host. It is nil for every
	// other protocol.
	VNC *VNCConfig `json:"vnc,omitempty"`
	// HasTOTP reports that an encrypted TOTP authenticator URI exists for this
	// Host. As with HasPassword, only the marker lives on the record.
	HasTOTP bool `json:"hasTotp,omitempty"`
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
	// is off by default and requires X11WarningAcknowledged. When
	// X11ForwardingSet is false the value is unset and the Host Group's X11
	// default fills it in.
	X11Forwarding bool `json:"x11Forwarding,omitempty"`
	// X11WarningAcknowledged records that the user accepted the X11 security
	// warning. It is meaningful only while X11Forwarding is true.
	X11WarningAcknowledged bool `json:"x11WarningAcknowledged,omitempty"`
	// X11ForwardingSet records that the user made an explicit X11 choice on
	// this Host. It makes an explicit off distinguishable from "inherit the
	// group's value", which a plain false cannot express once groups have an
	// X11 default. A true X11Forwarding implies ownership even without it, so
	// records written before group inheritance keep working.
	X11ForwardingSet bool `json:"x11ForwardingSet,omitempty"`
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
	// DirectProxy forces a direct connection and stops Host Group and app-wide
	// proxy inheritance. It wins over a manual Proxy and a ProxyProfileID; a
	// zero value means inherit.
	DirectProxy bool `json:"directProxy,omitempty"`
	// NetworkProfileID selects a saved Network Profile for the first endpoint
	// of this Host's route. An empty reference inherits the Host Group's
	// selection; with no group default the Host has no Network Route and
	// connects exactly as before.
	NetworkProfileID string `json:"networkProfileId,omitempty"`
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
	// AutoAttachTmux keeps an SSH Host's remote shell alive in a deterministic
	// tmux session. It is opt-in, off by default, and meaningful only for
	// ProtocolSSH: another protocol and network-device mode clear it.
	AutoAttachTmux bool `json:"autoAttachTmux,omitempty"`
	// NewlineMode, BackspaceMode, and LineEditingMode translate typed Enter
	// and Backspace keys locally before the Session writes them. The zero
	// value of each is today's behavior, so an old record needs no migration.
	// They are meaningful only for ProtocolSSH; another protocol clears them.
	NewlineMode     modelterminal.NewlineMode     `json:"newlineBehavior,omitempty"`
	BackspaceMode   modelterminal.BackspaceMode   `json:"backspaceBehavior,omitempty"`
	LineEditingMode modelterminal.LineEditingMode `json:"lineEditingBehavior,omitempty"`
	// MoshServerPath overrides the remote mosh-server binary for an SSH Host
	// with TransportMosh. Empty uses the remote PATH. It is kept only for the
	// Mosh transport.
	MoshServerPath string `json:"moshServerPath,omitempty"`
	// ETPort is the EternalTerminal server port for an SSH Host with
	// TransportET. Zero means the 2022 default. It is kept only for the ET
	// transport.
	ETPort int `json:"etPort,omitempty"`
	// LastConnectedAt records when the user last opened a Session to this
	// Host, in Unix milliseconds. It is device-local telemetry: sync strips it
	// exactly as the original's sanitizeHostsForSync does, so another device's
	// clock never reorders this one's Recently Connected list. Zero means the
	// Host has never been connected. The integer form matches the original's
	// payload, so an upstream export imports without conversion.
	LastConnectedAt int64     `json:"lastConnectedAt,omitempty"`
	OrderKey        int64     `json:"orderKey"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// Draft is the editable part of a Host: what a create or edit form supplies.
// The application layer owns the identity and timestamps.
type Draft struct {
	Label    string
	Hostname string
	Port     int
	Username string
	Protocol Protocol
	// Transport selects the SSH Session transport. Empty is plain SSH; mosh
	// and ET are the SSH-based transports. Every other protocol clears it.
	Transport Transport
	Group     string
	Tags      []string
	Notes     string
	Pinned    bool
	// StartupCommand is typed after the shell is ready and after every
	// reconnect. Empty means none.
	StartupCommand string
	// IdentityID, KeyID, AuthMethod, UseSSHAgent, and RequiresMFA select how
	// the Host authenticates. They carry references only, never material.
	IdentityID  model.IdentityID
	KeyID       model.KeyID
	AuthMethod  HostAuthMethod
	UseSSHAgent bool
	RequiresMFA bool
	// X11Forwarding and X11WarningAcknowledged enable X11 forwarding. The
	// acknowledgement must be set before the preference can be saved.
	// X11ForwardingSet records an explicit off, which must override a Host
	// Group default instead of inheriting it.
	X11Forwarding          bool
	X11WarningAcknowledged bool
	X11ForwardingSet       bool
	// HostChain and Proxy configure the route to this Host. Proxy.Password is
	// plaintext form input; the Vault encrypts it into the field-secret store.
	HostChain *HostChain
	Proxy     *ProxyConfig
	// ProxyProfileID selects a saved Proxy Profile. It is a soft reference:
	// deleting a profile clears it on every Host and Host Group, but a record
	// written by an older version may still hold an unknown identifier.
	ProxyProfileID string
	// DirectProxy forces a direct connection and stops inheritance. It is
	// cleared with the proxy selection it overrides.
	DirectProxy bool
	// NetworkProfileID selects a saved Network Profile. It is a soft reference
	// resolved like ProxyProfileID: a Host's own value wins over its Host
	// Group's default, and deleting the profile clears both.
	NetworkProfileID string
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
	// AutoAttachTmux keeps the SSH login shell in a deterministic tmux
	// session. It is cleared for another protocol and for network-device mode.
	AutoAttachTmux bool
	// NewlineMode, BackspaceMode, and LineEditingMode translate typed Enter
	// and Backspace keys. Each zero value preserves today's bytes.
	NewlineMode     modelterminal.NewlineMode
	BackspaceMode   modelterminal.BackspaceMode
	LineEditingMode modelterminal.LineEditingMode
	// MoshServerPath overrides the remote mosh-server binary. It is kept only
	// for the Mosh transport.
	MoshServerPath string
	// ETPort is the EternalTerminal server port. Zero means the default and is
	// kept only for the ET transport.
	ETPort int
	// VNC is the viewer configuration. It is normalized for a VNC Host and
	// cleared for every other protocol.
	VNC *VNCConfig
	// Password is plaintext form input. The Vault application encrypts it
	// into the field-secret store; it is never copied onto Host or written
	// to the JSON tail. An empty value on Update keeps the stored password.
	Password string
	// VNCPassword is plaintext form input for a VNC Host's own password. It is
	// stored under a dedicated field-secret slot and is never migrated to or
	// from Password. An empty value on Update keeps the stored VNC password.
	VNCPassword string
	// TOTP is plaintext form input: an otpauth://totp URI or a raw Base32
	// secret. NormalizeDraft canonicalizes it to a URI; the Vault encrypts it
	// into the field-secret store. An empty value on Update keeps the stored
	// authenticator unless ClearTOTP is set.
	TOTP string
	// ClearTOTP explicitly removes a stored TOTP authenticator. It wins over a
	// submitted TOTP value.
	ClearTOTP bool
}

// Field names the part of a Host a validation error concerns.
type Field string

const (
	FieldLabel       Field = "label"
	FieldHostname    Field = "hostname"
	FieldPort        Field = "port"
	FieldUsername    Field = "username"
	FieldProtocol    Field = "protocol"
	FieldTransport   Field = "transport"
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
	// FieldTOTP names the Host's TOTP authenticator input.
	FieldTOTP  Field = "totp"
	FieldChain Field = "chain"
	FieldProxy Field = "proxy"
	// FieldNetworkProfile names a Network Profile configuration. A Network
	// Profile editor has one form, so its Kind-specific rules share one field.
	FieldNetworkProfile Field = "network_profile"
	FieldAlgorithms     Field = "algorithms"
	FieldX11            Field = "x11"
	// FieldFileProtocol and FieldSFTPSudo name the remote-pane protocol
	// preference.
	FieldFileProtocol Field = "file_protocol"
	FieldSFTPSudo     Field = "sftp_sudo"
	// FieldMoshServerPath and FieldETPort name the Mosh and EternalTerminal
	// settings a validation error concerns.
	FieldMoshServerPath Field = "mosh_server_path"
	FieldETPort         Field = "et_port"
	// FieldStartupCommand names the command replayed on every connection.
	FieldStartupCommand Field = "startup_command"
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
	// Canonicalize the pair before any other rule runs: a blank protocol is
	// SSH, the input "ssh" transport is plain SSH, and a non-SSH protocol
	// clears the transport. Removed protocol IDs are preserved for validation
	// to reject; there is no compatibility mapping.
	draft.Protocol, draft.Transport = normalizeProtocolTransport(draft.Protocol, draft.Transport)
	capabilities := capabilitiesFor(draft.Protocol, draft.Transport)
	plainSSH := draft.Protocol == ProtocolSSH && draft.Transport == TransportPlain
	draft.AuthMethod = HostAuthMethod(strings.ToLower(strings.TrimSpace(string(draft.AuthMethod))))
	if draft.AuthMethod == "" {
		draft.AuthMethod = HostAuthAuto
	}
	draft.SFTPFileProtocol = FileProtocol(strings.ToLower(strings.TrimSpace(string(draft.SFTPFileProtocol))))
	if draft.SFTPFileProtocol == "" {
		draft.SFTPFileProtocol = FileProtocolAuto
	}
	draft.DeviceType = NormalizeDeviceType(draft.DeviceType)
	if !plainSSH {
		// Raw network-device execution needs a plain SSH PTY; Mosh, ET, and
		// another protocol must not keep a mode their transport cannot run.
		draft.DeviceType = DeviceTypeDefault
	}
	if !plainSSH || draft.DeviceType == DeviceTypeNetwork {
		// tmux wraps an interactive POSIX login shell. A non-plain-SSH
		// transport and a vendor CLI have no such shell, so a hidden control
		// must not leave the preference on the Host record.
		draft.AutoAttachTmux = false
	}
	draft.NewlineMode = modelterminal.NormalizeNewlineMode(draft.NewlineMode)
	draft.BackspaceMode = modelterminal.NormalizeBackspaceMode(draft.BackspaceMode)
	draft.LineEditingMode = modelterminal.NormalizeLineEditingMode(draft.LineEditingMode)
	if !plainSSH {
		// The translation is defined for plain SSH keyboard input. Mosh, ET,
		// Telnet, and VNC keep their own input behavior, so a Host that
		// switched transport must not carry a mode its Session never applies.
		draft.NewlineMode = modelterminal.NewlineCR
		draft.BackspaceMode = modelterminal.BackspaceDEL
		draft.LineEditingMode = modelterminal.LineEditingCharacter
	}
	draft.MoshServerPath = strings.TrimSpace(draft.MoshServerPath)
	if draft.Transport != TransportMosh {
		// A server path and an ET port belong to one transport. Keeping them
		// on another Host would surface a setting its transport never reads.
		draft.MoshServerPath = ""
	}
	if draft.Transport != TransportET {
		draft.ETPort = 0
	}
	if draft.Protocol == ProtocolVNC {
		// A new VNC Host starts from the viewer defaults, and an unknown enum
		// from an older or hand-edited record falls back to the same values.
		draft.VNC = NormalizeVNCConfig(draft.VNC)
		// VNC authenticates with its own password slot. The SSH password slot
		// is never written for a VNC Host, even when a switched form still
		// holds a value the hidden control no longer shows.
		draft.Password = ""
	} else {
		// A hidden VNC section must not leave configuration or a password on
		// another protocol.
		draft.VNC = nil
		draft.VNCPassword = ""
	}
	if !capabilities.SSHAuth {
		// Telnet and VNC never read the SSH credential bundle. Clear it here as
		// well as in the editor: a Host saved from a legacy or imported draft
		// must not keep credentials whose controls it hides.
		draft.IdentityID = ""
		draft.KeyID = ""
		draft.AuthMethod = HostAuthAuto
		draft.UseSSHAgent = false
		draft.RequiresMFA = false
		// A TOTP authenticator answers SSH keyboard-interactive prompts. A
		// protocol without SSH auth must not save one, and a stored value is
		// left alone unless the draft explicitly clears it.
		draft.TOTP = ""
		draft.ClearTOTP = false
	}
	// Jump chains, proxies, and algorithm overrides describe the route to the
	// Host, not the protocol spoken to it. Telnet keeps them because SSH jumps
	// can carry a Telnet stream. X11 and the file-transfer preference, by
	// contrast, are target-side SSH features: a Telnet target has neither an
	// SSH connection nor an SFTP subsystem, so the values would be unsaveable
	// dead state.
	if !capabilities.X11 {
		draft.X11Forwarding = false
		draft.X11WarningAcknowledged = false
		// X11ForwardingSet records that the user made an explicit choice. On a
		// protocol or transport without X11 no such choice is visible, so the
		// value must not survive to suppress a group default later.
		draft.X11ForwardingSet = false
	}
	if !capabilities.FileTransfer {
		draft.SFTPFileProtocol = FileProtocolAuto
		draft.SFTPSudo = false
	}
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Hostname = strings.TrimSpace(draft.Hostname)
	draft.Username = strings.TrimSpace(draft.Username)
	draft.Group = NormalizeGroup(draft.Group)
	draft.Notes = strings.TrimSpace(draft.Notes)
	draft.StartupCommand = strings.TrimSpace(draft.StartupCommand)
	draft.Tags = normalizeTags(draft.Tags)
	normalizedTOTP, err := normalizeTOTPDraft(draft)
	if err != nil {
		return Draft{}, err
	}
	draft.TOTP = normalizedTOTP
	draft.HostChain = normalizeHostChain(draft.HostChain)
	draft.Proxy = normalizeProxy(draft.Proxy)
	draft.ProxyProfileID = strings.TrimSpace(draft.ProxyProfileID)
	if draft.DirectProxy {
		// An explicit direct choice is not a configuration: keeping a manual
		// proxy or profile here would make the editor show two selections.
		draft.Proxy = nil
		draft.ProxyProfileID = ""
	}
	draft.NetworkProfileID = strings.TrimSpace(draft.NetworkProfileID)
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

// NormalizeHost canonicalizes the protocol and transport of a Host record read
// from storage, a sync payload, or an import. It is deliberately narrow: it
// does not re-run the draft rules, so editing an unrelated field never silently
// changes a stored record. Unknown values are preserved for validation to
// reject; there is no legacy protocol mapping.
func NormalizeHost(host Host) Host {
	host.Protocol, host.Transport = normalizeProtocolTransport(host.Protocol, host.Transport)
	return host
}

// normalizeTOTPDraft canonicalizes the TOTP form input. An empty field keeps
// any stored authenticator, and ClearTOTP wins over a submitted value.
func normalizeTOTPDraft(draft Draft) (string, error) {
	if draft.ClearTOTP {
		return "", nil
	}
	input := strings.TrimSpace(draft.TOTP)
	if input == "" {
		return "", nil
	}
	canonical, err := NormalizeTOTP(input)
	if err != nil {
		return "", &ValidationError{Field: FieldTOTP, Rule: RuleInvalid}
	}
	return canonical, nil
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
// the same protocol, SSH transport, normalized address, recorded port,
// username, and Host Group would connect to the same endpoint. The recorded
// port is compared, not the effective one: only the Host Group can resolve an
// empty port, so a blank port and an explicit protocol default are different
// endpoints when the group supplies its own port.
func (h Host) MergeKey() string {
	protocol, transport := normalizeProtocolTransport(h.Protocol, h.Transport)
	parts := []string{
		string(protocol),
		string(transport),
		strings.ToLower(h.Hostname),
		strconv.Itoa(h.Port),
		strings.ToLower(h.Username),
		NormalizeGroup(h.Group),
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
		Transport:              h.Transport,
		Group:                  h.Group,
		Tags:                   h.Tags,
		Notes:                  h.Notes,
		StartupCommand:         h.StartupCommand,
		AuthMethod:             h.AuthMethod,
		RequiresMFA:            h.RequiresMFA,
		UseSSHAgent:            h.UseSSHAgent,
		X11Forwarding:          h.X11Forwarding,
		X11WarningAcknowledged: h.X11WarningAcknowledged,
		X11ForwardingSet:       h.X11ForwardingSet,
		HostChain:              h.HostChain,
		Proxy:                  h.Proxy,
		ProxyProfileID:         h.ProxyProfileID,
		NetworkProfileID:       h.NetworkProfileID,
		Algorithms:             h.Algorithms,
		SFTPFileProtocol:       h.SFTPFileProtocol,
		SFTPSudo:               h.SFTPSudo,
		DeviceType:             h.DeviceType,
		AutoAttachTmux:         h.AutoAttachTmux,
		MoshServerPath:         h.MoshServerPath,
		ETPort:                 h.ETPort,
		VNC:                    CloneVNCConfig(h.VNC),
	})
}

// X11Enabled reports whether a saved Host may request X11 forwarding. Both the
// preference and the security acknowledgement must be present, so a record
// created before the warning existed never enables forwarding silently.
func (h Host) X11Enabled() bool {
	return h.Capabilities().X11 && h.X11Forwarding && h.X11WarningAcknowledged
}

// Capabilities resolves the Host's effective capability set. An SSH Host with
// a transport uses that transport's facts; the protocol name alone is never
// enough to decide a feature.
func (h Host) Capabilities() HostCapabilities {
	return capabilitiesFor(h.Protocol, h.Transport)
}

// UsesSSHAuth reports whether the Host authenticates with the shared SSH
// credential bundle.
func (h Host) UsesSSHAuth() bool { return h.Capabilities().SSHAuth }

// SupportsFileTransfer reports whether SFTP/SCP apply to the Host.
func (h Host) SupportsFileTransfer() bool { return h.Capabilities().FileTransfer }

// SupportsForwarding reports whether a Port Forwarding rule may target the
// Host.
func (h Host) SupportsForwarding() bool { return h.Capabilities().Forwarding }

// SupportsMonitoring reports whether remote metric sampling may target the
// Host.
func (h Host) SupportsMonitoring() bool { return h.Capabilities().Monitoring }

// SupportsProxy reports whether a jump chain, a proxy, and SSH algorithm
// overrides apply to the Host's route.
func (h Host) SupportsProxy() bool { return h.Capabilities().Proxy }

// X11 reports whether X11 forwarding applies to the Host.
func (h Host) X11Capability() bool { return h.Capabilities().X11 }

// Plaintext reports whether the Host's native transport sends credentials and
// input unencrypted.
func (h Host) Plaintext() bool { return h.Capabilities().Plaintext }

// SessionTransport names the Session transport the Host opens. An SSH Host
// with a transport returns that transport; every other supported protocol
// returns its own descriptor's transport. A protocol or transport this build
// does not support returns TransportUnsupported: it is never mapped to SSH,
// which would open a different endpoint than the record names.
func (h Host) SessionTransport() domainprotocol.Transport {
	protocol, transport := normalizeProtocolTransport(h.Protocol, h.Transport)
	if protocol == ProtocolSSH {
		switch transport {
		case TransportPlain:
			return domainprotocol.TransportSSH
		case TransportMosh, TransportET:
			return domainprotocol.Transport(transport)
		default:
			return domainprotocol.TransportUnsupported
		}
	}
	descriptor, ok := protocol.descriptor()
	if !ok {
		return domainprotocol.TransportUnsupported
	}
	return descriptor.Transport
}

// ConnectionSupported reports whether this Host's protocol and transport are
// ones this build can open. A removed protocol ID or an unknown transport is
// unsupported; it must be rejected rather than reinterpreted as SSH.
func (h Host) ConnectionSupported() bool {
	protocol, transport := normalizeProtocolTransport(h.Protocol, h.Transport)
	if _, ok := protocol.descriptor(); !ok {
		return false
	}
	if protocol != ProtocolSSH {
		// NormalizeTransport has already dropped a transport from a non-SSH
		// protocol, so there is nothing else to validate.
		return true
	}
	switch transport {
	case TransportPlain, TransportMosh, TransportET:
		return true
	default:
		return false
	}
}

// TransportLabel reports the SSH transport a Host should be presented with,
// or the empty string for a non-SSH Host. The Vault list keeps Mosh and ET
// visible instead of collapsing them into "ssh".
func (h Host) TransportLabel() Transport {
	protocol, transport := normalizeProtocolTransport(h.Protocol, h.Transport)
	if protocol != ProtocolSSH {
		return TransportPlain
	}
	switch transport {
	case TransportPlain, TransportMosh, TransportET:
		return transport
	default:
		return TransportPlain
	}
}

// IsNetworkDevice reports whether the Host runs a vendor CLI over plain SSH
// and therefore needs raw execution. Mosh and ET have their own session
// layer, so the mode is not usable there.
func (h Host) IsNetworkDevice() bool {
	return h.Protocol == ProtocolSSH && h.Transport == TransportPlain && h.DeviceType == DeviceTypeNetwork
}

// AutoAttachTmuxEnabled reports whether opening this Host should start the
// tmux wrapper: the user opted in on a plain SSH Host. Network-device mode
// speaks a vendor CLI, and mosh/ET have their own session persistence.
func (h Host) AutoAttachTmuxEnabled() bool {
	return h.AutoAttachTmux && h.Protocol == ProtocolSSH &&
		h.Transport == TransportPlain && !h.IsNetworkDevice()
}

// InputCompatibility reports the Enter/Backspace translation this Host
// selects. Only a plain SSH Host has one; every other transport returns the
// default, which leaves input byte-identical to today. An unknown stored value
// also falls back to the default rather than changing the bytes sent.
func (h Host) InputCompatibility() modelterminal.InputCompatibility {
	if h.Protocol != ProtocolSSH || h.Transport != TransportPlain {
		return modelterminal.InputCompatibility{}
	}
	return modelterminal.InputCompatibility{
		Newline:     h.NewlineMode,
		Backspace:   h.BackspaceMode,
		LineEditing: h.LineEditingMode,
	}.Normalize()
}

// Capabilities resolves the effective capability set of a Draft the same way a
// saved Host does, so an editor call site never compares protocol names.
func (d Draft) Capabilities() HostCapabilities {
	return capabilitiesFor(d.Protocol, d.Transport)
}

// UsesSSHAuth reports whether the draft authenticates with the shared SSH
// credential bundle.
func (d Draft) UsesSSHAuth() bool { return d.Capabilities().SSHAuth }

// SupportsFileTransfer reports whether SFTP/SCP apply to the draft.
func (d Draft) SupportsFileTransfer() bool { return d.Capabilities().FileTransfer }

// SupportsForwarding reports whether a Port Forwarding rule may target the
// draft's Host.
func (d Draft) SupportsForwarding() bool { return d.Capabilities().Forwarding }

// SupportsMonitoring reports whether remote metric sampling may target the
// draft's Host.
func (d Draft) SupportsMonitoring() bool { return d.Capabilities().Monitoring }

// SupportsProxy reports whether the route controls apply to the draft.
func (d Draft) SupportsProxy() bool { return d.Capabilities().Proxy }

// SupportsX11 reports whether X11 forwarding applies to the draft.
func (d Draft) SupportsX11() bool { return d.Capabilities().X11 }

// Plaintext reports whether the draft's native transport sends credentials
// and input unencrypted.
func (d Draft) Plaintext() bool { return d.Capabilities().Plaintext }

func validateFields(draft Draft) error {
	protocol, transport := normalizeProtocolTransport(draft.Protocol, draft.Transport)
	if !domainprotocol.ValidHostID(domainprotocol.ID(protocol)) {
		return validation(FieldProtocol, RuleUnsupported)
	}
	switch transport {
	case TransportPlain, TransportMosh, TransportET:
	default:
		return validation(FieldTransport, RuleUnsupported)
	}
	// A transport is meaningful only for SSH. normalizeProtocolTransport has
	// already cleared it for every other protocol, so an input pair such as
	// Telnet+Mosh validates as Telnet rather than saving dead state.
	capabilities := capabilitiesFor(protocol, transport)

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
	if !capabilities.FileTransfer {
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
		if !capabilities.X11 {
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
	if protocol == ProtocolVNC && draft.Port != 0 {
		// Display-number input is an editor presentation, but a Host that
		// records it can only describe a display in the 5900-5999 range.
		// A raw port that falls outside it must switch input mode first.
		displayMode := VNCDisplayNumber
		if draft.VNC != nil {
			displayMode = NormalizeDisplayMode(draft.VNC.DisplayMode)
		}
		if _, ok := VNCDisplayForPort(draft.Port); !ok && displayMode == VNCDisplayNumber {
			return validation(FieldPort, RuleOutOfRange)
		}
	}

	if err := validateText(FieldUsername, draft.Username, maxUsernameLength, false); err != nil {
		return err
	}

	if transport == TransportMosh {
		if err := validateText(FieldMoshServerPath, draft.MoshServerPath, maxMoshServerPathLength, false); err != nil {
			return err
		}
	}
	if transport == TransportET && draft.ETPort != 0 && (draft.ETPort < 1 || draft.ETPort > 65535) {
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
	if len(draft.StartupCommand) > maxStartupCommandLength {
		return validation(FieldStartupCommand, RuleTooLong)
	}
	if strings.ContainsRune(draft.StartupCommand, 0) {
		return validation(FieldStartupCommand, RuleInvalid)
	}

	if err := validateHostChain(draft.HostChain); err != nil {
		return err
	}

	if err := validateProxy(draft.Proxy); err != nil {
		return err
	}
	if err := validateText(FieldProxy, draft.ProxyProfileID, maxLabelLength, false); err != nil {
		return err
	}
	if err := validateText(FieldNetworkProfile, draft.NetworkProfileID, maxLabelLength, false); err != nil {
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

// validateHostChain rejects a chain a save cannot keep: more hops than a
// session supports, or a blank or duplicate hop. It is shared by the Host and
// Host Group drafts so both editors agree.
func validateHostChain(chain *HostChain) error {
	if chain == nil {
		return nil
	}
	if len(chain.HostIDs) > maxHostChainLength {
		return validation(FieldChain, RuleTooLong)
	}
	seen := make(map[model.HostID]bool, len(chain.HostIDs))
	for _, id := range chain.HostIDs {
		if strings.TrimSpace(string(id)) == "" || seen[id] {
			return validation(FieldChain, RuleInvalid)
		}
		seen[id] = true
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
	if descriptor, ok := protocol.descriptor(); ok {
		return descriptor.DefaultPort
	}
	return 22
}

// EffectivePort returns the port a connection uses. A Host with a port of its
// own wins; a zero port means it inherits its Host Group's port, and when no
// group sets one the protocol's default applies (22 for SSH, 23 for Telnet,
// 5900 for VNC).
// The Vault stores the zero so the inheritance survives a group change.
func EffectivePort(protocol Protocol, port int) int {
	if port != 0 {
		return port
	}
	return defaultPort(protocol)
}

// descriptor resolves the built-in protocol facts. A blank protocol is the
// SSH default the Vault applies when a Host is saved without one, so it
// resolves as SSH; an unknown persisted value has no descriptor and callers
// treat that as no capability.
func (p Protocol) descriptor() (domainprotocol.Descriptor, bool) {
	if p == "" {
		return domainprotocol.Resolve(domainprotocol.SSH)
	}
	return domainprotocol.Resolve(domainprotocol.ID(p))
}

// SessionTransport names the Session transport the protocol opens. The value
// matches application/session.Kind. An unknown protocol returns
// TransportUnsupported instead of falling back to SSH.
func (p Protocol) SessionTransport() domainprotocol.Transport {
	if descriptor, ok := p.descriptor(); ok {
		return descriptor.Transport
	}
	return domainprotocol.TransportUnsupported
}

// UsesSSHAuth reports whether a protocol authenticates with the shared SSH
// credential settings (Identity, SSH Key, Method, agent, and MFA). Only SSH
// does; Telnet and VNC do not, so their editors must not show those controls.
// A Host with an SSH transport resolves its effective capability from
// Capabilities instead.
func (p Protocol) UsesSSHAuth() bool {
	descriptor, ok := p.descriptor()
	return ok && descriptor.SSHAuth
}

// SupportsFileTransfer reports whether SFTP/SCP apply to the Host.
func (p Protocol) SupportsFileTransfer() bool {
	descriptor, ok := p.descriptor()
	return ok && descriptor.FileTransfer
}

// SupportsForwarding reports whether a Port Forwarding rule may target the
// Host. A Telnet Host has no SSH connection to carry a forwarding channel.
func (p Protocol) SupportsForwarding() bool {
	descriptor, ok := p.descriptor()
	return ok && descriptor.Forwarding
}

// SupportsMonitoring reports whether remote metric sampling may target the
// Host.
func (p Protocol) SupportsMonitoring() bool {
	descriptor, ok := p.descriptor()
	return ok && descriptor.Monitoring
}

// SupportsProxy reports whether a jump chain, a proxy, and SSH algorithm
// overrides apply to the Host's route. Telnet supports them even though its
// target has no SSH handshake: the SSH hops carry the route.
func (p Protocol) SupportsProxy() bool {
	descriptor, ok := p.descriptor()
	return ok && descriptor.Proxy
}

// X11 reports whether X11 forwarding applies to the Host. X11 is forwarded
// over the target's SSH connection, so only SSH qualifies.
func (p Protocol) X11() bool {
	descriptor, ok := p.descriptor()
	return ok && descriptor.X11
}

// Plaintext reports whether the protocol's native transport sends credentials
// and input unencrypted. The Vault editor uses this capability, not a protocol
// name comparison, to disclose the exposure.
func (p Protocol) Plaintext() bool {
	descriptor, ok := p.descriptor()
	return ok && descriptor.Plaintext
}

func validation(field Field, rule Rule) error {
	return &ValidationError{Field: field, Rule: rule}
}
