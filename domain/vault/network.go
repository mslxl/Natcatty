package vault

import (
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NetworkProfileKind selects the virtual network technology a Network Profile
// joins. It is stored on the profile so a record written by another build
// stays readable even when this build cannot run it.
type NetworkProfileKind string

const (
	// NetworkKindTailscale joins a tailnet with an in-process tsnet node.
	NetworkKindTailscale NetworkProfileKind = "tailscale"
	// NetworkKindWireGuard joins a WireGuard network with an in-process
	// userspace netstack.
	NetworkKindWireGuard NetworkProfileKind = "wireguard"
)

// DefaultWireGuardMTU is the tunnel MTU a WireGuard profile uses when the
// editor leaves MTU unset. It matches wg-quick's default.
const DefaultWireGuardMTU = 1420

// Limits keep a malformed Network Profile from entering the Vault. They are
// generous enough for real split-tunnel setups and small enough to catch a
// pasted file.
const (
	maxNetworkAddressCount   = 64
	maxNetworkDNSCount       = 32
	maxNetworkAllowedIPCount = 4096
	maxNetworkPeerCount      = 256
	maxNetworkPeerIDLength   = 64
	maxNetworkTags           = maxTagCount
	// WireGuard keys are 32 bytes; the standard textual form is unpadded
	// base64 of 44 characters, and hex of 64 characters is accepted on input.
	wireGuardKeyBase64Length = 44
	wireGuardKeyHexLength    = 64
	wireGuardKeyBytes        = 32
)

// NetworkProfile is a saved configuration for joining a virtual network. It is
// a Vault record of its own; a Host or Host Group holds only a soft reference.
// Secret material never appears here: Kind-specific configs carry Has* markers
// and the plaintext lives in the field-secret store.
type NetworkProfile struct {
	ID        string             `json:"id"`
	Label     string             `json:"label"`
	Kind      NetworkProfileKind `json:"kind"`
	Tailscale *TailscaleNetwork  `json:"tailscale,omitempty"`
	WireGuard *WireGuardNetwork  `json:"wireguard,omitempty"`
	// OrderKey orders profiles in the Vault, in steps of 1000.
	OrderKey  int64     `json:"orderKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TailscaleNetwork is a Tailscale Net profile's non-secret configuration. The
// Auth Key is form input only; the record carries HasAuthKey.
type TailscaleNetwork struct {
	Hostname   string   `json:"hostname,omitempty"`
	ControlURL string   `json:"controlUrl,omitempty"`
	Ephemeral  bool     `json:"ephemeral,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	HasAuthKey bool     `json:"hasAuthKey,omitempty"`
}

// WireGuardNetwork is a WireGuard Net profile's non-secret configuration. The
// Private Key and each peer's Preshared Key are form input only; the record
// carries HasPrivateKey and HasPresharedKey.
type WireGuardNetwork struct {
	Addresses     []string        `json:"addresses,omitempty"`
	DNS           []string        `json:"dns,omitempty"`
	MTU           int             `json:"mtu,omitempty"`
	Peers         []WireGuardPeer `json:"peers,omitempty"`
	HasPrivateKey bool            `json:"hasPrivateKey,omitempty"`
}

// WireGuardPeer is one peer in a WireGuard Net profile. ID is stable for the
// lifetime of the peer and names its Preshared Key in the field-secret store.
type WireGuardPeer struct {
	ID                  string   `json:"id"`
	PublicKey           string   `json:"publicKey"`
	Endpoint            string   `json:"endpoint,omitempty"`
	AllowedIPs          []string `json:"allowedIps,omitempty"`
	PersistentKeepalive int      `json:"persistentKeepalive,omitempty"`
	HasPresharedKey     bool     `json:"hasPresharedKey,omitempty"`
}

// NetworkProfileDraft is the editable part of a Network Profile, including
// credential plaintext that is handed to the field-secret store and never
// copied onto the profile record.
type NetworkProfileDraft struct {
	Label     string
	Kind      NetworkProfileKind
	Tailscale *TailscaleNetworkDraft
	WireGuard *WireGuardNetworkDraft
}

// TailscaleNetworkDraft is the editable Tailscale Net configuration. AuthKey
// is plaintext form input; an empty value on Update keeps the stored key.
type TailscaleNetworkDraft struct {
	Hostname   string
	AuthKey    string
	ControlURL string
	Ephemeral  bool
	Tags       []string
}

// WireGuardNetworkDraft is the editable WireGuard Net configuration.
// PrivateKey is plaintext form input; an empty value on Update keeps the
// stored key.
type WireGuardNetworkDraft struct {
	PrivateKey string
	Addresses  []string
	DNS        []string
	MTU        int
	Peers      []WireGuardPeerDraft
}

// WireGuardPeerDraft is one editable peer. An empty ID means a new peer and
// the service assigns a stable one; an existing ID is preserved so its
// Preshared Key stays addressed. PresharedKey is plaintext form input.
type WireGuardPeerDraft struct {
	ID                  string
	PublicKey           string
	PresharedKey        string
	Endpoint            string
	AllowedIPs          []string
	PersistentKeepalive int
}

// NetworkWarning identifies a non-blocking problem the editor must show. It is
// stable data, never display text.
type NetworkWarning string

// WarningControlURLInsecure reports a plain-http ControlURL. Plain http is
// accepted only for a local Headscale instance, and even then must be shown as
// a warning.
const WarningControlURLInsecure NetworkWarning = "control_url_insecure"

// NormalizeNetworkProfileDraft trims and validates a Network Profile draft. It
// returns a *ValidationError when the input cannot be saved.
func NormalizeNetworkProfileDraft(draft NetworkProfileDraft) (NetworkProfileDraft, error) {
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Kind = NetworkProfileKind(strings.ToLower(strings.TrimSpace(string(draft.Kind))))

	if draft.Label == "" {
		return NetworkProfileDraft{}, validation(FieldLabel, RuleRequired)
	}
	if err := validateText(FieldLabel, draft.Label, maxLabelLength, false); err != nil {
		return NetworkProfileDraft{}, err
	}

	switch draft.Kind {
	case NetworkKindTailscale:
		if draft.Tailscale == nil {
			return NetworkProfileDraft{}, validation(FieldNetworkProfile, RuleRequired)
		}
		if draft.WireGuard != nil {
			return NetworkProfileDraft{}, validation(FieldNetworkProfile, RuleInvalid)
		}
		normalized, err := normalizeTailscaleDraft(*draft.Tailscale)
		if err != nil {
			return NetworkProfileDraft{}, err
		}
		draft.Tailscale = &normalized
	case NetworkKindWireGuard:
		if draft.WireGuard == nil {
			return NetworkProfileDraft{}, validation(FieldNetworkProfile, RuleRequired)
		}
		if draft.Tailscale != nil {
			return NetworkProfileDraft{}, validation(FieldNetworkProfile, RuleInvalid)
		}
		normalized, err := normalizeWireGuardDraft(*draft.WireGuard)
		if err != nil {
			return NetworkProfileDraft{}, err
		}
		draft.WireGuard = &normalized
	default:
		return NetworkProfileDraft{}, validation(FieldNetworkProfile, RuleUnsupported)
	}
	return draft, nil
}

// NetworkProfileWarnings returns the non-blocking warnings a draft must show
// before it is saved.
func NetworkProfileWarnings(draft NetworkProfileDraft) []NetworkWarning {
	if draft.Kind != NetworkKindTailscale || draft.Tailscale == nil {
		return nil
	}
	controlURL := strings.TrimSpace(draft.Tailscale.ControlURL)
	if controlURL == "" {
		return nil
	}
	parsed, err := url.Parse(controlURL)
	if err != nil {
		return nil
	}
	// Only a local plain-http URL survives validation, so only that combination
	// is an accepted-but-insecure configuration worth warning about.
	if strings.EqualFold(parsed.Scheme, "http") && isLocalControlURLHost(parsed.Hostname()) {
		return []NetworkWarning{WarningControlURLInsecure}
	}
	return nil
}

func normalizeTailscaleDraft(draft TailscaleNetworkDraft) (TailscaleNetworkDraft, error) {
	draft.Hostname = strings.TrimSpace(draft.Hostname)
	if draft.Hostname != "" {
		if err := validateText(FieldHostname, draft.Hostname, maxHostnameLength, true); err != nil {
			return TailscaleNetworkDraft{}, err
		}
	}
	draft.AuthKey = strings.TrimSpace(draft.AuthKey)
	if err := validateText(FieldNetworkProfile, draft.AuthKey, maxKeyMaterialLength, false); err != nil {
		return TailscaleNetworkDraft{}, err
	}
	draft.ControlURL = strings.TrimSpace(draft.ControlURL)
	if draft.ControlURL != "" {
		if err := validateControlURL(draft.ControlURL); err != nil {
			return TailscaleNetworkDraft{}, err
		}
	}
	if len(draft.Tags) > maxNetworkTags {
		return TailscaleNetworkDraft{}, validation(FieldNetworkProfile, RuleTooLong)
	}
	draft.Tags = normalizeTags(draft.Tags)
	for _, tag := range draft.Tags {
		if err := validateText(FieldNetworkProfile, tag, maxTagLength, false); err != nil {
			return TailscaleNetworkDraft{}, err
		}
	}
	return draft, nil
}

// validateControlURL accepts an absolute https URL. A plain-http URL is
// accepted only when its host can reach nothing beyond the local machine or
// the local network segment; a remote control plane must use TLS.
func validateControlURL(value string) error {
	if hasControl(value) {
		return validation(FieldNetworkProfile, RuleInvalid)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return validation(FieldNetworkProfile, RuleInvalid)
	}
	switch {
	case strings.EqualFold(parsed.Scheme, "https"):
		return nil
	case strings.EqualFold(parsed.Scheme, "http"):
		if !isLocalControlURLHost(parsed.Hostname()) {
			return validation(FieldNetworkProfile, RuleInvalid)
		}
		return nil
	default:
		return validation(FieldNetworkProfile, RuleInvalid)
	}
}

// isLocalControlURLHost reports whether host names only loopback or local
// network endpoints. Hostnames are not resolved here, so a non-local name
// cannot qualify for plain http: DNS could point it anywhere. Localhost names
// and loopback, private, and link-local IP literals are specific enough to
// guarantee the traffic stays on the machine or the local segment.
func isLocalControlURLHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

func normalizeWireGuardDraft(draft WireGuardNetworkDraft) (WireGuardNetworkDraft, error) {
	key, ok := canonicalWireGuardKey(draft.PrivateKey)
	if draft.PrivateKey != "" && !ok {
		return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleInvalid)
	}
	draft.PrivateKey = key

	if len(draft.Addresses) == 0 {
		return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleRequired)
	}
	if len(draft.Addresses) > maxNetworkAddressCount {
		return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleTooLong)
	}
	addresses := make([]string, 0, len(draft.Addresses))
	for _, address := range draft.Addresses {
		normalized, ok := normalizeNetworkAddress(address)
		if !ok {
			return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleInvalid)
		}
		addresses = append(addresses, normalized)
	}
	draft.Addresses = addresses

	if len(draft.DNS) > maxNetworkDNSCount {
		return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleTooLong)
	}
	dns := make([]string, 0, len(draft.DNS))
	for _, server := range draft.DNS {
		address, err := netip.ParseAddr(strings.TrimSpace(server))
		if err != nil {
			return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleInvalid)
		}
		dns = append(dns, address.String())
	}
	draft.DNS = dns

	if draft.MTU == 0 {
		draft.MTU = DefaultWireGuardMTU
	}
	if draft.MTU < 576 || draft.MTU > 65535 {
		return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleOutOfRange)
	}

	if len(draft.Peers) == 0 {
		return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleRequired)
	}
	if len(draft.Peers) > maxNetworkPeerCount {
		return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleTooLong)
	}
	peers := make([]WireGuardPeerDraft, 0, len(draft.Peers))
	seenPublicKeys := make(map[string]bool, len(draft.Peers))
	seenIDs := make(map[string]bool, len(draft.Peers))
	for _, peer := range draft.Peers {
		normalized, err := normalizeWireGuardPeerDraft(peer)
		if err != nil {
			return WireGuardNetworkDraft{}, err
		}
		if seenPublicKeys[normalized.PublicKey] {
			return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleInvalid)
		}
		seenPublicKeys[normalized.PublicKey] = true
		if normalized.ID != "" {
			if seenIDs[normalized.ID] {
				return WireGuardNetworkDraft{}, validation(FieldNetworkProfile, RuleInvalid)
			}
			seenIDs[normalized.ID] = true
		}
		peers = append(peers, normalized)
	}
	draft.Peers = peers
	return draft, nil
}

