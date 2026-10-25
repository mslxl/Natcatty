package ssh

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// authMachine turns an application AuthPlan into x/crypto auth methods. All
// credential values stay in memory; the machine never logs them.
//
// The candidate order is already decided by application/connection. This type
// enforces the protocol-level rules: the transport probes "none" first, a
// method the server does not advertise is skipped without being consumed, a
// partial success starts a fresh pass while still suppressing rejected and
// completed methods, and keyboard-interactive may run a second factor but not
// an unbounded sequence.
type authMachine struct {
	ctx      context.Context
	plan     appconnection.AuthPlan
	prompter appconnection.AuthPrompter
	hostname string
	agent    AgentDialFunc
	keys     appconnection.SecurityKeyService

	agentConn   interface{ Close() error }
	agentClient agent.ExtendedAgent
	signers     map[string]xssh.Signer

	attempted map[string]bool
	failed    map[string]bool
	succeeded map[string]bool

	last         *appconnection.AuthCandidate
	partialSeen  int
	kiSuccess    int
	passwordOK   bool
	kiAutofilled bool

	// skFailure is the typed reason a security key could not be used (the agent
	// is absent, or the device missed its deadline). It is returned only when no
	// other credential was attempted, so a rejected password is not reported as
	// a missing OpenSSH installation.
	skFailure      error
	otherAttempted bool
}

// maxKeyboardInteractiveFactors matches the original's two-factor bound.
const maxKeyboardInteractiveFactors = 2

func newAuthMachine(ctx context.Context, plan appconnection.AuthPlan, prompter appconnection.AuthPrompter, hostname string, dial AgentDialFunc, keys appconnection.SecurityKeyService) *authMachine {
	if dial == nil {
		dial = defaultAgentDial
	}
	return &authMachine{
		ctx:       ctx,
		plan:      plan,
		prompter:  prompter,
		hostname:  hostname,
		agent:     dial,
		keys:      keys,
		signers:   make(map[string]xssh.Signer),
		attempted: make(map[string]bool),
		failed:    make(map[string]bool),
		succeeded: make(map[string]bool),
	}
}

