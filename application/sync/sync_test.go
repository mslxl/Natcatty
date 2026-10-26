package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeWaiter struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (w *fakeWaiter) Wait(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.durations = append(w.durations, d)
	w.mu.Unlock()
	return ctx.Err()
}

type memoryConfigs struct {
	mu      sync.Mutex
	config  Config
	saved   int
	saveErr error
}

func (m *memoryConfigs) Load(context.Context) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config, nil
}

func (m *memoryConfigs) Save(_ context.Context, config Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.config = config
	m.saved++
	return nil
}

type memoryTokens struct {
	mu    sync.Mutex
	token domainsync.Token
	set   bool
}

func (m *memoryTokens) Load(context.Context) (domainsync.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set {
		return domainsync.Token{}, ErrNoToken
	}
	return m.token, nil
}

func (m *memoryTokens) Save(_ context.Context, token domainsync.Token) error {
	m.mu.Lock()
	m.token = token
	m.set = true
	m.mu.Unlock()
	return nil
}

func (m *memoryTokens) Clear(context.Context) error {
	m.mu.Lock()
	m.token = domainsync.Token{}
	m.set = false
	m.mu.Unlock()
	return nil
}

type fakeVault struct {
	mu      sync.Mutex
	payload Payload
	applied []Payload
}

func (v *fakeVault) Snapshot(context.Context) (Payload, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return clonePayload(v.payload), nil
}

func (v *fakeVault) Apply(_ context.Context, payload Payload) error {
	v.mu.Lock()
	v.payload = clonePayload(payload)
	v.applied = append(v.applied, clonePayload(payload))
	v.mu.Unlock()
	return nil
}

func (v *fakeVault) set(payload Payload) {
	v.mu.Lock()
	v.payload = clonePayload(payload)
	v.mu.Unlock()
}

func clonePayload(p Payload) Payload {
	return Payload{
		Hosts:               append([]domainvault.Host(nil), p.Hosts...),
		SyncedAt:            p.SyncedAt,
		Secrets:             append([]SecretValue(nil), p.Secrets...),
		Groups:              append([]domainvault.Group(nil), p.Groups...),
		PortForwardingRules: p.PortForwardingRules,
	}
}

type fakeRemote struct {
	mu sync.Mutex

	code       DeviceCode
	pollErrors []error
	polls      int
	requestErr error
	userErr    error
	login      string
	readErr    error
	writeErr   error

	file         RemoteFile
	exists       bool
	etagSeq      int
	writeLog     [][]byte
	conflictNext bool
	conflictFile RemoteFile
	writes       chan struct{}
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{
		code:   DeviceCode{DeviceCode: "device", UserCode: "ABCD-1234", VerificationURI: "https://github.test/device", Interval: 1},
		login:  "octocat",
		writes: make(chan struct{}, 16),
	}
}

func (r *fakeRemote) RequestDeviceCode(context.Context) (DeviceCode, error) {
	if r.requestErr != nil {
		return DeviceCode{}, r.requestErr
	}
	return r.code, nil
}

func (r *fakeRemote) PollDeviceToken(context.Context, DeviceCode) (domainsync.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.polls++
	if len(r.pollErrors) == 0 {
		return domainsync.Token{AccessToken: "token", TokenType: "bearer"}, nil
	}
	err := r.pollErrors[0]
	r.pollErrors = r.pollErrors[1:]
	return domainsync.Token{}, err
}

func (r *fakeRemote) User(context.Context, domainsync.Token) (Grant, error) {
	if r.userErr != nil {
		return Grant{}, r.userErr
	}
	return Grant{Login: r.login}, nil
}

func (r *fakeRemote) Read(context.Context, domainsync.Token) (RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readErr != nil {
		return RemoteFile{}, false, r.readErr
	}
	if !r.exists {
		return RemoteFile{}, false, nil
	}
	file := r.file
	file.Content = append([]byte(nil), r.file.Content...)
	return file, true, nil
}