func normalizeWireGuardPeerDraft(peer WireGuardPeerDraft) (WireGuardPeerDraft, error) {
	peer.ID = strings.TrimSpace(peer.ID)
	if err := validateText(FieldNetworkProfile, peer.ID, maxNetworkPeerIDLength, true); err != nil {
		return WireGuardPeerDraft{}, err
	}
	publicKey, ok := canonicalWireGuardKey(peer.PublicKey)
	if strings.TrimSpace(peer.PublicKey) == "" {
		return WireGuardPeerDraft{}, validation(FieldNetworkProfile, RuleRequired)
	}
	if !ok {
		return WireGuardPeerDraft{}, validation(FieldNetworkProfile, RuleInvalid)
	}
	peer.PublicKey = publicKey
	presharedKey, ok := canonicalWireGuardKey(peer.PresharedKey)
	if peer.PresharedKey != "" && !ok {
		return WireGuardPeerDraft{}, validation(FieldNetworkProfile, RuleInvalid)
	}
	peer.PresharedKey = presharedKey

	peer.Endpoint = strings.TrimSpace(peer.Endpoint)
	if peer.Endpoint != "" {
		if err := validateWireGuardEndpoint(peer.Endpoint); err != nil {
			return WireGuardPeerDraft{}, err
		}
	}

	if len(peer.AllowedIPs) > maxNetworkAllowedIPCount {
		return WireGuardPeerDraft{}, validation(FieldNetworkProfile, RuleTooLong)
	}
	allowed := make([]string, 0, len(peer.AllowedIPs))
	for _, prefix := range peer.AllowedIPs {
		parsed, err := netip.ParsePrefix(strings.TrimSpace(prefix))
		if err != nil {
			return WireGuardPeerDraft{}, validation(FieldNetworkProfile, RuleInvalid)
		}
		allowed = append(allowed, parsed.Masked().String())
	}
	peer.AllowedIPs = allowed

	if peer.PersistentKeepalive < 0 || peer.PersistentKeepalive > 65535 {
		return WireGuardPeerDraft{}, validation(FieldNetworkProfile, RuleOutOfRange)
	}
	return peer, nil
}

func validateWireGuardEndpoint(value string) error {
	if hasControl(value) {
		return validation(FieldNetworkProfile, RuleInvalid)
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(host) == "" {
		return validation(FieldNetworkProfile, RuleInvalid)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return validation(FieldNetworkProfile, RuleInvalid)
	}
	return nil
}

// normalizeNetworkAddress accepts an IP literal or a CIDR prefix and returns
// the canonical prefix form. The stored form keeps the prefix for display; the
// provider later passes only the host address to the netstack.
func normalizeNetworkAddress(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked().String(), true
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return "", false
	}
	return netip.PrefixFrom(address, address.BitLen()).String(), true
}

// canonicalWireGuardKey validates a 32-byte WireGuard key and returns its
// canonical base64 form. Empty input is not a key and returns false; callers
// that allow an absent key check for emptiness first.
func canonicalWireGuardKey(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if len(value) == wireGuardKeyHexLength {
		if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == wireGuardKeyBytes {
			return base64.StdEncoding.EncodeToString(decoded), true
		}
	}
	if len(value) != wireGuardKeyBase64Length {
		return "", false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != wireGuardKeyBytes {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(decoded), true
}