// Prepare loads a selected key's signer before the network is touched when the
// mode is fail-closed. A selected key that cannot be parsed aborts
// immediately, so explicit key and certificate modes never substitute another
// credential. Automatic mode defers parsing until its turn, so a working agent
// or default key can complete authentication without a passphrase prompt.
func (m *authMachine) Prepare() error {
	if !m.plan.FailClosed {
		return nil
	}
	for i := range m.plan.Candidates {
		candidate := &m.plan.Candidates[i]
		if candidate.Kind != appconnection.AuthKindPublicKey || !candidate.Primary {
			continue
		}
		if _, err := m.signerFor(candidate); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the agent transport, if one was opened.
func (m *authMachine) Close() {
	if m.agentConn != nil {
		_ = m.agentConn.Close()
	}
}

// SuccessID is the candidate that completed authentication, or "" when the
// server accepted the initial "none" probe.
func (m *authMachine) SuccessID() string {
	if m.last == nil {
		return ""
	}
	return m.last.ID
}

// Next is the x/crypto ClientAuthCallback.
func (m *authMachine) Next(state *xssh.ClientAuthContext) (xssh.AuthMethod, error) {
	m.updatePhase(state)

	allowed := state.AllowedMethods
	hasPartial := len(state.PartialSuccessMethods) > 0

	// After any successful factor, prefer keyboard-interactive when the server
	// still offers it. This covers password->KI and key->KI MFA without the
	// user having to mark the Host as MFA-first.
	if hasPartial && methodAllowed(allowed, appconnection.AuthKindKeyboardInteractive) &&
		!m.attempted[appconnection.CandidateKeyboardInteractive] &&
		!m.failed[appconnection.CandidateKeyboardInteractive] &&
		m.kiSuccess < maxKeyboardInteractiveFactors {
		return m.choose(m.keyboardInteractiveCandidate())
	}

	for i := range m.plan.Candidates {
		candidate := &m.plan.Candidates[i]
		if m.attempted[candidate.ID] || !methodAllowed(allowed, candidate.Kind) {
			continue
		}
		switch candidate.Kind {
		case appconnection.AuthKindAgent:
			method, err := m.agentMethod()
			if err != nil {
				m.attempted[candidate.ID] = true
				m.failed[candidate.ID] = true
				continue
			}
			return m.chooseCandidate(candidate, method)
		case appconnection.AuthKindPublicKey:
			signer, err := m.signerFor(candidate)
			if err != nil {
				if candidate.Primary || m.plan.FailClosed {
					return nil, err
				}
				m.attempted[candidate.ID] = true
				m.failed[candidate.ID] = true
				continue
			}
			return m.chooseCandidate(candidate, xssh.PublicKeys(signer))
		case appconnection.AuthKindPassword:
			if m.plan.Password == "" {
				continue
			}
			return m.chooseCandidate(candidate, xssh.Password(m.plan.Password))
		case appconnection.AuthKindKeyboardInteractive:
			return m.chooseCandidate(candidate, xssh.KeyboardInteractive(m.challenge))
		}
	}
	return nil, m.finalFailure()
}

// finalFailure returns the reason to report when every candidate is exhausted.
// A missed device deadline is always the actionable answer: the user was asked
// to touch the key and no credential completed. A missing OpenSSH agent wins
// only when no ordinary credential was tried, so a rejected password is not
// reported as a missing installation.
func (m *authMachine) finalFailure() error {
	if m.skFailure != nil {
		var typed *appconnection.Error
		if errors.As(m.skFailure, &typed) && typed.Kind == appconnection.KindTimeout {
			return m.skFailure
		}
		if !m.otherAttempted {
			return m.skFailure
		}
	}
	return authFailure(fmt.Errorf("%w: all configured methods exhausted", appconnection.ErrAuthentication))
}

// updatePhase folds the server's response to the previous candidate into the
// machine's bookkeeping.
func (m *authMachine) updatePhase(state *xssh.ClientAuthContext) {
	if len(state.PartialSuccessMethods) > m.partialSeen {
		if m.last != nil {
			m.succeeded[m.last.ID] = true
		}
		m.partialSeen = len(state.PartialSuccessMethods)
		m.kiSuccess = countMethod(state.PartialSuccessMethods, "keyboard-interactive")
		m.passwordOK = countMethod(state.PartialSuccessMethods, "password") > 0

		// Start a fresh pass: unavailable methods become eligible again, while
		// rejected and completed methods stay suppressed.
		m.attempted = make(map[string]bool, len(m.failed)+len(m.succeeded))
		for id := range m.failed {
			m.attempted[id] = true
		}
		for id := range m.succeeded {
			m.attempted[id] = true
		}
		// A server may require keyboard-interactive twice (login password then
		// a second factor). Allow one repeat, never after it was rejected.
		if methodContains(state.AllowedMethods, "keyboard-interactive") &&
			m.kiSuccess == maxKeyboardInteractiveFactors-1 &&
			!m.failed[appconnection.CandidateKeyboardInteractive] {
			delete(m.attempted, appconnection.CandidateKeyboardInteractive)
		}
		m.last = nil
		return
	}
	if m.last != nil {
		// The callback is invoked again without a partial success, so the
		// server rejected the previous attempt.
		m.failed[m.last.ID] = true
		m.last = nil
	}
}

func (m *authMachine) choose(candidate *appconnection.AuthCandidate) (xssh.AuthMethod, error) {
	switch candidate.Kind {
	case appconnection.AuthKindKeyboardInteractive:
		return m.chooseCandidate(candidate, xssh.KeyboardInteractive(m.challenge))
	case appconnection.AuthKindPassword:
		return m.chooseCandidate(candidate, xssh.Password(m.plan.Password))
	default:
		return nil, authFailure(fmt.Errorf("%w: no method for candidate %s", appconnection.ErrAuthentication, candidate.ID))
	}
}

func (m *authMachine) chooseCandidate(candidate *appconnection.AuthCandidate, method xssh.AuthMethod) (xssh.AuthMethod, error) {
	m.attempted[candidate.ID] = true
	m.last = candidate
	if candidate.Kind != appconnection.AuthKindAgent {
		m.otherAttempted = true
	}
	return method, nil
}

func (m *authMachine) keyboardInteractiveCandidate() *appconnection.AuthCandidate {
	for i := range m.plan.Candidates {
		if m.plan.Candidates[i].Kind == appconnection.AuthKindKeyboardInteractive {
			return &m.plan.Candidates[i]
		}
	}
	return &appconnection.AuthCandidate{
		Kind: appconnection.AuthKindKeyboardInteractive,
		ID:   appconnection.CandidateKeyboardInteractive,
	}
}

func methodAllowed(allowed []string, kind appconnection.AuthCandidateKind) bool {
	if len(allowed) == 0 {
		// A server that omits the list is treated as offering everything, as
		// the original does.
		return true
	}
	switch kind {
	case appconnection.AuthKindAgent:
		return methodContains(allowed, "publickey") || methodContains(allowed, "agent")
	case appconnection.AuthKindPublicKey:
		return methodContains(allowed, "publickey")
	case appconnection.AuthKindPassword:
		return methodContains(allowed, "password")
	case appconnection.AuthKindKeyboardInteractive:
		return methodContains(allowed, "keyboard-interactive")
	default:
		return false
	}
}

func methodContains(methods []string, wanted string) bool {
	for _, method := range methods {
		if method == wanted {
			return true
		}
	}
	return false
}

func countMethod(methods []string, wanted string) int {
	var count int
	for _, method := range methods {
		if method == wanted {
			count++
		}
	}
	return count
}

// signerFor parses one candidate's key, prompting for a passphrase when the
// material is encrypted and no stored passphrase works.
func (m *authMachine) signerFor(candidate *appconnection.AuthCandidate) (xssh.Signer, error) {
	if signer, ok := m.signers[candidate.ID]; ok {
		return signer, nil
	}
	key := candidate.Key
	if key == nil || strings.TrimSpace(key.PrivateKey) == "" {
		return nil, authFailure(fmt.Errorf("%w: %s has no private material", appconnection.ErrNoCredentials, candidate.ID))
	}

	passphrase := key.Passphrase
	invalid := passphrase != ""
	for {
		signer, _, err := LoadSigner(key.PrivateKey, passphrase, key.Certificate)
		if err == nil {
			m.signers[candidate.ID] = signer
			return signer, nil
		}
		if !errors.Is(err, ErrKeyPassphraseRequired) && !errors.Is(err, ErrKeyWrongPassphrase) {
			return nil, classifyKeyFailure(err)
		}
		if m.prompter == nil {
			return nil, authFailure(fmt.Errorf("%w: %w", appconnection.ErrPromptUnavailable, err))
		}
		decision, promptErr := m.prompter.Passphrase(m.ctx, appconnection.PassphraseRequest{
			Hostname: m.hostname,
			Username: m.plan.Username,
			KeyLabel: key.Label,
			Invalid:  invalid,
		})
		if promptErr != nil {
			return nil, m.classifyPromptError(promptErr)
		}
		if decision.Cancelled {
			return nil, &appconnection.Error{
				Kind:  appconnection.KindCancelled,
				Cause: fmt.Errorf("%w: passphrase for %s", appconnection.ErrPromptCancelled, key.Label),
			}
		}
		if decision.Passphrase == "" {
			return nil, authFailure(appconnection.ErrNoCredentials)
		}
		passphrase = decision.Passphrase
		invalid = true
	}
}

// agentMethod opens the local agent and offers its identities. Hardware-backed
// (SK) identities are replaced by wrappers that preserve the OpenSSH signature
// framing and enforce the device deadline; ordinary identities keep using
// x/crypto's agent signer.
func (m *authMachine) agentMethod() (xssh.AuthMethod, error) {
	if m.agentClient == nil {
		conn, err := m.agent(m.ctx)
		if err != nil {
			// A missing OpenSSH agent is an unsupported capability, not a
			// credential rejection. It is recorded and reported only after the
			// rest of the plan has had its turn.
			m.recordActionableSecurityKeyFailure(appconnection.SecurityKeyUnsupported(err))
			return nil, err
		}
		m.agentConn = conn
		m.agentClient = agent.NewClient(conn)
	}
	signers, err := m.agentClient.Signers()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAgentUnavailable, err)
	}
	if len(signers) == 0 {
		return nil, ErrAgentUnavailable
	}

	hasSecurityKey := false
	for _, signer := range signers {
		if appconnection.IsSecurityKeyAlgorithm(signer.PublicKey().Type()) {
			hasSecurityKey = true
			break
		}
	}
	byBlob := make(map[string]appconnection.SecurityKey)
	if hasSecurityKey {
		keys, keyErr := m.keys.Identities(m.ctx)
		if keyErr != nil {
			m.recordActionableSecurityKeyFailure(keyErr)
		}
		for _, key := range keys {
			byBlob[string(key.Blob)] = key
		}
	}

	offered := make([]xssh.Signer, 0, len(signers))
	for _, signer := range signers {
		public := signer.PublicKey()
		if !appconnection.IsSecurityKeyAlgorithm(public.Type()) {
			offered = append(offered, signer)
			continue
		}
		key, ok := byBlob[string(public.Marshal())]
		if !ok {
			// The SK listing did not vouch for this identity, so it is not
			// offered through the unguarded x/crypto agent signer.
			continue
		}
		wrapper, err := newSecurityKeySigner(m.ctx, m.keys, key, m.recordActionableSecurityKeyFailure)
		if err != nil {
			m.recordActionableSecurityKeyFailure(err)
			continue
		}
		offered = append(offered, wrapper)
	}
	if len(offered) == 0 {
		return nil, ErrAgentUnavailable
	}
	return xssh.PublicKeys(offered...), nil
}

