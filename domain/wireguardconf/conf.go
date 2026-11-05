// Package wireguardconf parses wg-quick configuration text.
//
// It is a pure domain package: Parse reads only its argument, performs no DNS,
// executes nothing, writes no log, starts no goroutine, and touches no
// filesystem. The caller reads the file and enforces the file-size limit;
// Parse enforces every structural limit again so an oversized input cannot
// reach the normalizer through another caller.
//
// Parsing is strict about things the embedded userspace netstack cannot honor:
// shell hooks are refused instead of silently dropped, and fields that only
// affect the operating system's routing or firewall are validated, ignored,
// and reported as diagnostics.
package wireguardconf

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Defensive limits. They are not wg-quick semantics; they bound the work a
// pasted or downloaded file can cause.
const (
	// MaxFileBytes is the largest configuration Parse accepts.
	MaxFileBytes = 1 << 20 // 1 MiB
	// MaxLineBytes is the largest single line Parse accepts.
	MaxLineBytes = 64 << 10 // 64 KiB
	// MaxPeers bounds the number of [Peer] sections.
	MaxPeers = 256
	// MaxAddresses bounds the number of [Interface] Address entries.
	MaxAddresses = 64
	// MaxDNSServers bounds the number of DNS entries Parse accepts, including
	// search domains that are dropped with a warning. Bounding every entry keeps
	// a single line from producing an unbounded number of diagnostics.
	MaxDNSServers = 32
	// MaxAllowedIPs bounds one peer's AllowedIPs entries.
	MaxAllowedIPs = 4096
	// MaxEndpointHostBytes bounds an Endpoint host name.
	MaxEndpointHostBytes = 255
)

// Kind classifies a hard parse failure. The UI maps it to a localized message;
// it is stable data, never display text.
type Kind int

const (
	// ErrSyntax reports a malformed line, key, or value.
	ErrSyntax Kind = iota + 1
	// ErrUnknownKey reports a key that is unknown or not valid in its section.
	ErrUnknownKey
	// ErrUnknownSection reports a section other than [Interface] or [Peer].
	ErrUnknownSection
	// ErrMissingRequired reports a missing PrivateKey or peer PublicKey.
	ErrMissingRequired
	// ErrDuplicatePeer reports two peers with the same PublicKey.
	ErrDuplicatePeer
	// ErrUnsupported reports syntax that cannot be honored, such as shell
	// hooks or incremental AllowedIPs.
	ErrUnsupported
	// ErrLimit reports an exceeded defensive limit.
	ErrLimit
	// ErrEncoding reports text that is not valid UTF-8 or UTF-16.
	ErrEncoding
)

// String names one Kind for debugging and stable logging.
func (k Kind) String() string {
	switch k {
	case ErrSyntax:
		return "syntax"
	case ErrUnknownKey:
		return "unknown_key"
	case ErrUnknownSection:
		return "unknown_section"
	case ErrMissingRequired:
		return "missing_required"
	case ErrDuplicatePeer:
		return "duplicate_peer"
	case ErrUnsupported:
		return "unsupported"
	case ErrLimit:
		return "limit"
	case ErrEncoding:
		return "encoding"
	default:
		return "unknown"
	}
}

// ParseError is a hard configuration failure. Line is always 1-based so the
// UI can point at the source line; a file-level failure uses line 1. Field
// names the offending key, or a stable placeholder such as "file".
//
// Messages never contain a value, so PrivateKey and PresharedKey material can
// never reach a diagnostic, a log, or an error string.
type ParseError struct {
	Line  int
	Field string
	Kind  Kind
	Msg   string
}

// Error implements error. It never echoes a configuration value.
func (e *ParseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	line := e.Line
	if line < 1 {
		line = 1
	}
	field := e.Field
	if field == "" {
		field = "configuration"
	}
	message := e.Msg
	if message == "" {
		message = "invalid configuration"
	}
	return fmt.Sprintf("wireguard conf line %d (%s): %s", line, field, message)
}

// DiagnosticCode classifies a non-blocking finding. The UI localizes the code
// and never shows a parser message.
type DiagnosticCode string

