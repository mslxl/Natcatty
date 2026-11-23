package phoneshell

import (
	"context"
	"strings"
	"testing"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// fakeVaultSync is the VaultSyncService the page tests drive. The real engine
// and the portable-settings delivery are covered by
// TestPhoneVaultSyncCarriesPortableSettingsToADesktop in package app.
type fakeVaultSync struct {
	snapshot appsync.Snapshot

	password string

	connectWebDAV appsync.WebDAVSettings
	connectS3     appsync.S3Settings
	confirm       bool
	connectCalls  int
	connectErr    error
	// insecureFirst fails the first WebDAV connect the way the service's
	// plain-HTTP gate does, so the page's confirmation path can be driven.
	insecureFirst bool

	syncNow         int
	autoSync        bool
	disconnectCalls int
	disconnectErr   error

	// GitHub device-flow state. accessToken is a synthetic credential the page
	// must never render; it stands in for the token the real engine keeps in
	// the system credential store.
	githubStarts   int
	githubPassword string
	githubCancels  int
	accessToken    string
}

func (f *fakeVaultSync) Snapshot() appsync.Snapshot { return f.snapshot }

func (f *fakeVaultSync) SetPassword(password string) error {
	f.password = password
	return nil
}

func (f *fakeVaultSync) SetAutoSync(_ context.Context, enabled bool) error {
	f.autoSync = enabled
	f.snapshot.AutoSync = enabled
	return nil
}

func (f *fakeVaultSync) ConnectWebDAV(_ context.Context, password string,
	settings appsync.WebDAVSettings, confirmInsecure bool) error {
	f.connectCalls++
	f.password = password
	f.connectWebDAV = settings
	f.confirm = confirmInsecure
	if f.insecureFirst && !confirmInsecure {
		return appsync.ErrInsecureEndpoint
	}
	if f.connectErr != nil {
		return f.connectErr
	}
	f.snapshot.Connected = true
	f.snapshot.Provider = appsync.ProviderWebDAV
	f.snapshot.HasPassword = true
	return nil
}

func (f *fakeVaultSync) ConnectS3(_ context.Context, password string,
	settings appsync.S3Settings, confirmInsecure bool) error {
	f.connectCalls++
	f.password = password
	f.connectS3 = settings
	f.confirm = confirmInsecure
	if f.connectErr != nil {
		return f.connectErr
	}
	f.snapshot.Connected = true
	f.snapshot.Provider = appsync.ProviderS3
	f.snapshot.HasPassword = true
	return nil
}

func (f *fakeVaultSync) SyncNow() { f.syncNow++ }

// StartGitHubConnect models the shared engine: it moves to the authorizing
// state and publishes the public user code and verification address. The test
// advances to the outcome by editing snapshot, as the real engine does when
// its poll succeeds.
func (f *fakeVaultSync) StartGitHubConnect(password string) error {
	f.githubStarts++
	f.githubPassword = password
	f.snapshot.Provider = appsync.ProviderGitHub
	f.snapshot.Status = appsync.StatusAuthorizing
	f.snapshot.DeviceCode = appsync.DeviceCode{
		DeviceCode:      "device-code-that-must-not-render",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://github.com/login/device",
	}
	return nil
}

func (f *fakeVaultSync) CancelGitHubConnect() {
	f.githubCancels++
	f.snapshot.DeviceCode = appsync.DeviceCode{}
	f.snapshot.Status = appsync.StatusFailed
	f.snapshot.Problem = appsync.ProblemAuthorization
}

func (f *fakeVaultSync) Disconnect(context.Context) error {
	f.disconnectCalls++
	if f.disconnectErr != nil {
		return f.disconnectErr
	}
	f.snapshot.Connected = false
	f.snapshot.HasPassword = false
	return nil
}

// TestVaultSyncPageConnectsWebDAV drives the first-run form: the provider
// defaults to WebDAV, each field is written where the user typed it, and the
// page reports the connected state the engine now holds. It also checks that
// the typed credentials are not echoed back into the frame.
func TestVaultSyncPageConnectsWebDAV(t *testing.T) {
	service := &fakeVaultSync{}
	shell := New(WithSettings(newFakeSettings()), WithVaultSync(service))
	tt := openSectionPage(t, shell, Settings, "Vault sync")

	if !tt.HasText("Vault sync is not configured.") {
		t.Fatalf("page does not show the unconfigured state; texts: %v", tt.Texts())
	}
	if err := tt.Click("Endpoint"); err != nil {
		t.Fatalf("focus Endpoint: %v", err)
	}
	tt.Type("https://dav.example/vault/")
	if err := tt.Click("Username"); err != nil {
		t.Fatalf("focus Username: %v", err)
	}
	tt.Type("alice")
	if err := tt.Click("Password"); err != nil {
		t.Fatalf("focus Password: %v", err)
	}
	tt.Type("hunter2-secret")
	if err := tt.Click("Master password"); err != nil {
		t.Fatalf("focus Master password: %v", err)
	}
	tt.Type("correct horse battery staple")
	tt.Frame()
	if err := tt.Click("Connect"); err != nil {
		t.Fatalf("click Connect: %v", err)
	}

	if service.connectWebDAV.Endpoint != "https://dav.example/vault/" {
		t.Fatalf("endpoint = %q, want the typed endpoint", service.connectWebDAV.Endpoint)
	}
	if service.connectWebDAV.Username != "alice" {
		t.Fatalf("username = %q, want alice", service.connectWebDAV.Username)
	}
	if service.connectWebDAV.Password != "hunter2-secret" {
		t.Fatal("the typed WebDAV password did not reach the service")
	}
	if service.connectWebDAV.AuthType != domainsync.WebDAVAuthBasic {
		t.Fatalf("auth type = %q, want basic", service.connectWebDAV.AuthType)
	}
	if service.password != "correct horse battery staple" {
		t.Fatal("the typed master password did not reach the service")
	}
	if !service.snapshot.Connected || !tt.HasText("Sync now") {
		t.Fatalf("page does not show the connected state; texts: %v", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if text == "hunter2-secret" || text == "correct horse battery staple" {
			t.Fatalf("the page echoed a secret: %q", text)
		}
	}
}

// TestVaultSyncPageConnectsS3 checks the second provider's form reaches the
// right service method with every field.
func TestVaultSyncPageConnectsS3(t *testing.T) {
	service := &fakeVaultSync{}
	shell := New(WithSettings(newFakeSettings()), WithVaultSync(service))
	tt := openSectionPage(t, shell, Settings, "Vault sync")

	if err := tt.Click("Sync provider: S3"); err != nil {
		t.Fatalf("choose S3: %v", err)
	}
	fields := []struct{ label, value string }{
		{"Endpoint", "https://s3.example.com"},
		{"Region", "us-east-1"},
		{"Bucket", "natcatty"},
		{"Prefix", "phone/"},
		{"Access key", "AKIAEXAMPLE"},
		{"Secret key", "secret-access-key"},
		{"Master password", "master-pass"},
	}
	for _, field := range fields {
		if err := tt.Click(field.label); err != nil {
			t.Fatalf("focus %s: %v", field.label, err)
		}
		tt.Type(field.value)
		tt.Frame()
	}
	if err := tt.Click("Connect"); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	got := service.connectS3
	if got.Endpoint != "https://s3.example.com" || got.Region != "us-east-1" ||
		got.Bucket != "natcatty" || got.Prefix != "phone/" ||
		got.AccessKeyID != "AKIAEXAMPLE" || got.SecretAccessKey != "secret-access-key" {
		t.Fatalf("S3 settings = %+v, want the typed form", got)
	}
	if service.snapshot.Provider != appsync.ProviderS3 {
		t.Fatalf("provider = %q, want s3", service.snapshot.Provider)
	}
}

// TestVaultSyncPageShowsActionableProblems maps sentinel errors to localized
// sentences and never leaks the adapter's text.
func TestVaultSyncPageShowsActionableProblems(t *testing.T) {
	service := &fakeVaultSync{connectErr: appsync.ErrCredentialsRejected}
	shell := New(WithSettings(newFakeSettings()), WithVaultSync(service))
	tt := openSectionPage(t, shell, Settings, "Vault sync")
	if err := tt.Click("Endpoint"); err != nil {
		t.Fatalf("focus Endpoint: %v", err)
	}
	tt.Type("https://dav.example/vault/")
	if err := tt.Click("Username"); err != nil {
		t.Fatalf("focus Username: %v", err)
	}
	tt.Type("alice")
	if err := tt.Click("Password"); err != nil {
		t.Fatalf("focus Password: %v", err)
	}
	tt.Type("wrong")
	if err := tt.Click("Master password"); err != nil {
		t.Fatalf("focus Master password: %v", err)
	}
	tt.Type("master")
	tt.Frame()
	if err := tt.Click("Connect"); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	if !tt.HasText("Enter a valid endpoint and credentials.") {
		t.Fatalf("page does not show the credentials problem; texts: %v", tt.Texts())
	}
}

// TestVaultSyncPageConfirmsPlainHTTP checks the explicit gate: the first
// attempt stops at the warning, and only "Connect anyway" retries the exact
// captured settings with confirmInsecure set.
func TestVaultSyncPageConfirmsPlainHTTP(t *testing.T) {
	service := &fakeVaultSync{insecureFirst: true}
	shell := New(WithSettings(newFakeSettings()), WithVaultSync(service))
	tt := openSectionPage(t, shell, Settings, "Vault sync")
	if err := tt.Click("Endpoint"); err != nil {
		t.Fatalf("focus Endpoint: %v", err)
	}
	tt.Type("http://dav.example/vault/")
	if err := tt.Click("Username"); err != nil {
		t.Fatalf("focus Username: %v", err)
	}
	tt.Type("alice")
	if err := tt.Click("Password"); err != nil {
		t.Fatalf("focus Password: %v", err)
	}
	tt.Type("plain")
	if err := tt.Click("Master password"); err != nil {
		t.Fatalf("focus Master password: %v", err)
	}
	tt.Type("master")
	tt.Frame()
	if err := tt.Click("Connect"); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	if service.connectCalls != 1 || service.confirm {
		t.Fatalf("first attempt calls=%d confirm=%v, want 1 false", service.connectCalls, service.confirm)
	}
	if !tt.HasText("This endpoint is not encrypted.") {
		t.Fatalf("page does not show the plain-HTTP warning; texts: %v", tt.Texts())
	}
	if err := tt.Click("Connect anyway"); err != nil {
		t.Fatalf("click Connect anyway: %v", err)
	}
	if service.connectCalls != 2 || !service.confirm {
		t.Fatalf("confirmed attempt calls=%d confirm=%v, want 2 true", service.connectCalls, service.confirm)
	}
	if service.connectWebDAV.Endpoint != "http://dav.example/vault/" {
		t.Fatalf("confirmed attempt used %q, want the captured endpoint", service.connectWebDAV.Endpoint)
	}
	if service.password != "master" {
		t.Fatal("confirmed attempt did not reuse the captured master password")
	}
	if !service.snapshot.Connected {
		t.Fatal("the confirmed attempt did not connect")
	}
}

// TestVaultSyncPageRunsAndDisconnects covers the configured surface: the
// automatic-sync switch, a manual run, and a deletion that requires
// confirmation.
func TestVaultSyncPageRunsAndDisconnects(t *testing.T) {
	service := &fakeVaultSync{snapshot: appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderWebDAV,
		Status: appsync.StatusSynced, HasPassword: true,
	}}
	shell := New(WithSettings(newFakeSettings()), WithVaultSync(service))
	tt := openSectionPage(t, shell, Settings, "Vault sync")

	if !tt.HasText("The last sync finished.") {
		t.Fatalf("page does not show the synced state; texts: %v", tt.Texts())
	}
	if err := tt.Click("Sync now"); err != nil {
		t.Fatalf("click Sync now: %v", err)
	}
	if service.syncNow != 1 {
		t.Fatalf("SyncNow calls = %d, want 1", service.syncNow)
	}
	if err := tt.Click("Sync automatically"); err != nil {
		t.Fatalf("click Sync automatically: %v", err)
	}
	if !service.autoSync {
		t.Fatal("the automatic-sync switch did not reach the service")
	}
	if err := tt.Click("Disconnect"); err != nil {
		t.Fatalf("click Disconnect: %v", err)
	}
	if service.disconnectCalls != 0 {
		t.Fatal("tapping Disconnect deleted the profile without confirmation")
	}
	if !tt.HasText("Disconnecting removes the provider credential") {
		t.Fatalf("page does not ask before disconnecting; texts: %v", tt.Texts())
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	if service.disconnectCalls != 0 {
		t.Fatal("Cancel still disconnected")
	}
	if err := tt.Click("Disconnect"); err != nil {
		t.Fatalf("click Disconnect again: %v", err)
	}
	if err := tt.Click("Disconnect now"); err != nil {
		t.Fatalf("click Disconnect now: %v", err)
	}
	if service.disconnectCalls != 1 {
		t.Fatalf("disconnect calls = %d, want 1", service.disconnectCalls)
	}
	if !tt.HasText("Vault sync is not configured.") {
		t.Fatalf("page did not return to the setup state; texts: %v", tt.Texts())
	}
}

// TestVaultSyncPageConnectsGitHubByDeviceCode drives ticket 43's flow: the
// page offers GitHub Gist, starts the shared device authorization, shows the
// public user code and verification address, and reports the connected state
// once the engine's poll succeeds. The device code and any access token never
// appear in the frame.
func TestVaultSyncPageConnectsGitHubByDeviceCode(t *testing.T) {
	service := &fakeVaultSync{accessToken: "gho_phone_secret_token"}
	shell := New(WithSettings(newFakeSettings()), WithVaultSync(service))
	tt := openSectionPage(t, shell, Settings, "Vault sync")

	if err := tt.Click("Sync provider: GitHub Gist"); err != nil {
		t.Fatalf("choose GitHub Gist: %v", err)
	}
	if err := tt.Click("Master password"); err != nil {
		t.Fatalf("focus Master password: %v", err)
	}
	tt.Type("correct horse battery staple")
	tt.Frame()
	if err := tt.Click("Connect"); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	if service.githubStarts != 1 {
		t.Fatalf("StartGitHubConnect calls = %d, want 1", service.githubStarts)
	}
	if service.githubPassword != "correct horse battery staple" {
		t.Fatal("the typed master password did not reach the service")
	}

	// The engine published the one-time code mid-authorization.
	tt.Frame()
	if !tt.HasText("ABCD-1234") {
		t.Fatalf("page does not show the device user code; texts: %v", tt.Texts())
	}
	if !tt.HasText("https://github.com/login/device") {
		t.Fatalf("page does not show the verification address; texts: %v", tt.Texts())
	}

	// The poll succeeds: the engine connects and clears the one-time code.
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderGitHub,
		Login: "octocat", Status: appsync.StatusIdle, HasPassword: true,
	}
	tt.Frame()
	if !tt.HasText("Sync now") {
		t.Fatalf("page does not show the connected state after the poll; texts: %v", tt.Texts())
	}
	if !tt.HasText("GitHub") {
		t.Fatalf("page does not name the GitHub provider; texts: %v", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, "device-code-that-must-not-render") ||
			strings.Contains(text, service.accessToken) {
			t.Fatalf("the page rendered a credential: %q", text)
		}
	}
}

// TestVaultSyncPageGitHubCancelIsRetryable checks the cancel and expiry
// outcomes: both return the setup form with a clear reason and the Connect
// action still available, and the expired code is gone.
func TestVaultSyncPageGitHubCancelIsRetryable(t *testing.T) {
	service := &fakeVaultSync{}
	shell := New(WithSettings(newFakeSettings()), WithVaultSync(service))
	tt := openSectionPage(t, shell, Settings, "Vault sync")

	if err := tt.Click("Sync provider: GitHub Gist"); err != nil {
		t.Fatalf("choose GitHub Gist: %v", err)
	}
	if err := tt.Click("Master password"); err != nil {
		t.Fatalf("focus Master password: %v", err)
	}
	tt.Type("master")
	tt.Frame()
	if err := tt.Click("Connect"); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	tt.Frame()
	if !tt.HasText("ABCD-1234") {
		t.Fatalf("page does not show the device code before cancel; texts: %v", tt.Texts())
	}
	if err := tt.Click("Cancel sign-in"); err != nil {
		t.Fatalf("click Cancel sign-in: %v", err)
	}
	if service.githubCancels != 1 {
		t.Fatalf("CancelGitHubConnect calls = %d, want 1", service.githubCancels)
	}
	tt.Frame()
	if !tt.HasText("GitHub sign-in did not finish. Try again.") {
		t.Fatalf("page does not explain the cancelled sign-in; texts: %v", tt.Texts())
	}
	if tt.HasText("ABCD-1234") {
		t.Fatal("the cancelled code is still on screen")
	}
	if !tt.HasText("Connect") {
		t.Fatal("the page does not offer a retry after cancel")
	}

	// An expired code leaves the same retryable state without a Cancel action
	// because there is no code left to cancel.
	service.snapshot = appsync.Snapshot{
		Provider: appsync.ProviderGitHub, Status: appsync.StatusFailed,
		Problem: appsync.ProblemAuthorization,
	}
	tt.Frame()
	if !tt.HasText("GitHub sign-in did not finish. Try again.") {
		t.Fatalf("page does not explain the expired code; texts: %v", tt.Texts())
	}
	if !tt.HasText("Connect") {
		t.Fatal("the page does not offer a retry after expiry")
	}
}

// TestVaultSyncPageUnavailableState proves the page says what is missing
// instead of rendering blank when the composition root did not wire sync.
func TestVaultSyncPageUnavailableState(t *testing.T) {
	shell := New(WithSettings(newFakeSettings()))
	tt := openSectionPage(t, shell, Settings, "Vault sync")
	if !tt.HasText("Vault sync is not available on this device.") {
		t.Fatalf("page does not show the unavailable state; texts: %v", tt.Texts())
	}
}
