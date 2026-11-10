package plugin

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Data-profile failure sentinels. The application maps them to closed typed
// errors; they never carry provider field values or secret content.
var (
	// ErrDataProfileNotFound reports an unknown profile ID.
	ErrDataProfileNotFound = errors.New("plugin: data profile not found")
	// ErrDataSecretNotFound reports a reference whose value is absent.
	ErrDataSecretNotFound = errors.New("plugin: data profile secret not found")
	// ErrDataUnavailable reports a host port that is not wired.
	ErrDataUnavailable = errors.New("plugin: data provider host is not configured")
)

// DataProfileStore is the persistence port for DataProfile records. The Vault
// adapter stores whole records and keeps them ordered for display. Profiles
// are not Hosts and not Sessions.
type DataProfileStore interface {
	List(ctx context.Context) ([]domainplugin.DataProfile, error)
	Get(ctx context.Context, id string) (domainplugin.DataProfile, error)
	Save(ctx context.Context, profile domainplugin.DataProfile) error
	Delete(ctx context.Context, id string) error
}

// DataProfileSecrets is the Vault-backed secret port for provider-defined
// secret fields. Put returns an opaque reference stored on the profile; Get
// resolves it only when a connection is opened. Plaintext never appears in a
// DataProfile record.
type DataProfileSecrets interface {
	Put(ctx context.Context, profileID, field, value string) (string, error)
	Get(ctx context.Context, ref string) (string, error)
	Delete(ctx context.Context, ref string) error
}

// DataTunnel is the host-owned tunnel descriptor passed to a provider when a
// profile names a via Host. The provider receives only this opaque endpoint;
// the Host's trust, credentials, proxies, and jump chain stay host-owned.
type DataTunnel struct {
	Host     string `json:"host"`
	Endpoint string `json:"endpoint"`
}

// DataTunnelSource resolves a via Host into a host-owned tunnel. A profile
// with via_host set fails closed when the composition does not provide one.
type DataTunnelSource interface {
	Tunnel(ctx context.Context, hostID string) (DataTunnel, error)
}

// DataTestResult is the non-secret result of a profile test. It carries only
// latency, server identity, and an auth verdict; never a field value or
// credential.
type DataTestResult struct {
	LatencyMS      int64  `json:"latencyMs"`
	ServerIdentity string `json:"serverIdentity,omitempty"`
	AuthResult     string `json:"authResult,omitempty"`
}

// MemoryDataProfileStore is a deterministic DataProfileStore for tests and an
// in-memory composition.
type MemoryDataProfileStore struct {
	mu       sync.Mutex
	profiles map[string]domainplugin.DataProfile
}

// NewMemoryDataProfileStore returns an empty store.
func NewMemoryDataProfileStore() *MemoryDataProfileStore {
	return &MemoryDataProfileStore{profiles: make(map[string]domainplugin.DataProfile)}
}

// List implements DataProfileStore.
func (s *MemoryDataProfileStore) List(context.Context) ([]domainplugin.DataProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles := make([]domainplugin.DataProfile, 0, len(s.profiles))
	for _, profile := range s.profiles {
		profiles = append(profiles, profile)
	}
	sortDataProfiles(profiles)
	return profiles, nil
}

// Get implements DataProfileStore.
func (s *MemoryDataProfileStore) Get(_ context.Context, id string) (domainplugin.DataProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.profiles[id]
	if !ok {
		return domainplugin.DataProfile{}, ErrDataProfileNotFound
	}
	return profile, nil
}

// Save implements DataProfileStore.
func (s *MemoryDataProfileStore) Save(_ context.Context, profile domainplugin.DataProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[profile.ID] = profile
	return nil
}

// Delete implements DataProfileStore.
func (s *MemoryDataProfileStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[id]; !ok {
		return ErrDataProfileNotFound
	}
	delete(s.profiles, id)
	return nil
}

// MemoryDataProfileSecrets is a deterministic DataProfileSecrets for tests.
// References are stable per profile field, so rewriting a secret overwrites
// its value exactly like the Vault adapter.
type MemoryDataProfileSecrets struct {
	mu     sync.Mutex
	values map[string]string
}

// NewMemoryDataProfileSecrets returns an empty secret port.
func NewMemoryDataProfileSecrets() *MemoryDataProfileSecrets {
	return &MemoryDataProfileSecrets{values: make(map[string]string)}
}

// Put implements DataProfileSecrets.
func (s *MemoryDataProfileSecrets) Put(_ context.Context, profileID, field, value string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := fmt.Sprintf("memory:%s:%s", profileID, field)
	s.values[ref] = value
	return ref, nil
}

