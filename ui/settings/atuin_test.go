package settings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

const (
	atuinTestToken = "pasted-atuin-token"
	atuinTestKey   = "0123456789abcdef0123456789abcdef"
)

// fakeHistoryAccount is the real panel's account service. It records every
// submission so the UI test proves the control reached the seam, not just a
// private state field.
type fakeHistoryAccount struct {
	status historysync.Status

	loginRequests []historysync.LoginRequest
	logins        int
	loginErr      error

	keys         []string
	keyConfirms  []bool
	setKeyCalls  int
	setKeyErr    error
	keyStatus    historysync.Status
	askKeyChange bool

	confirmUnverifiedCalls int
	confirmErr             error
	confirmedStatus        historysync.Status

	forgetCalls  int
	forgetErr    error
	forgetStatus historysync.Status
	logoutCalls  int
	logoutErr    error
	logoutStatus historysync.Status
	statusCalls  int
}

func (f *fakeHistoryAccount) Status() historysync.Status {
	f.statusCalls++
	return f.status
}

func (f *fakeHistoryAccount) Login(_ context.Context,
	request historysync.LoginRequest) (historysync.Status, error) {
	f.logins++
	f.loginRequests = append(f.loginRequests, request)
	if f.loginErr != nil {
		return f.status, f.loginErr
	}
	f.status = historysync.Status{
		State: atuinmodel.SyncIdle, Configured: true,
		ServerURL: request.ServerURL, Username: request.Username,
		AccountID: "user-1", ServerVersion: historysync.PinnedAtuinVersion,
	}
	return f.status, nil
}

func (f *fakeHistoryAccount) SetMasterKey(_ context.Context, input string, confirm bool) (historysync.Status, error) {
	f.setKeyCalls++
	f.keys = append(f.keys, input)
	f.keyConfirms = append(f.keyConfirms, confirm)
	if f.askKeyChange && !confirm {
		return f.status, historysync.ErrKeyChangeConfirmation
	}
	if f.setKeyErr != nil {
		return f.status, f.setKeyErr
	}
	f.askKeyChange = false
	f.status = f.keyStatus
	return f.status, nil
}

func (f *fakeHistoryAccount) ConfirmUnverifiedKey(context.Context) (historysync.Status, error) {
	f.confirmUnverifiedCalls++
	if f.confirmErr != nil {
		return f.status, f.confirmErr
	}
	f.status = f.confirmedStatus
	return f.status, nil
}

func (f *fakeHistoryAccount) ForgetMasterKey(context.Context) (historysync.Status, error) {
	f.forgetCalls++
	if f.forgetErr != nil {
		return f.status, f.forgetErr
	}
	f.status = f.forgetStatus
	return f.status, nil
}

func (f *fakeHistoryAccount) Logout(context.Context) (historysync.Status, error) {
	f.logoutCalls++
	if f.logoutErr != nil {
		return f.status, f.logoutErr
	}
	f.status = f.logoutStatus
	return f.status, nil
}

func (f *fakeHistoryAccount) Check(context.Context) (historysync.Status, error) {
	return f.status, nil
}

func atuinPanel(t *testing.T, account HistoryAccount, mutate func(*domain.Settings)) (*ui.Tester, *fakePreferences, *AtuinState) {
	t.Helper()
	prefs := newFakePreferences()
	prefs.settings.History.SyncEnabled = true
	prefs.settings.History.SyncAddress = "https://atuin.example.test"
	prefs.settings.History.Username = "alice"
	if mutate != nil {
		mutate(&prefs.settings)
	}
	state := NewAtuinState(account)
	tester := ui.NewTester(viewAt(AreaLogs, Config{Preferences: NewPreferencesState(prefs), Atuin: state}),
		1100, 2400)
	tester.SetLocale("en")
	tester.Frame()
	return tester, prefs, state
}

