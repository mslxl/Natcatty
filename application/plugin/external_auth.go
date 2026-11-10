package plugin

import (
	"context"
	"strings"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// authAnswerBytes bounds one answer to a host prompt. It is an opaque
// response, never a credential: the host broker resolves credentials from the
// Vault and returns no plaintext.
const authAnswerBytes = 4 << 10

// authCancelTimeout bounds a best-effort cancel that runs during revocation
// or unload, when no guest context exists.
const authCancelTimeout = 2 * time.Second

// authChallenge is one live host-owned authentication flow.
type authChallenge struct {
	id         string
	hostID     string
	method     AuthMethod
	scope      domainplugin.Scope
	canRespond bool
	createdAt  time.Time
}

// cancel releases the challenge in the host broker. It is used when the
// fiber unloads or loses its authority.
func (c *authChallenge) cancel(broker AuthBroker) {
	if c == nil || broker == nil || c.id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), authCancelTimeout)
	defer cancel()
	_ = broker.Cancel(ctx, c.id)
}

// AuthBegin starts a host-owned authentication flow. The plugin sees only a
// challenge description; credential collection and resolution stay host-side.
func (s *ServiceSession) AuthBegin(ctx context.Context, hostID string,
	method AuthMethod) (AuthChallenge, error) {
	hostID = strings.TrimSpace(hostID)
	method = AuthMethod(strings.ToLower(strings.TrimSpace(string(method))))
	if hostID == "" {
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, domainplugin.Scope{},
			workspaceInvalid("auth.begin requires a Host ID"))
	}
	if !authMethodKnown(method) {
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: hostID},
			workspaceInvalid("unknown authentication method %q", method))
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: hostID}
	if err := s.check(ServiceAuth, "begin", domainplugin.PermissionAuthInvoke, scope); err != nil {
		return AuthChallenge{}, err
	}
	if s.host.auth == nil {
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, scope, externalUnavailable("authentication broker"))
	}

	epoch, err := s.reserveAuthChallenge()
	if err != nil {
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, scope, err)
	}
	// Broker work may exceed one guest compute slice and must stop on
	// revocation or unload.
	workCtx, cancelWork := s.workContext(ctx)
	defer cancelWork()
	challenge, err := s.host.auth.Begin(workCtx, AuthBeginRequest{HostID: hostID, Method: method})
	if err != nil {
		s.releaseAuthReservation()
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, scope,
			workspaceErrorf(ServiceCodeFault, "authentication broker: %v", err))
	}
	challenge.HostID = hostID
	challenge.Method = method
	challenge.ID = strings.TrimSpace(challenge.ID)
	if challenge.ID == "" {
		s.releaseAuthReservation()
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, scope,
			workspaceErrorf(ServiceCodeFault, "authentication broker returned no challenge id"))
	}
	if revoked := s.capabilityRevoked(epoch, domainplugin.PermissionAuthInvoke, scope); revoked != nil {
		s.releaseAuthReservation()
		cancelCtx, cancel := context.WithTimeout(context.Background(), authCancelTimeout)
		_ = s.host.auth.Cancel(cancelCtx, challenge.ID)
		cancel()
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, scope,
			revokedServiceError(ServiceAuth, "begin", s.subject,
				domainplugin.PermissionAuthInvoke, scope,
				"the plugin capability was revoked during authentication"))
	}
	if err := s.commitAuthChallenge(epoch, challenge); err != nil {
		cancelCtx, cancel := context.WithTimeout(context.Background(), authCancelTimeout)
		_ = s.host.auth.Cancel(cancelCtx, challenge.ID)
		cancel()
		return AuthChallenge{}, s.recordFailure(ServiceAuth, "begin",
			domainplugin.PermissionAuthInvoke, scope, err)
	}
	s.recordSuccess(ServiceAuth, "begin", domainplugin.PermissionAuthInvoke, scope)
	return challenge, nil
}

// AuthRespond answers a host prompt. answer is an opaque response to the
// host-owned flow; the service never treats it as a credential, never returns
// it, and never records it.
func (s *ServiceSession) AuthRespond(ctx context.Context, challengeID,
	answer string) (AuthResult, error) {
	challenge, err := s.liveAuthChallenge(challengeID)
	if err != nil {
		return AuthResult{}, s.recordFailure(ServiceAuth, "respond",
			domainplugin.PermissionAuthInvoke, domainplugin.Scope{}, err)
	}
	if err := s.check(ServiceAuth, "respond", domainplugin.PermissionAuthInvoke, challenge.scope); err != nil {
		return AuthResult{}, err
	}
	if s.host.auth == nil {
		return AuthResult{}, s.recordFailure(ServiceAuth, "respond",
			domainplugin.PermissionAuthInvoke, challenge.scope, externalUnavailable("authentication broker"))
	}
	if len(answer) > authAnswerBytes {
		return AuthResult{}, s.recordFailure(ServiceAuth, "respond",
			domainplugin.PermissionAuthInvoke, challenge.scope,
			workspaceLimit("auth.respond answer exceeds %d bytes", authAnswerBytes))
	}
	if err := validateAuthAnswer(answer); err != nil {
		return AuthResult{}, s.recordFailure(ServiceAuth, "respond",
			domainplugin.PermissionAuthInvoke, challenge.scope, err)
	}
	epoch := s.handleEpochSnapshot()
	// Broker work may exceed one guest compute slice and must stop on
	// revocation or unload.
	workCtx, cancelWork := s.workContext(ctx)
	defer cancelWork()
	result, err := s.host.auth.Respond(workCtx, challenge.id, answer)
	if err != nil {
		return AuthResult{}, s.recordFailure(ServiceAuth, "respond",
			domainplugin.PermissionAuthInvoke, challenge.scope,
			workspaceErrorf(ServiceCodeFault, "authentication broker: %v", err))
	}
	// A revocation that raced the broker wins: the flow is gone, so the
	// success must not be reported or audited as a completed authentication.
	if revoked := s.capabilityRevoked(epoch, domainplugin.PermissionAuthInvoke, challenge.scope); revoked != nil {
		s.removeAuthChallenge(challenge)
		return AuthResult{}, s.recordFailure(ServiceAuth, "respond",
			domainplugin.PermissionAuthInvoke, challenge.scope,
			revokedServiceError(ServiceAuth, "respond", s.subject,
				domainplugin.PermissionAuthInvoke, challenge.scope,
				"the plugin capability was revoked during authentication"))
	}
	result.ChallengeID = challenge.id
	if authStatusTerminal(result.Status) {
		s.removeAuthChallenge(challenge)
	}
	s.record(ServiceAuth, "respond", domainplugin.PermissionAuthInvoke,
		challenge.scope, string(result.Status))
	return result, nil
}

