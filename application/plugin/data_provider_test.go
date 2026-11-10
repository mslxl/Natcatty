package plugin_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// dataTestHostID returns a Host ID generated exactly like the Vault does: a
// canonical 26-character Crockford base32 ULID.
func dataTestHostID(t *testing.T) string {
	t.Helper()
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	id[0] = byte(milliseconds >> 40)
	id[1] = byte(milliseconds >> 32)
	id[2] = byte(milliseconds >> 24)
	id[3] = byte(milliseconds >> 16)
	id[4] = byte(milliseconds >> 8)
	id[5] = byte(milliseconds)
	if _, err := rand.Read(id[6:]); err != nil {
		t.Fatalf("generate Host ID: %v", err)
	}
	encoding := base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").
		WithPadding(base32.NoPadding)
	return encoding.EncodeToString(id[:])
}

const dataTestFiber domainplugin.FiberID = "fiber-data-1"

func dataTestContribution() domainplugin.DataProvider {
	return domainplugin.DataProvider{
		ID:   "warehouse",
		Name: "Warehouse",
		Capabilities: domainplugin.DataCapabilities{
			Family: domainplugin.DataFamilyTabular,
			Features: []domainplugin.DataFeature{
				domainplugin.DataFeatureTransactions,
				domainplugin.DataFeatureMetadata,
				domainplugin.DataFeatureMutations,
				domainplugin.DataFeatureCancellation,
				domainplugin.DataFeatureStreaming,
				domainplugin.DataFeaturePlans,
				domainplugin.DataFeatureTesting,
			},
			MaxConnections: 2,
		},
		ProfileFields: []domainplugin.DataProfileField{
			{Key: "endpoint", Label: "Endpoint", Kind: domainplugin.DataProfileFieldText, Required: true},
			{Key: "password", Label: "Password", Kind: domainplugin.DataProfileFieldText, Required: true, Secret: true},
			{Key: "token", Label: "Token", Kind: domainplugin.DataProfileFieldText, Secret: true},
			{Key: "other", Label: "Other", Kind: domainplugin.DataProfileFieldText, Secret: true},
			{Key: "tls", Label: "TLS", Kind: domainplugin.DataProfileFieldBoolean},
			{Key: "flavor", Label: "Flavor", Kind: domainplugin.DataProfileFieldSelect,
				Options: []string{"fast", "safe"}},
		},
	}
}

func dataTestDraft() domainplugin.DataProfileDraft {
	return domainplugin.DataProfileDraft{
		Label:    "Reporting",
		Provider: domainplugin.QualifiedDataProviderID("acme.data", "warehouse"),
		Mode:     domainplugin.DataModeReadOnly,
		Fields: map[string]string{
			"endpoint": "warehouse.example.test:443",
			"tls":      "true",
			"flavor":   "safe",
		},
		Secrets: map[string]string{"password": "hunter2", "token": "tok-1"},
	}
}

type dataRecordingAudit struct {
	mu     sync.Mutex
	events []domainplugin.AuditEvent
}

func (r *dataRecordingAudit) Record(event domainplugin.AuditEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *dataRecordingAudit) Close() error { return nil }

func (r *dataRecordingAudit) snapshot() []domainplugin.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domainplugin.AuditEvent(nil), r.events...)
}

func (r *dataRecordingAudit) hasReason(reason string) bool {
	for _, event := range r.snapshot() {
		if event.Event == domainplugin.AuditData && event.Reason == reason {
			return true
		}
	}
	return false
}

func (r *dataRecordingAudit) encoded() string {
	encoded, _ := json.Marshal(r.snapshot())
	return string(encoded)
}

type fakeClock struct {
	mu      sync.Mutex
	current time.Time
	step    time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(c.step)
	return c.current
}

type fakeTimer struct {
	delay   time.Duration
	fn      func()
	stopped bool
	fired   bool
}

type fakeScheduler struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

func (s *fakeScheduler) Schedule(delay time.Duration, fn func()) func() {
	s.mu.Lock()
	timer := &fakeTimer{delay: delay, fn: fn}
	s.timers = append(s.timers, timer)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		timer.stopped = true
		s.mu.Unlock()
	}
}

// Fire runs every live timer in registration order.
func (s *fakeScheduler) Fire() {
	s.mu.Lock()
	var ready []*fakeTimer
	for _, timer := range s.timers {
		if !timer.stopped && !timer.fired {
			timer.fired = true
			ready = append(ready, timer)
		}
	}
	s.mu.Unlock()
	for _, timer := range ready {
		timer.fn()
	}
}

func (s *fakeScheduler) delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	delays := make([]time.Duration, 0, len(s.timers))
	for _, timer := range s.timers {
		delays = append(delays, timer.delay)
	}
	return delays
}

// fakeDataServer answers the host's data-provider calls. The default replies
// cover the common happy paths; individual tests override one behavior.
type fakeDataServer struct {
	mu sync.Mutex

	supports bool
	handler  func(appplugin.DataProviderRequest, context.Context) ([]byte, error)

	calls           []domainpluginabi.DataProviderMethod
	opens           int
	closes          int
	pings           int
	openTest        bool
	openFields      map[string]string
	openSecret      map[string]string
	openTunnel      map[string]string
	openMode        string
	lastQuery       string
	pingFail        bool
	openFail        string
	openFailMessage string
	echoSecret      bool

	queryReply map[string]domainplugin.DataResult
	queryToken map[string]uint64

	fetchRows   []domainplugin.DataResult
	fetchIndex  int
	fetchNotify chan struct{}
	fetchWait   bool
	cancelSeen  chan struct{}

	metadata     *domainplugin.DataMetadata
	plan         *domainplugin.DataResult
	capabilities *domainplugin.DataCapabilities
	cancelErr    error
}

func (s *fakeDataServer) SupportsDataProvider() bool { return s.supports }

func (s *fakeDataServer) InvokeDataProvider(ctx context.Context,
	request appplugin.DataProviderRequest) ([]byte, error) {

	s.mu.Lock()
	s.calls = append(s.calls, request.Method)
	handler := s.handler
	s.mu.Unlock()
	if handler != nil {
		return handler(request, ctx)
	}
	return s.respond(request, ctx)
}

func (s *fakeDataServer) methods() []domainpluginabi.DataProviderMethod {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domainpluginabi.DataProviderMethod(nil), s.calls...)
}

func (s *fakeDataServer) count(method domainpluginabi.DataProviderMethod) int {
	count := 0
	for _, called := range s.methods() {
		if called == method {
			count++
		}
	}
	return count
}

func (s *fakeDataServer) respond(request appplugin.DataProviderRequest,
	ctx context.Context) ([]byte, error) {

	switch request.Method {
	case domainpluginabi.DataProviderMethodOpen:
		return s.respondOpen(request.Payload)
	case domainpluginabi.DataProviderMethodClose:
		s.mu.Lock()
		s.closes++
		s.mu.Unlock()
		return dataOK(nil), nil
	case domainpluginabi.DataProviderMethodPing:
		s.mu.Lock()
		s.pings++
		fail := s.pingFail
		s.mu.Unlock()
		if fail {
			return dataStatus("unreachable", "ping failed"), nil
		}
		return dataOK(nil), nil
	case domainpluginabi.DataProviderMethodCapabilities:
		s.mu.Lock()
		capabilities := s.capabilities
		s.mu.Unlock()
		if capabilities == nil {
			capabilities = &domainplugin.DataCapabilities{
				Family: domainplugin.DataFamilyTabular,
				Features: []domainplugin.DataFeature{
					domainplugin.DataFeatureTransactions,
				},
				MaxConnections: 1,
			}
		}
		return dataOK(map[string]any{"capabilities": capabilities}), nil
	case domainpluginabi.DataProviderMethodQuery:
		return s.respondQuery(request.Payload)
	case domainpluginabi.DataProviderMethodFetch:
		return s.respondFetch(request, ctx)
	case domainpluginabi.DataProviderMethodCancel:
		s.mu.Lock()
		cancelErr := s.cancelErr
		s.mu.Unlock()
		if cancelErr != nil {
			return nil, cancelErr
		}
		return dataOK(nil), nil
	case domainpluginabi.DataProviderMethodCommand:
		return dataOK(map[string]any{
			"result": domainplugin.DataResult{
				Kind:    domainplugin.DataResultSummary,
				Summary: &domainplugin.DataSummary{AffectedRows: 1},
				Class:   domainplugin.DataClassRead,
			},
		}), nil
	case domainpluginabi.DataProviderMethodBegin:
		return dataOK(map[string]any{"transaction": uint64(11)}), nil
	case domainpluginabi.DataProviderMethodCommit,
		domainpluginabi.DataProviderMethodRollback:
		return dataOK(nil), nil
	case domainpluginabi.DataProviderMethodMetadata:
		s.mu.Lock()
		metadata := s.metadata
		s.mu.Unlock()
		if metadata == nil {
			metadata = &domainplugin.DataMetadata{
				Nodes: []domainplugin.DataMetadataNode{
					{Kind: domainplugin.DataMetadataCollection, Name: "tables", Path: "tables"},
				},
			}
		}
		return dataOK(map[string]any{"metadata": metadata}), nil
	case domainpluginabi.DataProviderMethodPlan:
		s.mu.Lock()
		plan := s.plan
		s.mu.Unlock()
		if plan == nil {
			plan = &domainplugin.DataResult{
				Kind: domainplugin.DataResultPlan,
				Plan: &domainplugin.DataPlan{Text: "scan"},
			}
		}
		return dataOK(map[string]any{"result": plan}), nil
	}
	return dataStatus("unsupported", "unknown method"), nil
}

func (s *fakeDataServer) respondOpen(payload []byte) ([]byte, error) {
	var open struct {
		ProfileID string                `json:"profileId"`
		Mode      string                `json:"mode"`
		Fields    map[string]string     `json:"fields"`
		Secrets   map[string]string     `json:"secrets"`
		Test      bool                  `json:"test"`
		Tunnel    *appplugin.DataTunnel `json:"tunnel"`
	}
	if err := json.Unmarshal(payload, &open); err != nil {
		return dataStatus("invalid", "bad open payload"), nil
	}
	s.mu.Lock()
	s.opens++
	number := s.opens
	s.openTest = open.Test
	s.openFields = open.Fields
	s.openSecret = open.Secrets
	s.openMode = open.Mode
	if open.Tunnel != nil {
		s.openTunnel = map[string]string{"host": open.Tunnel.Host, "endpoint": open.Tunnel.Endpoint}
	}
	fail := s.openFail
	failMessage := s.openFailMessage
	echo := s.echoSecret
	s.mu.Unlock()
	if fail != "" {
		if failMessage == "" {
			failMessage = "open refused"
		}
		return dataStatus(fail, failMessage), nil
	}
	response := map[string]any{"status": "ok", "connection": uint64(100 + number)}
	if open.Test {
		response["serverIdentity"] = "warehouse-1"
		response["authResult"] = "accepted"
	}
	if echo {
		response["serverIdentity"] = "identity:" + open.Secrets["password"]
	}
	return json.Marshal(response)
}