func (r *fakeRemote) Write(_ context.Context, _ domainsync.Token, current RemoteFile, content []byte) (RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return RemoteFile{}, r.writeErr
	}
	if r.conflictNext {
		r.conflictNext = false
		r.file = r.conflictFile
		r.exists = true
		r.etagSeq++
		r.file.ETag = "etag-" + string(rune('0'+r.etagSeq))
		return RemoteFile{}, ErrConflict
	}
	if current.ID == "" && r.exists {
		return RemoteFile{}, ErrConflict
	}
	if current.ID != "" && (!r.exists || current.ETag != r.file.ETag) {
		return RemoteFile{}, ErrConflict
	}
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return RemoteFile{}, err
	}
	r.etagSeq++
	r.file = RemoteFile{
		ID:        "gist-1",
		ETag:      "etag-" + string(rune('0'+r.etagSeq)),
		Content:   append([]byte(nil), content...),
		UpdatedAt: envelope.Meta.UpdatedAt,
	}
	r.exists = true
	r.writeLog = append(r.writeLog, append([]byte(nil), content...))
	select {
	case r.writes <- struct{}{}:
	default:
	}
	return r.file, nil
}

func (r *fakeRemote) payload(t *testing.T, password string) Payload {
	t.Helper()
	r.mu.Lock()
	file := r.file
	r.mu.Unlock()
	if file.Content == nil {
		t.Fatal("no remote content")
	}
	envelope, err := domainsync.UnmarshalFile(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := domainsync.DecryptPayload(envelope, password)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func (r *fakeRemote) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.writeLog)
}

func payloadWithHost(label string, syncedAt int64) Payload {
	return Payload{
		Hosts:    []domainvault.Host{{ID: "host-1", Label: label, Hostname: "example.com", Port: 22, Protocol: domainvault.ProtocolSSH}},
		SyncedAt: syncedAt,
	}
}

func testService(t *testing.T) (*Service, *fakeRemote, *fakeVault, *memoryConfigs, *memoryTokens, *fakeWaiter) {
	t.Helper()
	return testServiceWith(t)
}

func testServiceWith(t *testing.T, opts ...Option) (*Service, *fakeRemote, *fakeVault, *memoryConfigs, *memoryTokens, *fakeWaiter) {
	t.Helper()
	remote := newFakeRemote()
	vault := &fakeVault{payload: payloadWithHost("local", 1000)}
	configs := &memoryConfigs{config: Config{Connected: true, Login: "octocat", Strategy: domainsync.StrategyNewest}}
	tokens := &memoryTokens{token: domainsync.Token{AccessToken: "token"}, set: true}
	waiter := &fakeWaiter{}
	options := []Option{
		WithWaiter(waiter),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithDevice("device-1", "Test Device", "1.0"),
		WithDebounce(10 * time.Millisecond),
	}
	options = append(options, opts...)
	service := NewService(remote, vault, configs, tokens, options...)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword("secret"); err != nil {
		t.Fatal(err)
	}
	return service, remote, vault, configs, tokens, waiter
}

