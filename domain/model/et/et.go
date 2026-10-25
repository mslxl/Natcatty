// Package et holds the EternalTerminal transport vocabulary: the endpoint,
// credentials, jump restriction, SSH option values, and the SSH_ASKPASS prompt
// matching used by the bundled `et` client.
//
// Unlike Mosh, `et` performs its own SSH bootstrap and ET handshake. Netcatty
// spawns the bundled binary in a private HOME and feeds saved credentials to
// its internal ssh through an SSH_ASKPASS helper. The helper can answer
// password and key-passphrase prompts only, so a second factor or a host-key
// confirmation is never guessed; the original restricted ET to one jump host
// and no proxy for the same reason.
package et

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultPort is the SSH bootstrap port.
const DefaultPort = 22

// DefaultETPort is the EternalTerminal server port.
const DefaultETPort = 2022

// DefaultTerm is the terminal type the bundled et client runs under.
const DefaultTerm = "xterm-256color"

// Authentication method values, matching the Vault vocabulary.
const (
	AuthAuto        = "auto"
	AuthPassword    = "password"
	AuthKey         = "key"
	AuthCertificate = "certificate"
)

// Errors the transport and the UI classify without parsing text.
var (
	// ErrInvalidConfig reports an unusable ET configuration.
	ErrInvalidConfig = errors.New("et: invalid configuration")
	// ErrProxyUnsupported reports a Host route that uses a proxy. The bundled
	// et client has no proxy handoff.
	ErrProxyUnsupported = errors.New("et: proxy connections are not supported")
	// ErrMultipleJumpHosts reports more than one jump Host. Netcatty supports
	// at most one because et exposes a single --jumphost.
	ErrMultipleJumpHosts = errors.New("et: at most one jump host is supported")
	// ErrBundleUnavailable reports a missing bundled et binary.
	ErrBundleUnavailable = errors.New("et: bundled client is not available")
	// ErrChecksumMismatch reports a bundled et binary that failed SHA-256
	// verification.
	ErrChecksumMismatch = errors.New("et: bundled client failed checksum verification")
	// ErrPlatformUnsupported reports a platform with no published et build.
	ErrPlatformUnsupported = errors.New("et: this platform has no bundled client")
)

// ValidationError reports the configuration field that is unusable.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("et: %s %s", e.Field, e.Reason)
}

// Is makes every validation failure match ErrInvalidConfig.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidConfig }

// Auth is one endpoint's authentication material. Password, Passphrase,
// PrivateKey, and Certificate are memory-only plaintext; the adapter writes
// them to a private temporary directory and never logs them.
type Auth struct {
	Method            string
	Password          string
	Passphrase        string
	PrivateKey        string
	Certificate       string
	IdentityFilePaths []string
	UseSSHAgent       bool
	IdentitiesOnly    bool
	AgentSocket       string
	RequiresMFA       bool
}