func (s *fakeDataServer) respondQuery(payload []byte) ([]byte, error) {
	var query struct {
		Query      string                   `json:"query"`
		Parameters []domainplugin.DataValue `json:"parameters"`
	}
	if err := json.Unmarshal(payload, &query); err != nil {
		return dataStatus("invalid", "bad query payload"), nil
	}
	s.mu.Lock()
	s.lastQuery = query.Query
	reply, ok := s.queryReply[query.Query]
	token := s.queryToken[query.Query]
	s.mu.Unlock()
	if !ok {
		reply = domainplugin.DataResult{
			Kind: domainplugin.DataResultTable,
			Table: &domainplugin.DataTable{
				Columns: []domainplugin.DataColumn{{Name: "value", Type: domainplugin.DataValueString}},
				Rows: [][]domainplugin.DataValue{{
					{Kind: domainplugin.DataValueString, String: "ok"},
				}},
			},
			Class: domainplugin.DataClassRead,
		}
	}
	if token == 0 {
		token = 7
	}
	return dataOK(map[string]any{"query": token, "result": reply}), nil
}

func (s *fakeDataServer) respondFetch(request appplugin.DataProviderRequest,
	ctx context.Context) ([]byte, error) {

	s.mu.Lock()
	wait := s.fetchWait
	notify := s.fetchNotify
	s.mu.Unlock()
	if wait {
		if notify != nil {
			select {
			case notify <- struct{}{}:
			default:
			}
		}
		// A real provider polls data_provider_cancelled while it works. The
		// fake polls the host callback with the same correlation id.
		for {
			if s.cancelRequested(request.ID) {
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			time.Sleep(time.Millisecond)
		}
		s.mu.Lock()
		s.cancelSeen <- struct{}{}
		s.mu.Unlock()
	}
	s.mu.Lock()
	if s.fetchIndex < len(s.fetchRows) {
		result := s.fetchRows[s.fetchIndex]
		s.fetchIndex++
		s.mu.Unlock()
		return dataOK(map[string]any{"result": result}), nil
	}
	s.mu.Unlock()
	result := domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{AffectedRows: 0},
		Class:   domainplugin.DataClassRead,
	}
	return dataOK(map[string]any{"result": result}), nil
}

// cancelRequested is installed by the test environment after registration.
var dataTestCancelled func(uint64) bool

func (s *fakeDataServer) cancelRequested(requestID uint64) bool {
	if dataTestCancelled == nil {
		return false
	}
	return dataTestCancelled(requestID)
}

func dataOK(extra map[string]any) []byte {
	response := map[string]any{"status": "ok"}
	for key, value := range extra {
		response[key] = value
	}
	// Encode without HTML escaping. The host bounds a provider payload by its
	// raw data bytes, and JSON does not require '<', '>', or '&' to be escaped;
	// a provider that writes them literally must not lose headroom to a
	// transport expansion the contract does not charge for.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(response); err != nil {
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func dataStatus(status, message string) []byte {
	encoded, _ := json.Marshal(map[string]any{"status": status, "message": message})
	return encoded
}

type dataEnv struct {
	t        *testing.T
	host     *appplugin.DataProviderHost
	server   *fakeDataServer
	profiles *appplugin.MemoryDataProfileStore
	secrets  *appplugin.MemoryDataProfileSecrets
	tunnel   *appplugin.MemoryDataTunnelSource
	audit    *dataRecordingAudit
	schedule *fakeScheduler
	clock    *fakeClock
	provider *appplugin.DataProvider
	profile  domainplugin.DataProfile
}

func newDataEnv(t *testing.T, maxMode domainplugin.DataAccessMode,
	limits appplugin.DataLimits) *dataEnv {

	t.Helper()
	env := &dataEnv{
		t: t,
		server: &fakeDataServer{
			supports:   true,
			queryReply: map[string]domainplugin.DataResult{},
			queryToken: map[string]uint64{},
		},
		profiles: appplugin.NewMemoryDataProfileStore(),
		secrets:  appplugin.NewMemoryDataProfileSecrets(),
		tunnel:   &appplugin.MemoryDataTunnelSource{Endpoint: "host-owned:2222"},
		audit:    &dataRecordingAudit{},
		schedule: &fakeScheduler{},
		clock: &fakeClock{
			current: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC),
			step:    10 * time.Millisecond,
		},
	}
	env.host = appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Audit:    env.audit,
		Now:      env.clock.Now,
		Schedule: env.schedule.Schedule,
		Limits:   limits,
		Profiles: env.profiles,
		Secrets:  env.secrets,
		Tunnel:   env.tunnel,
		NewProfileID: func() string {
			return "profile-1"
		},
	})
	provider, err := env.host.Register(dataTestFiber,
		appplugin.PluginRef{ID: "acme.data"}, dataTestContribution(), maxMode, env.server)
	if err != nil {
		t.Fatalf("register provider: %v", err)
	}
	env.provider = provider
	dataTestCancelled = func(requestID uint64) bool {
		return env.host.Callbacks(dataTestFiber).Cancelled(requestID)
	}
	t.Cleanup(func() { dataTestCancelled = nil })
	return env
}

func (e *dataEnv) saveProfile(draft domainplugin.DataProfileDraft) domainplugin.DataProfile {
	e.t.Helper()
	profile, err := e.host.SaveProfile(context.Background(), "", draft)
	if err != nil {
		e.t.Fatalf("save profile: %v", err)
	}
	e.profile = profile
	return profile
}

func (e *dataEnv) acquire(mode domainplugin.DataAccessMode) *appplugin.DataLease {
	e.t.Helper()
	lease, err := e.host.Acquire(context.Background(), e.profile.ID, mode)
	if err != nil {
		e.t.Fatalf("acquire: %v", err)
	}
	return lease
}

func wantDataKind(t *testing.T, err error, kind appplugin.DataErrorKind) {
	t.Helper()
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Kind != kind {
		t.Fatalf("error = %v, want data error %q", err, kind)
	}
}

// dataTestLeaksSecret reports whether text carries secret or its leading
// eight bytes as raw text, a Go %q escape, or a JSON escape.
func dataTestLeaksSecret(text, secret string) bool {
	values := []string{secret}
	if len(secret) >= 8 {
		values = append(values, secret[:8])
	}
	forms := make([]string, 0, 4*len(values))
	for _, value := range values {
		forms = append(forms, value)
		if quoted := strconv.Quote(value); len(quoted) > 1 {
			forms = append(forms, quoted[1:len(quoted)-1])
		}
		if quoted := strconv.QuoteToASCII(value); len(quoted) > 1 {
			forms = append(forms, quoted[1:len(quoted)-1])
		}
		if encoded, err := json.Marshal(value); err == nil && len(encoded) > 1 {
			forms = append(forms, string(encoded[1:len(encoded)-1]))
		}
	}
	for _, form := range forms {
		if form != "" && strings.Contains(text, form) {
			return true
		}
	}
	return false
}

func TestDataProfilesPersistSecretReferences(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})

	profile := env.saveProfile(dataTestDraft())
	if profile.ID != "profile-1" || profile.Provider != "plugin.acme.data.warehouse" {
		t.Fatalf("profile = %+v", profile)
	}
	if profile.Fields["endpoint"] != "warehouse.example.test:443" ||
		profile.Fields["flavor"] != "safe" {
		t.Fatalf("profile fields = %v", profile.Fields)
	}
	if !strings.HasPrefix(profile.SecretRefs["password"], "memory:") ||
		!strings.HasPrefix(profile.SecretRefs["token"], "memory:") {
		t.Fatalf("secret refs = %v", profile.SecretRefs)
	}
	encoded, _ := json.Marshal(profile)
	for _, secret := range []string{"hunter2", "tok-1"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("profile record contains plaintext %q: %s", secret, encoded)
		}
	}
	if value, err := env.secrets.Get(context.Background(),
		profile.SecretRefs["password"]); err != nil || value != "hunter2" {
		t.Fatalf("stored secret = %q, %v", value, err)
	}

	listed, err := env.host.ListProfiles(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != profile.ID {
		t.Fatalf("list = %+v, %v", listed, err)
	}

	result, err := env.host.TestProfile(context.Background(), profile.ID)
	if err != nil {
		t.Fatalf("test profile: %v", err)
	}
	if result.LatencyMS <= 0 || result.ServerIdentity != "warehouse-1" ||
		result.AuthResult != "accepted" {
		t.Fatalf("test result = %+v", result)
	}
	if !env.server.openTest || env.server.openSecret["password"] != "hunter2" {
		t.Fatalf("test open payload = test %v secrets %v",
			env.server.openTest, env.server.openSecret)
	}
	if env.server.openFields["endpoint"] != "warehouse.example.test:443" {
		t.Fatalf("open fields = %v", env.server.openFields)
	}

	// An update that omits the secret keeps the existing reference and does
	// not need the plaintext again.
	draft := dataTestDraft()
	draft.Label = "Reporting v2"
	delete(draft.Secrets, "password")
	updated, err := env.host.SaveProfile(context.Background(), profile.ID, draft)
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if updated.SecretRefs["password"] != profile.SecretRefs["password"] ||
		updated.Label != "Reporting v2" {
		t.Fatalf("updated profile = %+v", updated)
	}

	// Clearing an optional secret removes the stored value.
	clear := dataTestDraft()
	delete(clear.Secrets, "token")
	clear.ClearSecrets = []string{"token"}
	if _, err := env.host.SaveProfile(context.Background(), profile.ID, clear); err != nil {
		t.Fatalf("clear optional secret: %v", err)
	}
	if _, err := env.secrets.Get(context.Background(),
		profile.SecretRefs["token"]); !errors.Is(err, appplugin.ErrDataSecretNotFound) {
		t.Fatalf("cleared secret still resolves: %v", err)
	}

	if err := env.host.DeleteProfile(context.Background(), profile.ID); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	if _, err := env.secrets.Get(context.Background(),
		profile.SecretRefs["password"]); !errors.Is(err, appplugin.ErrDataSecretNotFound) {
		t.Fatalf("secret survived profile deletion: %v", err)
	}
	if _, err := env.host.GetProfile(context.Background(), profile.ID); !errors.Is(err,
		appplugin.ErrDataProfileNotFound) {
		t.Fatalf("deleted profile lookup = %v", err)
	}

	if env.audit.hasReason("data.secret:ok") == false {
		t.Fatal("secret reads were not audited")
	}
	if strings.Contains(env.audit.encoded(), "hunter2") ||
		strings.Contains(env.audit.encoded(), "tok-1") {
		t.Fatal("audit recorded secret content")
	}
}