func atuinStatus(keyState atuinmodel.KeyState) historysync.Status {
	return historysync.Status{
		State: atuinmodel.SyncIdle, Configured: true,
		ServerURL: "https://atuin.example.test", Username: "alice",
		AuthMode:  atuinmodel.AuthModeSession,
		AccountID: "user-1", ServerVersion: historysync.PinnedAtuinVersion,
		KeyFingerprint: "sha256:0123456789abcdef",
		KeyState:       keyState,
	}
}

func TestAtuinPanelShowsStatusFingerprintAndNoCredential(t *testing.T) {
	account := &fakeHistoryAccount{status: atuinStatus(atuinmodel.KeyStateVerified)}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")

	for _, want := range []string{
		m.SettingsAtuinSectionTitle(),
		m.SettingsAtuinAccountTitle(),
		m.SettingsAtuinStatusIdle(),
		m.SettingsAtuinKeyFingerprint("sha256:0123456789abcdef"),
		m.SettingsAtuinServerVersion(historysync.PinnedAtuinVersion),
		m.SettingsAtuinAccountID("user-1"),
		m.SettingsAtuinKeyVerified(),
	} {
		if !tester.HasText(want) {
			t.Errorf("panel is missing %q: %v", want, tester.Texts())
		}
	}
	*state.Password() = "hunter2"
	*state.Token() = atuinTestToken
	tester.Frame()
	texts := strings.Join(tester.Texts(), "\n")
	for _, secret := range []string{"hunter2", atuinTestToken, atuinTestKey} {
		if strings.Contains(texts, secret) {
			t.Fatalf("panel rendered credential %q: %s", secret, texts)
		}
	}
}

