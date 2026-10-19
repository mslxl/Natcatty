package connection

import (
	"context"
	"strings"

	"github.com/mslxl/natcatty/domain/model"
)

// AuthMode is the Host's authentication selection, mirrored here so the
// connection package does not depend on Vault persistence types.
type AuthMode string

const (
	// AuthModeAuto tries the configured material in the OpenSSH-style order:
	// agent, selected key, password, then keyboard-interactive.
	AuthModeAuto AuthMode = "auto"
	// AuthModePassword offers only password and keyboard-interactive. No key
	// or agent is tried.
	AuthModePassword AuthMode = "password"
	// AuthModeKey offers the selected SSH Key first and fails closed when that
	// material cannot be used. Password and keyboard-interactive may follow.
	AuthModeKey AuthMode = "key"
	// AuthModeCertificate offers the selected key with its signed certificate
	// and fails closed when that material cannot be used.
	AuthModeCertificate AuthMode = "certificate"
)

// AuthCandidateKind is the protocol mechanism a planned attempt uses.
type AuthCandidateKind string

const (
	// AuthKindAgent offers the local SSH agent's keys.
	AuthKindAgent AuthCandidateKind = "agent"
	// AuthKindPublicKey offers one SSH Key.
	AuthKindPublicKey AuthCandidateKind = "publickey"
	// AuthKindPassword offers the saved password.
	AuthKindPassword AuthCandidateKind = "password"
	// AuthKindKeyboardInteractive runs the server's question sequence.
	AuthKindKeyboardInteractive AuthCandidateKind = "keyboard-interactive"
)

// Candidate IDs mirror the original cache values. They are stable strings,
// never key material.
const (
	CandidateAgent               = "agent"
	CandidatePassword            = "password"
	CandidateKeyboardInteractive = "keyboard-interactive"
	candidateUserKeyPrefix       = "publickey-user"
	candidateDefaultKeyPrefix    = "publickey-default-"
)

// KeyMaterial is one SSH Key as the transport needs it. PrivateKey and
// Passphrase are plaintext in memory only: they are never logged, persisted,
// or copied onto a domain entity by this package.
type KeyMaterial struct {
	ID          model.KeyID
	Label       string
	PrivateKey  string
	Passphrase  string
	Certificate string
}

// AuthCandidate is one ordered attempt. Key is set only for
// AuthKindPublicKey. Primary marks the selected key whose failure must not
// silently fall back to unrelated key material.
type AuthCandidate struct {
	Kind    AuthCandidateKind
	ID      string
	Key     *KeyMaterial
	Primary bool
}

// AuthPlan is the ordered authentication strategy for one Connection.
type AuthPlan struct {
	Mode        AuthMode
	Username    string
	Password    string
	Candidates  []AuthCandidate
	FailClosed  bool
	RequiresMFA bool
}

// AuthSpec is the non-secret input to BuildAuthPlan. Password is credential
// plaintext and is only carried in memory.
type AuthSpec struct {
	Username     string
	Mode         AuthMode
	Password     string
	SelectedKey  *KeyMaterial
	DefaultKeys  []KeyMaterial
	UseAgent     bool
	RequiresMFA  bool
	CachedMethod string
}

// AuthSource resolves a Host's saved authentication material. The app bridges
// the Vault; tests provide a fake. Implementations must not persist or log the
// returned plaintext.
type AuthSource interface {
	AuthPlan(ctx context.Context, req Request) (AuthPlan, error)
}

// PassphraseRequest asks for an encrypted SSH Key's passphrase.
type PassphraseRequest struct {
	Hostname string
	Username string
	KeyLabel string
	// Invalid reports that a previously entered or stored passphrase was
	// rejected, so the prompt can say so.
	Invalid bool
}

// PassphraseDecision is the user's answer. Cancelled aborts authentication
// without treating empty input as a passphrase.
type PassphraseDecision struct {
	Passphrase string
	Cancelled  bool
}

// KeyboardInteractivePrompt is one server-supplied question.
type KeyboardInteractivePrompt struct {
	Text string
	Echo bool
}

// KeyboardInteractiveRequest is one server keyboard-interactive round. Rounds
// are ordered; a server may issue more than one during a multi-factor login.
type KeyboardInteractiveRequest struct {
	Hostname    string
	Username    string
	Name        string
	Instruction string
	Prompts     []KeyboardInteractivePrompt
}

// KeyboardInteractiveDecision is the user's ordered answers. Answers must have
// one entry per prompt. Cancelled aborts authentication.
type KeyboardInteractiveDecision struct {
	Answers   []string
	Cancelled bool
}