// failOnceProfileStore delegates to a healthy store and fails one Save.
type failOnceProfileStore struct {
	appplugin.DataProfileStore
	failSave bool
}

func (s *failOnceProfileStore) Save(ctx context.Context,
	profile domainplugin.DataProfile) error {
	if s.failSave {
		s.failSave = false
		return errors.New("profile store is full")
	}
	return s.DataProfileStore.Save(ctx, profile)
}

func TestDataProfileUpdateRestoresSecretWhenSaveFails(t *testing.T) {
	ctx := context.Background()
	profiles := &failOnceProfileStore{
		DataProfileStore: appplugin.NewMemoryDataProfileStore(),
	}
	secrets := appplugin.NewMemoryDataProfileSecrets()
	host := appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Profiles: profiles,
		Secrets:  secrets,
	})
	server := &fakeDataServer{supports: true}
	if _, err := host.Register(dataTestFiber, appplugin.PluginRef{ID: "acme.data"},
		dataTestContribution(), domainplugin.DataModeReadWrite, server); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer host.Close()
	profile, err := host.SaveProfile(ctx, "", dataTestDraft())
	if err != nil {
		t.Fatalf("save profile: %v", err)
	}

	replacement := dataTestDraft()
	replacement.Secrets["password"] = "replacement-secret"
	profiles.failSave = true
	if _, err := host.SaveProfile(ctx, profile.ID, replacement); err == nil {
		t.Fatal("the failing save was accepted")
	}
	value, err := secrets.Get(ctx, profile.SecretRefs["password"])
	if err != nil || value != "hunter2" {
		t.Fatalf("previous secret after failed update = %q, %v", value, err)
	}
}

func TestDataProfileDraftTestDoesNotPersistAndRejectsSecretEcho(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	draft := dataTestDraft()
	draft.Secrets["password"] = "draft-secret"
	result, err := env.host.TestDraftProfile(context.Background(), draft)
	if err != nil {
		t.Fatalf("test draft: %v", err)
	}
	if result.AuthResult != "accepted" {
		t.Fatalf("draft result = %+v", result)
	}
	if profiles, _ := env.host.ListProfiles(context.Background()); len(profiles) != 0 {
		t.Fatalf("draft test persisted a profile: %+v", profiles)
	}
	if env.server.openSecret["password"] != "draft-secret" {
		t.Fatalf("draft secret was not delivered for the test: %v", env.server.openSecret)
	}

	env.server.echoSecret = true
	_, err = env.host.TestDraftProfile(context.Background(), draft)
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if strings.Contains(err.Error(), "draft-secret") {
		t.Fatal("echo error leaked the secret")
	}

	// A provider error message is not a secret channel either.
	env.server.echoSecret = false
	env.server.openFail = "auth-failed"
	env.server.openFailMessage = "credential draft-secret was rejected"
	_, err = env.host.TestDraftProfile(context.Background(), draft)
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if strings.Contains(err.Error(), "draft-secret") {
		t.Fatal("provider error message leaked the secret")
	}
}

func TestDataLimitsDefaultsFollowTheContract(t *testing.T) {
	limits := appplugin.DataLimits{}.Normalize()
	if limits.InteractiveRows != 100_000 || limits.InteractiveBytes != 64<<20 ||
		limits.IdleTTL != 5*time.Minute {
		t.Fatalf("default data limits = %+v", limits)
	}
	widened := appplugin.DataLimits{
		InteractiveRows:  1_000_000,
		InteractiveBytes: 1 << 30,
	}.Normalize()
	if widened.InteractiveRows != 100_000 || widened.InteractiveBytes != 64<<20 {
		t.Fatalf("widened budgets were accepted: %+v", widened)
	}
}

func TestDataProviderWouldBlockKeepsQueryOpen(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	env.server.queryReply["wait"] = domainplugin.DataResult{WouldBlock: true}
	env.server.queryToken["wait"] = 7
	query, first, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "wait"})
	if err != nil || !first.WouldBlock {
		t.Fatalf("would-block query = %+v, %v", first, err)
	}
	env.server.fetchRows = []domainplugin.DataResult{{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{AffectedRows: 1},
		Class:   domainplugin.DataClassRead,
	}}
	second, err := query.Fetch(context.Background())
	if err != nil || second.Kind != domainplugin.DataResultSummary {
		t.Fatalf("fetch after would-block = %+v, %v", second, err)
	}
	if query.Class() != domainplugin.DataClassUnknown {
		t.Fatalf("would-block query class = %q", query.Class())
	}
}

func TestDataProviderTypedQueryAndEnvelopes(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)

	table := domainplugin.DataResult{
		Kind: domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{
			Columns: []domainplugin.DataColumn{
				{Name: "id", Type: domainplugin.DataValueInt},
				{Name: "amount", Type: domainplugin.DataValueDecimal},
				{Name: "seen", Type: domainplugin.DataValueTime},
			},
			Rows: [][]domainplugin.DataValue{{
				{Kind: domainplugin.DataValueInt, Int: "9007199254740993"},
				{Kind: domainplugin.DataValueDecimal, Decimal: "1234567890.000000000000000001"},
				{Kind: domainplugin.DataValueTime, Time: "2026-11-08T10:00:00+08:00"},
			}},
		},
		Class: domainplugin.DataClassRead,
	}
	env.server.queryReply["typed"] = table
	query, result, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "typed"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if result.Table.Rows[0][0].Int != "9007199254740993" ||
		result.Table.Rows[0][1].Decimal != "1234567890.000000000000000001" {
		t.Fatalf("precision was lost: %+v", result.Table.Rows[0])
	}
	if query.Class() != domainplugin.DataClassRead || result.More {
		t.Fatalf("query = class %q more %v", query.Class(), result.More)
	}

	envelopes := map[string]domainplugin.DataResult{
		"documents": {
			Kind: domainplugin.DataResultDocuments,
			Documents: &domainplugin.DataDocuments{Documents: []map[string]domainplugin.DataValue{{
				"id": {Kind: domainplugin.DataValueInt, Int: "1"},
			}}},
			Class: domainplugin.DataClassRead,
		},
		"pairs": {
			Kind: domainplugin.DataResultPairs,
			Pairs: &domainplugin.DataPairs{Pairs: []domainplugin.DataPair{{
				Key:   domainplugin.DataValue{Kind: domainplugin.DataValueString, String: "k"},
				Value: domainplugin.DataValue{Kind: domainplugin.DataValueBool, Bool: true},
			}}},
			Class: domainplugin.DataClassRead,
		},
		"chunks": {
			Kind: domainplugin.DataResultChunks,
			Chunks: &domainplugin.DataChunks{Chunks: []domainplugin.DataChunk{{
				Offset: 0, Data: []byte("stream"),
			}}},
			Class: domainplugin.DataClassRead,
		},
		"summary": {
			Kind:    domainplugin.DataResultSummary,
			Summary: &domainplugin.DataSummary{AffectedRows: 3},
			Class:   domainplugin.DataClassRead,
		},
	}
	for name, envelope := range envelopes {
		env.server.queryReply[name] = envelope
		_, result, err := lease.Query(context.Background(),
			appplugin.DataQueryRequest{Query: name})
		if err != nil {
			t.Fatalf("%s query: %v", name, err)
		}
		if result.Kind != envelope.Kind {
			t.Fatalf("%s kind = %q", name, result.Kind)
		}
	}

	capabilities, err := lease.Capabilities(context.Background())
	if err != nil || capabilities.MaxConnections != 1 ||
		!capabilities.Supports(domainplugin.DataFeatureTransactions) {
		t.Fatalf("capabilities = %+v, %v", capabilities, err)
	}
	metadata, err := lease.Metadata(context.Background(), "tables", "")
	if err != nil || len(metadata.Nodes) != 1 {
		t.Fatalf("metadata = %+v, %v", metadata, err)
	}
	plan, err := lease.Plan(context.Background(), "select 1", nil)
	if err != nil || plan.Kind != domainplugin.DataResultPlan {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	transaction, err := lease.Begin(context.Background())
	if err != nil || transaction != 11 {
		t.Fatalf("begin = %d, %v", transaction, err)
	}
	if err := lease.Commit(context.Background(), transaction); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := lease.Rollback(context.Background(), transaction); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	command, err := lease.Command(context.Background(),
		appplugin.DataCommandRequest{Command: "select 1"})
	if err != nil || command.Kind != domainplugin.DataResultSummary {
		t.Fatalf("command = %+v, %v", command, err)
	}
	lease.Release()
}

func TestDataProviderRejectsInvalidEnvelopesAndOversizedInput(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	tooMany := make([][]domainplugin.DataValue, domainplugin.MaxDataBatchRows+1)
	env.server.queryReply["too-many"] = domainplugin.DataResult{
		Kind: domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{
			Columns: []domainplugin.DataColumn{{Name: "n", Type: domainplugin.DataValueInt}},
			Rows:    tooMany,
		},
		Class: domainplugin.DataClassRead,
	}
	_, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "too-many"})
	wantDataKind(t, err, appplugin.DataErrorProtocol)

	oversized := domainplugin.DataResult{
		Kind: domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{
			Columns: []domainplugin.DataColumn{{Name: "v", Type: domainplugin.DataValueString}},
			Rows: [][]domainplugin.DataValue{{
				{Kind: domainplugin.DataValueString,
					String: strings.Repeat("x", domainplugin.MaxDataBatchBytes)},
			}},
		},
		Class: domainplugin.DataClassRead,
	}
	env.server.queryReply["oversized"] = oversized
	_, _, err = lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "oversized"})
	wantDataKind(t, err, appplugin.DataErrorProtocol)

	_, _, err = lease.Query(context.Background(), appplugin.DataQueryRequest{
		Query: strings.Repeat("q", domainplugin.MaxDataInputBytes+1),
	})
	wantDataKind(t, err, appplugin.DataErrorLimit)

	parameters := make([]domainplugin.DataValue, domainplugin.MaxDataParameters+1)
	for i := range parameters {
		parameters[i] = domainplugin.DataValue{Kind: domainplugin.DataValueInt, Int: "1"}
	}
	_, _, err = lease.Query(context.Background(), appplugin.DataQueryRequest{
		Query: "many-params", Parameters: parameters,
	})
	wantDataKind(t, err, appplugin.DataErrorLimit)

	env.server.queryReply["outside"] = domainplugin.DataResult{
		Kind:  domainplugin.DataResultSummary,
		Class: "invented",
	}
	env.server.queryReply["outside"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{},
		Class:   "invented",
	}
	_, _, err = lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "outside"})
	wantDataKind(t, err, appplugin.DataErrorDenied)
}