func TestConnectRunsTheDeviceFlow(t *testing.T) {
	remote := newFakeRemote()
	remote.pollErrors = []error{ErrAuthorizationPending, ErrSlowDown}
	vault := &fakeVault{}
	service := NewService(remote, vault, &memoryConfigs{}, &memoryTokens{},
		WithWaiter(&fakeWaiter{}),
		WithDevice("d", "n", "v"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	var shown DeviceCode
	if err := service.Connect(context.Background(), "secret", func(code DeviceCode) { shown = code }); err != nil {
		t.Fatal(err)
	}
	if shown.UserCode != "ABCD-1234" {
		t.Fatalf("device code shown = %+v", shown)
	}
	if remote.polls != 3 {
		t.Fatalf("polls = %d, want pending, slow_down, success", remote.polls)
	}
	snapshot := service.Snapshot()
	if !snapshot.Connected || snapshot.Login != "octocat" || snapshot.Status != StatusIdle {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestConnectReportsDeniedAndExpired(t *testing.T) {
	for name, pollErr := range map[string]error{
		"denied":  ErrAuthorizationDenied,
		"expired": ErrDeviceCodeExpired,
	} {
		t.Run(name, func(t *testing.T) {
			remote := newFakeRemote()
			remote.pollErrors = []error{pollErr}
			service := NewService(remote, &fakeVault{}, &memoryConfigs{}, &memoryTokens{}, WithWaiter(&fakeWaiter{}))
			if err := service.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			err := service.Connect(context.Background(), "secret", nil)
			if !errors.Is(err, pollErr) {
				t.Fatalf("error = %v, want %v", err, pollErr)
			}
			if snapshot := service.Snapshot(); snapshot.Problem != ProblemAuthorization {
				t.Fatalf("problem = %q", snapshot.Problem)
			}
		})
	}
}

func TestManualSyncUploadsTheFirstCopy(t *testing.T) {
	service, remote, _, configs, _, _ := testService(t)
	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Uploaded || result.Action != domainsync.ActionUpload {
		t.Fatalf("result = %+v", result)
	}
	payload := remote.payload(t, "secret")
	if len(payload.Hosts) != 1 || payload.Hosts[0].Label != "local" {
		t.Fatalf("remote payload = %+v", payload)
	}
	if configs.config.LocalSignature == "" || configs.config.RemoteID != "gist-1" {
		t.Fatalf("config = %+v", configs.config)
	}
	if snapshot := service.Snapshot(); snapshot.Status != StatusSynced || snapshot.LastAction != domainsync.ActionUpload {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestManualSyncDownloadsTheNewerRemoteCopy(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	remote.exists = true
	remote.file = RemoteFile{ID: "gist-1", ETag: "etag-other", UpdatedAt: 9000, Content: encryptForTest(t, payloadWithHost("cloud", 9000), "secret", 9000)}

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Action != domainsync.ActionDownload {
		t.Fatalf("result = %+v", result)
	}
	vault.mu.Lock()
	label := vault.payload.Hosts[0].Label
	vault.mu.Unlock()
	if label != "cloud" {
		t.Fatalf("local label = %q", label)
	}
	if remote.writeCount() != 0 {
		t.Fatal("download must not write")
	}
}

func TestAppliedSnapshotNotifiesOnlyWhenADownloadIsApplied(t *testing.T) {
	applied := 0
	service, remote, vault, _, _, _ := testServiceWith(t, WithAppliedSnapshot(func() { applied++ }))

	// A first upload changes only the cloud copy.
	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Uploaded || applied != 0 {
		t.Fatalf("upload = %+v, notifications = %d, want no applied notification", result, applied)
	}

	// A newer cloud copy is applied to the local Vault.
	remote.exists = true
	remote.file = RemoteFile{ID: "gist-1", ETag: "etag-other", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHost("cloud", 9000), "secret", 9000)}
	result, err = service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || applied != 1 {
		t.Fatalf("download = %+v, notifications = %d, want exactly one", result, applied)
	}

	// A converged no-op applies nothing.
	result, err = service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionNone || applied != 1 {
		t.Fatalf("no-op = %+v, notifications = %d, want still one", result, applied)
	}

	// A lost conditional write that newest-wins resolves to a download uses
	// the same apply path and must notify too.
	vault.set(payloadWithHost("edited", 6000))
	remote.mu.Lock()
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{ID: "gist-1", ETag: "etag-cloud", UpdatedAt: 12000,
		Content: encryptForTest(t, payloadWithHost("other-device", 12000), "secret", 12000)}
	remote.mu.Unlock()
	result, err = service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || applied != 2 {
		t.Fatalf("conflict download = %+v, notifications = %d, want two", result, applied)
	}
}

func TestAfterDownloadTheNextSyncIsANoOp(t *testing.T) {
	service, remote, _, configs, _, _ := testService(t)
	remote.exists = true
	remote.file = RemoteFile{ID: "gist-1", ETag: "etag-other", UpdatedAt: 9000, Content: encryptForTest(t, payloadWithHost("cloud", 9000), "secret", 9000)}

	if result, err := service.Sync(context.Background(), TriggerManual); err != nil || !result.Downloaded {
		t.Fatalf("download = %+v, %v", result, err)
	}
	// The baseline must describe the applied copy, not the replaced one.
	if got := configs.config.LocalSignature; got == "" {
		t.Fatal("download recorded no baseline signature")
	}
	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionNone || result.Uploaded || result.Downloaded {
		t.Fatalf("second sync = %+v, want no-op", result)
	}
	if remote.writeCount() != 0 {
		t.Fatal("the downloaded copy was uploaded back")
	}
}

func TestAfterConflictDownloadTheNextSyncIsANoOp(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	vault.set(payloadWithHost("edited", 6000))
	// The local side changed and the cloud side changed under a lost
	// conditional write; newest-wins applies the cloud copy.
	remote.mu.Lock()
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{
		ID: "gist-1", ETag: "etag-cloud", UpdatedAt: 9000,
		Content: encryptForTest(t, payloadWithHost("other-device", 9000), "secret", 9000),
	}
	remote.mu.Unlock()
	if err := service.SetStrategy(context.Background(), domainsync.StrategyNewest); err != nil {
		t.Fatal(err)
	}
	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded {
		t.Fatalf("result = %+v, want download", result)
	}
	result, err = service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != domainsync.ActionNone || result.Uploaded || result.Downloaded {
		t.Fatalf("second sync = %+v, want no-op", result)
	}
}

func TestEmptyVaultNeverOverwritesTheCloudCopy(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	// Establish a populated cloud copy and a baseline first.
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	before := remote.writeCount()

	// The user deletes everything locally. Both triggers must refuse.
	vault.set(Payload{})
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrEmptyVault) {
		t.Fatalf("manual error = %v, want ErrEmptyVault", err)
	}
	if snapshot := service.Snapshot(); snapshot.Problem != ProblemEmptyVault {
		t.Fatalf("problem = %q", snapshot.Problem)
	}
	if _, err := service.Sync(context.Background(), TriggerAuto); !errors.Is(err, ErrEmptyVault) {
		t.Fatalf("auto error = %v, want ErrEmptyVault", err)
	}
	if remote.writeCount() != before {
		t.Fatal("the empty Vault overwrote the cloud copy")
	}
	if payload := remote.payload(t, "secret"); len(payload.Hosts) != 1 {
		t.Fatalf("cloud payload was lost: %+v", payload)
	}
}

func TestAutomaticSyncIsDebounced(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	if err := service.SetAutoSync(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// Drain the initial automatic sync triggered by enabling auto sync.
	waitForWrite(t, remote)
	vault.set(payloadWithHost("edited", 2000))
	service.NotifyLocalChange()
	waitForWrite(t, remote)
	if payload := remote.payload(t, "secret"); payload.Hosts[0].Label != "edited" {
		t.Fatalf("remote label = %q", payload.Hosts[0].Label)
	}
}

func TestAutoSyncDoesNothingWhenDisabled(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	vault.set(payloadWithHost("edited", 2000))
	service.NotifyLocalChange()
	// The condition is observable: a write would arrive on the channel. The
	// timeout only proves the absence.
	select {
	case <-remote.writes:
		t.Fatal("auto sync wrote while disabled")
	case <-time.After(150 * time.Millisecond):
	}
	if remote.writeCount() != 0 {
		t.Fatal("auto sync wrote while disabled")
	}
}

func TestConditionalWriteConflictRedecidesWithNewestWins(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	vault.set(payloadWithHost("local-new", 3000))
	// Another device writes a newer copy between our read and our write.
	newer := payloadWithHost("other-device", 9000)
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{
		ID: "gist-1", ETag: "etag-new", UpdatedAt: 9000,
		Content: encryptForTest(t, newer, "secret", 9000),
	}
	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded {
		t.Fatalf("result = %+v, want the newer remote copy", result)
	}
	vault.mu.Lock()
	label := vault.payload.Hosts[0].Label
	vault.mu.Unlock()
	if label != "other-device" {
		t.Fatalf("local label = %q", label)
	}
}

func TestWrongRemotePasswordFailsWithoutWriting(t *testing.T) {
	service, remote, _, _, _, _ := testService(t)
	remote.exists = true
	remote.file = RemoteFile{ID: "gist-1", ETag: "etag-x", UpdatedAt: 9000, Content: encryptForTest(t, payloadWithHost("cloud", 9000), "different", 9000)}
	_, err := service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("error = %v, want ErrWrongPassword", err)
	}
	if remote.writeCount() != 0 {
		t.Fatal("wrong password wrote to the cloud")
	}
	if snapshot := service.Snapshot(); snapshot.Problem != ProblemWrongPassword {
		t.Fatalf("problem = %q", snapshot.Problem)
	}
}

func TestSyncNeedsAConfiguredPassword(t *testing.T) {
	service, _, _, _, _, _ := testService(t)
	service.ClearPassword()
	_, err := service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("error = %v", err)
	}
	if snapshot := service.Snapshot(); snapshot.Status != StatusNeedsPassword {
		t.Fatalf("status = %q", snapshot.Status)
	}
}

func TestNewerSyncSchemaFailsClosed(t *testing.T) {
	service, remote, _, _, _, _ := testService(t)
	envelope, err := domainsync.EncryptPayload(Payload{}, "secret", domainsync.EncryptOptions{
		Version: 1, UpdatedAt: 9000, DeviceID: "future", DeviceName: "Future", AppVersion: "2",
		SyncSchemaVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatal(err)
	}
	remote.exists = true
	remote.file = RemoteFile{ID: "gist-1", ETag: "etag-future", UpdatedAt: 9000, Content: content}

	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
	}
	if remote.writeCount() != 0 {
		t.Fatal("a newer-schema cloud copy was overwritten")
	}
	if snapshot := service.Snapshot(); snapshot.Problem != ProblemUnsupported {
		t.Fatalf("problem = %q", snapshot.Problem)
	}
}

func TestConflictWithAnEmptyCloudCopyForcesUpload(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	vault.set(payloadWithHost("local-edit", 6000))
	empty, err := domainsync.EncryptPayload(Payload{}, "secret", domainsync.EncryptOptions{
		Version: 2, UpdatedAt: 9000, DeviceID: "peer", DeviceName: "Peer", AppVersion: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := domainsync.MarshalFile(empty)
	if err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{ID: "gist-1", UpdatedAt: 9000, Content: content}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Uploaded || result.Downloaded {
		t.Fatalf("result = %+v, want upload", result)
	}
	vault.mu.Lock()
	label := vault.payload.Hosts[0].Label
	vault.mu.Unlock()
	if label != "local-edit" {
		t.Fatalf("the empty cloud copy replaced the Vault: %q", label)
	}
	if payload := remote.payload(t, "secret"); firstHostLabel(payload) != "local-edit" {
		t.Fatalf("cloud label = %q", firstHostLabel(payload))
	}
}

func TestAutomaticSyncCoalescesEditsIntoOneTimer(t *testing.T) {
	service, _, _, _, _, _ := testService(t)
	if err := service.SetAutoSync(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	service.CancelDebounce()
	service.NotifyLocalChange()
	service.NotifyLocalChange()
	service.NotifyLocalChange()

	service.mu.Lock()
	scheduled, timer := service.scheduled, service.timer
	service.mu.Unlock()
	if !scheduled || timer == nil {
		t.Fatal("three rapid edits did not share one debounce timer")
	}
	service.Stop()
}

func TestAutomaticSyncRunsAPendingPassAfterAnInFlightSync(t *testing.T) {
	service, _, _, _, _, _ := testService(t)
	if err := service.SetAutoSync(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	service.CancelDebounce()

	service.mu.Lock()
	service.syncing = true
	service.mu.Unlock()
	service.NotifyLocalChange()
	service.mu.Lock()
	pending := service.pending
	service.syncing = false
	service.mu.Unlock()
	if !pending {
		t.Fatal("an edit during a sync was not queued")
	}
	service.completeSync(domainsync.ActionNone)
	service.mu.Lock()
	scheduled := service.scheduled
	service.mu.Unlock()
	if !scheduled {
		t.Fatal("the queued edit was not scheduled after the in-flight sync")
	}
	service.Stop()
}

func TestBaselineSaveFailureIsNotReportedAsSuccess(t *testing.T) {
	service, remote, _, configs, _, _ := testService(t)
	configs.mu.Lock()
	configs.saveErr = errors.New("disk full")
	configs.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err == nil || result.Action != "" {
		t.Fatalf("Sync = %+v, %v; want a save error", result, err)
	}
	if remote.writeCount() != 1 {
		t.Fatalf("writes = %d, want the upload then a failed baseline", remote.writeCount())
	}
	if snapshot := service.Snapshot(); snapshot.Problem != ProblemLocal {
		t.Fatalf("problem = %q", snapshot.Problem)
	}
}

func firstHostLabel(payload Payload) string {
	if len(payload.Hosts) == 0 {
		return ""
	}
	return payload.Hosts[0].Label
}

func encryptForTest(t *testing.T, payload Payload, password string, updatedAt int64) []byte {
	t.Helper()
	envelope, err := domainsync.EncryptPayload(payload, password, domainsync.EncryptOptions{
		Version: 1, UpdatedAt: updatedAt, DeviceID: "other", DeviceName: "Other", AppVersion: "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitForWrite(t *testing.T, remote *fakeRemote) {
	t.Helper()
	select {
	case <-remote.writes:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the automatic sync write")
	}
}
