package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// DataConnection is one provider-owned connection recorded by the host. It is
// not a Host and not a Session: it carries a profile, a mode, a provider token,
// and the pool state only.
type DataConnection struct {
	provider  *DataProvider
	profileID string
	mode      domainplugin.DataAccessMode
	token     uint64

	// leaseCount is 1 while a DataLease owns the connection and 0 while it sits
	// in the pool. queries tracks interactive results that still return More.
	leaseCount int
	queries    map[uint64]*DataQuery
	// closed is set once under the host mutex when any path claims the
	// connection's teardown. closeClaimed records that claim, so exactly one
	// path performs the guest close and writes the one close audit row even
	// when Unregister, the idle expirer, a lease, and a ping-reuse race all
	// observe the same connection.
	closed       bool
	closeClaimed bool
	idleGen      uint64
	cancelIdle   func()
	// guard holds the resolved plaintext secrets for this connection's
	// lifetime so every provider-visible message can be checked for an echo.
	guard *dataSecretGuard
}

// DataLease is one acquired profile connection. Release returns it to the
// idle pool; Close tells the provider to release it immediately. A lease is
// safe for concurrent use.
type DataLease struct {
	host       *DataProviderHost
	connection *DataConnection
	mu         sync.Mutex
	released   bool
}

// ProfileID returns the profile the connection was opened from.
func (l *DataLease) ProfileID() string {
	if l == nil || l.connection == nil {
		return ""
	}
	return l.connection.profileID
}

// Mode returns the access mode the connection was opened in.
func (l *DataLease) Mode() domainplugin.DataAccessMode {
	if l == nil || l.connection == nil {
		return ""
	}
	return l.connection.mode
}

// ProviderID returns the qualified provider ID.
func (l *DataLease) ProviderID() string {
	if l == nil || l.connection == nil {
		return ""
	}
	return l.connection.provider.qualifiedID
}

// ConnectionID returns the provider's opaque connection token.
func (l *DataLease) ConnectionID() uint64 {
	if l == nil || l.connection == nil {
		return 0
	}
	return l.connection.token
}

func (l *DataLease) usable() (*DataConnection, error) {
	if l == nil || l.host == nil || l.connection == nil {
		return nil, ErrDataUnavailable
	}
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	if released {
		return nil, &DataError{
			Kind: DataErrorFaulted, Provider: l.connection.provider.qualifiedID,
			Message: "lease is no longer held",
		}
	}
	l.host.mu.Lock()
	closed := l.connection.closed
	l.host.mu.Unlock()
	if closed {
		return nil, &DataError{
			Kind: DataErrorFaulted, Provider: l.connection.provider.qualifiedID,
			Message: "connection is closed",
		}
	}
	return l.connection, nil
}