func TestDataProviderInteractiveBudget(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly,
		appplugin.DataLimits{InteractiveRows: 5, InteractiveBytes: 1 << 20})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	row := []domainplugin.DataValue{{Kind: domainplugin.DataValueString, String: "x"}}
	columns := []domainplugin.DataColumn{{Name: "v", Type: domainplugin.DataValueString}}
	env.server.queryReply["many"] = domainplugin.DataResult{
		Kind:  domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{Columns: columns, Rows: [][]domainplugin.DataValue{row, row, row}},
		More:  true,
		Class: domainplugin.DataClassRead,
	}
	env.server.queryToken["many"] = 7
	env.server.fetchRows = []domainplugin.DataResult{{
		Kind:  domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{Columns: columns, Rows: [][]domainplugin.DataValue{row, row, row}},
		More:  true,
		Class: domainplugin.DataClassRead,
	}}

	query, first, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "many"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(first.Table.Rows) != 3 || !first.More || first.Limit {
		t.Fatalf("first batch = %d rows, more %v, limit %v",
			len(first.Table.Rows), first.More, first.Limit)
	}
	second, err := query.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(second.Table.Rows) != 2 || !second.Limit || second.More {
		t.Fatalf("second batch = %d rows, more %v, limit %v",
			len(second.Table.Rows), second.More, second.Limit)
	}
	if _, err := query.Fetch(context.Background()); err == nil {
		t.Fatal("fetch after the limit was accepted")
	}
	if !env.audit.hasReason("data.fetch:limit") {
		t.Fatalf("limit was not audited: %s", env.audit.encoded())
	}
}

func TestDataProviderByteBudget(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly,
		appplugin.DataLimits{InteractiveBytes: 600})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	rows := make([][]domainplugin.DataValue, 6)
	for i := range rows {
		rows[i] = []domainplugin.DataValue{{
			Kind: domainplugin.DataValueString, String: strings.Repeat("x", 100),
		}}
	}
	env.server.queryReply["wide"] = domainplugin.DataResult{
		Kind: domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{
			Columns: []domainplugin.DataColumn{{Name: "v", Type: domainplugin.DataValueString}},
			Rows:    rows,
		},
		More: true, Class: domainplugin.DataClassRead,
	}
	_, result, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "wide"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !result.Limit || result.More || len(result.Table.Rows) >= len(rows) {
		t.Fatalf("byte budget = %d rows, limit %v, more %v",
			len(result.Table.Rows), result.Limit, result.More)
	}
	if result.DataResultBytes() > 600 {
		t.Fatalf("delivered %d bytes, budget 600", result.DataResultBytes())
	}
}

func TestDataProviderCancellationMarksPartial(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	row := []domainplugin.DataValue{{Kind: domainplugin.DataValueString, String: "delivered"}}
	env.server.queryReply["slow"] = domainplugin.DataResult{
		Kind:  domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{Columns: []domainplugin.DataColumn{{Name: "v", Type: domainplugin.DataValueString}}},
		More:  true, Class: domainplugin.DataClassRead,
	}
	env.server.queryToken["slow"] = 9
	env.server.fetchWait = true
	env.server.fetchNotify = make(chan struct{}, 1)
	env.server.cancelSeen = make(chan struct{}, 1)
	env.server.fetchRows = []domainplugin.DataResult{{
		Kind: domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{
			Columns: []domainplugin.DataColumn{{Name: "v", Type: domainplugin.DataValueString}},
			Rows:    [][]domainplugin.DataValue{row, row},
		},
		More: true, Class: domainplugin.DataClassRead,
	}}

	query, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "slow"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type fetchResult struct {
		result domainplugin.DataResult
		err    error
	}
	done := make(chan fetchResult, 1)
	go func() {
		result, err := query.Fetch(ctx)
		done <- fetchResult{result: result, err: err}
	}()
	select {
	case <-env.server.fetchNotify:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not start")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("cancelled fetch: %v", got.err)
		}
		if !got.result.Partial || got.result.More {
			t.Fatalf("cancelled result = partial %v, more %v",
				got.result.Partial, got.result.More)
		}
		if len(got.result.Table.Rows) != 2 {
			t.Fatalf("cancelled result delivered %d rows", len(got.result.Table.Rows))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled fetch did not return")
	}
	select {
	case <-env.server.cancelSeen:
	case <-time.After(time.Second):
		t.Fatal("the provider did not observe cancellation")
	}
	if !env.audit.hasReason("data.fetch:cancelled") &&
		!env.audit.hasReason("data.fetch:partial") {
		t.Fatalf("cancellation was not audited: %s", env.audit.encoded())
	}
}

func TestDataProviderPoolPingsBeforeReuseAndIdlesOut(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	token := lease.ConnectionID()
	lease.Release()
	if env.host.DataPoolSize(env.profile.ID, domainplugin.DataModeReadOnly) != 1 {
		t.Fatal("released connection was not pooled")
	}
	delays := env.schedule.delays()
	if len(delays) != 1 || delays[0] != appplugin.DefaultDataIdleTTL {
		t.Fatalf("idle delays = %v, want %v", delays, appplugin.DefaultDataIdleTTL)
	}

	reused := env.acquire(domainplugin.DataModeReadOnly)
	if reused.ConnectionID() != token || env.server.count(domainpluginabi.DataProviderMethodPing) != 1 {
		t.Fatalf("reuse token %d ping count %d",
			reused.ConnectionID(), env.server.count(domainpluginabi.DataProviderMethodPing))
	}
	if env.host.DataPoolSize(env.profile.ID, domainplugin.DataModeReadOnly) != 0 {
		t.Fatal("reused connection stayed in the pool")
	}
	reused.Release()
	env.schedule.Fire()
	if env.server.count(domainpluginabi.DataProviderMethodClose) == 0 {
		t.Fatal("idle expiration did not close the connection")
	}
	if env.host.DataConnectionCount(env.profile.ID) != 0 {
		t.Fatal("idle connection remained tracked")
	}
	if !env.audit.hasReason("data.idle:expired") {
		t.Fatal("idle expiration was not audited")
	}
}

func TestDataProviderPoolReopensAfterFailedPing(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	first := env.acquire(domainplugin.DataModeReadOnly)
	firstToken := first.ConnectionID()
	first.Release()

	env.server.pingFail = true
	second := env.acquire(domainplugin.DataModeReadOnly)
	if second.ConnectionID() == firstToken {
		t.Fatal("a failed ping reused the connection")
	}
	if env.server.opens != 2 {
		t.Fatalf("opens = %d, want 2", env.server.opens)
	}
	second.Release()
}

func TestDataProviderConnectionLimit(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	first := env.acquire(domainplugin.DataModeReadOnly)
	defer first.Release()
	second := env.acquire(domainplugin.DataModeReadOnly)
	defer second.Release()
	_, err := env.host.Acquire(context.Background(), env.profile.ID,
		domainplugin.DataModeReadOnly)
	wantDataKind(t, err, appplugin.DataErrorLimit)
	if !env.audit.hasReason("data.open:limit") {
		t.Fatal("connection limit was not audited")
	}
	first.Release()
	third, err := env.host.Acquire(context.Background(), env.profile.ID,
		domainplugin.DataModeReadOnly)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	third.Release()
}

func TestDataProviderAccessModesAndAudit(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadWrite, appplugin.DataLimits{})
	draft := dataTestDraft()
	draft.Mode = domainplugin.DataModeFull
	_, err := env.host.SaveProfile(context.Background(), "", draft)
	wantDataKind(t, err, appplugin.DataErrorDenied)
	if !env.audit.hasReason("data.profile:escalation") {
		t.Fatal("profile escalation was not audited")
	}

	profile := env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	env.server.queryReply["secret query text"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{AffectedRows: 1},
		Class:   domainplugin.DataClassWrite,
	}
	_, _, err = lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "secret query text"})
	wantDataKind(t, err, appplugin.DataErrorDenied)
	if !env.audit.hasReason("data.query:denied") {
		t.Fatal("write class in read-only mode was not audited")
	}

	_, err = env.host.Acquire(context.Background(), profile.ID,
		domainplugin.DataModeFull)
	wantDataKind(t, err, appplugin.DataErrorDenied)
	if !env.audit.hasReason("data.open:escalation") {
		t.Fatal("mode escalation was not audited")
	}

	if strings.Contains(env.audit.encoded(), "secret query text") {
		t.Fatal("audit recorded query text")
	}
}

func TestDataProviderFullModeAndProviderGrant(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeFull, appplugin.DataLimits{})
	draft := dataTestDraft()
	draft.Mode = domainplugin.DataModeFull
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeFull)
	defer lease.Release()

	env.server.queryReply["destructive"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{AffectedRows: 1},
		Class:   domainplugin.DataClassDestructive,
	}
	if _, result, err := lease.Query(context.Background(), appplugin.DataQueryRequest{
		Query: "destructive",
	}); err != nil || result.Kind != domainplugin.DataResultSummary {
		t.Fatalf("full mode query = %+v, %v", result, err)
	}

	readOnly := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	readOnly.saveProfile(dataTestDraft())
	_, err := readOnly.host.Acquire(context.Background(), readOnly.profile.ID,
		domainplugin.DataModeReadOnly)
	if err != nil {
		t.Fatalf("read-only acquire: %v", err)
	}
	_, err = readOnly.host.Acquire(context.Background(), readOnly.profile.ID,
		domainplugin.DataModeReadWrite)
	wantDataKind(t, err, appplugin.DataErrorDenied)
}

func TestDataProviderRegistrationAndDormantProfiles(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())

	missing := &fakeDataServer{}
	_, err := env.host.Register("fiber-2", appplugin.PluginRef{ID: "acme.other"},
		dataTestContribution(), domainplugin.DataModeReadOnly, missing)
	if err == nil || !strings.Contains(err.Error(), domainpluginabi.ExportDataProviderRequest) {
		t.Fatalf("registration without export = %v", err)
	}
	_, err = env.host.Register(dataTestFiber, appplugin.PluginRef{ID: "acme.data"},
		dataTestContribution(), "", env.server)
	if err == nil {
		t.Fatal("registration without an access grant was accepted")
	}
	_, err = env.host.Register(dataTestFiber, appplugin.PluginRef{ID: "acme.data"},
		dataTestContribution(), domainplugin.DataModeReadOnly, env.server)
	if err == nil {
		t.Fatal("duplicate registration was accepted")
	}

	if err := env.host.Unregister(env.provider); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if err := env.host.Unregister(env.provider); err != nil {
		t.Fatalf("second unregister: %v", err)
	}
	_, err = env.host.Acquire(context.Background(), env.profile.ID,
		domainplugin.DataModeReadOnly)
	wantDataKind(t, err, appplugin.DataErrorUnavailable)
	env.host.Close()
	env.host.Close()
}