// AuthCancel cancels one host-owned authentication flow.
func (s *ServiceSession) AuthCancel(ctx context.Context, challengeID string) error {
	challenge, err := s.liveAuthChallenge(challengeID)
	if err != nil {
		return s.recordFailure(ServiceAuth, "cancel",
			domainplugin.PermissionAuthInvoke, domainplugin.Scope{}, err)
	}
	if err := s.check(ServiceAuth, "cancel", domainplugin.PermissionAuthInvoke, challenge.scope); err != nil {
		return err
	}
	if s.host.auth != nil {
		if err := s.host.auth.Cancel(ctx, challenge.id); err != nil {
			return s.recordFailure(ServiceAuth, "cancel",
				domainplugin.PermissionAuthInvoke, challenge.scope,
				workspaceErrorf(ServiceCodeFault, "authentication broker: %v", err))
		}
	}
	s.removeAuthChallenge(challenge)
	s.recordSuccess(ServiceAuth, "cancel", domainplugin.PermissionAuthInvoke, challenge.scope)
	return nil
}

func authStatusTerminal(status AuthStatus) bool {
	switch status {
	case AuthStatusSucceeded, AuthStatusFailed, AuthStatusCancelled:
		return true
	default:
		return false
	}
}

func validateAuthAnswer(answer string) error {
	for _, r := range answer {
		if r < 0x20 || r == 0x7f {
			return workspaceInvalid("auth.respond answer contains a control character")
		}
	}
	return nil
}

func (s *ServiceSession) reserveAuthChallenge() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, workspaceInvalid("the plugin instance is closed")
	}
	if len(s.challenges)+s.pendingAuth >= s.host.limits.AuthChallenges {
		return 0, workspaceLimit("authentication challenge limit of %d reached",
			s.host.limits.AuthChallenges)
	}
	// The broker call can take a while, so the in-flight slot is counted
	// before it starts.
	s.pendingAuth++
	return s.handleEpoch, nil
}

func (s *ServiceSession) releaseAuthReservation() {
	s.mu.Lock()
	if s.pendingAuth > 0 {
		s.pendingAuth--
	}
	s.mu.Unlock()
}

func (s *ServiceSession) commitAuthChallenge(epoch uint64, challenge AuthChallenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingAuth > 0 {
		s.pendingAuth--
	}
	if s.closed || s.handleEpoch != epoch {
		return workspaceInvalid("the plugin instance was revoked while beginning authentication")
	}
	if _, exists := s.challenges[challenge.ID]; exists {
		return workspaceConflict("authentication challenge already exists")
	}
	s.challenges[challenge.ID] = &authChallenge{
		id:         challenge.ID,
		hostID:     challenge.HostID,
		method:     challenge.Method,
		scope:      domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: challenge.HostID},
		canRespond: challenge.CanRespond,
		createdAt:  s.host.now().UTC(),
	}
	return nil
}

func (s *ServiceSession) liveAuthChallenge(challengeID string) (*authChallenge, error) {
	challengeID = strings.TrimSpace(challengeID)
	if challengeID == "" {
		return nil, &ServiceError{Service: ServiceAuth, Method: "challenge",
			Code: ServiceCodeInvalid, Permission: domainplugin.PermissionAuthInvoke,
			Message: "an authentication challenge ID is required"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	challenge, ok := s.challenges[challengeID]
	if !ok || challenge == nil || challenge.id == "" {
		return nil, &ServiceError{Service: ServiceAuth, Method: "challenge",
			Code: ServiceCodeNotFound, Permission: domainplugin.PermissionAuthInvoke,
			Message: "authentication challenge does not exist"}
	}
	return challenge, nil
}

func (s *ServiceSession) removeAuthChallenge(challenge *authChallenge) {
	if s == nil || challenge == nil {
		return
	}
	s.mu.Lock()
	if existing, ok := s.challenges[challenge.id]; ok && existing == challenge {
		delete(s.challenges, challenge.id)
	}
	s.mu.Unlock()
}