// Acquire opens or reuses one profile connection. The requested mode defaults
// to the profile's stored mode and can never exceed it or the activation
// grant. An idle pooled connection is pinged before reuse; a failed ping is
// discarded and a fresh connection is opened. Every refused escalation is
// audited.
func (h *DataProviderHost) Acquire(ctx context.Context, profileID string,
	mode domainplugin.DataAccessMode) (*DataLease, error) {

	if h == nil || h.profiles == nil {
		return nil, ErrDataUnavailable
	}
	mode = domainplugin.NormalizeDataAccessMode(mode)
	if !mode.Valid() {
		return nil, &DataError{Kind: DataErrorInvalid, Message: "unknown access mode"}
	}
	profile, err := h.profiles.Get(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if err := domainplugin.ValidateDataProfile(profile); err != nil {
		return nil, err
	}
	provider, generation := h.providerRegistration(profile.Provider)
	if provider == nil {
		return nil, &DataError{
			Kind: DataErrorUnavailable, Provider: profile.Provider,
			Message: "the data provider is not active",
		}
	}
	storedMode := domainplugin.NormalizeDataAccessMode(profile.Mode)
	if !provider.maxMode.AllowsMode(mode) || !storedMode.AllowsMode(mode) {
		h.recordDataEvent(provider, "open", "escalation", mode, "", 0, 0)
		return nil, &DataError{
			Kind: DataErrorDenied, Provider: provider.qualifiedID,
			Message: "the requested access mode exceeds the profile or provider grant",
		}
	}

	for {
		connection := h.takeIdleConnection(profileID, mode)
		if connection == nil {
			break
		}
		started := h.now()
		err := connection.provider.ping(ctx, connection.token, connection.guard)
		h.recordDataEvent(provider, "ping", dataStatusWord(err), mode, "",
			h.now().Sub(started), 0)
		if err == nil {
			// Unregister may have swept the pool while the ping was in flight.
			// Do not hand out a lease whose registration is gone.
			h.mu.Lock()
			live := h.registrationLiveLocked(provider, generation) &&
				!connection.closed && connection.leaseCount > 0
			h.mu.Unlock()
			if !live {
				h.closeConnectionNow(connection, "unregistered")
				return nil, &DataError{
					Kind: DataErrorUnavailable, Provider: provider.qualifiedID,
					Message: "the data provider was unregistered while the connection was being reused",
				}
			}
			h.recordDataEvent(provider, "reuse", "ok", mode, "", 0, 0)
			return &DataLease{host: h, connection: connection}, nil
		}
		h.closeConnectionNow(connection, "ping-failed")
	}

	secrets, err := h.resolveDataSecrets(ctx, provider, profile, nil)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	count := h.connectionCountLocked(profileID)
	h.mu.Unlock()
	if count >= provider.contribution.Capabilities.EffectiveMaxConnections() {
		h.recordDataEvent(provider, "open", "limit", mode, "", 0, 0)
		return nil, &DataError{
			Kind: DataErrorLimit, Provider: provider.qualifiedID,
			Message: "the profile connection limit is reached",
		}
	}
	openProfile := profile
	openProfile.Mode = mode
	started := h.now()
	response, err := provider.open(ctx, dataOpenRequest{
		Profile: openProfile, Fields: profile.Fields, Secrets: secrets,
	})
	elapsed := h.now().Sub(started)
	if err != nil {
		h.recordDataEvent(provider, "open", dataErrorStatus(err), mode, "", elapsed, 0)
		return nil, err
	}
	if response.Connection == 0 {
		err := provider.protocolError("provider returned no connection token")
		h.recordDataEvent(provider, "open", string(DataErrorProtocol), mode, "", elapsed, 0)
		return nil, err
	}
	connection := &DataConnection{
		provider: provider, profileID: profileID, mode: mode,
		token:      response.Connection,
		leaseCount: 1, queries: make(map[uint64]*DataQuery),
		guard: newDataSecretGuard(secrets),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		// The host Close path may have marked the instance inactive before this
		// in-flight open returned, so the ordinary close path would refuse to
		// reach the guest. Use the forced close: the connection was never
		// published, so this path owns the token and closes it exactly once.
		provider.closeConnection(connection.token)
		return nil, ErrClosed
	}
	// The guest open runs without the host lock, so Unregister can sweep the
	// registration while it is in flight. Re-check the live registration and
	// its generation under the same lock that publishes the connection: if the
	// provider is gone, the freshly opened connection is torn down and never
	// enters h.connections.
	if !h.registrationLiveLocked(provider, generation) {
		h.mu.Unlock()
		provider.closeConnection(connection.token)
		h.recordDataEvent(provider, "open", "unregistered", mode, "", elapsed, 0)
		return nil, &DataError{
			Kind: DataErrorUnavailable, Provider: provider.qualifiedID,
			Message: "the data provider was unregistered while the connection was opening",
		}
	}
	if h.findConnectionLocked(provider, connection.token) != nil {
		h.mu.Unlock()
		provider.closeConnection(connection.token)
		h.recordDataEvent(provider, "open", "duplicate-token", mode, "", elapsed, 0)
		return nil, provider.protocolError("provider reused an open connection token")
	}
	if h.connectionCountLocked(profileID) >=
		provider.contribution.Capabilities.EffectiveMaxConnections() {
		h.mu.Unlock()
		provider.closeConnection(connection.token)
		h.recordDataEvent(provider, "open", "limit", mode, "", elapsed, 0)
		return nil, &DataError{
			Kind: DataErrorLimit, Provider: provider.qualifiedID,
			Message: "the profile connection limit is reached",
		}
	}
	h.connections[connection] = struct{}{}
	h.mu.Unlock()
	h.recordDataEvent(provider, "open", "ok", mode, "", elapsed, 0)
	return &DataLease{host: h, connection: connection}, nil
}

// resolveDataSecrets resolves every stored reference, with seed values (a
// draft's new secrets) taking precedence over the stored reference for the
// same field. Only references that need a Vault read are audited.
func (h *DataProviderHost) resolveDataSecrets(ctx context.Context,
	provider *DataProvider, profile domainplugin.DataProfile,
	seed map[string]string) (map[string]string, error) {

	secrets := make(map[string]string, len(profile.SecretRefs)+len(seed))
	for field, value := range seed {
		secrets[field] = value
	}
	for field, ref := range profile.SecretRefs {
		if _, ok := secrets[field]; ok {
			continue
		}
		if h.secrets == nil {
			return nil, ErrDataUnavailable
		}
		value, err := h.secrets.Get(ctx, ref)
		if err != nil {
			h.recordDataEvent(provider, "secret", "missing", profile.Mode, "", 0, 0)
			if errors.Is(err, ErrDataSecretNotFound) {
				return nil, &DataError{
					Kind: DataErrorNotFound, Provider: provider.qualifiedID,
					Message: "a stored profile secret is missing",
				}
			}
			return nil, err
		}
		if len(value) > domainplugin.MaxDataProfileSecretValue {
			h.recordDataEvent(provider, "secret", "limit", profile.Mode, "",
				0, len(value))
			return nil, &DataError{
				Kind: DataErrorLimit, Provider: provider.qualifiedID,
				Message: "a stored profile secret exceeds the value limit",
			}
		}
		secrets[field] = value
		h.recordDataEvent(provider, "secret", "ok", profile.Mode, "", 0, len(value))
	}
	if len(secrets) == 0 {
		return nil, nil
	}
	return secrets, nil
}

func (p *DataProvider) ping(ctx context.Context, token uint64,
	guard *dataSecretGuard) error {

	ctx, cancel := context.WithTimeout(nilContext(ctx), p.host.limits.CallTimeout)
	defer cancel()
	_, err := p.invoke(ctx, domainpluginabi.DataProviderMethodPing,
		dataConnectionPayload{Connection: token}, p.host.limits.CallTimeout, guard)
	return err
}

// Release returns the connection to the idle pool. A connection with an
// unfinished query is closed instead, because a partially consumed result
// cannot be resumed by the next lease.
func (l *DataLease) Release() {
	if l == nil || l.host == nil || l.connection == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	l.mu.Unlock()
	l.host.releaseConnection(l.connection)
}

// Close asks the provider to release the connection immediately and removes
// it from the pool.
func (l *DataLease) Close(ctx context.Context) error {
	if l == nil || l.host == nil || l.connection == nil {
		return nil
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	l.mu.Unlock()
	return l.host.closeConnectionNow(l.connection, "ok")
}

// Ping checks that the connection is still usable.
func (l *DataLease) Ping(ctx context.Context) error {
	connection, err := l.usable()
	if err != nil {
		return err
	}
	started := l.host.now()
	err = connection.provider.ping(ctx, connection.token, connection.guard)
	l.host.recordDataEvent(connection.provider, "ping", dataStatusWord(err),
		connection.mode, "", l.host.now().Sub(started), 0)
	return err
}

// Capabilities asks the provider to refine its static capabilities for this
// connection. A refinement may narrow the family feature set but never widen
// it.
func (l *DataLease) Capabilities(ctx context.Context) (domainplugin.DataCapabilities, error) {
	connection, err := l.usable()
	if err != nil {
		return domainplugin.DataCapabilities{}, err
	}
	response, err := connection.provider.invoke(ctx,
		domainpluginabi.DataProviderMethodCapabilities,
		dataConnectionPayload{Connection: connection.token},
		l.host.limits.CallTimeout, connection.guard)
	if err != nil {
		l.host.recordDataEvent(connection.provider, "capabilities",
			dataErrorStatus(err), connection.mode, "", 0, 0)
		return domainplugin.DataCapabilities{}, err
	}
	if response.Capabilities == nil {
		return domainplugin.DataCapabilities{}, connection.provider.protocolError(
			"provider returned no capabilities")
	}
	refined := *response.Capabilities
	static := connection.provider.contribution.Capabilities
	if err := validateDataCapabilities(static, refined); err != nil {
		// The refinement error quotes the guest's family and feature names.
		// Scan the complete text against the connection's resolved secrets
		// before bounding it, so a truncated echo cannot leak a long secret.
		rawMessage := err.Error()
		message := boundedDataText(rawMessage, 256)
		result := "protocol-error"
		if connection.guard.echoes(rawMessage) {
			message = "the provider echoed secret content"
			result = "secret-echo"
		}
		l.host.recordDataEvent(connection.provider, "capabilities", result,
			connection.mode, "", 0, 0)
		return domainplugin.DataCapabilities{}, &DataError{
			Kind: DataErrorProtocol, Provider: connection.provider.qualifiedID,
			Message: message,
		}
	}
	// A refinement that leaves the bound at the zero default must not report
	// more connections than the static declaration allows.
	if refined.MaxConnections <= 0 {
		refined.MaxConnections = static.EffectiveMaxConnections()
	}
	l.host.recordDataEvent(connection.provider, "capabilities", "ok",
		connection.mode, "", 0, 0)
	return refined, nil
}

// validateDataCapabilities checks a guest's per-connection refinement against
// the host-held static declaration. The refined family and features are
// untrusted guest text, so no error may quote them: a provider that returns a
// secret as a family or feature name must not have it reflected through a
// caller-visible message (quoting would also escape quotes, backslashes,
// newlines, tabs, and NULs past a raw-byte secret scan). Every rejection is a
// constant message; only the static family, which was validated at
// registration and comes from the host-held contribution, is named.
func validateDataCapabilities(static, refined domainplugin.DataCapabilities) error {
	if !refined.Family.Valid() {
		return fmt.Errorf("data provider: refined capability family is not a known family")
	}
	if refined.Family != static.Family {
		return fmt.Errorf("data provider: refined capability family does not match the declared %s family",
			static.Family)
	}
	for index, feature := range refined.Features {
		if !static.Supports(feature) {
			return fmt.Errorf("data provider: refined feature at index %d is not a declared capability",
				index)
		}
	}
	if refined.MaxConnections < 0 ||
		refined.MaxConnections > static.EffectiveMaxConnections() {
		return fmt.Errorf("data provider: refined connection bound exceeds the declaration")
	}
	return nil
}

// DataQueryRequest is one typed query. Export disables the interactive budget;
// the caller must already have the export authority.
type DataQueryRequest struct {
	Query      string
	Parameters []domainplugin.DataValue
	Export     bool
}

// DataCommandRequest is one provider-specific command. The provider classifies
// it in the result envelope; the host rejects a class above the profile mode.
type DataCommandRequest struct {
	Command    string
	Parameters []domainplugin.DataValue
}

// DataQuery is one started result stream. Fetch continues it only while the
// provider reports More. A cancelled query returns the delivered batch marked
// Partial and never asks for more.
type DataQuery struct {
	host       *DataProviderHost
	lease      *DataLease
	connection *DataConnection
	token      uint64
	class      domainplugin.DataOperationClass
	export     bool

	mu        sync.Mutex
	rows      int
	bytes     int
	cancelled bool
	done      bool
}

// ID returns the provider's opaque query token.
func (q *DataQuery) ID() uint64 {
	if q == nil {
		return 0
	}
	return q.token
}

// Class returns the operation class the provider reported when the query
// started.
func (q *DataQuery) Class() domainplugin.DataOperationClass {
	if q == nil {
		return domainplugin.DataClassUnknown
	}
	return q.class
}

// Query starts one typed query and returns the first result batch. The host
// validates the envelope, enforces the operation class and input limits, and
// applies the interactive budget before the caller sees any value.
func (l *DataLease) Query(ctx context.Context,
	request DataQueryRequest) (*DataQuery, domainplugin.DataResult, error) {

	connection, err := l.usable()
	if err != nil {
		return nil, domainplugin.DataResult{}, err
	}
	provider := connection.provider
	if err := checkDataInput(provider, request.Query, request.Parameters); err != nil {
		return nil, domainplugin.DataResult{}, err
	}
	started := l.host.now()
	response, err := provider.invoke(ctx, domainpluginabi.DataProviderMethodQuery,
		dataQueryPayload{
			Connection: connection.token, Query: request.Query,
			Parameters: request.Parameters,
		}, l.host.limits.FetchTimeout, connection.guard)
	if err != nil {
		l.host.recordDataEvent(provider, "query", dataErrorStatus(err),
			connection.mode, "", l.host.now().Sub(started), 0)
		return nil, domainplugin.DataResult{}, err
	}
	if response.Result == nil {
		l.host.recordDataEvent(provider, "query", "protocol-error",
			connection.mode, "", l.host.now().Sub(started), 0)
		return nil, domainplugin.DataResult{}, provider.protocolError(
			"provider returned no query result")
	}
	result := *response.Result
	// The class is guest text. Map it onto the host's closed vocabulary before
	// it can reach an audit row, the returned envelope, or the tracked query.
	// An invalid value is treated as unknown and denied; the constant safe
	// class is the only thing the audit records.
	class, classValid := hostDataClass(result.Class)
	result.Class = class
	// A would-block result carries no payload and never executed an operation,
	// so it is a control envelope rather than a classified operation. An
	// invalid class is always denied, even in full mode.
	if !classValid || (!result.WouldBlock && !connection.mode.AllowsClass(class)) {
		l.host.recordDataEvent(provider, "query", "denied", connection.mode,
			class, 0, 0)
		return nil, domainplugin.DataResult{}, &DataError{
			Kind: DataErrorDenied, Provider: provider.qualifiedID,
			Message: "the query operation class is not allowed in this mode",
		}
	}
	if err := result.Validate(); err != nil {
		l.host.recordDataEvent(provider, "query", "protocol-error",
			connection.mode, class, 0, 0)
		return nil, domainplugin.DataResult{}, provider.protocolError(
			"invalid query result envelope")
	}
	// Plan text and a summary message are host-visible diagnostics. The opaque
	// row/document payloads are provider-owned data and are not scanned; see
	// echoesResultDiagnostics.
	if connection.guard.echoesResultDiagnostics(result) {
		l.host.recordDataEvent(provider, "query", "secret-echo",
			connection.mode, class, 0, 0)
		return nil, domainplugin.DataResult{}, provider.secretEchoError()
	}
	if err := checkDataStreaming(provider, result); err != nil {
		l.host.recordDataEvent(provider, "query", "unsupported", connection.mode,
			class, 0, 0)
		return nil, domainplugin.DataResult{}, err
	}
	if result.More && response.Query == 0 {
		return nil, domainplugin.DataResult{}, provider.protocolError(
			"provider reported more rows without a query token")
	}
	query := &DataQuery{
		host: l.host, lease: l, connection: connection,
		token: response.Query, class: class, export: request.Export,
	}
	result = query.apply(result)
	// Only an unfinished query keeps the connection out of the pool. A provider
	// that returns a token with More=false has finished the stream, so the
	// token is not tracked and Release can pool the connection.
	if response.Query != 0 && (result.More || result.WouldBlock) {
		l.host.trackQuery(connection, query)
	}
	l.host.recordDataEvent(provider, "query", dataResultWord(result),
		connection.mode, class, l.host.now().Sub(started),
		result.DataResultBytes())
	return query, result, nil
}

// Fetch asks for the next batch of an interactive result. The host stops at
// the row or byte budget and reports Limit; a cancellation marks the batch
// Partial and clears More.
func (q *DataQuery) Fetch(ctx context.Context) (domainplugin.DataResult, error) {
	if q == nil || q.host == nil || q.connection == nil {
		return domainplugin.DataResult{}, ErrDataUnavailable
	}
	q.mu.Lock()
	done, cancelled := q.done, q.cancelled
	q.mu.Unlock()
	if cancelled {
		return partialDataResult(), nil
	}
	if done {
		return domainplugin.DataResult{}, &DataError{
			Kind: DataErrorConflict, Provider: q.connection.provider.qualifiedID,
			Message: "query has no more batches",
		}
	}
	connection, err := q.lease.usable()
	if err != nil {
		return domainplugin.DataResult{}, err
	}
	provider := connection.provider
	started := q.host.now()
	response, err := provider.invoke(ctx, domainpluginabi.DataProviderMethodFetch,
		dataFetchPayload{Connection: connection.token, Query: q.token},
		q.host.limits.FetchTimeout, connection.guard)
	if err != nil {
		var dataErr *DataError
		if errors.As(err, &dataErr) && dataErr.Kind == DataErrorCancelled {
			q.markCancelled()
			q.host.recordDataEvent(provider, "fetch", "cancelled",
				connection.mode, q.class, q.host.now().Sub(started), 0)
			return partialDataResult(), nil
		}
		q.host.recordDataEvent(provider, "fetch", dataErrorStatus(err),
			connection.mode, q.class, q.host.now().Sub(started), 0)
		return domainplugin.DataResult{}, err
	}
	if response.Result == nil {
		return domainplugin.DataResult{}, provider.protocolError(
			"provider returned no fetch result")
	}
	result := *response.Result
	rawClass := result.Class
	if rawClass == "" {
		rawClass = q.class
	}
	// Never trust or persist the fetch class as guest text. An invalid value is
	// treated as unknown and denied, even in full mode.
	class, classValid := hostDataClass(rawClass)
	result.Class = class
	if !classValid || (!result.WouldBlock && !connection.mode.AllowsClass(class)) {
		q.host.recordDataEvent(provider, "fetch", "denied", connection.mode,
			class, 0, 0)
		return domainplugin.DataResult{}, &DataError{
			Kind: DataErrorDenied, Provider: provider.qualifiedID,
			Message: "the fetch operation class is not allowed in this mode",
		}
	}
	if err := result.Validate(); err != nil {
		q.host.recordDataEvent(provider, "fetch", "protocol-error",
			connection.mode, class, 0, 0)
		return domainplugin.DataResult{}, provider.protocolError(
			"invalid fetch result envelope")
	}
	if connection.guard.echoesResultDiagnostics(result) {
		q.host.recordDataEvent(provider, "fetch", "secret-echo",
			connection.mode, class, 0, 0)
		return domainplugin.DataResult{}, provider.secretEchoError()
	}
	if err := checkDataStreaming(provider, result); err != nil {
		q.host.recordDataEvent(provider, "fetch", "unsupported", connection.mode,
			class, 0, 0)
		return domainplugin.DataResult{}, err
	}
	result = q.apply(result)
	if !result.More && !result.WouldBlock {
		q.host.untrackQuery(connection, q.token)
	}
	q.host.recordDataEvent(provider, "fetch", dataResultWord(result),
		connection.mode, class, q.host.now().Sub(started),
		result.DataResultBytes())
	return result, nil
}

// Cancel stops a query. The host marks the query cancelled and asks the guest
// to stop; a guest already holding the call slot learns from the cancellation
// poll instead. Delivered rows are reported as partial by the next fetch.
func (q *DataQuery) Cancel(ctx context.Context) error {
	if q == nil || q.host == nil || q.connection == nil {
		return ErrDataUnavailable
	}
	q.markCancelled()
	provider := q.connection.provider
	if err := provider.requireFeature(domainplugin.DataFeatureCancellation); err != nil {
		q.host.recordDataEvent(provider, "cancel", "unsupported",
			q.connection.mode, q.class, 0, 0)
		return err
	}
	_, err := provider.invoke(ctx, domainpluginabi.DataProviderMethodCancel,
		dataCancelPayload{Connection: q.connection.token, Query: q.token},
		q.host.limits.CallTimeout, q.connection.guard)
	if err == nil || errors.Is(err, ErrCallInFlight) {
		q.host.untrackQuery(q.connection, q.token)
		q.host.recordDataEvent(provider, "cancel", "ok", q.connection.mode,
			q.class, 0, 0)
		return nil
	}
	q.host.recordDataEvent(provider, "cancel", dataErrorStatus(err),
		q.connection.mode, q.class, 0, 0)
	return err
}

func (q *DataQuery) markCancelled() {
	q.mu.Lock()
	q.cancelled = true
	q.done = true
	q.mu.Unlock()
}

// apply enforces the interactive budget and the cancellation marker. It is
// the only place a result batch becomes visible to the caller.
func (q *DataQuery) apply(result domainplugin.DataResult) domainplugin.DataResult {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancelled {
		result.Partial = true
		result.More = false
	}
	if !q.export {
		remainingRows := q.host.limits.InteractiveRows - q.rows
		remainingBytes := q.host.limits.InteractiveBytes - q.bytes
		if remainingRows < 0 {
			remainingRows = 0
		}
		if remainingBytes < 0 {
			remainingBytes = 0
		}
		trimmed, dropped := domainplugin.TruncateDataResult(result,
			remainingRows, remainingBytes)
		if dropped {
			trimmed.Limit = true
			trimmed.More = false
		}
		result = trimmed
		q.rows += result.DataRows()
		q.bytes += result.DataResultBytes()
		if result.More && (q.rows >= q.host.limits.InteractiveRows ||
			q.bytes >= q.host.limits.InteractiveBytes) {
			result.Limit = true
			result.More = false
		}
	} else {
		q.rows += result.DataRows()
		q.bytes += result.DataResultBytes()
	}
	if !result.More && !result.WouldBlock {
		q.done = true
	}
	return result
}

func partialDataResult() domainplugin.DataResult {
	return domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{},
		Partial: true,
	}
}

// Command runs one provider-specific command. The mutations feature is
// required and the reported class must fit the profile mode.
func (l *DataLease) Command(ctx context.Context,
	request DataCommandRequest) (domainplugin.DataResult, error) {

	connection, err := l.usable()
	if err != nil {
		return domainplugin.DataResult{}, err
	}
	provider := connection.provider
	if err := provider.requireFeature(domainplugin.DataFeatureMutations); err != nil {
		l.host.recordDataEvent(provider, "command", "unsupported",
			connection.mode, domainplugin.DataClassUnknown, 0, 0)
		return domainplugin.DataResult{}, err
	}
	if err := checkDataInput(provider, request.Command, request.Parameters); err != nil {
		return domainplugin.DataResult{}, err
	}
	started := l.host.now()
	response, err := provider.invoke(ctx, domainpluginabi.DataProviderMethodCommand,
		dataCommandPayload{
			Connection: connection.token, Command: request.Command,
			Parameters: request.Parameters,
		}, l.host.limits.FetchTimeout, connection.guard)
	if err != nil {
		l.host.recordDataEvent(provider, "command", dataErrorStatus(err),
			connection.mode, "", l.host.now().Sub(started), 0)
		return domainplugin.DataResult{}, err
	}
	if response.Result == nil {
		return domainplugin.DataResult{}, provider.protocolError(
			"provider returned no command result")
	}
	result := *response.Result
	// The provider classifies a command, but only the host's closed vocabulary
	// may reach the audit or the returned envelope. An invalid value is treated
	// as unknown and denied.
	class, classValid := hostDataClass(result.Class)
	result.Class = class
	if !classValid || !connection.mode.AllowsClass(class) {
		l.host.recordDataEvent(provider, "command", "denied", connection.mode,
			class, 0, 0)
		return domainplugin.DataResult{}, &DataError{
			Kind: DataErrorDenied, Provider: provider.qualifiedID,
			Message: "the command operation class is not allowed in this mode",
		}
	}
	if err := result.Validate(); err != nil {
		return domainplugin.DataResult{}, provider.protocolError(
			"invalid command result envelope")
	}
	if connection.guard.echoesResultDiagnostics(result) {
		l.host.recordDataEvent(provider, "command", "secret-echo",
			connection.mode, class, 0, 0)
		return domainplugin.DataResult{}, provider.secretEchoError()
	}
	l.host.recordDataEvent(provider, "command", "ok", connection.mode, class,
		l.host.now().Sub(started), result.DataResultBytes())
	return result, nil
}

// Begin starts one transaction. The transactions feature is required.
func (l *DataLease) Begin(ctx context.Context) (uint64, error) {
	connection, err := l.usable()
	if err != nil {
		return 0, err
	}
	provider := connection.provider
	if err := provider.requireFeature(domainplugin.DataFeatureTransactions); err != nil {
		l.host.recordDataEvent(provider, "begin", "unsupported",
			connection.mode, "", 0, 0)
		return 0, err
	}
	response, err := provider.invoke(ctx, domainpluginabi.DataProviderMethodBegin,
		dataConnectionPayload{Connection: connection.token},
		l.host.limits.CallTimeout, connection.guard)
	if err != nil {
		l.host.recordDataEvent(provider, "begin", dataErrorStatus(err),
			connection.mode, "", 0, 0)
		return 0, err
	}
	if response.Transaction == 0 {
		return 0, provider.protocolError("provider returned no transaction token")
	}
	l.host.recordDataEvent(provider, "begin", "ok", connection.mode, "", 0, 0)
	return response.Transaction, nil
}

// Commit commits one transaction.
func (l *DataLease) Commit(ctx context.Context, transaction uint64) error {
	return l.finishTransaction(ctx, domainpluginabi.DataProviderMethodCommit,
		"commit", transaction)
}

// Rollback rolls one transaction back.
func (l *DataLease) Rollback(ctx context.Context, transaction uint64) error {
	return l.finishTransaction(ctx, domainpluginabi.DataProviderMethodRollback,
		"rollback", transaction)
}

func (l *DataLease) finishTransaction(ctx context.Context,
	method domainpluginabi.DataProviderMethod, action string, transaction uint64) error {

	connection, err := l.usable()
	if err != nil {
		return err
	}
	provider := connection.provider
	if err := provider.requireFeature(domainplugin.DataFeatureTransactions); err != nil {
		l.host.recordDataEvent(provider, action, "unsupported", connection.mode, "", 0, 0)
		return err
	}
	if transaction == 0 {
		return &DataError{
			Kind: DataErrorInvalid, Provider: provider.qualifiedID,
			Message: "transaction token is required",
		}
	}
	_, err = provider.invoke(ctx, method, dataTransactionPayload{
		Connection: connection.token, Transaction: transaction,
	}, l.host.limits.CallTimeout, connection.guard)
	l.host.recordDataEvent(provider, action, dataStatusWord(err), connection.mode, "", 0, 0)
	return err
}

// Metadata returns one page of the provider's metadata tree.
func (l *DataLease) Metadata(ctx context.Context, path, next string) (domainplugin.DataMetadata, error) {
	connection, err := l.usable()
	if err != nil {
		return domainplugin.DataMetadata{}, err
	}
	provider := connection.provider
	if err := provider.requireFeature(domainplugin.DataFeatureMetadata); err != nil {
		l.host.recordDataEvent(provider, "metadata", "unsupported",
			connection.mode, "", 0, 0)
		return domainplugin.DataMetadata{}, err
	}
	if len(path) > 4096 || len(next) > domainplugin.MaxDataProfileSecretRef {
		return domainplugin.DataMetadata{}, &DataError{
			Kind: DataErrorLimit, Provider: provider.qualifiedID,
			Message: "metadata path or cursor is too large",
		}
	}
	response, err := provider.invoke(ctx, domainpluginabi.DataProviderMethodMetadata,
		dataMetadataPayload{Connection: connection.token, Path: path, Next: next},
		l.host.limits.CallTimeout, connection.guard)
	if err != nil {
		l.host.recordDataEvent(provider, "metadata", dataErrorStatus(err),
			connection.mode, "", 0, 0)
		return domainplugin.DataMetadata{}, err
	}
	if response.Metadata == nil {
		return domainplugin.DataMetadata{}, provider.protocolError(
			"provider returned no metadata")
	}
	metadata := *response.Metadata
	if err := metadata.Validate(); err != nil {
		return domainplugin.DataMetadata{}, provider.protocolError(
			"invalid metadata tree")
	}
	l.host.recordDataEvent(provider, "metadata", "ok", connection.mode, "", 0, 0)
	return metadata, nil
}

// Plan returns the optional query plan. Plans are read-only by contract.
func (l *DataLease) Plan(ctx context.Context, query string,
	parameters []domainplugin.DataValue) (domainplugin.DataResult, error) {

	connection, err := l.usable()
	if err != nil {
		return domainplugin.DataResult{}, err
	}
	provider := connection.provider
	if err := provider.requireFeature(domainplugin.DataFeaturePlans); err != nil {
		l.host.recordDataEvent(provider, "plan", "unsupported",
			connection.mode, "", 0, 0)
		return domainplugin.DataResult{}, err
	}
	if err := checkDataInput(provider, query, parameters); err != nil {
		return domainplugin.DataResult{}, err
	}
	response, err := provider.invoke(ctx, domainpluginabi.DataProviderMethodPlan,
		dataPlanPayload{Connection: connection.token, Query: query,
			Parameters: parameters}, l.host.limits.FetchTimeout, connection.guard)
	if err != nil {
		l.host.recordDataEvent(provider, "plan", dataErrorStatus(err),
			connection.mode, "", 0, 0)
		return domainplugin.DataResult{}, err
	}
	if response.Result == nil {
		return domainplugin.DataResult{}, provider.protocolError(
			"provider returned no plan result")
	}
	result := *response.Result
	if result.Kind != domainplugin.DataResultPlan {
		return domainplugin.DataResult{}, provider.protocolError(
			"provider returned a non-plan result")
	}
	if err := result.Validate(); err != nil {
		return domainplugin.DataResult{}, provider.protocolError(
			"invalid plan result envelope")
	}
	if connection.guard.echoesResultDiagnostics(result) {
		l.host.recordDataEvent(provider, "plan", "secret-echo",
			connection.mode, domainplugin.DataClassRead, 0, 0)
		return domainplugin.DataResult{}, provider.secretEchoError()
	}
	l.host.recordDataEvent(provider, "plan", "ok", connection.mode,
		domainplugin.DataClassRead, 0, result.DataResultBytes())
	return result, nil
}

// checkDataStreaming rejects a chunked result from a provider that did not
// declare the streaming feature.
func checkDataStreaming(provider *DataProvider, result domainplugin.DataResult) error {
	if result.Kind != domainplugin.DataResultChunks {
		return nil
	}
	return provider.requireFeature(domainplugin.DataFeatureStreaming)
}

// checkDataInput enforces the query/command/plan input bound and parameter
// count before the guest sees the request.
func checkDataInput(provider *DataProvider, input string,
	parameters []domainplugin.DataValue) error {

	if len(parameters) > domainplugin.MaxDataParameters {
		return &DataError{
			Kind: DataErrorLimit, Provider: provider.qualifiedID,
			Message: "too many parameters",
		}
	}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return &DataError{
			Kind: DataErrorProtocol, Provider: provider.qualifiedID,
			Message: "encode parameters",
		}
	}
	if len(input)+len(encoded) > domainplugin.MaxDataInputBytes {
		return &DataError{
			Kind: DataErrorLimit, Provider: provider.qualifiedID,
			Message: "query input exceeds the input limit",
		}
	}
	return nil
}