func TestDataProviderTunnelIsHostOwned(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	hostID := dataTestHostID(t)
	draft := dataTestDraft()
	draft.ViaHost = hostID
	profile := env.saveProfile(draft)
	stored, err := env.host.GetProfile(context.Background(), profile.ID)
	if err != nil || stored.ViaHost != hostID {
		t.Fatalf("round-tripped via host = %q, %v", stored.ViaHost, err)
	}
	if _, err := env.host.TestProfile(context.Background(), profile.ID); err != nil {
		t.Fatalf("test through tunnel: %v", err)
	}
	hosts := env.tunnel.Hosts()
	if len(hosts) != 1 || hosts[0] != hostID {
		t.Fatalf("tunnel hosts = %v", hosts)
	}
	if env.server.openTunnel["host"] != hostID ||
		env.server.openTunnel["endpoint"] != "host-owned:2222" {
		t.Fatalf("provider tunnel = %v", env.server.openTunnel)
	}

	// A connection-token shaped value is not a Host ID and is refused.
	invalid := dataTestDraft()
	invalid.ViaHost = "host-42"
	if _, err := env.host.SaveProfile(context.Background(), "", invalid); err == nil ||
		!strings.Contains(err.Error(), "via_host") {
		t.Fatalf("non-Host via_host = %v", err)
	}

	noTunnel := appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Profiles: env.profiles,
		Secrets:  env.secrets,
	})
	provider, err := noTunnel.Register(dataTestFiber, appplugin.PluginRef{ID: "acme.data"},
		dataTestContribution(), domainplugin.DataModeReadOnly, env.server)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer noTunnel.Close()
	_ = provider
	_, err = noTunnel.TestProfile(context.Background(), profile.ID)
	wantDataKind(t, err, appplugin.DataErrorUnsupported)
}

func TestDataProviderCommandsRequireMutationFeature(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	env.server.handler = func(request appplugin.DataProviderRequest,
		ctx context.Context) ([]byte, error) {
		return dataOK(map[string]any{
			"result": domainplugin.DataResult{
				Kind:    domainplugin.DataResultSummary,
				Summary: &domainplugin.DataSummary{AffectedRows: 1},
				Class:   domainplugin.DataClassWrite,
			},
		}), nil
	}
	_, err := lease.Command(context.Background(),
		appplugin.DataCommandRequest{Command: "delete everything"})
	wantDataKind(t, err, appplugin.DataErrorDenied)
	if !env.audit.hasReason("data.command:denied") {
		t.Fatal("command escalation was not audited")
	}
	if strings.Contains(env.audit.encoded(), "delete everything") {
		t.Fatal("audit recorded command text")
	}
}

func TestDataProviderCancelMethod(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()
	env.server.queryReply["stream"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{},
		More:    true,
		Class:   domainplugin.DataClassRead,
	}
	query, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "stream"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if err := query.Cancel(context.Background()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if env.server.count(domainpluginabi.DataProviderMethodCancel) != 1 {
		t.Fatal("cancel did not reach the provider")
	}
	result, err := query.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch after cancel: %v", err)
	}
	if !result.Partial {
		t.Fatal("cancelled query did not report partial rows")
	}
}

func TestDataProviderProfileNotFound(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	_, err := env.host.Acquire(context.Background(), "missing",
		domainplugin.DataModeReadOnly)
	if !errors.Is(err, appplugin.ErrDataProfileNotFound) {
		t.Fatalf("missing profile = %v", err)
	}
	_, err = env.host.TestProfile(context.Background(), "missing")
	if !errors.Is(err, appplugin.ErrDataProfileNotFound) {
		t.Fatalf("test missing profile = %v", err)
	}
}

func TestDataProfileTestRequiresTestingFeature(t *testing.T) {
	contribution := dataTestContribution()
	features := make([]domainplugin.DataFeature, 0,
		len(contribution.Capabilities.Features))
	for _, feature := range contribution.Capabilities.Features {
		if feature != domainplugin.DataFeatureTesting {
			features = append(features, feature)
		}
	}
	contribution.Capabilities.Features = features
	server := &fakeDataServer{supports: true}
	host := appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Profiles: appplugin.NewMemoryDataProfileStore(),
		Secrets:  appplugin.NewMemoryDataProfileSecrets(),
	})
	if _, err := host.Register(dataTestFiber, appplugin.PluginRef{ID: "acme.data"},
		contribution, domainplugin.DataModeReadOnly, server); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer host.Close()
	profile, err := host.SaveProfile(context.Background(), "", dataTestDraft())
	if err != nil {
		t.Fatalf("save profile: %v", err)
	}
	_, err = host.TestProfile(context.Background(), profile.ID)
	wantDataKind(t, err, appplugin.DataErrorUnsupported)
	if server.count(domainpluginabi.DataProviderMethodOpen) != 0 {
		t.Fatal("an unsupported profile test reached the provider")
	}
}

func TestDataProfileTestRejectsSecretEchoBeforeTruncation(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	longSecret := strings.Repeat("S", 600)
	draft := dataTestDraft()
	draft.Secrets["password"] = longSecret
	profile := env.saveProfile(draft)

	// A secret longer than the 512-byte identity display bound is echoed in
	// full by the provider. Scanning only the truncated value would miss it.
	env.server.echoSecret = true
	_, err := env.host.TestProfile(context.Background(), profile.ID)
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if strings.Contains(err.Error(), longSecret) ||
		strings.Contains(err.Error(), longSecret[:128]) {
		t.Fatalf("long identity echo leaked: %v", err)
	}

	// The same secret in a provider error message is longer than the 256-byte
	// message bound and must still be caught before truncation.
	env.server.echoSecret = false
	env.server.openFail = "auth-failed"
	env.server.openFailMessage = "credential " + longSecret + " was rejected"
	_, err = env.host.TestProfile(context.Background(), profile.ID)
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if strings.Contains(err.Error(), longSecret[:128]) {
		t.Fatalf("long error echo leaked: %v", err)
	}

	// Control: a benign long provider message still reaches the caller, bounded
	// to the documented 256-byte message limit.
	benign := strings.Repeat("b", 600)
	env.server.openFailMessage = benign
	_, err = env.host.TestProfile(context.Background(), profile.ID)
	wantDataKind(t, err, appplugin.DataErrorAuthFailed)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Message != benign[:256] {
		t.Fatalf("benign long message = %q", dataErr.Message)
	}
}

func TestDataProfileTestChecksEveryResolvedSecret(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	longSecret := strings.Repeat("P", 600)
	token := "token-secret-value"
	draft := dataTestDraft()
	draft.Secrets["password"] = longSecret
	draft.Secrets["token"] = token
	profile := env.saveProfile(draft)

	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodOpen {
			response, _ := json.Marshal(map[string]any{
				"status": "ok", "serverIdentity": "identity:" + token,
				"authResult": "accepted",
			})
			return response, nil
		}
		return dataOK(nil), nil
	}
	_, err := env.host.TestProfile(context.Background(), profile.ID)
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if strings.Contains(err.Error(), token) {
		t.Fatalf("second secret echo leaked: %v", err)
	}
}

func TestDataProviderMessagesNeverEchoSecrets(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	longSecret := strings.Repeat("Q", 600)
	otherSecret := "tok-other-secret"
	draft := dataTestDraft()
	draft.Secrets["password"] = longSecret
	draft.Secrets["token"] = otherSecret
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	// Start a tracked query so Fetch can be exercised too.
	env.server.queryReply["stream"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{},
		More:    true, Class: domainplugin.DataClassRead,
	}
	env.server.queryToken["stream"] = 9
	query, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "stream"})
	if err != nil {
		t.Fatalf("start stream: %v", err)
	}

	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		switch request.Method {
		case domainpluginabi.DataProviderMethodOpen,
			domainpluginabi.DataProviderMethodPing,
			domainpluginabi.DataProviderMethodClose,
			domainpluginabi.DataProviderMethodCancel:
			return dataOK(nil), nil
		case domainpluginabi.DataProviderMethodQuery,
			domainpluginabi.DataProviderMethodFetch,
			domainpluginabi.DataProviderMethodMetadata:
			return dataStatus("invalid", "credential "+longSecret+" rejected"), nil
		default:
			return dataStatus("denied", "token "+otherSecret+" rejected"), nil
		}
	}
	check := func(name string, err error) {
		t.Helper()
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		if strings.Contains(err.Error(), longSecret[:128]) ||
			strings.Contains(err.Error(), otherSecret) {
			t.Fatalf("%s leaked a secret: %v", name, err)
		}
	}
	_, _, err = lease.Query(context.Background(), appplugin.DataQueryRequest{Query: "bad"})
	check("query", err)
	_, err = lease.Command(context.Background(), appplugin.DataCommandRequest{Command: "bad"})
	check("command", err)
	_, err = query.Fetch(context.Background())
	check("fetch", err)
	_, err = lease.Metadata(context.Background(), "", "")
	check("metadata", err)
	_, err = lease.Capabilities(context.Background())
	check("capabilities", err)
	_, err = lease.Begin(context.Background())
	check("begin", err)
	_, err = lease.Plan(context.Background(), "q", nil)
	check("plan", err)

	// A provider that echoes only a long prefix of a secret must be rejected
	// too: the guard matches every secret prefix at or above its documented
	// eight-byte fragment bound.
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodQuery {
			return dataStatus("invalid", "credential "+longSecret[:400]+" rejected"), nil
		}
		return dataOK(nil), nil
	}
	_, _, err = lease.Query(context.Background(), appplugin.DataQueryRequest{Query: "prefix"})
	check("query long prefix", err)
}

func TestDataProviderChunkBatchCapAndBudget(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly,
		appplugin.DataLimits{InteractiveBytes: 1000})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	// Two 400-byte chunks encode to more than the 1,000-byte interactive
	// budget; the host keeps one chunk, reports Limit, and stops asking.
	env.server.queryReply["chunks"] = domainplugin.DataResult{
		Kind: domainplugin.DataResultChunks,
		Chunks: &domainplugin.DataChunks{Chunks: []domainplugin.DataChunk{
			{Offset: 0, Data: make([]byte, 400)},
			{Offset: 400, Data: make([]byte, 400)},
		}},
		More: true, Class: domainplugin.DataClassRead,
	}
	env.server.queryToken["chunks"] = 7
	_, result, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "chunks"})
	if err != nil {
		t.Fatalf("chunk query: %v", err)
	}
	if !result.Limit || result.More {
		t.Fatalf("chunk budget = limit %v, more %v", result.Limit, result.More)
	}
	if len(result.Chunks.Chunks) != 1 {
		t.Fatalf("chunk budget delivered %d chunks", len(result.Chunks.Chunks))
	}
	if result.DataResultBytes() > 1000 {
		t.Fatalf("chunk budget delivered %d bytes", result.DataResultBytes())
	}

	// Two 200 KiB chunks validate as individual values but exceed the literal
	// 256 KiB aggregate batch cap, so the host rejects the envelope.
	env.server.queryReply["big"] = domainplugin.DataResult{
		Kind: domainplugin.DataResultChunks,
		Chunks: &domainplugin.DataChunks{Chunks: []domainplugin.DataChunk{
			{Offset: 0, Data: make([]byte, 200<<10)},
			{Offset: 200 << 10, Data: make([]byte, 200<<10)},
		}},
		Class: domainplugin.DataClassRead,
	}
	_, _, err = lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "big"})
	wantDataKind(t, err, appplugin.DataErrorProtocol)
}