const (
	// WarnIgnoredField reports FwMark, Table, or SaveConfig: valid syntax that
	// has no meaning for the in-process netstack.
	WarnIgnoredField DiagnosticCode = "ignored_field"
	// WarnSearchDomain reports a DNS entry that is not an IP address.
	WarnSearchDomain DiagnosticCode = "search_domain"
	// WarnHostBits reports AllowedIPs whose host bits were masked away.
	WarnHostBits DiagnosticCode = "host_bits"
	// WarnEmptyAllowedIPs reports a peer that will receive no traffic.
	WarnEmptyAllowedIPs DiagnosticCode = "empty_allowed_ips"
	// WarnDuplicateScalar reports a repeated scalar key whose last value wins.
	WarnDuplicateScalar DiagnosticCode = "duplicate_scalar"
	// WarnEmptyHook reports an empty hook value, which is a no-op.
	WarnEmptyHook DiagnosticCode = "empty_hook"
)

// Diagnostic is a non-blocking finding tied to a source line. Line is
// 1-based. It never contains a value.
type Diagnostic struct {
	Line  int
	Field string
	Code  DiagnosticCode
}

// Endpoint is an unparsed WireGuard endpoint. Host is the literal host name or
// IP address text from the file; resolving it is the Provider's job.
type Endpoint struct {
	Host string
	Port uint16
}

// String renders the canonical host:port form, bracketing an IPv6 host.
func (e Endpoint) String() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port)))
}

// Interface is the normalized [Interface] section.
type Interface struct {
	PrivateKey [32]byte
	// ListenPort is 0 when the file left it unset, meaning a random port.
	ListenPort uint16
	// Addresses keeps each prefix as written. A bare address is normalized to
	// a host prefix (/32 or /128); the netstack later uses only its host part.
	Addresses []netip.Prefix
	// DNS contains only IP servers. Search domains are reported and dropped.
	DNS []netip.Addr
	// MTU is 0 when the file left it unset, meaning DefaultMTU.
	MTU uint16
}

// Peer is one normalized [Peer] section.
type Peer struct {
	PublicKey    [32]byte
	PresharedKey *[32]byte
	// Endpoint is unparsed. Its zero value means the peer has no endpoint.
	Endpoint Endpoint
	// AllowedIPs is masked and in file order. Duplicates are preserved.
	AllowedIPs          []netip.Prefix
	PersistentKeepalive uint16
}

// Config is a fully normalized wg-quick configuration. Parse returns it only
// when the whole document is valid.
type Config struct {
	Interface Interface
	Peers     []Peer
}

// DefaultMTU is the tunnel MTU a missing [Interface] MTU means.
const DefaultMTU = 1420

// EncodeKey renders a 32-byte key in the standard base64 form wg-quick uses.
func EncodeKey(key [32]byte) string {
	return base64.StdEncoding.EncodeToString(key[:])
}

// Parse normalizes a wg-quick configuration. It returns a nil Config and nil
// diagnostics when any hard error is found; a failed parse never yields a
// partial result. Warnings are returned only with a successful Config.
func Parse(data []byte) (*Config, []Diagnostic, error) {
	if len(data) > MaxFileBytes {
		return nil, nil, limitError(1, "file", "file exceeds the 1 MiB import limit")
	}
	text, err := decodeText(data)
	if err != nil {
		return nil, nil, err
	}
	p := &parser{interfaceSeenKeys: make(map[string]bool)}
	if err := p.run(text); err != nil {
		return nil, nil, err
	}
	config, finishErr := p.finish()
	if finishErr != nil {
		return nil, nil, finishErr
	}
	return config, p.diagnostics, nil
}

type parser struct {
	config            Config
	diagnostics       []Diagnostic
	interfaceSeen     bool
	privateKeySet     bool
	interfaceSeenKeys map[string]bool
	dnsEntries        int
	peers             []*peerState
	peer              *peerState
}

type peerState struct {
	peer          Peer
	line          int
	publicKeyLine int
	hasPublicKey  bool
	seenKeys      map[string]bool
}

