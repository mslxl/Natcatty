package settings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// TestAtuinAccountCardOwnsTheIdentityAndSyncCardDoesNot proves the one
// account reads as one card: the server address and username are inside the
// Atuin account card, above its status row, beside the sign-in controls. The
// Command History sync card keeps only the sync toggle and interval, and the
// page renders exactly one identity input for each.
func TestAtuinAccountCardOwnsTheIdentityAndSyncCardDoesNot(t *testing.T) {
	account := &fakeHistoryAccount{status: atuinStatus(atuinmodel.KeyStateVerified)}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")

	// Count real keyboard focus stops rather than label nodes: a field's
	// label also names its input, so the same string can appear twice.
	searchLabel := m.SettingsSearchPlaceholder()
	inputStops := func(label string) int {
		for step := 0; step < 120 && !tester.Focused(searchLabel); step++ {
			tester.Key(0, ui.KeyTab)
			tester.Frame()
		}
		if !tester.Focused(searchLabel) {
			t.Fatalf("Tab never reached the search field %q", searchLabel)
		}
		count := 0
		for step := 0; step < 120; step++ {
			tester.Key(0, ui.KeyTab)
			tester.Frame()
			if tester.Focused(searchLabel) {
				return count
			}
			if tester.Focused(label) {
				count++
			}
		}
		t.Fatalf("focus did not wrap back to the search field: %v", tester.Texts())
		return 0
	}
	for _, label := range []string{m.SettingsAtuinAddress(), m.SettingsAtuinUsername()} {
		if got := inputStops(label); got != 1 {
			t.Fatalf("the page gives %q %d focusable inputs, want exactly one", label, got)
		}
	}

	find := func(text string) ui.Rect {
		t.Helper()
		rect, ok := tester.Find(text)
		if !ok {
			t.Fatalf("the page does not show %q: %v", text, tester.Texts())
		}
		return rect
	}
	syncTitle := find(m.SettingsAtuinSectionTitle())
	accountTitle := find(m.SettingsAtuinAccountTitle())
	address := find(m.SettingsAtuinAddress())
	username := find(m.SettingsAtuinUsername())
	statusLine := find(m.SettingsAtuinAccountID("user-1"))
	signIn := find(m.SettingsAtuinSignIn())

	// The sync card comes before the account card, so its controls end above
	// the account title; none of them is an identity input.
	for _, label := range []string{
		m.SettingsAtuinSyncEnabled(), m.SettingsAtuinSyncInterval(),
	} {
		rect := find(label)
		if rect.Y < syncTitle.Y || rect.Y+rect.H > accountTitle.Y+0.5 {
			t.Errorf("the sync control %q is outside the sync card: %+v", label, rect)
		}
	}

	// Identity sits at the top of the account card, before the status row;
	// sign-in follows the status row in the same card.
	if address.Y <= accountTitle.Y+accountTitle.H || username.Y <= accountTitle.Y+accountTitle.H {
		t.Errorf("the identity inputs are not inside the account card: address=%+v username=%+v",
			address, username)
	}
	if address.Y >= statusLine.Y || username.Y >= statusLine.Y {
		t.Errorf("the identity inputs are not above the status row: address=%+v username=%+v status=%+v",
			address, username, statusLine)
	}
	if signIn.Y <= statusLine.Y || signIn.Y <= accountTitle.Y {
		t.Errorf("the sign-in control is not below the status row inside the account card: %+v", signIn)
	}

	// The auth-mode selector is meaningful only for a pasted token, so the
	// password path renders it nowhere.
	if got := inputStops(m.SettingsAtuinAuthMode()); got != 0 {
		t.Fatalf("the password path gives the auth-mode selector %d focus stops, want none", got)
	}
	state.SetLoginMethod(AtuinLoginToken)
	tester.Frame()
	if got := inputStops(m.SettingsAtuinAuthMode()); got != 1 {
		t.Fatalf("the pasted-token path gives the auth-mode selector %d focus stops, want once", got)
	}
	if auth := find(m.SettingsAtuinAuthMode()); auth.Y <= accountTitle.Y {
		t.Errorf("the auth-mode selector is outside the account card: %+v", auth)
	}
}