// Normalized fills the automatic default and trims whitespace.
func (a Auth) Normalized() Auth {
	a.Method = strings.ToLower(strings.TrimSpace(a.Method))
	if a.Method == "" {
		a.Method = AuthAuto
	}
	a.AgentSocket = strings.TrimSpace(a.AgentSocket)
	paths := make([]string, 0, len(a.IdentityFilePaths))
	for _, path := range a.IdentityFilePaths {
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	a.IdentityFilePaths = paths
	return a
}

// HasPublicKey reports whether the endpoint offers a key or certificate. An
// agent or certificate alone still counts: et may use the agent for the key.
func (a Auth) HasPublicKey() bool {
	return a.UseSSHAgent || a.PrivateKey != "" || a.Certificate != "" || len(a.IdentityFilePaths) > 0
}

// HasPassword reports whether a saved password is available to askpass.
func (a Auth) HasPassword() bool { return a.Password != "" }

// Hop is the optional single jump Host. Its ET port is the port ET's TCP
// socket targets on the jump Host; the SSH port is Port.
type Hop struct {
	Hostname string
	Port     int
	ETPort   int
	Username string
	Auth     Auth
	// LegacyAlgorithms widens the jump's negotiation. The target's own setting
	// also applies to the jump, matching the original.
	LegacyAlgorithms bool
}

// Normalized fills the SSH and ET port defaults.
func (h Hop) Normalized() Hop {
	h.Hostname = strings.TrimSpace(h.Hostname)
	h.Username = strings.TrimSpace(h.Username)
	h.Auth = h.Auth.Normalized()
	if h.Port == 0 {
		h.Port = DefaultPort
	}
	if h.ETPort == 0 {
		h.ETPort = DefaultETPort
	}
	return h
}

// Config is one ET Session.
type Config struct {
	Hostname string
	Port     int
	Username string
	// ETPort is the EternalTerminal server port, 2022 by default.
	ETPort int
	Auth   Auth
	// LegacyAlgorithms widens the SSH and jump negotiation for older servers.
	LegacyAlgorithms bool
	// Jump is the single allowed jump Host, or nil.
	Jump *Hop
	// VerifyHostKeys keeps StrictHostKeyChecking=accept-new. False points
	// OpenSSH at an empty known-hosts snapshot.
	VerifyHostKeys bool
	// KnownHostsPath is the user's real known_hosts used for verification.
	KnownHostsPath string
	// KnownHostsContent, when non-empty, is an authoritative snapshot written
	// to the private HOME instead of the user's file.
	KnownHostsContent string
	// JumpKnownHostsContent is the jump Host's authoritative snapshot. It is
	// separate because the jump's ssh child never sees the destination's
	// --ssh-option values.
	JumpKnownHostsContent string
	// Env is the Session's extra environment, merged under the ET variables.
	Env []string
	// Cols and Rows size the pseudo-terminal.
	Cols, Rows int
	// SessionID names the private HOME so concurrent Sessions never share one.
	SessionID string
	// AskpassProgram overrides the askpass executable. Empty uses the app.
	AskpassProgram string
}

// Normalized fills the defaults a blank form implies.
func (c Config) Normalized() Config {
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.Username = strings.TrimSpace(c.Username)
	c.Auth = c.Auth.Normalized()
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.ETPort == 0 {
		c.ETPort = DefaultETPort
	}
	if c.Jump != nil {
		jump := c.Jump.Normalized()
		if jump.LegacyAlgorithms {
			c.LegacyAlgorithms = true
		}
		c.Jump = &jump
	}
	c.KnownHostsPath = strings.TrimSpace(c.KnownHostsPath)
	c.AskpassProgram = strings.TrimSpace(c.AskpassProgram)
	return c
}

// Validate checks that a normalized configuration can be launched.
func (c Config) Validate() error {
	if c.Hostname == "" {
		return &ValidationError{Field: "hostname", Reason: "is required"}
	}
	if c.Port < 1 || c.Port > 65535 {
		return &ValidationError{Field: "port", Reason: "is out of range"}
	}
	if c.ETPort < 1 || c.ETPort > 65535 {
		return &ValidationError{Field: "et port", Reason: "is out of range"}
	}
	switch c.Auth.Method {
	case AuthAuto, AuthPassword, AuthKey, AuthCertificate:
	default:
		return &ValidationError{Field: "authentication method", Reason: "is unsupported"}
	}
	if c.Jump != nil {
		if c.Jump.Hostname == "" {
			return &ValidationError{Field: "jump host", Reason: "is required"}
		}
		if c.Jump.Port < 1 || c.Jump.Port > 65535 {
			return &ValidationError{Field: "jump port", Reason: "is out of range"}
		}
		if c.Jump.ETPort < 1 || c.Jump.ETPort > 65535 {
			return &ValidationError{Field: "jump et port", Reason: "is out of range"}
		}
	}
	if c.Cols < 0 || c.Rows < 0 {
		return &ValidationError{Field: "size", Reason: "is out of range"}
	}
	return nil
}

// CheckRoute enforces the original restrictions before any process starts.
func CheckRoute(jumpCount int, proxied bool) error {
	if proxied {
		return ErrProxyUnsupported
	}
	if jumpCount > 1 {
		return ErrMultipleJumpHosts
	}
	return nil
}

// UserHost is the positional ET destination.
func (c Config) UserHost(fallbackUser string) string {
	username := c.Username
	if username == "" {
		username = fallbackUser
	}
	if username == "" {
		return c.Hostname
	}
	return username + "@" + c.Hostname
}

// Args builds the bundled et argv: the ET port when non-default, one
// --ssh-option per comma-free value, the jump pair, and the destination.
func (c Config) Args(sshOptions []string, fallbackUser string) []string {
	args := make([]string, 0, 4+2*len(sshOptions))
	if c.ETPort != 0 && c.ETPort != DefaultETPort {
		args = append(args, "-p", fmt.Sprint(c.ETPort))
	}
	for _, option := range sshOptions {
		args = append(args, "--ssh-option", option)
	}
	if c.Jump != nil {
		jumpUser := c.Jump.Username
		if jumpUser == "" {
			jumpUser = fallbackUser
		}
		jumpTarget := c.Jump.Hostname
		if jumpUser != "" {
			jumpTarget = jumpUser + "@" + c.Jump.Hostname
		}
		args = append(args, "--jumphost", jumpTarget, "--jport", fmt.Sprint(c.Jump.ETPort))
	}
	args = append(args, c.UserHost(fallbackUser))
	return args
}

// PreferredAuthentications returns the OpenSSH method order for one endpoint.
// It reproduces the original order: an explicit password Host never offers
// public keys, automatic mode tries keys first, and a key Host offers the
// password only when one is saved.
func PreferredAuthentications(auth Auth, hasPassword, hasPublicKey bool) string {
	method := auth.Normalized().Method
	switch {
	case method == AuthPassword:
		return "password,keyboard-interactive"
	case method == AuthAuto:
		return "publickey,password,keyboard-interactive"
	case hasPublicKey && hasPassword:
		return "publickey,password,keyboard-interactive"
	case hasPublicKey:
		return "publickey"
	case hasPassword:
		return "password,keyboard-interactive"
	default:
		return ""
	}
}

// LegacyAlgorithmLines returns the additive OpenSSH directives that widen
// negotiation for old servers. They contain commas, so the adapter writes them
// to a config file instead of --ssh-option.
func LegacyAlgorithmLines(indent string) []string {
	lines := []string{
		"KexAlgorithms +diffie-hellman-group14-sha1,diffie-hellman-group1-sha1",
		"Ciphers +aes128-cbc,aes256-cbc,3des-cbc",
		"HostKeyAlgorithms +ssh-rsa,ssh-dss",
		"PubkeyAcceptedAlgorithms +ssh-rsa,ssh-dss",
	}
	for index, line := range lines {
		lines[index] = indent + line
	}
	return lines
}

// AskpassPrompt is one saved secret and the prompts that may consume it.
// Secret is plaintext; the adapter writes it to a 0600 file and the helper
// reads only that file.
type AskpassPrompt struct {
	Type     string
	Matchers []string
	Secret   string
}

// AskpassEntries builds the saved prompts for a target and its jump.
func AskpassEntries(cfg Config) []AskpassPrompt {
	var entries []AskpassPrompt
	add := func(kind string, matchers []string, secret string) {
		if secret == "" {
			return
		}
		lowered := make([]string, 0, len(matchers))
		seen := make(map[string]struct{}, len(matchers))
		for _, matcher := range matchers {
			normalized := strings.ToLower(strings.TrimSpace(matcher))
			if normalized == "" {
				continue
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			lowered = append(lowered, normalized)
		}
		if len(lowered) == 0 {
			return
		}
		entries = append(entries, AskpassPrompt{Type: kind, Matchers: lowered, Secret: secret})
	}
	add("password", PasswordPromptMatchers(cfg.Hostname, cfg.Username, cfg.Port), cfg.Auth.Password)
	if cfg.Jump != nil {
		jump := cfg.Jump.Normalized()
		add("password", PasswordPromptMatchers(jump.Hostname, jump.Username, jump.Port), jump.Auth.Password)
	}
	return entries
}

// AddPassphraseEntry records a key passphrase for a private key file. The
// adapter calls it once for each temporary key it writes.
func AddPassphraseEntry(entries []AskpassPrompt, keyPath, passphrase string) []AskpassPrompt {
	if passphrase == "" {
		return entries
	}
	return append(entries, AskpassPrompt{
		Type:     "passphrase",
		Matchers: PassphrasePromptMatchers(keyPath),
		Secret:   passphrase,
	})
}

// PasswordPromptMatchers reproduces the original matcher set. The prompt text
// is lowercased before matching, so every matcher is lowercased here.
func PasswordPromptMatchers(hostname, username string, port int) []string {
	host := strings.ToLower(strings.TrimSpace(hostname))
	if host == "" {
		return nil
	}
	matchers := []string{host}
	if user := strings.ToLower(strings.TrimSpace(username)); user != "" {
		target := user + "@" + host
		matchers = append(matchers, target, target+"'s password")
		if port > 0 {
			matchers = append(matchers, fmt.Sprintf("%s:%d", target, port))
		}
	}
	return matchers
}

// PassphrasePromptMatchers matches an OpenSSH key passphrase prompt, which
// mentions the key path.
func PassphrasePromptMatchers(keyPath string) []string {
	normalized := filepath.ToSlash(filepath.Clean(keyPath))
	lowered := strings.ToLower(normalized)
	return []string{lowered, strings.ToLower(filepath.Base(normalized))}
}

var secondFactorRE = regexp.MustCompile(`one[\s-]?time|\botp\b|verification|passcode|\btoken\b|2fa|two[\s-]?factor|multi[\s-]?factor|\bmfa\b|second\s+factor|secondary(?:\s+\w+){0,3}\s+passw|second(?:\s+\w+){0,3}\s+passw|additional(?:\s+\w+){0,3}\s+passw|re[-\s]?enter\s+passw|confirm\s+passw|\bedr\b|duo|动态|一次性|验证码|验证信息|令牌|双因素|多因素|短信验证|手机验证|二次|安全密码|挑战码`)

// passwordRE matches a password prompt in English or Chinese.
var passwordRE = regexp.MustCompile(`passw(or)?d|密\s*码|口\s*令`)

// PickAskpassEntry chooses the secret for one SSH prompt. A passphrase prompt
// consumes passphrase entries; a password prompt consumes password entries and
// never a one-time code. The longest matching matcher wins, then declaration
// order.
func PickAskpassEntry(prompt string, entries []AskpassPrompt) *AskpassPrompt {
	normalized := strings.ToLower(prompt)
	wantsPassphrase := strings.Contains(normalized, "passphrase")

	knownLoginPrompt := false
	for _, entry := range entries {
		if entry.Type != "password" {
			continue
		}
		for _, matcher := range entry.Matchers {
			value := strings.ToLower(matcher)
			if strings.HasSuffix(value, "'s password") && strings.Contains(normalized, value) {
				knownLoginPrompt = true
			}
		}
	}
	secondFactor := !knownLoginPrompt && secondFactorRE.MatchString(normalized)
	wantsPassword := !secondFactor && passwordRE.MatchString(normalized)
	if !wantsPassphrase && !wantsPassword {
		return nil
	}

	wantedType := "password"
	if wantsPassphrase {
		wantedType = "passphrase"
	}
	best := -1
	bestScore := 0
	for index, entry := range entries {
		if entry.Type != wantedType {
			continue
		}
		score := 0
		for _, matcher := range entry.Matchers {
			value := strings.ToLower(matcher)
			if value != "" && strings.Contains(normalized, value) && len(value) > score {
				score = len(value)
			}
		}
		if score > bestScore {
			best, bestScore = index, score
		}
	}
	if best >= 0 {
		return &entries[best]
	}
	if wantsPassword {
		count := 0
		index := -1
		for position, entry := range entries {
			if entry.Type == "password" {
				count++
				index = position
			}
		}
		if count == 1 {
			return &entries[index]
		}
	}
	return nil
}