func (p *parser) run(text string) error {
	lines := strings.Split(text, "\n")
	for index, raw := range lines {
		lineNo := index + 1
		line := strings.TrimSuffix(raw, "\r")
		if len(line) > MaxLineBytes {
			return limitError(lineNo, "line", "line exceeds the 64 KiB import limit")
		}
		if comment := strings.IndexByte(line, '#'); comment >= 0 {
			line = line[:comment]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if err := p.section(line, lineNo); err != nil {
				return err
			}
			continue
		}
		if err := p.keyValue(line, lineNo); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) section(line string, lineNo int) error {
	if !strings.HasSuffix(line, "]") {
		return syntaxError(lineNo, "section", "section header must end with ]")
	}
	name := strings.TrimSpace(line[1 : len(line)-1])
	switch strings.ToLower(name) {
	case "interface":
		p.interfaceSeen = true
		p.peer = nil
		return nil
	case "peer":
		if len(p.peers) >= MaxPeers {
			return limitError(lineNo, "Peer", "too many [Peer] sections")
		}
		p.peer = &peerState{line: lineNo, seenKeys: make(map[string]bool)}
		p.peers = append(p.peers, p.peer)
		return nil
	default:
		return &ParseError{Line: lineNo, Field: "section", Kind: ErrUnknownSection,
			Msg: "unknown section"}
	}
}

func (p *parser) keyValue(line string, lineNo int) error {
	equals := strings.IndexByte(line, '=')
	if equals < 0 {
		return syntaxError(lineNo, "key", "expected key = value")
	}
	key := strings.TrimSpace(line[:equals])
	value := strings.TrimSpace(line[equals+1:])
	if key == "" {
		return syntaxError(lineNo, "key", "key is empty")
	}
	if strings.IndexFunc(key, unicode.IsSpace) >= 0 {
		return syntaxError(lineNo, "key", "key contains whitespace")
	}
	canonical, known := canonicalKey(key)
	if !known {
		return &ParseError{Line: lineNo, Field: key, Kind: ErrUnknownKey,
			Msg: "unknown key"}
	}
	if p.peer != nil {
		if !peerField(canonical) {
			return &ParseError{Line: lineNo, Field: canonical, Kind: ErrUnknownKey,
				Msg: "key is not valid in [Peer]"}
		}
		return p.peerKey(canonical, value, lineNo)
	}
	if !p.interfaceSeen {
		return syntaxError(lineNo, canonical, "key appears outside a section")
	}
	if !interfaceField(canonical) {
		return &ParseError{Line: lineNo, Field: canonical, Kind: ErrUnknownKey,
			Msg: "key is not valid in [Interface]"}
	}
	return p.interfaceKey(canonical, value, lineNo)
}

func canonicalKey(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "privatekey":
		return "PrivateKey", true
	case "listenport":
		return "ListenPort", true
	case "fwmark":
		return "FwMark", true
	case "table":
		return "Table", true
	case "saveconfig":
		return "SaveConfig", true
	case "preup":
		return "PreUp", true
	case "postup":
		return "PostUp", true
	case "predown":
		return "PreDown", true
	case "postdown":
		return "PostDown", true
	case "address":
		return "Address", true
	case "dns":
		return "DNS", true
	case "mtu":
		return "MTU", true
	case "publickey":
		return "PublicKey", true
	case "presharedkey":
		return "PresharedKey", true
	case "allowedips":
		return "AllowedIPs", true
	case "endpoint":
		return "Endpoint", true
	case "persistentkeepalive":
		return "PersistentKeepalive", true
	default:
		return "", false
	}
}

func interfaceField(name string) bool {
	switch name {
	case "PrivateKey", "ListenPort", "FwMark", "Table", "SaveConfig",
		"PreUp", "PostUp", "PreDown", "PostDown", "Address", "DNS", "MTU":
		return true
	default:
		return false
	}
}

func peerField(name string) bool {
	switch name {
	case "PublicKey", "PresharedKey", "AllowedIPs", "Endpoint", "PersistentKeepalive":
		return true
	default:
		return false
	}
}

func (p *parser) interfaceKey(name, value string, lineNo int) error {
	if value == "" && name != "PreUp" && name != "PostUp" && name != "PreDown" && name != "PostDown" {
		return syntaxError(lineNo, name, "value is empty")
	}
	if name != "Address" && name != "DNS" && (name != "PreUp" && name != "PostUp" &&
		name != "PreDown" && name != "PostDown") {
		p.noteScalar(p.interfaceSeenKeys, name, lineNo)
	}
	switch name {
	case "PrivateKey":
		key, ok := parseKey(value)
		if !ok {
			return syntaxError(lineNo, name, "invalid WireGuard key")
		}
		p.config.Interface.PrivateKey = key
		p.privateKeySet = true
	case "ListenPort":
		port, ok := parseUint(value, 16)
		if !ok {
			return syntaxError(lineNo, name, "invalid port")
		}
		p.config.Interface.ListenPort = uint16(port)
	case "FwMark":
		if !validFwMark(value) {
			return syntaxError(lineNo, name, "invalid firewall mark")
		}
		p.warn(lineNo, name, WarnIgnoredField)
	case "Table":
		if !validTable(value) {
			return syntaxError(lineNo, name, "invalid routing table")
		}
		p.warn(lineNo, name, WarnIgnoredField)
	case "SaveConfig":
		if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
			return syntaxError(lineNo, name, "expected true or false")
		}
		p.warn(lineNo, name, WarnIgnoredField)
	case "PreUp", "PostUp", "PreDown", "PostDown":
		if value != "" {
			return &ParseError{Line: lineNo, Field: name, Kind: ErrUnsupported,
				Msg: "shell hooks cannot be imported"}
		}
		p.warn(lineNo, name, WarnEmptyHook)
	case "Address":
		return p.addresses(value, lineNo)
	case "DNS":
		return p.dns(value, lineNo)
	case "MTU":
		mtu, ok := parseUint(value, 16)
		if !ok || mtu < 576 {
			return syntaxError(lineNo, name, "MTU must be between 576 and 65535")
		}
		p.config.Interface.MTU = uint16(mtu)
	}
	return nil
}