// Get implements DataProfileSecrets.
func (s *MemoryDataProfileSecrets) Get(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[ref]
	if !ok {
		return "", ErrDataSecretNotFound
	}
	return value, nil
}

// Delete implements DataProfileSecrets.
func (s *MemoryDataProfileSecrets) Delete(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, ref)
	return nil
}

// MemoryDataTunnelSource returns a fixed host-owned tunnel descriptor.
type MemoryDataTunnelSource struct {
	mu       sync.Mutex
	Endpoint string
	Calls    []string
	Err      error
}

// Tunnel implements DataTunnelSource.
func (s *MemoryDataTunnelSource) Tunnel(_ context.Context, hostID string) (DataTunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, hostID)
	if s.Err != nil {
		return DataTunnel{}, s.Err
	}
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "127.0.0.1:0"
	}
	return DataTunnel{Host: hostID, Endpoint: endpoint}, nil
}

// Hosts returns the Host IDs whose tunnel was requested.
func (s *MemoryDataTunnelSource) Hosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Calls...)
}

var dataProfileIDEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func newDataProfileID() string {
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	id[0] = byte(milliseconds >> 40)
	id[1] = byte(milliseconds >> 32)
	id[2] = byte(milliseconds >> 24)
	id[3] = byte(milliseconds >> 16)
	id[4] = byte(milliseconds >> 8)
	id[5] = byte(milliseconds)
	if _, err := rand.Read(id[6:]); err != nil {
		binary.BigEndian.PutUint64(id[8:], uint64(time.Now().UnixNano()))
	}
	return dataProfileIDEncoding.EncodeToString(id[:])
}

func (h *DataProviderHost) newProfileID() string {
	if h != nil && h.newID != nil {
		return h.newID()
	}
	return newDataProfileID()
}

func sortDataProfiles(profiles []domainplugin.DataProfile) {
	for i := 1; i < len(profiles); i++ {
		for j := i; j > 0 && strings.ToLower(profiles[j].Label) < strings.ToLower(profiles[j-1].Label); j-- {
			profiles[j], profiles[j-1] = profiles[j-1], profiles[j]
		}
	}
}