// AuthPrompter asks the user for interactive authentication input. The UI
// implements it; tests provide a fake. Implementations should bound a prompt's
// lifetime (the original used 2 minutes for a passphrase and 5 minutes for
// keyboard-interactive).
type AuthPrompter interface {
	Passphrase(ctx context.Context, req PassphraseRequest) (PassphraseDecision, error)
	KeyboardInteractive(ctx context.Context, req KeyboardInteractiveRequest) (KeyboardInteractiveDecision, error)
}

// BuildAuthPlan produces the ordered attempts for a Host's resolved mode. The
// "none" probe happens inside the transport before this plan is consulted.
//
// Ordering matches the original:
//   - password mode: keyboard-interactive first only when RequiresMFA, then
//     password, then keyboard-interactive last; no key or agent.
//   - key/certificate mode: the selected key first, then password and
//     keyboard-interactive; no default key or agent substitution.
//   - auto: keyboard-interactive first when MFA is requested and no password
//     is saved; then agent, the selected key, default keys, password, and
//     keyboard-interactive last.
func BuildAuthPlan(spec AuthSpec) AuthPlan {
	mode := spec.Mode
	if mode == "" {
		mode = AuthModeAuto
	}
	plan := AuthPlan{
		Mode:        mode,
		Username:    spec.Username,
		Password:    spec.Password,
		RequiresMFA: spec.RequiresMFA,
	}
	passwordMethods := func(preferKeyboardInteractive bool) {
		if preferKeyboardInteractive {
			addCandidate(&plan, AuthCandidate{Kind: AuthKindKeyboardInteractive, ID: CandidateKeyboardInteractive})
		}
		if spec.Password != "" {
			addCandidate(&plan, AuthCandidate{Kind: AuthKindPassword, ID: CandidatePassword})
		}
	}
	ensureKeyboardInteractive := func() {
		for _, candidate := range plan.Candidates {
			if candidate.Kind == AuthKindKeyboardInteractive {
				return
			}
		}
		addCandidate(&plan, AuthCandidate{Kind: AuthKindKeyboardInteractive, ID: CandidateKeyboardInteractive})
	}

	switch mode {
	case AuthModePassword:
		passwordMethods(spec.RequiresMFA)
	case AuthModeKey, AuthModeCertificate:
		plan.FailClosed = true
		if spec.SelectedKey != nil {
			addCandidate(&plan, AuthCandidate{
				Kind:    AuthKindPublicKey,
				ID:      candidateUserKeyPrefix,
				Key:     spec.SelectedKey,
				Primary: true,
			})
		}
		passwordMethods(spec.RequiresMFA)
	case AuthModeAuto:
		if spec.RequiresMFA && spec.Password == "" {
			addCandidate(&plan, AuthCandidate{Kind: AuthKindKeyboardInteractive, ID: CandidateKeyboardInteractive})
		}
		if spec.UseAgent {
			addCandidate(&plan, AuthCandidate{Kind: AuthKindAgent, ID: CandidateAgent})
		}
		if spec.SelectedKey != nil {
			addCandidate(&plan, AuthCandidate{
				Kind:    AuthKindPublicKey,
				ID:      candidateUserKeyPrefix,
				Key:     spec.SelectedKey,
				Primary: true,
			})
		}
		for i := range spec.DefaultKeys {
			key := &spec.DefaultKeys[i]
			addCandidate(&plan, AuthCandidate{
				Kind: AuthKindPublicKey,
				ID:   candidateDefaultKeyPrefix + defaultKeyName(key),
				Key:  key,
			})
		}
		passwordMethods(spec.RequiresMFA && spec.Password != "")
	default:
		// An unknown mode is a caller bug. Treat it as password-only rather
		// than silently offering keys the user did not select.
		passwordMethods(spec.RequiresMFA)
	}
	ensureKeyboardInteractive()
	PromoteCachedCandidate(&plan, spec.CachedMethod, mode)
	return plan
}

// PromoteCachedCandidate moves a previously successful agent or public-key
// candidate to the front, as the original does only in automatic mode. A
// cached password is never promoted, and explicit key or password hosts keep
// their configured order.
func PromoteCachedCandidate(plan *AuthPlan, method string, mode AuthMode) {
	if plan == nil || method == "" || mode != AuthModeAuto {
		return
	}
	if method != CandidateAgent && !strings.HasPrefix(method, "publickey") {
		return
	}
	for i, candidate := range plan.Candidates {
		if candidate.ID != method || i == 0 {
			continue
		}
		copy(plan.Candidates[1:i+1], plan.Candidates[0:i])
		plan.Candidates[0] = candidate
		return
	}
}

func addCandidate(plan *AuthPlan, candidate AuthCandidate) {
	plan.Candidates = append(plan.Candidates, candidate)
}

func defaultKeyName(key *KeyMaterial) string {
	if key.Label != "" {
		return key.Label
	}
	return string(key.ID)
}