func TestAtuinPanelSignsInWithPasswordOrPastedToken(t *testing.T) {
	account := &fakeHistoryAccount{}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")

	*state.Password() = "hunter2"
	if err := tester.Click(m.SettingsAtuinSignIn()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(account.loginRequests) != 1 {
		t.Fatalf("login requests = %+v", account.loginRequests)
	}
	first := account.loginRequests[0]
	if first.ServerURL != "https://atuin.example.test" || first.Username != "alice" ||
		first.Password != "hunter2" || first.Token != "" ||
		first.AuthMode != atuinmodel.AuthModeSession {
		t.Fatalf("password login request = %+v", first)
	}
	if *state.Password() != "" {
		t.Fatal("the password survived a successful login")
	}
	if strings.Contains(strings.Join(tester.Texts(), "\n"), "hunter2") {
		t.Fatal("the panel rendered the password after login")
	}

	state.SetLoginMethod(AtuinLoginToken)
	tester.Frame()
	*state.Token() = atuinTestToken
	tester.Frame()
	if strings.Contains(strings.Join(tester.Texts(), "\n"), atuinTestToken) {
		t.Fatal("the panel rendered the pasted token")
	}
	if err := tester.Click(m.SettingsAtuinSignIn()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(account.loginRequests) != 2 {
		t.Fatalf("login requests = %+v", account.loginRequests)
	}
	second := account.loginRequests[1]
	if second.Token != atuinTestToken || second.Password != "" {
		t.Fatalf("token login request = %+v", second)
	}
	if *state.Token() != "" {
		t.Fatal("the pasted token survived a successful login")
	}
}

func TestAtuinPanelConfirmsAnUnverifiedKey(t *testing.T) {
	unverified := atuinStatus(atuinmodel.KeyStateUnverified)
	unverified.Problem = atuinmodel.ProblemKeyUnverified
	confirmed := unverified
	confirmed.KeyConfirmed = true
	account := &fakeHistoryAccount{
		status: atuinStatus(atuinmodel.KeyStateVerified), keyStatus: unverified,
		confirmedStatus: confirmed,
	}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")

	*state.MasterKey() = atuinTestKey
	if err := tester.Click(m.SettingsAtuinSaveKey()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if account.setKeyCalls != 1 || account.keys[0] != atuinTestKey {
		t.Fatalf("key submissions = %+v", account.keys)
	}
	if *state.MasterKey() != "" {
		t.Fatal("the master key draft survived a successful save")
	}
	if !tester.HasText(m.SettingsAtuinKeyUnverifiedTitle()) {
		t.Fatalf("the unverified warning is missing: %v", tester.Texts())
	}
	if strings.Contains(strings.Join(tester.Texts(), "\n"), atuinTestKey) {
		t.Fatal("the panel rendered the master key")
	}

	if err := tester.Click(m.SettingsAtuinConfirmUnverified()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if account.confirmUnverifiedCalls != 1 {
		t.Fatalf("confirmation calls = %d", account.confirmUnverifiedCalls)
	}
	if !tester.HasText(m.SettingsAtuinKeyConfirmed()) {
		t.Fatalf("the confirmed state is missing: %v", tester.Texts())
	}
}

func TestAtuinPanelRequiresExplicitKeyChangeKeyLossAndLogout(t *testing.T) {
	account := &fakeHistoryAccount{
		status:       atuinStatus(atuinmodel.KeyStateVerified),
		askKeyChange: true,
		keyStatus:    atuinStatus(atuinmodel.KeyStateVerified),
		forgetStatus: atuinStatus(atuinmodel.KeyStateMissing),
		logoutStatus: historysync.Status{
			State: atuinmodel.SyncNotConfigured, Problem: atuinmodel.ProblemNotConfigured,
		},
	}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")

	*state.MasterKey() = atuinTestKey
	if err := tester.Click(m.SettingsAtuinSaveKey()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAtuinKeyChangeTitle()) {
		t.Fatalf("the key-change warning is missing: %v", tester.Texts())
	}
	if len(account.keyConfirms) != 1 || account.keyConfirms[0] {
		t.Fatalf("unconfirmed key was stored: %+v", account.keyConfirms)
	}
	if *state.MasterKey() != atuinTestKey {
		t.Fatal("the pending key draft was lost before confirmation")
	}
	if err := tester.Click(m.SettingsAtuinReplaceKey()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(account.keyConfirms) != 2 || !account.keyConfirms[1] {
		t.Fatalf("confirmed key submission = %+v", account.keyConfirms)
	}
	if tester.HasText(m.SettingsAtuinKeyChangeTitle()) {
		t.Fatal("the key-change warning survived confirmation")
	}

	if err := tester.Click(m.SettingsAtuinForgetKey()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAtuinForgetKeyTitle()) || account.forgetCalls != 0 {
		t.Fatalf("forgetting the key did not ask first (calls=%d): %v", account.forgetCalls, tester.Texts())
	}
	if err := tester.Click(m.SettingsAtuinForgetKeyConfirm()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if account.forgetCalls != 1 {
		t.Fatalf("forget calls = %d", account.forgetCalls)
	}

	if err := tester.Click(m.SettingsAtuinLogout()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAtuinLogoutTitle()) || account.logoutCalls != 0 {
		t.Fatalf("logout did not ask first (calls=%d): %v", account.logoutCalls, tester.Texts())
	}
	if err := tester.Click(m.SettingsAtuinLogoutConfirm()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if account.logoutCalls != 1 {
		t.Fatalf("logout calls = %d", account.logoutCalls)
	}
}

func TestAtuinPanelReportsAProblemWithoutRawErrorText(t *testing.T) {
	account := &fakeHistoryAccount{loginErr: errors.New("server said token=" + atuinTestToken)}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")

	*state.Password() = "hunter2"
	if err := tester.Click(m.SettingsAtuinSignIn()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAtuinProblemSave()) {
		t.Fatalf("the generic action problem is missing: %v", tester.Texts())
	}
	if strings.Contains(strings.Join(tester.Texts(), "\n"), atuinTestToken) {
		t.Fatal("the panel rendered an error string containing a credential")
	}
}
