package phoneshell

import (
	"context"
	"testing"
	"time"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// fakeHistory is the HistorySyncService the phone page tests drive. The real
// account and sync services are covered by the app-level phone tests against a
// loopback Atuin server.
type fakeHistory struct {
	status   historysync.Status
	settings domainpreferences.History

	lastLogin   historysync.LoginRequest
	loginErr    error
	syncNow     int
	logoutCalls int
	confirmed   int
}

func (f *fakeHistory) Status() historysync.Status { return f.status }

func (f *fakeHistory) HistorySettings() domainpreferences.History { return f.settings }

func (f *fakeHistory) UpdateHistorySettings(change func(*domainpreferences.History)) error {
	change(&f.settings)
	return nil
}

func (f *fakeHistory) Login(_ context.Context, request historysync.LoginRequest) (historysync.Status, error) {
	f.lastLogin = request
	if f.loginErr != nil {
		return f.status, f.loginErr
	}
	f.status.Configured = true
	return f.status, nil
}

func (f *fakeHistory) SetMasterKey(context.Context, string, bool) (historysync.Status, error) {
	return f.status, nil
}

func (f *fakeHistory) ConfirmUnverifiedKey(context.Context) (historysync.Status, error) {
	f.confirmed++
	f.status.KeyState = atuinmodel.KeyStateVerified
	return f.status, nil
}

func (f *fakeHistory) Logout(context.Context) (historysync.Status, error) {
	f.logoutCalls++
	f.status.Configured = false
	return f.status, nil
}

func (f *fakeHistory) Check(context.Context) (historysync.Status, error) { return f.status, nil }

func (f *fakeHistory) SyncNow() { f.syncNow++ }

// TestHistorySyncPageShowsLiveStatusAndFailure covers "sync status is visible
// in the app, and a failure is reported": the page renders the engine's state,
// problem, account, pending count, and last sync time on the phone.
func TestHistorySyncPageShowsLiveStatusAndFailure(t *testing.T) {
	settings := newFakeSettings()
	history := &fakeHistory{
		settings: domainpreferences.History{
			SyncEnabled: true, SyncAddress: "https://api.atuin.sh", Username: "alice",
		},
		status: historysync.Status{
			State: atuinmodel.SyncError, Problem: atuinmodel.ProblemOffline,
			Configured: true, Username: "alice", ServerURL: "https://api.atuin.sh",
			LastSyncAt:     time.Date(2026, 11, 20, 9, 30, 0, 0, time.Local),
			PendingUploads: 3,
		},
	}
	shell := New(WithSettings(settings), WithHistorySync(history))
	tt := openSectionPage(t, shell, Settings, "History sync")

	if tt.HasText("Replace key") {
		t.Fatalf("a stray replacement button rendered before any key conflict: %v", texts(t, tt))
	}
	for _, want := range []string{
		"Sync failed; retrying in the background.",
		"The server could not be reached.",
		"Account: alice",
		"Pending uploads: 3",
		"Last sync: 2026-11-20 09:30",
	} {
		if !tt.HasText(want) {
			t.Fatalf("history page does not show %q; texts: %v", want, texts(t, tt))
		}
	}
}

// TestHistorySyncPageUsesTheSharedAccountDocument covers the phone's edit
// path: the sync switch writes the same device-local History document the
// desktop uses, and sign-in submits the address and username from that
// document while keeping the token in the draft until the service returns.
func TestHistorySyncPageUsesTheSharedAccountDocument(t *testing.T) {
	settings := newFakeSettings()
	history := &fakeHistory{
		settings: domainpreferences.History{
			SyncEnabled: true, SyncAddress: "https://api.atuin.sh", Username: "alice",
		},
		status: historysync.Status{State: atuinmodel.SyncNotConfigured,
			Problem: atuinmodel.ProblemNotConfigured},
	}
	shell := New(WithSettings(settings), WithHistorySync(history))
	tt := openSectionPage(t, shell, Settings, "History sync")

	if !tt.HasText("No Atuin account is configured.") {
		t.Fatalf("history page lacks the not-configured state; texts: %v", texts(t, tt))
	}

	if err := tt.Click("Sync history"); err != nil {
		t.Fatalf("click History sync: %v", err)
	}
	if history.settings.SyncEnabled {
		t.Fatal("tapping the switch did not write SyncEnabled through the shared document")
	}
	if err := tt.Click("Sync history"); err != nil {
		t.Fatalf("click History sync again: %v", err)
	}
	if !history.settings.SyncEnabled {
		t.Fatal("re-enabling history sync did not persist")
	}

	if err := tt.Click("Token"); err != nil {
		t.Fatalf("choose token login: %v", err)
	}
	if err := tt.Click("Token type: Bearer"); err != nil {
		t.Fatalf("choose Bearer tokens: %v", err)
	}
	if err := tt.Click("Session token"); err != nil {
		t.Fatalf("focus token field: %v", err)
	}
	tt.Type("pasted-token")
	tt.Frame()
	if err := tt.Click("Sign in"); err != nil {
		t.Fatalf("click Sign in: %v", err)
	}
	if history.lastLogin.Token != "pasted-token" ||
		history.lastLogin.AuthMode != atuinmodel.AuthModeBearer {
		t.Fatalf("login request = %+v, want the pasted token and Bearer auth", history.lastLogin)
	}
	if history.lastLogin.ServerURL != "https://api.atuin.sh" ||
		history.lastLogin.Username != "alice" {
		t.Fatalf("login request = %+v, want the shared account document fields", history.lastLogin)
	}
	if history.settings.AuthMode != atuinmodel.AuthModeBearer {
		t.Fatalf("persisted AuthMode = %q, want bearer", history.settings.AuthMode)
	}
	if !history.status.Configured {
		t.Fatal("the page did not refresh the account status after sign-in")
	}
}

// TestHistorySyncPageReportsActionFailures says a rejected sign-in is shown as
// a localized sentence and never as the service's error text.
func TestHistorySyncPageReportsActionFailures(t *testing.T) {
	settings := newFakeSettings()
	history := &fakeHistory{
		settings: domainpreferences.History{
			SyncEnabled: true, SyncAddress: "https://api.atuin.sh", Username: "alice",
		},
		status:   historysync.Status{State: atuinmodel.SyncNotConfigured},
		loginErr: historysync.ErrAuthFailed,
	}
	shell := New(WithSettings(settings), WithHistorySync(history))
	tt := openSectionPage(t, shell, Settings, "History sync")
	if err := tt.Click("Sign in"); err != nil {
		t.Fatalf("click Sign in: %v", err)
	}
	if !tt.HasText("The server rejected these credentials.") {
		t.Fatalf("history page does not report the failure; texts: %v", texts(t, tt))
	}
	if tt.HasText(historysync.ErrAuthFailed.Error()) {
		t.Fatal("the page rendered the raw service error")
	}
}