// SaveProfile creates or updates one profile. id is empty on create. Field
// values are validated against the live provider's declared schema; secret
// values are written to the Vault and only their references are persisted.
func (h *DataProviderHost) SaveProfile(ctx context.Context, id string,
	draft domainplugin.DataProfileDraft) (domainplugin.DataProfile, error) {

	if h == nil || h.profiles == nil {
		return domainplugin.DataProfile{}, ErrDataUnavailable
	}
	provider := h.provider(draft.Provider)
	if provider == nil {
		return domainplugin.DataProfile{}, &DataError{
			Kind: DataErrorUnavailable, Provider: draft.Provider,
			Message: "the data provider is not active",
		}
	}
	if !provider.maxMode.Valid() {
		return domainplugin.DataProfile{}, &DataError{
			Kind: DataErrorProtocol, Provider: provider.qualifiedID,
			Message: "the provider grant has no valid access mode",
		}
	}
	var existing domainplugin.DataProfile
	var hasExisting bool
	if id != "" {
		loaded, err := h.profiles.Get(ctx, id)
		if err != nil {
			return domainplugin.DataProfile{}, err
		}
		existing = loaded
		hasExisting = true
		if existing.Provider != provider.qualifiedID {
			return domainplugin.DataProfile{}, &DataError{
				Kind: DataErrorDenied, Provider: provider.qualifiedID,
				Message: "the profile belongs to a different provider",
			}
		}
	}
	// A required secret already stored on the profile satisfies validation
	// unless this update clears or replaces it.
	draft.SecretRefs = map[string]string{}
	if hasExisting {
		cleared := make(map[string]bool, len(draft.ClearSecrets))
		for _, field := range draft.ClearSecrets {
			cleared[field] = true
		}
		for field, ref := range existing.SecretRefs {
			if _, replacing := draft.Secrets[field]; replacing || cleared[field] {
				continue
			}
			draft.SecretRefs[field] = ref
		}
	}
	normalized, err := domainplugin.ValidateDataProfileDraft(
		provider.qualifiedID, provider.contribution, draft)
	if err != nil {
		return domainplugin.DataProfile{}, err
	}
	if !provider.maxMode.AllowsMode(normalized.Mode) {
		h.recordDataEvent(provider, "profile", "escalation", normalized.Mode, "", 0, 0)
		return domainplugin.DataProfile{}, &DataError{
			Kind: DataErrorDenied, Provider: provider.qualifiedID,
			Message: "the selected access mode exceeds the provider grant",
		}
	}
	if len(normalized.Secrets) > 0 && h.secrets == nil {
		return domainplugin.DataProfile{}, ErrDataUnavailable
	}
	if id == "" {
		id = h.newProfileID()
	}
	refs := make(map[string]string, len(normalized.SecretRefs)+len(normalized.Secrets))
	for field, ref := range normalized.SecretRefs {
		refs[field] = ref
	}
	var written []dataSecretWrite
	for field, value := range normalized.Secrets {
		write := dataSecretWrite{field: field}
		if oldRef := existing.SecretRefs[field]; oldRef != "" && h.secrets != nil {
			// Both secret adapters use one stable reference per profile field,
			// so restoring the previous value at the same reference undoes an
			// update that fails after this write.
			if oldValue, err := h.secrets.Get(ctx, oldRef); err == nil {
				write.oldValue, write.hadOld = oldValue, true
			}
		}
		ref, err := h.secrets.Put(ctx, id, field, value)
		if err != nil {
			h.rollbackDataSecrets(ctx, id, written)
			h.recordDataEvent(provider, "profile", "fault", normalized.Mode, "", 0, 0)
			return domainplugin.DataProfile{}, &DataError{
				Kind: DataErrorFaulted, Provider: provider.qualifiedID,
				Message: "the secret store rejected the value",
			}
		}
		write.ref = ref
		refs[field] = ref
		written = append(written, write)
	}
	now := h.now()
	record := domainplugin.DataProfile{
		ID: id, Provider: provider.qualifiedID, Label: normalized.Label,
		Mode: normalized.Mode, ViaHost: normalized.ViaHost,
		Fields: copyDataFields(normalized.Fields), SecretRefs: refs,
		CreatedAt: now, UpdatedAt: now,
	}
	if hasExisting {
		record.CreatedAt = existing.CreatedAt
		// Provider-defined structural data is not part of the editable draft.
		// A profile edit preserves it so an ordinary save never deletes data
		// whose provider is absent or dormant.
		record.PluginData = existing.PluginData
	}
	if err := domainplugin.ValidateDataProfile(record); err != nil {
		h.rollbackDataSecrets(ctx, id, written)
		return domainplugin.DataProfile{}, err
	}
	if err := h.profiles.Save(ctx, record); err != nil {
		h.rollbackDataSecrets(ctx, id, written)
		return domainplugin.DataProfile{}, err
	}
	for _, field := range normalized.ClearSecrets {
		if ref := existing.SecretRefs[field]; ref != "" && h.secrets != nil {
			_ = h.secrets.Delete(ctx, ref)
		}
	}
	action := "profile.create"
	if hasExisting {
		action = "profile.update"
	}
	h.recordDataEvent(provider, action, "ok", normalized.Mode, "", 0, 0)
	return record, nil
}

// GetProfile returns one profile record.
func (h *DataProviderHost) GetProfile(ctx context.Context, id string) (domainplugin.DataProfile, error) {
	if h == nil || h.profiles == nil {
		return domainplugin.DataProfile{}, ErrDataUnavailable
	}
	return h.profiles.Get(ctx, id)
}

// ListProfiles returns every profile in display order.
func (h *DataProviderHost) ListProfiles(ctx context.Context) ([]domainplugin.DataProfile, error) {
	if h == nil || h.profiles == nil {
		return nil, ErrDataUnavailable
	}
	return h.profiles.List(ctx)
}

// DeleteProfile removes one profile and every secret it references.
func (h *DataProviderHost) DeleteProfile(ctx context.Context, id string) error {
	if h == nil || h.profiles == nil {
		return ErrDataUnavailable
	}
	profile, err := h.profiles.Get(ctx, id)
	if err != nil {
		return err
	}
	if h.secrets != nil {
		for _, ref := range profile.SecretRefs {
			_ = h.secrets.Delete(ctx, ref)
		}
	}
	if err := h.profiles.Delete(ctx, id); err != nil {
		return err
	}
	if provider := h.provider(profile.Provider); provider != nil {
		h.recordDataEvent(provider, "profile.delete", "ok", profile.Mode, "", 0, 0)
	}
	return nil
}