func TestDataProviderInputExactBoundaries(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	parameters := make([]domainplugin.DataValue, domainplugin.MaxDataParameters)
	for i := range parameters {
		parameters[i] = domainplugin.DataValue{Kind: domainplugin.DataValueInt, Int: "1"}
	}
	if _, _, err := lease.Query(context.Background(), appplugin.DataQueryRequest{
		Query: "params", Parameters: parameters,
	}); err != nil {
		t.Fatalf("1024 parameters were rejected: %v", err)
	}

	// The input bound includes the encoded parameter list, which is "[]" for an
	// explicitly empty list.
	input := strings.Repeat("q", domainplugin.MaxDataInputBytes-2)
	if _, _, err := lease.Query(context.Background(), appplugin.DataQueryRequest{
		Query: input, Parameters: []domainplugin.DataValue{},
	}); err != nil {
		t.Fatalf("input at the exact limit was rejected: %v", err)
	}
	if _, _, err := lease.Query(context.Background(), appplugin.DataQueryRequest{
		Query: input + "q", Parameters: []domainplugin.DataValue{},
	}); err == nil {
		t.Fatal("input one byte above the limit was accepted")
	} else {
		wantDataKind(t, err, appplugin.DataErrorLimit)
	}
}

func TestDataProviderFinishedTokenQueryIsPooled(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	token := lease.ConnectionID()

	env.server.queryReply["done"] = domainplugin.DataResult{
		Kind: domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{
			Columns: []domainplugin.DataColumn{{Name: "v", Type: domainplugin.DataValueString}},
			Rows:    [][]domainplugin.DataValue{{{Kind: domainplugin.DataValueString, String: "x"}}},
		},
		Class: domainplugin.DataClassRead,
	}
	env.server.queryToken["done"] = 314
	_, result, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "done"})
	if err != nil || result.More {
		t.Fatalf("finished-token query = more %v, %v", result.More, err)
	}
	lease.Release()
	if env.host.DataPoolSize(env.profile.ID, domainplugin.DataModeReadOnly) != 1 {
		t.Fatal("a finished query kept the connection out of the pool")
	}
	reused := env.acquire(domainplugin.DataModeReadOnly)
	if reused.ConnectionID() != token {
		t.Fatalf("reused connection = %d, want %d", reused.ConnectionID(), token)
	}
	if env.server.count(domainpluginabi.DataProviderMethodPing) != 1 {
		t.Fatalf("reuse did not ping: %d pings",
			env.server.count(domainpluginabi.DataProviderMethodPing))
	}
	reused.Release()
}

func TestDataProviderCancelInFlightPreservesSentinel(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	env.server.queryReply["stream"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{},
		More:    true, Class: domainplugin.DataClassRead,
	}
	env.server.queryToken["stream"] = 9
	query, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "stream"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// The guest is busy, so the cancel call slot is in flight. Cancel treats it
	// as a successful cooperative cancellation instead of a protocol error.
	env.server.cancelErr = appplugin.ErrCallInFlight
	if err := query.Cancel(context.Background()); err != nil {
		t.Fatalf("cancel in flight = %v, want nil", err)
	}
	if env.server.count(domainpluginabi.DataProviderMethodCancel) != 1 {
		t.Fatal("cancel did not reach the provider")
	}
	lease.Release()
	if env.host.DataPoolSize(env.profile.ID, domainplugin.DataModeReadOnly) != 1 {
		t.Fatal("a cancelled query kept the connection out of the pool")
	}
}

func TestDataProviderCapabilityRefinementIsCapped(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	// The static declaration caps the profile at two connections. A refined
	// zero means "default", which must not report the wider default of eight.
	env.server.capabilities = &domainplugin.DataCapabilities{
		Family: domainplugin.DataFamilyTabular,
		Features: []domainplugin.DataFeature{
			domainplugin.DataFeatureTransactions,
		},
		MaxConnections: 0,
	}
	refined, err := lease.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if refined.MaxConnections != 2 || refined.EffectiveMaxConnections() != 2 {
		t.Fatalf("refined connection bound = %d (effective %d), want 2",
			refined.MaxConnections, refined.EffectiveMaxConnections())
	}

	env.server.capabilities = &domainplugin.DataCapabilities{
		Family: domainplugin.DataFamilyTabular,
		Features: []domainplugin.DataFeature{
			domainplugin.DataFeatureTransactions,
		},
		MaxConnections: 3,
	}
	_, err = lease.Capabilities(context.Background())
	wantDataKind(t, err, appplugin.DataErrorProtocol)
}

func TestDataProviderUnregisterConcurrentWithLeases(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
		env.saveProfile(dataTestDraft())
		held := env.acquire(domainplugin.DataModeReadOnly)

		start := make(chan struct{})
		var wg sync.WaitGroup
		for worker := 0; worker < 2; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for attempt := 0; attempt < 200; attempt++ {
					_ = held.Ping(context.Background())
				}
			}()
		}
		for worker := 0; worker < 2; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for attempt := 0; attempt < 25; attempt++ {
					lease, err := env.host.Acquire(context.Background(),
						env.profile.ID, domainplugin.DataModeReadOnly)
					if err != nil {
						continue
					}
					lease.Release()
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := env.host.Unregister(env.provider); err != nil {
				t.Errorf("unregister: %v", err)
			}
		}()
		close(start)
		wg.Wait()
		env.host.Close()
	}
}

func TestDataProviderUnsupportedFeatures(t *testing.T) {
	contribution := dataTestContribution()
	contribution.Capabilities.Features = []domainplugin.DataFeature{
		domainplugin.DataFeatureTesting,
	}
	server := &fakeDataServer{supports: true}
	host := appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Profiles: appplugin.NewMemoryDataProfileStore(),
		Secrets:  appplugin.NewMemoryDataProfileSecrets(),
	})
	if _, err := host.Register(dataTestFiber, appplugin.PluginRef{ID: "acme.data"},
		contribution, domainplugin.DataModeReadOnly, server); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer host.Close()

	if _, err := host.SaveProfile(context.Background(), "",
		domainplugin.DataProfileDraft{
			Label:    "P",
			Provider: domainplugin.QualifiedDataProviderID("acme.data", "warehouse"),
			Fields:   map[string]string{"endpoint": "x"},
			Secrets:  map[string]string{"password": "p"},
		}); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	profiles, err := host.ListProfiles(context.Background())
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles = %+v, %v", profiles, err)
	}
	lease, err := host.Acquire(context.Background(), profiles[0].ID,
		domainplugin.DataModeReadOnly)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Release()
	if _, err := lease.Begin(context.Background()); err == nil {
		t.Fatal("transactions were accepted without the feature")
	}
	if _, err := lease.Metadata(context.Background(), "", ""); err == nil {
		t.Fatal("metadata was accepted without the feature")
	}
	if _, err := lease.Plan(context.Background(), "q", nil); err == nil {
		t.Fatal("plan was accepted without the feature")
	}
	if _, err := lease.Command(context.Background(),
		appplugin.DataCommandRequest{Command: "c"}); err == nil {
		t.Fatal("command was accepted without the mutations feature")
	}
}

// The capability refinement error quotes guest-controlled family and feature
// names. A secret echoed there must be caught before the message is bounded,
// including when the provider returns only a long prefix of a resolved secret.
func TestDataProviderCapabilityRefinementNeverEchoesSecrets(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	longSecret := strings.Repeat("C", 600)
	draft := dataTestDraft()
	draft.Secrets["password"] = longSecret
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	fragment := longSecret[:8]
	check := func(name string, err error) {
		t.Helper()
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		var dataErr *appplugin.DataError
		if !errors.As(err, &dataErr) || dataErr.Message == "" ||
			len(dataErr.Message) > 256 {
			t.Fatalf("%s message = %q, want a bounded constant error", name, dataErr.Message)
		}
		// The refinement error must never quote the guest family or feature,
		// raw or with the quote escaping a %q format would apply.
		quoted := strconv.Quote(longSecret)
		if strings.Contains(err.Error(), fragment) ||
			strings.Contains(err.Error(), quoted[1:len(quoted)-1]) {
			t.Fatalf("%s returned a secret fragment: %v", name, err)
		}
	}

	// A full secret as the refined family.
	env.server.capabilities = &domainplugin.DataCapabilities{
		Family:         domainplugin.DataFamily(longSecret),
		Features:       []domainplugin.DataFeature{domainplugin.DataFeatureTransactions},
		MaxConnections: 1,
	}
	_, err := lease.Capabilities(context.Background())
	check("refined family", err)

	// A long prefix of the secret as the refined family. The error text is
	// longer than the 256-byte message bound, so scanning only the bounded text
	// would miss it.
	env.server.capabilities = &domainplugin.DataCapabilities{
		Family:         domainplugin.DataFamily(longSecret[:400]),
		Features:       []domainplugin.DataFeature{domainplugin.DataFeatureTransactions},
		MaxConnections: 1,
	}
	_, err = lease.Capabilities(context.Background())
	check("refined family prefix", err)

	// A full secret as an undeclared refined feature.
	env.server.capabilities = &domainplugin.DataCapabilities{
		Family:         domainplugin.DataFamilyTabular,
		Features:       []domainplugin.DataFeature{domainplugin.DataFeature(longSecret)},
		MaxConnections: 1,
	}
	_, err = lease.Capabilities(context.Background())
	check("refined feature", err)

	// A benign mismatch is still reported, but as a constant message: the guest
	// family is not repeated to the caller even when it is harmless.
	env.server.capabilities = &domainplugin.DataCapabilities{
		Family:         domainplugin.DataFamilyDocument,
		Features:       []domainplugin.DataFeature{domainplugin.DataFeatureTransactions},
		MaxConnections: 1,
	}
	_, err = lease.Capabilities(context.Background())
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) ||
		dataErr.Message != "data provider: refined capability family does not match the declared tabular family" {
		t.Fatalf("benign refinement message = %q", dataErr.Message)
	}
	if strings.Contains(err.Error(), string(domainplugin.DataFamilyDocument)) {
		t.Fatalf("the guest family reached the caller: %v", err)
	}
	if strings.Contains(env.audit.encoded(), fragment) {
		t.Fatal("the audit carried a secret fragment")
	}
}