// TestAtuinAccountCardFollowsTheSyncToggle proves the account card is absent
// while command history sync is off, appears when the user enables sync on the
// real checkbox, and disappears again when it is turned off. The sync card
// itself never leaves the page, so sync can always be re-enabled from it.
func TestAtuinAccountCardFollowsTheSyncToggle(t *testing.T) {
	t.Run("account card follows the sync checkbox", func(t *testing.T) {
		account := &fakeHistoryAccount{status: atuinStatus(atuinmodel.KeyStateVerified)}
		tester, prefs, _ := atuinPanel(t, account,
			func(s *domain.Settings) { s.History.SyncEnabled = false })
		m := locales.In("en")

		accountControls := []string{
			m.SettingsAtuinAccountTitle(), m.SettingsAtuinAddress(), m.SettingsAtuinUsername(),
			m.SettingsAtuinSignIn(), m.SettingsAtuinKeyTitle(), m.SettingsAtuinKeyPurpose(),
			m.SettingsAtuinChangeKey(), m.SettingsAtuinLogout(),
		}
		syncControls := []string{
			m.SettingsAtuinSectionTitle(), m.SettingsAtuinSyncEnabled(),
			m.SettingsAtuinSyncInterval(),
		}
		assertPresence := func(step string, present bool, labels []string) {
			t.Helper()
			for _, label := range labels {
				if _, ok := tester.Find(label); ok != present {
					t.Fatalf("%s: presence of %q = %v, want %v: %v",
						step, label, ok, present, tester.Texts())
				}
			}
		}

		if prefs.settings.History.SyncEnabled {
			t.Fatal("the fixture did not start with sync disabled")
		}
		assertPresence("sync off", false, accountControls)
		assertPresence("sync off", true, syncControls)

		if err := tester.Click(m.SettingsAtuinSyncEnabled()); err != nil {
			t.Fatalf("click the sync checkbox: %v", err)
		}
		tester.Frame()
		if !prefs.settings.History.SyncEnabled {
			t.Fatal("the sync checkbox did not reach the preferences service")
		}
		assertPresence("sync on", true, accountControls)
		assertPresence("sync on", true, syncControls)

		if err := tester.Click(m.SettingsAtuinSyncEnabled()); err != nil {
			t.Fatalf("click the sync checkbox again: %v", err)
		}
		tester.Frame()
		if prefs.settings.History.SyncEnabled {
			t.Fatal("the second click did not disable sync")
		}
		assertPresence("sync off again", false, accountControls)
		assertPresence("sync off again", true, syncControls)
	})

	// A nil Preferences is a test-only constructor with no persisted document
	// to consult, so the account card keeps rendering as it did before.
	t.Run("nil preferences", func(t *testing.T) {
		state := NewAtuinState(&fakeHistoryAccount{status: atuinStatus(atuinmodel.KeyStateVerified)})
		tester := ui.NewTester(viewAt(AreaLogs, Config{Atuin: state}), 1100, 2400)
		tester.SetLocale("en")
		tester.Frame()
		m := locales.In("en")
		for _, label := range []string{
			m.SettingsAtuinAccountTitle(), m.SettingsAtuinSignIn(),
			m.SettingsAtuinKeyTitle(), m.SettingsAtuinLogout(),
		} {
			if _, ok := tester.Find(label); !ok {
				t.Fatalf("nil Preferences dropped %q: %v", label, tester.Texts())
			}
		}
	})
}