// recordActionableSecurityKeyFailure keeps only the reasons a user can act
// on (missing OpenSSH or a missed device deadline). Other agent or framing
// failures stay diagnostics and do not replace the ordinary authentication
// verdict.
func (m *authMachine) recordActionableSecurityKeyFailure(err error) {
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		return
	}
	if typed.Kind != appconnection.KindUnsupported && typed.Kind != appconnection.KindTimeout {
		return
	}
	if m.skFailure == nil {
		m.skFailure = err
	}
}

// challenge runs one keyboard-interactive round. A single password-looking
// non-echo prompt is auto-filled with the saved password, unless a password or
// keyboard-interactive factor already succeeded, or the prompt looks like an
// OTP/MFA request.
func (m *authMachine) challenge(name, instruction string, questions []string, echos []bool) ([]string, error) {
	if m.prompter == nil {
		return nil, authFailure(appconnection.ErrPromptUnavailable)
	}
	if answers, ok := m.autoFill(questions, echos); ok {
		return answers, nil
	}

	prompts := make([]appconnection.KeyboardInteractivePrompt, len(questions))
	for i, question := range questions {
		echo := true
		if i < len(echos) {
			echo = echos[i]
		}
		prompts[i] = appconnection.KeyboardInteractivePrompt{Text: question, Echo: echo}
	}
	decision, err := m.prompter.KeyboardInteractive(m.ctx, appconnection.KeyboardInteractiveRequest{
		Hostname:    m.hostname,
		Username:    m.plan.Username,
		Name:        name,
		Instruction: instruction,
		Prompts:     prompts,
	})
	if err != nil {
		return nil, m.classifyPromptError(err)
	}
	if decision.Cancelled {
		return nil, &appconnection.Error{
			Kind:  appconnection.KindCancelled,
			Cause: fmt.Errorf("%w: keyboard-interactive", appconnection.ErrPromptCancelled),
		}
	}
	if len(decision.Answers) != len(questions) {
		return nil, authFailure(errors.New("keyboard-interactive response count does not match prompts"))
	}
	return decision.Answers, nil
}