// A %q-formatted refinement error escapes quotes, backslashes, newlines, tabs,
// and NULs, so a raw-byte secret scan misses those forms. The refinement error
// no longer quotes guest text at all: every rejection is a constant host
// message. This covers full and 400-byte prefix secrets in both the family and
// the feature position.
func TestDataProviderCapabilityRefinementRejectsEscapedSecretEchoes(t *testing.T) {
	specials := []struct {
		name  string
		value string
	}{
		{"quote", `p@ss"word-verifier`},
		{"backslash", `p@ss\word-verifier`},
		{"newline", "p@ss\nword-verifier"},
		{"tab", "p@ss\tword-verifier"},
		{"nul", "p@ss\x00word-verifier"},
	}
	for _, special := range specials {
		t.Run(special.name, func(t *testing.T) {
			// The long tail guarantees that a 400-byte prefix still contains
			// the special byte.
			secret := special.value + strings.Repeat("x", 600)
			env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
			draft := dataTestDraft()
			draft.Secrets["password"] = secret
			env.saveProfile(draft)
			lease := env.acquire(domainplugin.DataModeReadOnly)
			defer lease.Release()

			for _, prefixLen := range []int{len(secret), 400} {
				prefix := secret[:prefixLen]
				for _, target := range []string{"family", "feature"} {
					name := fmt.Sprintf("%d/%s", prefixLen, target)
					capabilities := &domainplugin.DataCapabilities{
						Family: domainplugin.DataFamilyTabular,
						Features: []domainplugin.DataFeature{
							domainplugin.DataFeatureTransactions,
						},
						MaxConnections: 1,
					}
					if target == "family" {
						capabilities.Family = domainplugin.DataFamily(prefix)
					} else {
						capabilities.Features = []domainplugin.DataFeature{
							domainplugin.DataFeature(prefix),
						}
					}
					env.server.capabilities = capabilities
					_, err := lease.Capabilities(context.Background())
					if err == nil {
						t.Fatalf("%s accepted the guest text", name)
					}
					wantDataKind(t, err, appplugin.DataErrorProtocol)
					if dataTestLeaksSecret(err.Error(), prefix) {
						t.Fatalf("%s leaked the guest text: %v", name, err)
					}
					if dataTestLeaksSecret(env.audit.encoded(), prefix) {
						t.Fatalf("%s leaked the guest text into the audit", name)
					}
				}
			}
		})
	}
}

// The provider class is guest text. It must never be persisted verbatim in the
// host audit or returned in an envelope; an invalid class is a protocol error
// and the audit records the constant host vocabulary value instead.
func TestDataProviderAuditUsesOnlyHostOperationClasses(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeFull, appplugin.DataLimits{})
	longSecret := strings.Repeat("K", 600)
	draft := dataTestDraft()
	draft.Mode = domainplugin.DataModeFull
	draft.Secrets["password"] = longSecret
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeFull)
	defer lease.Release()

	guestClass := domainplugin.DataOperationClass(longSecret)
	env.server.queryReply["leak"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{AffectedRows: 1},
		Class:   guestClass,
	}
	if _, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "leak"}); err == nil {
		t.Fatal("a query with a guest-text class was accepted")
	} else {
		wantDataKind(t, err, appplugin.DataErrorDenied)
		if strings.Contains(err.Error(), longSecret[:8]) {
			t.Fatalf("the class leaked through the error: %v", err)
		}
	}

	// The command result class travels the same path.
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodCommand {
			return dataOK(map[string]any{"result": domainplugin.DataResult{
				Kind:    domainplugin.DataResultSummary,
				Summary: &domainplugin.DataSummary{AffectedRows: 1},
				Class:   guestClass,
			}}), nil
		}
		return dataOK(nil), nil
	}
	if _, err := lease.Command(context.Background(),
		appplugin.DataCommandRequest{Command: "mutate"}); err == nil {
		t.Fatal("a command with a guest-text class was accepted")
	} else {
		wantDataKind(t, err, appplugin.DataErrorDenied)
	}

	// A fetch batch reclassifies itself, so it is checked too.
	env.server.handler = nil
	env.server.fetchRows = []domainplugin.DataResult{{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{},
		Class:   guestClass,
	}}
	env.server.queryReply["stream"] = domainplugin.DataResult{
		Kind:    domainplugin.DataResultSummary,
		Summary: &domainplugin.DataSummary{},
		More:    true,
		Class:   domainplugin.DataClassRead,
	}
	env.server.queryToken["stream"] = 31
	query, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "stream"})
	if err != nil {
		t.Fatalf("start stream: %v", err)
	}
	if _, err := query.Fetch(context.Background()); err == nil {
		t.Fatal("a fetch with a guest-text class was accepted")
	} else {
		wantDataKind(t, err, appplugin.DataErrorDenied)
	}

	encoded := env.audit.encoded()
	if strings.Contains(encoded, longSecret[:8]) {
		t.Fatalf("the audit carried the guest class: %s", encoded)
	}
	for _, event := range env.audit.snapshot() {
		if event.Class == "" {
			continue
		}
		if !domainplugin.DataOperationClass(event.Class).Valid() {
			t.Fatalf("audit class %q is outside the host vocabulary", event.Class)
		}
	}
}

// A profile test must reject a secret split across serverIdentity and
// authResult, a long prefix of a long secret, and must still display benign
// values of the same scale.
func TestDataProfileTestRejectsSplitAndPrefixSecretEchoes(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	// The reported reproduction is an 8-byte leading fragment and the remaining
	// 15 bytes in the second field.
	secret := "wqzhvdrm" + "pkceufxzjrdhvbm"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env.saveProfile(draft)

	respond := func(identity, auth string) {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method != domainpluginabi.DataProviderMethodOpen {
				return dataOK(nil), nil
			}
			return json.Marshal(map[string]any{
				"status": "ok", "serverIdentity": identity, "authResult": auth,
			})
		}
	}
	fragment := secret[:8]
	expectRejected := func(name string) {
		t.Helper()
		result, err := env.host.TestProfile(context.Background(), env.profile.ID)
		if err == nil {
			t.Fatalf("%s was accepted as %+v", name, result)
		}
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		if strings.Contains(err.Error(), fragment) {
			t.Fatalf("%s returned a secret fragment: %v", name, err)
		}
		if strings.Contains(env.audit.encoded(), fragment) {
			t.Fatalf("%s leaked into the audit", name)
		}
	}

	// The reported 8/15 split: each field looks harmless alone, but the pair
	// reconstructs one secret.
	respond(secret[:8], secret[8:])
	expectRejected("8/15 split")

	// Every split position, in both field orders.
	for cut := 1; cut < len(secret); cut++ {
		respond(secret[:cut], secret[cut:])
		expectRejected(fmt.Sprintf("split at %d", cut))
		respond(secret[cut:], secret[:cut])
		expectRejected(fmt.Sprintf("reversed split at %d", cut))
	}

	// A 400-byte prefix of a 600-byte secret is returned without the full
	// value, so a full-value-only guard would miss it.
	longSecret := strings.Repeat("Z", 600)
	update := dataTestDraft()
	update.Secrets["password"] = longSecret
	if _, err := env.host.SaveProfile(context.Background(), env.profile.ID, update); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	respond(longSecret[:400], "accepted")
	result, err := env.host.TestProfile(context.Background(), env.profile.ID)
	if err == nil {
		t.Fatalf("a 400-byte secret prefix was accepted as %+v", result)
	}
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if strings.Contains(err.Error(), longSecret[:8]) ||
		strings.Contains(env.audit.encoded(), longSecret[:8]) {
		t.Fatal("a long secret prefix leaked")
	}

	// Control: the longest conforming server name and a closed auth verdict
	// still round-trip.
	benignIdentity := strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) +
		"." + strings.Repeat("d", 63) + "." + strings.Repeat("e", 61)
	benignAuth := "rejected"
	respond(benignIdentity, benignAuth)
	result, err = env.host.TestProfile(context.Background(), env.profile.ID)
	if err != nil {
		t.Fatalf("benign profile test: %v", err)
	}
	if result.ServerIdentity != benignIdentity || result.AuthResult != benignAuth {
		t.Fatalf("benign result = %+v", result)
	}
}

// The profile-test result is guest text shown to the user. serverIdentity must
// be a conservative DNS-style name and authResult must come from the host's
// closed verdict vocabulary. A free-form or escaped value is rejected or
// collapsed to a constant, so no secret piece can ride either field out.
func TestDataProfileTestSanitizesResultFields(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	secret := `p@ss"word-verifier`
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env.saveProfile(draft)

	respond := func(identity, auth string) {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method != domainpluginabi.DataProviderMethodOpen {
				return dataOK(nil), nil
			}
			return json.Marshal(map[string]any{
				"status": "ok", "serverIdentity": identity, "authResult": auth,
			})
		}
	}

	// Free-form identity text is rejected with a constant message that does not
	// repeat the guest value.
	guestIdentity := `PostgreSQL 16.2 (x86_64) "primary"`
	respond(guestIdentity, "accepted")
	_, err := env.host.TestProfile(context.Background(), env.profile.ID)
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) ||
		dataErr.Message != "the provider returned a server identity outside the allowed shape" {
		t.Fatalf("non-conforming identity message = %q", dataErr.Message)
	}
	if strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("the guest identity reached the caller: %v", err)
	}

	// An escaped echo of the secret is also outside the identity shape and must
	// not be reflected raw or decoded.
	respond(strconv.Quote(secret), "accepted")
	_, err = env.host.TestProfile(context.Background(), env.profile.ID)
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if dataTestLeaksSecret(err.Error(), secret) ||
		dataTestLeaksSecret(env.audit.encoded(), secret) {
		t.Fatalf("escaped identity leaked the secret: %v", err)
	}

	// A conforming hostname and a closed verdict round-trip; verdict case is
	// normalized.
	respond("db-1.example.test", "ACCEPTED")
	result, err := env.host.TestProfile(context.Background(), env.profile.ID)
	if err != nil {
		t.Fatalf("benign profile test: %v", err)
	}
	if result.ServerIdentity != "db-1.example.test" || result.AuthResult != "accepted" {
		t.Fatalf("benign result = %+v", result)
	}

	// Any verdict outside the closed set becomes the constant unknown verdict.
	respond("db-1.example.test", "primary-replica")
	result, err = env.host.TestProfile(context.Background(), env.profile.ID)
	if err != nil {
		t.Fatalf("unknown verdict profile test: %v", err)
	}
	if result.AuthResult != "unknown" {
		t.Fatalf("unknown verdict result = %+v", result)
	}
}