// TestProfile opens one bounded test connection for a saved profile. It
// returns latency, server identity, and an auth verdict, never a field or
// secret value.
func (h *DataProviderHost) TestProfile(ctx context.Context, id string) (DataTestResult, error) {
	if h == nil || h.profiles == nil {
		return DataTestResult{}, ErrDataUnavailable
	}
	profile, err := h.profiles.Get(ctx, id)
	if err != nil {
		return DataTestResult{}, err
	}
	provider := h.provider(profile.Provider)
	if provider == nil {
		return DataTestResult{}, &DataError{
			Kind: DataErrorUnavailable, Provider: profile.Provider,
			Message: "the data provider is not active",
		}
	}
	if err := provider.requireFeature(domainplugin.DataFeatureTesting); err != nil {
		h.recordDataEvent(provider, "test", "unsupported", profile.Mode, "", 0, 0)
		return DataTestResult{}, err
	}
	if err := domainplugin.ValidateDataProfile(profile); err != nil {
		return DataTestResult{}, err
	}
	if !provider.maxMode.AllowsMode(profile.Mode) {
		h.recordDataEvent(provider, "test", "escalation", profile.Mode, "", 0, 0)
		return DataTestResult{}, &DataError{
			Kind: DataErrorDenied, Provider: profile.Provider,
			Message: "the profile access mode exceeds the provider grant",
		}
	}
	return h.testDataProfile(ctx, provider, profile, nil)
}

// TestDraftProfile validates a draft against the live provider schema and
// runs the same non-secret test without persisting anything. New secret
// values in the draft are used only for this call.
func (h *DataProviderHost) TestDraftProfile(ctx context.Context,
	draft domainplugin.DataProfileDraft) (DataTestResult, error) {

	if h == nil {
		return DataTestResult{}, ErrDataUnavailable
	}
	provider := h.provider(draft.Provider)
	if provider == nil {
		return DataTestResult{}, &DataError{
			Kind: DataErrorUnavailable, Provider: draft.Provider,
			Message: "the data provider is not active",
		}
	}
	if err := provider.requireFeature(domainplugin.DataFeatureTesting); err != nil {
		h.recordDataEvent(provider, "test", "unsupported", draft.Mode, "", 0, 0)
		return DataTestResult{}, err
	}
	normalized, err := domainplugin.ValidateDataProfileDraft(
		provider.qualifiedID, provider.contribution, draft)
	if err != nil {
		return DataTestResult{}, err
	}
	if !provider.maxMode.AllowsMode(normalized.Mode) {
		h.recordDataEvent(provider, "test", "escalation", normalized.Mode, "", 0, 0)
		return DataTestResult{}, &DataError{
			Kind: DataErrorDenied, Provider: provider.qualifiedID,
			Message: "the selected access mode exceeds the provider grant",
		}
	}
	return h.testDataProfile(ctx, provider, domainplugin.DataProfile{
		Label: normalized.Label, Provider: provider.qualifiedID,
		Mode: normalized.Mode, ViaHost: normalized.ViaHost,
		Fields: normalized.Fields, SecretRefs: normalized.SecretRefs,
	}, normalized.Secrets)
}

// The profile-test result fields are guest text that the caller displays. They
// are reduced to host-defined shapes before they leave the host: a conservative
// DNS-style server name, and an auth verdict from a closed vocabulary. Free
// text never round-trips, so a provider cannot hide secret pieces there even
// when the pieces are interleaved with junk or split across both fields.
const (
	maxDataServerIdentityBytes      = 253
	maxDataServerIdentityLabelBytes = 63
	dataAuthResultAccepted          = "accepted"
	dataAuthResultRejected          = "rejected"
	dataAuthResultUnknown           = "unknown"
)

// sanitizeDataServerIdentity accepts empty or a DNS-style host name: labels of
// 1-63 ASCII letters, digits, and internal hyphens, separated by dots, at most
// 253 bytes overall. Anything else is rejected by the caller with a constant
// error, never echoed.
func sanitizeDataServerIdentity(identity string) (string, bool) {
	if identity == "" {
		return "", true
	}
	if len(identity) > maxDataServerIdentityBytes {
		return "", false
	}
	for _, label := range strings.Split(identity, ".") {
		if len(label) == 0 || len(label) > maxDataServerIdentityLabelBytes {
			return "", false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for index := 0; index < len(label); index++ {
			c := label[index]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
				c >= '0' && c <= '9' || c == '-' {
				continue
			}
			return "", false
		}
	}
	return identity, true
}

// sanitizeDataAuthResult maps a provider's auth verdict onto the host's closed
// vocabulary. An empty verdict stays empty; any other value outside the set is
// reported as the constant unknown verdict, never returned verbatim.
func sanitizeDataAuthResult(result string) string {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "":
		return ""
	case dataAuthResultAccepted:
		return dataAuthResultAccepted
	case dataAuthResultRejected:
		return dataAuthResultRejected
	default:
		return dataAuthResultUnknown
	}
}