// passwordLikePrompt and mfaLikePrompt mirror the original's conservative
// auto-fill rules: only a reusable password prompt qualifies, and any OTP/MFA
// vocabulary disqualifies it.
var (
	passwordLikePrompt = regexp.MustCompile(`(?i)passw(or)?d|密\s*码|口\s*令`)
	mfaLikePrompt      = regexp.MustCompile(`(?i)one[\s-]?time|\botp\b|verification|passcode|\btoken\b|\b2fa\b|two[\s-]?factor|multi[\s-]?factor|\bmfa\b|second\s+factor|duo|动态|一次性|验证码|验证信息|令牌|双因素|多因素|短信验证|手机验证|二次|安全密码|挑战码`)
)

func (m *authMachine) autoFill(questions []string, echos []bool) ([]string, bool) {
	if m.kiAutofilled || m.passwordOK || m.kiSuccess > 0 || m.plan.Password == "" {
		return nil, false
	}
	if len(questions) != 1 || len(echos) != 1 || echos[0] {
		return nil, false
	}
	if mfaLikePrompt.MatchString(questions[0]) {
		return nil, false
	}
	if !passwordLikePrompt.MatchString(questions[0]) {
		return nil, false
	}
	m.kiAutofilled = true
	return []string{m.plan.Password}, true
}

func (m *authMachine) classifyPromptError(err error) error {
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(m.ctx.Err(), context.Canceled):
		return &appconnection.Error{
			Kind:  appconnection.KindCancelled,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrPromptCancelled, err),
		}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(m.ctx.Err(), context.DeadlineExceeded):
		return &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrAuthTimeout, err),
		}
	default:
		return authFailure(err)
	}
}

// classifyKeyFailure maps key-format failures onto typed connection errors.
// The cause retains the key-format sentinel for diagnostics but never carries
// key material.
func classifyKeyFailure(err error) error {
	if errors.Is(err, ErrKeyUnsupported) {
		return &appconnection.Error{
			Kind:  appconnection.KindUnsupported,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrUnsupportedAlgorithm, err),
		}
	}
	return authFailure(err)
}

func authFailure(cause error) error {
	return &appconnection.Error{Kind: appconnection.KindAuth, Cause: cause}
}