func (p *parser) addresses(value string, lineNo int) error {
	items := splitList(value)
	if len(items) == 0 {
		return syntaxError(lineNo, "Address", "Address is empty")
	}
	for _, item := range items {
		prefix, ok := parseAddressPrefix(item)
		if !ok {
			return syntaxError(lineNo, "Address", "invalid address")
		}
		if len(p.config.Interface.Addresses) >= MaxAddresses {
			return limitError(lineNo, "Address", "too many Address entries")
		}
		p.config.Interface.Addresses = append(p.config.Interface.Addresses, prefix)
	}
	return nil
}

func (p *parser) dns(value string, lineNo int) error {
	items := splitList(value)
	if len(items) == 0 {
		return syntaxError(lineNo, "DNS", "DNS is empty")
	}
	for _, item := range items {
		p.dnsEntries++
		if p.dnsEntries > MaxDNSServers {
			return limitError(lineNo, "DNS", "too many DNS entries")
		}
		server, err := netip.ParseAddr(item)
		if err != nil || server.Zone() != "" {
			// wg-quick treats every non-IP entry as a search domain. The
			// netstack has no search list, so the entry is dropped.
			p.warn(lineNo, "DNS", WarnSearchDomain)
			continue
		}
		p.config.Interface.DNS = append(p.config.Interface.DNS, server)
	}
	return nil
}

func (p *parser) peerKey(name, value string, lineNo int) error {
	if value == "" && name != "AllowedIPs" {
		return syntaxError(lineNo, name, "value is empty")
	}
	if name != "AllowedIPs" {
		p.noteScalar(p.peer.seenKeys, name, lineNo)
	}
	switch name {
	case "PublicKey":
		key, ok := parseKey(value)
		if !ok {
			return syntaxError(lineNo, name, "invalid WireGuard key")
		}
		// A repeated PublicKey in one section follows the scalar rule: the last
		// value wins. Duplicate peers are detected on the final keys in finish.
		p.peer.peer.PublicKey = key
		p.peer.publicKeyLine = lineNo
		p.peer.hasPublicKey = true
	case "PresharedKey":
		key, ok := parseKey(value)
		if !ok {
			return syntaxError(lineNo, name, "invalid WireGuard key")
		}
		p.peer.peer.PresharedKey = &key
	case "AllowedIPs":
		return p.allowedIPs(value, lineNo)
	case "Endpoint":
		endpoint, err := parseEndpoint(value, lineNo)
		if err != nil {
			return err
		}
		p.peer.peer.Endpoint = endpoint
	case "PersistentKeepalive":
		if strings.EqualFold(value, "off") {
			p.peer.peer.PersistentKeepalive = 0
			return nil
		}
		seconds, ok := parseUint(value, 16)
		if !ok {
			return syntaxError(lineNo, name, "invalid persistent keepalive")
		}
		p.peer.peer.PersistentKeepalive = uint16(seconds)
	}
	return nil
}