// A provider can interleave the pieces of a secret with junk across
// serverIdentity and authResult. Each field then looks harmless and the
// contiguous field concatenation does not reconstruct the secret, but the full
// secret is still an ordered subsequence of the two fields. The test must fail
// closed rather than return either field.
func TestDataProfileTestRejectsInterleavedSecretEchoes(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	secret := "wqzhvdrmpkceufxzjrdhvbm"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env.saveProfile(draft)

	respond := func(identity, auth string) {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method != domainpluginabi.DataProviderMethodOpen {
				return dataOK(nil), nil
			}
			return json.Marshal(map[string]any{
				"status": "ok", "serverIdentity": identity, "authResult": auth,
			})
		}
	}
	check := func(name string, identity, auth string) {
		t.Helper()
		respond(identity, auth)
		result, err := env.host.TestProfile(context.Background(), env.profile.ID)
		if err == nil {
			t.Fatalf("%s was accepted as %+v", name, result)
		}
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		if dataTestLeaksSecret(err.Error(), secret) ||
			dataTestLeaksSecret(env.audit.encoded(), secret) {
			t.Fatalf("%s leaked the secret: %v", name, err)
		}
		if strings.Contains(err.Error(), secret[:6]) {
			t.Fatalf("%s returned a secret piece: %v", name, err)
		}
	}

	// Four pieces in order, each separated by junk, split across the fields.
	check("four-piece interleave",
		"a"+secret[0:6]+"b"+secret[6:12]+"c",
		"d"+secret[12:18]+"e"+secret[18:]+"f")

	// Junk between every secret character, still within the two fields.
	interleave := func(text, junk string) string {
		var builder strings.Builder
		for index := 0; index < len(text); index++ {
			if index > 0 {
				builder.WriteString(junk)
			}
			builder.WriteByte(text[index])
		}
		return builder.String()
	}
	check("character interleave",
		"a"+interleave(secret[:12], "jq")+"b",
		"c"+interleave(secret[12:], "kw")+"d")
}

// Acquire resolves the provider before the guest open and publishes the
// connection only after it returns. An Unregister that completes during that
// window must win: no connection may enter the host map and the caller gets a
// typed unavailable error instead of a lease.
func TestDataProviderAcquireLosesToConcurrentUnregister(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())

	openStarted := make(chan struct{})
	releaseOpen := make(chan struct{})
	var once sync.Once
	var closeMu sync.Mutex
	closes := 0
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		switch request.Method {
		case domainpluginabi.DataProviderMethodOpen:
			once.Do(func() { close(openStarted) })
			<-releaseOpen
			return json.Marshal(map[string]any{
				"status": "ok", "connection": uint64(4242),
			})
		case domainpluginabi.DataProviderMethodClose:
			closeMu.Lock()
			closes++
			closeMu.Unlock()
			return dataOK(nil), nil
		}
		return dataOK(nil), nil
	}

	type acquireResult struct {
		lease *appplugin.DataLease
		err   error
	}
	results := make(chan acquireResult, 1)
	go func() {
		lease, err := env.host.Acquire(context.Background(), env.profile.ID,
			domainplugin.DataModeReadOnly)
		results <- acquireResult{lease: lease, err: err}
	}()

	select {
	case <-openStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the guest open never started")
	}
	if err := env.host.Unregister(env.provider); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	close(releaseOpen)

	var got acquireResult
	select {
	case got = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not return")
	}
	if got.lease != nil || got.err == nil {
		t.Fatalf("acquire after unregister = lease %v, err %v", got.lease, got.err)
	}
	var dataErr *appplugin.DataError
	if !errors.As(got.err, &dataErr) ||
		(dataErr.Kind != appplugin.DataErrorUnavailable &&
			dataErr.Kind != appplugin.DataErrorFaulted) {
		t.Fatalf("acquire error = %v, want unavailable or faulted", got.err)
	}
	if count := env.host.DataConnectionCount(env.profile.ID); count != 0 {
		t.Fatalf("connections after unregister = %d, want 0", count)
	}
	if pool := env.host.DataPoolSize(env.profile.ID,
		domainplugin.DataModeReadOnly); pool != 0 {
		t.Fatalf("pool after unregister = %d, want 0", pool)
	}
	closeMu.Lock()
	defer closeMu.Unlock()
	if closes != 1 {
		t.Fatalf("swept connection close calls = %d, want 1", closes)
	}
}

// A pooled connection that is pinged for reuse while Unregister sweeps it used
// to be closed twice: once by the sweep and once by the failed reuse path,
// producing two guest close calls and two audit rows. The teardown claim makes
// the close idempotent.
func TestDataProviderReuseRaceClosesPooledConnectionOnce(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())
	lease := env.acquire(domainplugin.DataModeReadOnly)
	lease.Release()
	if pool := env.host.DataPoolSize(env.profile.ID,
		domainplugin.DataModeReadOnly); pool != 1 {
		t.Fatalf("pool size = %d, want 1", pool)
	}

	pingStarted := make(chan struct{})
	releasePing := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	closes := 0
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		switch request.Method {
		case domainpluginabi.DataProviderMethodPing:
			once.Do(func() { close(pingStarted) })
			<-releasePing
			return dataOK(nil), nil
		case domainpluginabi.DataProviderMethodClose:
			mu.Lock()
			closes++
			mu.Unlock()
			return dataOK(nil), nil
		}
		return dataOK(nil), nil
	}

	results := make(chan error, 1)
	go func() {
		_, err := env.host.Acquire(context.Background(), env.profile.ID,
			domainplugin.DataModeReadOnly)
		results <- err
	}()
	select {
	case <-pingStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the reuse ping never started")
	}
	if err := env.host.Unregister(env.provider); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	close(releasePing)

	select {
	case err := <-results:
		if err == nil {
			t.Fatal("acquire after the sweep returned a lease")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not return")
	}

	mu.Lock()
	got := closes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("guest close calls = %d, want 1", got)
	}
	closeRows := 0
	for _, event := range env.audit.snapshot() {
		if event.Event != domainplugin.AuditData ||
			!strings.HasPrefix(event.Reason, "data.close:") {
			continue
		}
		closeRows++
		if event.Reason != "data.close:unloaded" {
			t.Fatalf("close audit = %q, want the sweep owner's row", event.Reason)
		}
	}
	if closeRows != 1 {
		t.Fatalf("close audit rows = %d, want 1", closeRows)
	}
	if count := env.host.DataConnectionCount(env.profile.ID); count != 0 {
		t.Fatalf("connections after unregister = %d, want 0", count)
	}
}

// Concurrent reuse, release, sweep, and host shutdown must never close one
// guest token more than once.
func TestDataProviderCloseIsIdempotentUnderConcurrency(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())

	var mu sync.Mutex
	nextToken := uint64(1000)
	closed := map[uint64]int{}
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		switch request.Method {
		case domainpluginabi.DataProviderMethodOpen:
			mu.Lock()
			nextToken++
			token := nextToken
			mu.Unlock()
			return dataOK(map[string]any{"connection": token}), nil
		case domainpluginabi.DataProviderMethodClose:
			var payload struct {
				Connection uint64 `json:"connection"`
			}
			_ = json.Unmarshal(request.Payload, &payload)
			mu.Lock()
			closed[payload.Connection]++
			mu.Unlock()
			return dataOK(nil), nil
		}
		return dataOK(nil), nil
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for attempt := 0; attempt < 100; attempt++ {
				lease, err := env.host.Acquire(context.Background(), env.profile.ID,
					domainplugin.DataModeReadOnly)
				if err != nil {
					continue
				}
				if attempt%3 == 0 {
					lease.Close(context.Background())
					continue
				}
				lease.Release()
			}
		}(worker)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		time.Sleep(time.Millisecond)
		_ = env.host.Unregister(env.provider)
	}()
	close(start)
	wg.Wait()
	env.host.Close()

	mu.Lock()
	defer mu.Unlock()
	for token, count := range closed {
		if count > 1 {
			t.Fatalf("connection %d was closed %d times", token, count)
		}
	}
}

// Host Close can win the race against a guest open that is still in flight.
// The open owner must force-close the token through the swept path; the
// ordinary close path refuses once the instance is inactive and leaks it.
func TestDataProviderHostCloseDuringOpenClosesGuestToken(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(dataTestDraft())

	openStarted := make(chan struct{})
	releaseOpen := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	closes := 0
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		switch request.Method {
		case domainpluginabi.DataProviderMethodOpen:
			once.Do(func() { close(openStarted) })
			<-releaseOpen
			return dataOK(map[string]any{"connection": uint64(4242)}), nil
		case domainpluginabi.DataProviderMethodClose:
			mu.Lock()
			closes++
			mu.Unlock()
			return dataOK(nil), nil
		}
		return dataOK(nil), nil
	}

	results := make(chan error, 1)
	go func() {
		_, err := env.host.Acquire(context.Background(), env.profile.ID,
			domainplugin.DataModeReadOnly)
		results <- err
	}()
	select {
	case <-openStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the open never started")
	}
	env.host.Close()
	close(releaseOpen)

	select {
	case err := <-results:
		if !errors.Is(err, appplugin.ErrClosed) {
			t.Fatalf("acquire after host Close = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not return")
	}
	mu.Lock()
	got := closes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("guest close calls = %d, want 1", got)
	}
	if count := env.host.DataConnectionCount(env.profile.ID); count != 0 {
		t.Fatalf("connections after host Close = %d, want 0", count)
	}
}

// TestSaveProfilePreservesPluginData pins the ordinary DataProfile edit path:
// a draft never carries provider-defined structural data, so an update must
// copy the stored map forward instead of replacing it with nil.
func TestSaveProfilePreservesPluginData(t *testing.T) {
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	profile := env.saveProfile(dataTestDraft())
	providerData := plugindata.Map{
		"acme.data": {
			SchemaVersion: 1,
			Fields: map[string]json.RawMessage{
				"name": json.RawMessage(`"ok"`),
			},
		},
	}
	profile.PluginData = providerData
	if err := env.profiles.Save(context.Background(), profile); err != nil {
		t.Fatalf("seed provider data: %v", err)
	}
	draft := dataTestDraft()
	draft.Label = "Reporting v2"
	updated, err := env.host.SaveProfile(context.Background(), profile.ID, draft)
	if err != nil {
		t.Fatalf("SaveProfile update: %v", err)
	}
	if updated.Label != "Reporting v2" {
		t.Fatalf("profile label = %q", updated.Label)
	}
	if len(updated.PluginData) != 1 ||
		string(updated.PluginData["acme.data"].Fields["name"]) != `"ok"` {
		t.Fatalf("SaveProfile dropped provider data: %+v", updated.PluginData)
	}
	stored, err := env.profiles.Get(context.Background(), profile.ID)
	if err != nil {
		t.Fatalf("Get stored profile: %v", err)
	}
	if len(stored.PluginData) != 1 ||
		string(stored.PluginData["acme.data"].Fields["name"]) != `"ok"` {
		t.Fatalf("stored profile lost provider data: %+v", stored.PluginData)
	}
}