func dataStatusWord(err error) string {
	if err == nil {
		return "ok"
	}
	return dataErrorStatus(err)
}

func dataResultWord(result domainplugin.DataResult) string {
	switch {
	case result.Partial:
		return "partial"
	case result.Limit:
		return "limit"
	case result.WouldBlock:
		return "would-block"
	default:
		return "ok"
	}
}

// takeIdleConnection pops one idle connection for the profile and mode. It
// marks the connection leased and invalidates its pending expiry.
func (h *DataProviderHost) takeIdleConnection(profileID string,
	mode domainplugin.DataAccessMode) *DataConnection {

	key := dataPoolKey(profileID, mode)
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.pool[key]
	for len(list) > 0 {
		connection := list[len(list)-1]
		list = list[:len(list)-1]
		if connection.closed || connection.leaseCount > 0 {
			continue
		}
		h.pool[key] = list
		connection.leaseCount = 1
		connection.idleGen++
		if connection.cancelIdle != nil {
			connection.cancelIdle()
			connection.cancelIdle = nil
		}
		return connection
	}
	h.pool[key] = list
	return nil
}

// releaseConnection either pools an idle connection or closes it immediately
// when the host is shutting down or a query is still open.
func (h *DataProviderHost) releaseConnection(connection *DataConnection) {
	key := dataPoolKey(connection.profileID, connection.mode)
	h.mu.Lock()
	if h.closed || connection.closed || len(connection.queries) > 0 {
		unfinished := len(connection.queries) > 0
		owned := h.claimConnectionCloseLocked(connection)
		h.mu.Unlock()
		if !owned {
			return
		}
		result := "ok"
		if unfinished {
			result = "unfinished-query"
		}
		if connection.provider.closeConnection(connection.token) {
			h.auditDataClose(connection, result)
		}
		return
	}
	connection.leaseCount = 0
	connection.idleGen++
	generation := connection.idleGen
	h.pool[key] = append(h.pool[key], connection)
	h.mu.Unlock()

	stop := h.schedule(h.limits.IdleTTL, func() {
		h.expireIdleConnection(connection, generation)
	})
	h.mu.Lock()
	if connection.idleGen == generation && !connection.closed &&
		connection.leaseCount == 0 {
		connection.cancelIdle = stop
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	stop()
}

// expireIdleConnection closes one connection whose idle TTL elapsed. The
// generation check makes a concurrent reuse win.
func (h *DataProviderHost) expireIdleConnection(connection *DataConnection, generation uint64) {
	h.mu.Lock()
	if connection.closed || connection.leaseCount != 0 ||
		connection.idleGen != generation {
		h.mu.Unlock()
		return
	}
	owned := h.claimConnectionCloseLocked(connection)
	h.mu.Unlock()
	if !owned {
		return
	}
	if connection.provider.closeConnection(connection.token) {
		h.recordDataEvent(connection.provider, "idle", "expired", connection.mode, "", 0, 0)
		h.auditDataClose(connection, "idle")
	}
}

// closeConnectionNow removes a connection from the pool and asks the guest to
// close it. It is idempotent: the first caller to claim the connection closes
// it and writes the single close audit row; a concurrent sweep, expiry, or
// lease simply observes the claim and does nothing.
func (h *DataProviderHost) closeConnectionNow(connection *DataConnection, result string) error {
	if connection == nil {
		return nil
	}
	h.mu.Lock()
	owned := h.claimConnectionCloseLocked(connection)
	h.mu.Unlock()
	if !owned {
		return nil
	}
	if connection.provider.closeConnection(connection.token) {
		h.auditDataClose(connection, result)
	}
	return nil
}

// claimConnectionCloseLocked marks connection closed and records the single
// owner of its guest close and close audit. It reports false when another path
// already claimed the teardown. h.mu must be held.
func (h *DataProviderHost) claimConnectionCloseLocked(connection *DataConnection) bool {
	if h == nil || connection == nil || connection.closeClaimed {
		return false
	}
	connection.closeClaimed = true
	connection.closed = true
	h.removeConnectionLocked(connection)
	return true
}

func (h *DataProviderHost) removeConnectionLocked(connection *DataConnection) {
	delete(h.connections, connection)
	key := dataPoolKey(connection.profileID, connection.mode)
	list := h.pool[key]
	for i, candidate := range list {
		if candidate == connection {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(h.pool, key)
	} else {
		h.pool[key] = list
	}
	if connection.cancelIdle != nil {
		stop := connection.cancelIdle
		connection.cancelIdle = nil
		// Stop outside the lock would be cleaner, but the timer callback only
		// takes the same lock; stopping does not run the callback.
		stop()
	}
}

func (h *DataProviderHost) findConnectionLocked(provider *DataProvider,
	token uint64) *DataConnection {

	for connection := range h.connections {
		if connection.provider == provider && connection.token == token {
			return connection
		}
	}
	return nil
}

func (h *DataProviderHost) connectionCountLocked(profileID string) int {
	count := 0
	for connection := range h.connections {
		if connection.profileID == profileID {
			count++
		}
	}
	return count
}

func (h *DataProviderHost) trackQuery(connection *DataConnection, query *DataQuery) {
	h.mu.Lock()
	connection.queries[query.token] = query
	h.mu.Unlock()
}

func (h *DataProviderHost) untrackQuery(connection *DataConnection, token uint64) {
	if token == 0 {
		return
	}
	h.mu.Lock()
	delete(connection.queries, token)
	h.mu.Unlock()
}

func dataPoolKey(profileID string, mode domainplugin.DataAccessMode) string {
	return profileID + "\x00" + string(mode)
}

// DataPoolSize reports the idle connection count for one profile and mode. It
// exists for diagnostics and deterministic tests.
func (h *DataProviderHost) DataPoolSize(profileID string, mode domainplugin.DataAccessMode) int {
	if h == nil {
		return 0
	}
	mode = domainplugin.NormalizeDataAccessMode(mode)
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pool[dataPoolKey(profileID, mode)])
}

// DataConnectionCount reports every open connection for one profile,
// including leased and idle ones.
func (h *DataProviderHost) DataConnectionCount(profileID string) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connectionCountLocked(profileID)
}