func (p *parser) allowedIPs(value string, lineNo int) error {
	if value == "" {
		p.warn(lineNo, "AllowedIPs", WarnEmptyAllowedIPs)
		return nil
	}
	for _, raw := range strings.Split(value, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			return syntaxError(lineNo, "AllowedIPs", "empty AllowedIPs entry")
		}
		if strings.HasPrefix(item, "+") || strings.HasPrefix(item, "-") {
			return &ParseError{Line: lineNo, Field: "AllowedIPs", Kind: ErrUnsupported,
				Msg: "incremental AllowedIPs cannot be imported"}
		}
		prefix, masked, ok := parseAllowedPrefix(item)
		if !ok {
			return syntaxError(lineNo, "AllowedIPs", "invalid AllowedIPs prefix")
		}
		if masked {
			p.warn(lineNo, "AllowedIPs", WarnHostBits)
		}
		if len(p.peer.peer.AllowedIPs) >= MaxAllowedIPs {
			return limitError(lineNo, "AllowedIPs", "too many AllowedIPs entries")
		}
		p.peer.peer.AllowedIPs = append(p.peer.peer.AllowedIPs, prefix)
	}
	return nil
}

func (p *parser) noteScalar(seen map[string]bool, name string, lineNo int) {
	if seen[name] {
		p.warn(lineNo, name, WarnDuplicateScalar)
	}
	seen[name] = true
}

func (p *parser) warn(lineNo int, field string, code DiagnosticCode) {
	p.diagnostics = append(p.diagnostics, Diagnostic{Line: lineNo, Field: field, Code: code})
}

func (p *parser) finish() (*Config, error) {
	if !p.privateKeySet {
		return nil, &ParseError{Line: 1, Field: "PrivateKey", Kind: ErrMissingRequired,
			Msg: "PrivateKey is required"}
	}
	config := p.config
	config.Peers = make([]Peer, 0, len(p.peers))
	seenPublicKeys := make(map[[32]byte]bool, len(p.peers))
	for _, built := range p.peers {
		if !built.hasPublicKey {
			return nil, &ParseError{Line: built.line, Field: "PublicKey", Kind: ErrMissingRequired,
				Msg: "peer is missing PublicKey"}
		}
		if seenPublicKeys[built.peer.PublicKey] {
			return nil, &ParseError{Line: built.publicKeyLine, Field: "PublicKey",
				Kind: ErrDuplicatePeer, Msg: "another peer already uses this public key"}
		}
		seenPublicKeys[built.peer.PublicKey] = true
		config.Peers = append(config.Peers, built.peer)
	}
	return &config, nil
}

func syntaxError(line int, field, message string) *ParseError {
	return &ParseError{Line: line, Field: field, Kind: ErrSyntax, Msg: message}
}

func limitError(line int, field, message string) *ParseError {
	return &ParseError{Line: line, Field: field, Kind: ErrLimit, Msg: message}
}

// decodeText accepts UTF-8, and UTF-16 with a byte-order mark. It rejects NUL
// bytes and invalid sequences rather than guessing at a broken file.
func decodeText(data []byte) (string, *ParseError) {
	switch {
	case len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF:
		data = data[3:]
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		return decodeUTF16(data[2:], binary.LittleEndian)
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		return decodeUTF16(data[2:], binary.BigEndian)
	}
	if !utf8.Valid(data) {
		return "", encodingError("file is not valid UTF-8 or UTF-16 text")
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return "", encodingError("file contains a NUL byte")
	}
	return string(data), nil
}

func decodeUTF16(data []byte, order binary.ByteOrder) (string, *ParseError) {
	if len(data)%2 != 0 {
		return "", encodingError("UTF-16 file has an odd number of bytes")
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = order.Uint16(data[index*2:])
	}
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if !utf16.IsSurrogate(rune(unit)) {
			continue
		}
		if unit >= 0xD800 && unit <= 0xDBFF && index+1 < len(units) &&
			units[index+1] >= 0xDC00 && units[index+1] <= 0xDFFF {
			index++
			continue
		}
		return "", encodingError("UTF-16 file contains an unpaired surrogate")
	}
	text := string(utf16.Decode(units))
	if strings.IndexByte(text, 0) >= 0 {
		return "", encodingError("file contains a NUL byte")
	}
	return text, nil
}

func encodingError(message string) *ParseError {
	return &ParseError{Line: 1, Field: "encoding", Kind: ErrEncoding, Msg: message}
}

// parseKey accepts the standard 44-character base64 form of a 32-byte key.
// Strict decoding rejects non-zero trailing padding bits, which would produce
// a second spelling for the same key.
func parseKey(value string) ([32]byte, bool) {
	var key [32]byte
	if len(value) != 44 {
		return key, false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return key, false
	}
	copy(key[:], decoded)
	return key, true
}

// parseUint parses an unsigned decimal integer with no sign, whitespace, or
// service name.
func parseUint(value string, bits int) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	return parsed, err == nil
}