// testDataProfile runs one open with the test flag and releases it
// immediately. The guest's reported identity and auth verdict are checked
// against every resolved secret so a provider cannot echo credential content
// back to the host UI. A provider error message is checked the same way. The
// result then keeps only host-shaped values: a server name the host can
// validate and a verdict from the closed auth vocabulary.
func (h *DataProviderHost) testDataProfile(ctx context.Context,
	provider *DataProvider, profile domainplugin.DataProfile,
	draftSecrets map[string]string) (DataTestResult, error) {

	secrets, err := h.resolveDataSecrets(ctx, provider, profile, draftSecrets)
	if err != nil {
		return DataTestResult{}, err
	}
	guard := newDataSecretGuard(secrets)
	started := h.now()
	response, err := provider.open(ctx, dataOpenRequest{
		Profile: profile, Fields: profile.Fields, Secrets: secrets, Test: true,
	})
	elapsed := h.now().Sub(started)
	if err != nil {
		h.recordDataEvent(provider, "test", dataErrorStatus(err), profile.Mode, "",
			elapsed, 0)
		// open already sanitizes provider messages, but scan the complete error
		// text again before anything can be shown to the caller.
		if guard.echoes(err.Error()) {
			h.recordDataEvent(provider, "test", "secret-echo", profile.Mode, "",
				elapsed, 0)
			return DataTestResult{}, &DataError{
				Kind: DataErrorProtocol, Provider: provider.qualifiedID,
				Message: "the provider echoed secret content in the test result",
			}
		}
		return DataTestResult{}, err
	}
	if response.Connection != 0 {
		provider.closeConnection(response.Connection)
	}
	// Check the supplied fields before bounding either value, so a secret
	// longer than the display bound is still rejected instead of being
	// displayed as a truncated prefix. The fields are also checked as a
	// concatenation because a provider can split one secret between
	// serverIdentity and authResult, which separate scans cannot see. This
	// keeps the prior split protection while the returned-value scan below is
	// the authoritative one.
	if guard.echoesFields(response.ServerIdentity, response.AuthResult) {
		h.recordDataEvent(provider, "test", "secret-echo", profile.Mode, "", elapsed, 0)
		return DataTestResult{}, &DataError{
			Kind: DataErrorProtocol, Provider: provider.qualifiedID,
			Message: "the provider echoed secret content in the test result",
		}
	}
	identity, identityOK := sanitizeDataServerIdentity(response.ServerIdentity)
	if !identityOK {
		h.recordDataEvent(provider, "test", "protocol-error", profile.Mode, "", elapsed, 0)
		return DataTestResult{}, &DataError{
			Kind: DataErrorProtocol, Provider: provider.qualifiedID,
			Message: "the provider returned a server identity outside the allowed shape",
		}
	}
	authResult := sanitizeDataAuthResult(response.AuthResult)
	// Scan the exact strings that leave the host: the sanitized identity the
	// caller displays and the closed auth verdict. authResult above may have
	// been reduced to a constant, so an oversized discarded value can no
	// longer suppress scanning of the identity that is actually returned.
	if guard.echoesFields(identity, authResult) {
		h.recordDataEvent(provider, "test", "secret-echo", profile.Mode, "", elapsed, 0)
		return DataTestResult{}, &DataError{
			Kind: DataErrorProtocol, Provider: provider.qualifiedID,
			Message: "the provider echoed secret content in the test result",
		}
	}
	result := DataTestResult{
		LatencyMS:      elapsed.Milliseconds(),
		ServerIdentity: identity,
		AuthResult:     authResult,
	}
	h.recordDataEvent(provider, "test", "ok", profile.Mode, "", elapsed, 0)
	return result, nil
}

// dataSecretWrite records one completed secret write so a later failure in the
// same save can restore the previous value instead of destroying it.
type dataSecretWrite struct {
	field    string
	ref      string
	oldValue string
	hadOld   bool
}

func (h *DataProviderHost) rollbackDataSecrets(ctx context.Context, profileID string,
	written []dataSecretWrite) {
	if h == nil || h.secrets == nil {
		return
	}
	for i := len(written) - 1; i >= 0; i-- {
		write := written[i]
		if write.hadOld {
			_, _ = h.secrets.Put(ctx, profileID, write.field, write.oldValue)
			continue
		}
		_ = h.secrets.Delete(ctx, write.ref)
	}
}

func copyDataFields(fields map[string]string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}