// TestAtuinPanelStatesWhatTheServerCanSee pins the spec-required limitation
// notice: the server sees record identifiers and metadata, not command text.
func TestAtuinPanelStatesWhatTheServerCanSee(t *testing.T) {
	account := &fakeHistoryAccount{status: atuinStatus(atuinmodel.KeyStateVerified)}
	tester, _, _ := atuinPanel(t, account, nil)
	description := locales.In("en").SettingsAtuinAccountDescription()
	for _, want := range []string{"record identifiers and metadata", "not command text"} {
		if !strings.Contains(description, want) {
			t.Fatalf("the account description is missing %q: %q", want, description)
		}
	}
	if !tester.HasText(description) {
		t.Fatalf("the panel does not render the account description: %v", tester.Texts())
	}
}

// TestAtuinPanelShowsEngineProgressAndManualSync proves the account panel
// reads the background engine live: pending uploads, the next retry time, and
// last sync appear without an account action, and the Sync now control reaches
// the engine seam.
func TestAtuinPanelShowsEngineProgressAndManualSync(t *testing.T) {
	account := &fakeHistoryAccount{status: atuinStatus(atuinmodel.KeyStateVerified)}
	prefs := newFakePreferences()
	prefs.settings.History.SyncEnabled = true
	prefs.settings.History.SyncAddress = "https://atuin.example.test"
	prefs.settings.History.Username = "alice"
	lastSync := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	engine := historysync.SyncStatus{
		State:          atuinmodel.SyncOffline,
		Problem:        atuinmodel.ProblemOffline,
		ServerVersion:  historysync.PinnedAtuinVersion,
		LastSyncAt:     lastSync,
		PendingUploads: 3,
		NextRetryAt:    lastSync.Add(30 * time.Minute),
	}
	manual := 0
	state := NewAtuinState(account,
		WithAtuinSyncStatus(func() historysync.SyncStatus { return engine }),
		WithAtuinSyncNow(func() { manual++ }))
	tester := ui.NewTester(viewAt(AreaLogs,
		Config{Preferences: NewPreferencesState(prefs), Atuin: state}), 1100, 2400)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	merged := state.Status()
	if merged.State != atuinmodel.SyncOffline || merged.LastSyncAt != lastSync ||
		merged.PendingUploads != 3 || merged.NextRetryAt != lastSync.Add(30*time.Minute) {
		t.Fatalf("merged status = %+v", merged)
	}
	for _, want := range []string{
		m.SettingsAtuinStatusOffline(),
		m.SettingsAtuinPendingUploads("3"),
		m.SettingsAtuinLastSync("2026-11-08 12:00"),
		m.SettingsAtuinNextRetry("2026-11-08 12:30"),
	} {
		if !tester.HasText(want) {
			t.Errorf("panel is missing %q: %v", want, tester.Texts())
		}
	}
	if err := tester.Click(m.SettingsAtuinSyncNow()); err != nil {
		t.Fatalf("click Sync now: %v", err)
	}
	if manual != 1 {
		t.Fatalf("manual sync calls = %d", manual)
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

	// The fixture starts with a stored key, so the editor is collapsed until
	// the user explicitly opens it.
	if err := tester.Click(m.SettingsAtuinChangeKey()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
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

	// The fixture starts with a stored key, so open the editor before
	// submitting a different key.
	if err := tester.Click(m.SettingsAtuinChangeKey()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
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
	if state.EditingKey() || tester.HasText(m.SettingsAtuinKeyDescription()) ||
		tester.HasText(m.SettingsAtuinSaveKey()) {
		t.Fatalf("the editor survived the confirmed key replacement: %v", tester.Texts())
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

// TestAtuinPanelCollapsesTheMasterKeyEditorForAStoredKey pins the stored-key
// card: the long description, input, and save button stay hidden behind one
// explicit Change key action, so the master key does not read as a second
// sign-in method. The purpose line stays visible in every key state, and the
// editor is always open when no usable key is stored.
func TestAtuinPanelCollapsesTheMasterKeyEditorForAStoredKey(t *testing.T) {
	m := locales.In("en")
	assertPresence := func(t *testing.T, tester *ui.Tester, step string, present bool, labels ...string) {
		t.Helper()
		for _, label := range labels {
			if _, ok := tester.Find(label); ok != present {
				t.Fatalf("%s: presence of %q = %v, want %v: %v",
					step, label, ok, present, tester.Texts())
			}
		}
	}
	hiddenEditor := []string{
		m.SettingsAtuinKeyDescription(), m.SettingsAtuinSaveKey(), m.SettingsCancel(),
	}

	storedKeyStates := []struct {
		name     string
		keyState atuinmodel.KeyState
		status   string
	}{
		{"verified", atuinmodel.KeyStateVerified, m.SettingsAtuinKeyVerified()},
		{"unverified", atuinmodel.KeyStateUnverified, m.SettingsAtuinKeyUnverifiedTitle()},
		{"mismatch", atuinmodel.KeyStateMismatch, m.SettingsAtuinProblemKeyMismatch()},
	}
	for _, test := range storedKeyStates {
		t.Run(test.name+" key starts collapsed", func(t *testing.T) {
			account := &fakeHistoryAccount{status: atuinStatus(test.keyState)}
			tester, _, state := atuinPanel(t, account, nil)
			if state.EditingKey() {
				t.Fatal("the editor started expanded for a stored key")
			}
			assertPresence(t, tester, "collapsed", true,
				m.SettingsAtuinKeyTitle(), m.SettingsAtuinKeyPurpose(), test.status,
				m.SettingsAtuinChangeKey(), m.SettingsAtuinForgetKey(),
				m.SettingsAtuinKeyFingerprint("sha256:0123456789abcdef"))
			assertPresence(t, tester, "collapsed", false, hiddenEditor...)
		})
	}

	t.Run("change key expands and cancel clears the draft", func(t *testing.T) {
		account := &fakeHistoryAccount{status: atuinStatus(atuinmodel.KeyStateVerified)}
		tester, _, state := atuinPanel(t, account, nil)
		if err := tester.Click(m.SettingsAtuinChangeKey()); err != nil {
			t.Fatalf("click Change key: %v", err)
		}
		tester.Frame()
		if !state.EditingKey() {
			t.Fatal("Change key did not expand the editor")
		}
		assertPresence(t, tester, "expanded", true,
			m.SettingsAtuinKeyPurpose(), m.SettingsAtuinKeyDescription(),
			m.SettingsAtuinSaveKey(), m.SettingsCancel())
		*state.MasterKey() = atuinTestKey
		tester.Frame()
		if err := tester.Click(m.SettingsCancel()); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		tester.Frame()
		if state.EditingKey() {
			t.Fatal("Cancel did not collapse the editor")
		}
		if *state.MasterKey() != "" {
			t.Fatal("Cancel kept the master-key draft")
		}
		assertPresence(t, tester, "cancelled", false, hiddenEditor...)
		if !tester.HasText(m.SettingsAtuinChangeKey()) {
			t.Fatalf("the Change key action is missing after Cancel: %v", tester.Texts())
		}
	})

	t.Run("successful save collapses the editor", func(t *testing.T) {
		account := &fakeHistoryAccount{
			status:    atuinStatus(atuinmodel.KeyStateVerified),
			keyStatus: atuinStatus(atuinmodel.KeyStateVerified),
		}
		tester, _, state := atuinPanel(t, account, nil)
		if err := tester.Click(m.SettingsAtuinChangeKey()); err != nil {
			t.Fatalf("click Change key: %v", err)
		}
		tester.Frame()
		*state.MasterKey() = atuinTestKey
		if err := tester.Click(m.SettingsAtuinSaveKey()); err != nil {
			t.Fatalf("click Save key: %v", err)
		}
		tester.Frame()
		if account.setKeyCalls != 1 || account.keys[0] != atuinTestKey {
			t.Fatalf("key submissions = %+v", account.keys)
		}
		if state.EditingKey() {
			t.Fatal("a successful save left the editor expanded")
		}
		if *state.MasterKey() != "" {
			t.Fatal("the master-key draft survived a successful save")
		}
		assertPresence(t, tester, "saved", false, hiddenEditor...)
		if !tester.HasText(m.SettingsAtuinChangeKey()) {
			t.Fatalf("the Change key action is missing after saving: %v", tester.Texts())
		}
	})

	t.Run("cancelling the key-change warning keeps the draft and editor", func(t *testing.T) {
		account := &fakeHistoryAccount{
			status:       atuinStatus(atuinmodel.KeyStateVerified),
			askKeyChange: true,
			keyStatus:    atuinStatus(atuinmodel.KeyStateVerified),
		}
		tester, _, state := atuinPanel(t, account, nil)
		if err := tester.Click(m.SettingsAtuinChangeKey()); err != nil {
			t.Fatalf("click Change key: %v", err)
		}
		tester.Frame()
		*state.MasterKey() = atuinTestKey
		if err := tester.Click(m.SettingsAtuinSaveKey()); err != nil {
			t.Fatalf("click Save key: %v", err)
		}
		tester.Frame()
		if !tester.HasText(m.SettingsAtuinKeyChangeTitle()) {
			t.Fatalf("the key-change warning is missing: %v", tester.Texts())
		}
		if !state.EditingKey() || *state.MasterKey() != atuinTestKey {
			t.Fatal("the pending key draft was lost before confirmation")
		}
		// The confirmation owns the only Cancel while it is open.
		if tester.HasText(m.SettingsAtuinSaveKey()) {
			t.Fatalf("the editor's save control is still rendered: %v", tester.Texts())
		}
		if err := tester.Click(m.SettingsCancel()); err != nil {
			t.Fatalf("click Cancel: %v", err)
		}
		tester.Frame()
		if tester.HasText(m.SettingsAtuinKeyChangeTitle()) {
			t.Fatal("Cancel kept the key-change warning open")
		}
		if !state.EditingKey() || *state.MasterKey() != atuinTestKey {
			t.Fatal("CancelKeyChange did not keep the editor open with its draft")
		}
		assertPresence(t, tester, "retry", true,
			m.SettingsAtuinKeyDescription(), m.SettingsAtuinSaveKey(), m.SettingsCancel())
	})

	t.Run("no stored key keeps the editor visible", func(t *testing.T) {
		for _, keyState := range []atuinmodel.KeyState{atuinmodel.KeyStateNone, atuinmodel.KeyStateMissing} {
			account := &fakeHistoryAccount{status: atuinStatus(keyState)}
			tester, _, state := atuinPanel(t, account, nil)
			if state.EditingKey() {
				t.Fatalf("%v: the editing flag started set", keyState)
			}
			assertPresence(t, tester, string(keyState), true,
				m.SettingsAtuinKeyPurpose(), m.SettingsAtuinKeyDescription(),
				m.SettingsAtuinSaveKey())
			assertPresence(t, tester, string(keyState), false,
				m.SettingsAtuinChangeKey(), m.SettingsCancel())
		}
	})

	t.Run("forget returns to the empty editor", func(t *testing.T) {
		forgotten := atuinStatus(atuinmodel.KeyStateNone)
		forgotten.KeyFingerprint = ""
		account := &fakeHistoryAccount{
			status:       atuinStatus(atuinmodel.KeyStateVerified),
			forgetStatus: forgotten,
		}
		tester, _, state := atuinPanel(t, account, nil)
		if err := tester.Click(m.SettingsAtuinChangeKey()); err != nil {
			t.Fatalf("click Change key: %v", err)
		}
		tester.Frame()
		if err := tester.Click(m.SettingsAtuinForgetKey()); err != nil {
			t.Fatalf("click Forget key: %v", err)
		}
		tester.Frame()
		if err := tester.Click(m.SettingsAtuinForgetKeyConfirm()); err != nil {
			t.Fatalf("confirm Forget key: %v", err)
		}
		tester.Frame()
		if account.forgetCalls != 1 {
			t.Fatalf("forget calls = %d", account.forgetCalls)
		}
		if state.EditingKey() || *state.MasterKey() != "" {
			t.Fatal("forgetting the key kept the editor state")
		}
		assertPresence(t, tester, "forgotten", true,
			m.SettingsAtuinKeyDescription(), m.SettingsAtuinSaveKey())
		assertPresence(t, tester, "forgotten", false,
			m.SettingsAtuinChangeKey(), m.SettingsCancel())
	})

	t.Run("logout returns to the empty editor", func(t *testing.T) {
		account := &fakeHistoryAccount{
			status: atuinStatus(atuinmodel.KeyStateVerified),
			logoutStatus: historysync.Status{
				State: atuinmodel.SyncNotConfigured, Problem: atuinmodel.ProblemNotConfigured,
			},
		}
		tester, _, state := atuinPanel(t, account, nil)
		if err := tester.Click(m.SettingsAtuinChangeKey()); err != nil {
			t.Fatalf("click Change key: %v", err)
		}
		tester.Frame()
		if err := tester.Click(m.SettingsAtuinLogout()); err != nil {
			t.Fatalf("click Log out: %v", err)
		}
		tester.Frame()
		if err := tester.Click(m.SettingsAtuinLogoutConfirm()); err != nil {
			t.Fatalf("confirm Log out: %v", err)
		}
		tester.Frame()
		if account.logoutCalls != 1 {
			t.Fatalf("logout calls = %d", account.logoutCalls)
		}
		if state.EditingKey() || *state.MasterKey() != "" {
			t.Fatal("logout kept the editor state")
		}
		assertPresence(t, tester, "logged out", true,
			m.SettingsAtuinKeyDescription(), m.SettingsAtuinSaveKey())
		assertPresence(t, tester, "logged out", false,
			m.SettingsAtuinChangeKey(), m.SettingsCancel())
	})
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

// TestAppLockMapsToTheLockedProblem pins the account service's fail-closed
// result to the localized locked message. The raw error is never rendered.
func TestAppLockMapsToTheLockedProblem(t *testing.T) {
	if got := problemFor(historysync.ErrAppLocked); got != AtuinProblemLocked {
		t.Fatalf("problemFor(ErrAppLocked) = %q, want %q", got, AtuinProblemLocked)
	}
}

// TestAtuinDetailsControlIsCollapsedAndShowsSanitizedFields proves the details
// surface exists, is collapsed by default, and reveals only the typed
// non-sensitive values after the explicit click.
func TestAtuinDetailsControlIsCollapsedAndShowsSanitizedFields(t *testing.T) {
	status := atuinStatus(atuinmodel.KeyStateVerified)
	lastSync := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	status.LastSyncAt = lastSync
	status.PendingUploads = 4
	status.NextRetryAt = lastSync.Add(30 * time.Minute)
	account := &fakeHistoryAccount{status: status, loginErr: historysync.ErrOffline}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")
	*state.Password() = "hunter2"
	if err := tester.Click(m.SettingsAtuinSignIn()); err != nil {
		t.Fatalf("click Sign in: %v", err)
	}

	if !tester.HasText(m.SettingsAtuinDetailsShow()) {
		t.Fatalf("the Details control is missing: %v", tester.Texts())
	}
	if state.ShowDetails() {
		t.Fatal("the Details control started expanded")
	}
	for _, hidden := range []string{
		m.SettingsAtuinDetailsTitle(),
		m.SettingsAtuinDetailsHint(),
		m.SettingsAtuinDetailsServer(status.ServerVersion),
		m.SettingsAtuinDetailsPending("4"),
	} {
		if tester.HasText(hidden) {
			t.Fatalf("collapsed details already showed %q", hidden)
		}
	}

	if err := tester.Click(m.SettingsAtuinDetailsShow()); err != nil {
		t.Fatalf("click Details: %v", err)
	}
	tester.Frame()
	if !state.ShowDetails() {
		t.Fatal("Details did not expand")
	}
	for _, want := range []string{
		m.SettingsAtuinDetailsHide(),
		m.SettingsAtuinDetailsTitle(),
		m.SettingsAtuinDetailsHint(),
		m.SettingsAtuinDetailsState(m.SettingsAtuinStatusIdle()),
		m.SettingsAtuinDetailsProblem(m.SettingsAtuinProblemOffline()),
		m.SettingsAtuinDetailsServer(status.ServerVersion),
		m.SettingsAtuinDetailsLastSync("2026-11-08 12:00"),
		m.SettingsAtuinDetailsPending("4"),
		m.SettingsAtuinDetailsNextRetry("2026-11-08 12:30"),
	} {
		if !tester.HasText(want) {
			t.Errorf("expanded details is missing %q: %v", want, tester.Texts())
		}
	}
}

// TestAtuinDetailsNeverRenderAnInternalError plants the same secret in the
// internal error and in Status.Detail. Neither may reach the rendered details:
// the control renders only AtuinDiagnostics, which has no error-string field.
func TestAtuinDetailsNeverRenderAnInternalError(t *testing.T) {
	const secret = "sztq-planted-secret-pflmzbwx"
	status := atuinStatus(atuinmodel.KeyStateVerified)
	status.Detail = "server said token=" + secret
	account := &fakeHistoryAccount{
		status:   status,
		loginErr: errors.New("secret store: token=" + secret),
	}
	tester, _, state := atuinPanel(t, account, nil)
	m := locales.In("en")
	*state.Password() = "hunter2"
	if err := tester.Click(m.SettingsAtuinSignIn()); err != nil {
		t.Fatalf("click Sign in: %v", err)
	}
	if err := tester.Click(m.SettingsAtuinDetailsShow()); err != nil {
		t.Fatalf("click Details: %v", err)
	}
	tester.Frame()
	texts := strings.Join(tester.Texts(), "\n")
	for _, leaked := range []string{secret, "hunter2", "token="} {
		if strings.Contains(texts, leaked) {
			t.Fatalf("the details control rendered internal error content")
		}
	}
	if !tester.HasText(m.SettingsAtuinDetailsTitle()) {
		t.Fatalf("the details control did not expand: %v", tester.Texts())
	}
}

// TestAtuinDetailsStringsExistInEveryLocale keeps the details control fully
// translated in every shipped catalog.
func TestAtuinDetailsStringsExistInEveryLocale(t *testing.T) {
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		values := map[string]string{
			"show":       m.SettingsAtuinDetailsShow(),
			"hide":       m.SettingsAtuinDetailsHide(),
			"title":      m.SettingsAtuinDetailsTitle(),
			"hint":       m.SettingsAtuinDetailsHint(),
			"state":      m.SettingsAtuinDetailsState("state"),
			"problem":    m.SettingsAtuinDetailsProblem("problem"),
			"server":     m.SettingsAtuinDetailsServer("1.2.3"),
			"last sync":  m.SettingsAtuinDetailsLastSync("12:00"),
			"pending":    m.SettingsAtuinDetailsPending("1"),
			"next retry": m.SettingsAtuinDetailsNextRetry("12:30"),
		}
		for name, value := range values {
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s: details string %q is empty", locale, name)
				continue
			}
			if name == "state" && !strings.Contains(value, "state") {
				t.Errorf("%s: state string lost its placeholder: %q", locale, value)
			}
			if name == "problem" && !strings.Contains(value, "problem") {
				t.Errorf("%s: problem string lost its placeholder: %q", locale, value)
			}
			if name == "server" && !strings.Contains(value, "1.2.3") {
				t.Errorf("%s: server string lost its version: %q", locale, value)
			}
			if name == "last sync" && !strings.Contains(value, "12:00") {
				t.Errorf("%s: last-sync string lost its time: %q", locale, value)
			}
			if name == "pending" && !strings.Contains(value, "1") {
				t.Errorf("%s: pending string lost its count: %q", locale, value)
			}
			if name == "next retry" && !strings.Contains(value, "12:30") {
				t.Errorf("%s: next-retry string lost its time: %q", locale, value)
			}
		}
	}
}