func validFwMark(value string) bool {
	if strings.EqualFold(value, "off") {
		return true
	}
	text := value
	base := 10
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		text = text[2:]
		base = 16
		if text == "" {
			return false
		}
	}
	_, err := strconv.ParseUint(text, base, 32)
	return err == nil
}

func validTable(value string) bool {
	switch {
	case strings.EqualFold(value, "off"),
		strings.EqualFold(value, "auto"),
		strings.EqualFold(value, "main"):
		return true
	}
	_, err := strconv.ParseUint(value, 10, 32)
	return err == nil
}

// splitList splits comma- or whitespace-separated values and drops empty
// fields, matching wg-quick's array handling.
func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}

// parseAddressPrefix keeps a prefix as written; a bare address becomes a host
// prefix. Address semantics use only the host part, so the mask is not applied.
func parseAddressPrefix(value string) (netip.Prefix, bool) {
	if prefix, err := netip.ParsePrefix(value); err == nil {
		if prefix.Addr().Zone() != "" {
			return netip.Prefix{}, false
		}
		return prefix, true
	}
	address, err := netip.ParseAddr(value)
	if err != nil || address.Zone() != "" {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(address, address.BitLen()), true
}

// parseAllowedPrefix parses one AllowedIPs entry. The returned prefix is
// always masked; masked reports that masking changed the host bits.
func parseAllowedPrefix(value string) (prefix netip.Prefix, masked bool, ok bool) {
	if parsed, err := netip.ParsePrefix(value); err == nil {
		if parsed.Addr().Zone() != "" {
			return netip.Prefix{}, false, false
		}
		maskedPrefix := parsed.Masked()
		return maskedPrefix, maskedPrefix != parsed, true
	}
	address, err := netip.ParseAddr(value)
	if err != nil || address.Zone() != "" {
		return netip.Prefix{}, false, false
	}
	return netip.PrefixFrom(address, address.BitLen()), false, true
}

func parseEndpoint(value string, lineNo int) (Endpoint, *ParseError) {
	var host, portText string
	if strings.HasPrefix(value, "[") {
		end := strings.IndexByte(value, ']')
		if end < 0 {
			return Endpoint{}, syntaxError(lineNo, "Endpoint", "IPv6 endpoint is missing ]")
		}
		host = value[1:end]
		rest := value[end+1:]
		if !strings.HasPrefix(rest, ":") {
			return Endpoint{}, syntaxError(lineNo, "Endpoint", "endpoint is missing a port")
		}
		portText = rest[1:]
		address, err := netip.ParseAddr(host)
		if err != nil || address.Zone() != "" || !address.Is6() {
			return Endpoint{}, syntaxError(lineNo, "Endpoint", "brackets are valid only for an IPv6 address")
		}
	} else {
		if strings.Count(value, ":") != 1 {
			return Endpoint{}, syntaxError(lineNo, "Endpoint",
				"endpoint must be host:port; IPv6 addresses need brackets")
		}
		separator := strings.IndexByte(value, ':')
		host = value[:separator]
		portText = value[separator+1:]
		if host == "" {
			return Endpoint{}, syntaxError(lineNo, "Endpoint", "endpoint host is empty")
		}
		if strings.ContainsAny(host, "[]") {
			return Endpoint{}, syntaxError(lineNo, "Endpoint", "endpoint host is invalid")
		}
		if address, err := netip.ParseAddr(host); err == nil && address.Zone() != "" {
			return Endpoint{}, syntaxError(lineNo, "Endpoint", "endpoint host is invalid")
		}
	}
	if host == "" {
		return Endpoint{}, syntaxError(lineNo, "Endpoint", "endpoint host is empty")
	}
	if len(host) > MaxEndpointHostBytes {
		return Endpoint{}, limitError(lineNo, "Endpoint", "endpoint host is too long")
	}
	if !validEndpointHost(host) {
		return Endpoint{}, syntaxError(lineNo, "Endpoint", "endpoint host is invalid")
	}
	port, ok := parseUint(portText, 16)
	if !ok || port == 0 {
		return Endpoint{}, syntaxError(lineNo, "Endpoint", "invalid endpoint port")
	}
	return Endpoint{Host: host, Port: uint16(port)}, nil
}

func validEndpointHost(host string) bool {
	for _, r := range host {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
		switch r {
		case '/', '\\', '?', '#', '@', '[', ']':
			return false
		}
	}
	return true
}
