package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// HistorySyncService is the phone Settings page's seam onto Command History
// sync. The phone composition root implements it with the same account and
// sync services the desktop uses; tests provide a fake. No method returns a
// token or a master key.
type HistorySyncService interface {
	Status() historysync.Status
	HistorySettings() domainpreferences.History
	UpdateHistorySettings(func(*domainpreferences.History)) error
	Login(context.Context, historysync.LoginRequest) (historysync.Status, error)
	SetMasterKey(context.Context, string, bool) (historysync.Status, error)
	ConfirmUnverifiedKey(context.Context) (historysync.Status, error)
	Logout(context.Context) (historysync.Status, error)
	Check(context.Context) (historysync.Status, error)
	SyncNow()
}

// historyDraft is the page's unsaved form state. Credentials live only in
// these fields while the user types and are cleared after a successful action;
// nothing here is ever rendered after it is submitted.
type historyDraft struct {
	loaded   bool
	server   string
	username string
	password string
	token    string
	master   string

	tokenMethod  bool
	authMode     atuinmodel.AuthMode
	askingKey    bool
	askingLogout bool
	problem      string
}

// historySync draws the account and sync status, the device-local account
// configuration, and the sign-in and master-key forms. The page refreshes on
// its own timer, so a background failure appears without the user leaving the
// Session they are in: the engine owns the retry and the terminal never sees
// the error.
func (p *settingsPages) historySync(c *ui.Context) {
	if p.service == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			"Settings are not available on this device.", nil)
		return
	}
	service := p.history
	if service == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			"History sync is not available on this device.", nil)
		return
	}
	if !p.historyForm.loaded {
		settings := service.HistorySettings()
		p.historyForm.server = settings.SyncAddress
		p.historyForm.username = settings.Username
		p.historyForm.tokenMethod = settings.AuthMode == atuinmodel.AuthModeBearer
		p.historyForm.authMode = atuinmodel.NormalizeAuthMode(settings.AuthMode)
		p.historyForm.loaded = true
	}
	// Keep the frame coming while the page is open so background sync
	// progress and failures become visible without a user action.
	c.After(1500 * time.Millisecond)

	status := service.Status()
	p.historyStatus(c, status)
	if p.historyForm.problem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.historyForm.problem, nil)
	}

	p.toggle(c, "Sync history", service.HistorySettings().SyncEnabled, func(on bool) {
		p.updateHistory(service, func(settings *domainpreferences.History) {
			settings.SyncEnabled = on
		}, "Could not save the history sync setting.")
	})
	if service.HistorySettings().SyncEnabled {
		p.historyAccountForm(c, service, status)
	}
}

func (p *settingsPages) historyStatus(c *ui.Context, status historysync.Status) {
	t := c.Theme()
	tone := t.TextMuted
	if status.State == atuinmodel.SyncError ||
		status.State == atuinmodel.SyncAuthExpired ||
		status.State == atuinmodel.SyncKeyMismatch ||
		status.State == atuinmodel.SyncIncompatibleServer ||
		status.State == atuinmodel.SyncOffline ||
		status.Problem != atuinmodel.ProblemNone && status.Problem != atuinmodel.ProblemDisabled {
		tone = t.Danger
	}
	ui.Column(c).FillWidth().Gap(4).Padding(12, 14).Background(t.Surface).Radius(12).Children(func() {
		ui.Text(c, historyStateText(status.State)).FontSize(14).FontWeight(600).TextColor(tone)
		if problem := historyProblemText(status.Problem); problem != "" {
			ui.Text(c, problem).FontSize(13).TextColor(tone)
		}
		if status.Configured {
			ui.Text(c, "Account: "+status.Username).FontSize(12).TextColor(t.TextMuted)
		}
		ui.Text(c, "Last sync: "+historyTimeText(status.LastSyncAt)).
			FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, fmt.Sprintf("Pending uploads: %d", status.PendingUploads)).
			FontSize(12).TextColor(t.TextMuted)
		if !status.NextRetryAt.IsZero() {
			ui.Text(c, "Next retry: "+historyTimeText(status.NextRetryAt)).
				FontSize(12).TextColor(t.TextMuted)
		}
	})
}

func (p *settingsPages) historyAccountForm(c *ui.Context, service HistorySyncService,
	status historysync.Status) {
	if !status.Configured {
		p.historyLogin(c, service)
	}
	if status.Configured && status.KeyState == atuinmodel.KeyStateUnverified {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"This account has no records yet, so the master key cannot be checked.",
			func() {
				if kit.PrimaryButton(c, "", "Confirm key").Clicked() {
					if _, err := service.ConfirmUnverifiedKey(context.Background()); err != nil {
						p.historyForm.problem = historyActionProblem(err)
					} else {
						p.historyForm.problem = ""
					}
				}
			})
	}
	if status.Configured {
		p.historyMasterKey(c, service, status)
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if kit.PrimaryButton(c, "", "Sync now").Clicked() {
				service.SyncNow()
				p.historyForm.problem = ""
			}
			if kit.SecondaryButton(c, "", "Refresh status").Clicked() {
				if _, err := service.Check(context.Background()); err != nil {
					p.historyForm.problem = historyActionProblem(err)
				} else {
					p.historyForm.problem = ""
				}
			}
			if kit.SecondaryButton(c, "", "Log out").Clicked() {
				p.historyForm.askingLogout = true
			}
		})
		if p.historyForm.askingLogout {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
				"Logging out removes the token and master key from this phone. Local history stays.",
				nil)
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				if kit.PrimaryButton(c, "", "Log out now").Clicked() {
					p.historyForm.askingLogout = false
					if _, err := service.Logout(context.Background()); err != nil {
						p.historyForm.problem = historyActionProblem(err)
					} else {
						p.historyForm.problem = ""
						p.clearHistoryCredentials()
					}
				}
				if kit.SecondaryButton(c, "", "Cancel").Clicked() {
					p.historyForm.askingLogout = false
				}
			})
		}
	}
}

func (p *settingsPages) historyLogin(c *ui.Context, service HistorySyncService) {
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		var password ui.Element
		if p.historyForm.tokenMethod {
			password = kit.SecondaryButton(c, "", "Password")
		} else {
			password = kit.PrimaryButton(c, "", "Password")
		}
		if password.Clicked() && p.historyForm.tokenMethod {
			p.historyForm.tokenMethod = false
			p.historyForm.token = ""
		}
		var token ui.Element
		if p.historyForm.tokenMethod {
			token = kit.PrimaryButton(c, "", "Token")
		} else {
			token = kit.SecondaryButton(c, "", "Token")
		}
		if token.Clicked() && !p.historyForm.tokenMethod {
			p.historyForm.tokenMethod = true
			p.historyForm.password = ""
		}
	})
	kit.FormRow(c, "Server", func() {
		ui.TextInput(c, &p.historyForm.server).Placeholder("https://api.atuin.sh").FillWidth()
	})
	kit.FormRow(c, "Username", func() {
		ui.TextInput(c, &p.historyForm.username).Placeholder("Atuin username").FillWidth()
	})
	if p.historyForm.tokenMethod {
		p.choices(c, "Token type", []choice{
			pick("Session", p.historyForm.authMode == atuinmodel.AuthModeSession, func() {
				p.historyForm.authMode = atuinmodel.AuthModeSession
			}),
			pick("Bearer", p.historyForm.authMode == atuinmodel.AuthModeBearer, func() {
				p.historyForm.authMode = atuinmodel.AuthModeBearer
			}),
		})
		kit.FormRow(c, "Session token", func() {
			ui.TextInput(c, &p.historyForm.token).Password().FillWidth()
		})
	} else {
		kit.FormRow(c, "Password", func() {
			ui.TextInput(c, &p.historyForm.password).Password().FillWidth()
		})
	}
	if kit.PrimaryButton(c, "", "Sign in").Clicked() {
		p.signIn(service)
	}
}

func (p *settingsPages) historyMasterKey(c *ui.Context, service HistorySyncService,
	status historysync.Status) {
	if !p.historyForm.askingKey && status.KeyState == atuinmodel.KeyStateVerified {
		return
	}
	kit.FormRow(c, "Master key", func() {
		ui.TextInput(c, &p.historyForm.master).Password().FillWidth().
			Placeholder("Atuin master key")
	})
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		var save ui.Element
		if p.historyForm.askingKey {
			save = kit.PrimaryButton(c, "", "Replace key")
		} else {
			save = kit.PrimaryButton(c, "", "Save master key")
		}
		if save.Clicked() {
			confirm := p.historyForm.askingKey
			_, err := service.SetMasterKey(context.Background(), p.historyForm.master, confirm)
			switch {
			case errors.Is(err, historysync.ErrKeyChangeConfirmation):
				p.historyForm.askingKey = true
				p.historyForm.problem = "A different key is already stored. Replace it?"
			case err != nil:
				p.historyForm.problem = historyActionProblem(err)
			default:
				p.historyForm.master = ""
				p.historyForm.askingKey = false
				p.historyForm.problem = ""
			}
		}
		if p.historyForm.askingKey && kit.SecondaryButton(c, "", "Cancel").Clicked() {
			p.historyForm.askingKey = false
			p.historyForm.problem = ""
		}
	})
}

func (p *settingsPages) signIn(service HistorySyncService) {
	request := historysync.LoginRequest{
		ServerURL: p.historyForm.server,
		Username:  p.historyForm.username,
	}
	if p.historyForm.tokenMethod {
		request.Token = p.historyForm.token
		request.AuthMode = p.historyForm.authMode
	} else {
		request.Password = p.historyForm.password
		request.AuthMode = atuinmodel.AuthModeSession
	}
	// Persist the address and username before signing in. The service reads
	// them from this same document, so what the form shows is what is used.
	p.updateHistory(service, func(settings *domainpreferences.History) {
		settings.SyncAddress = p.historyForm.server
		settings.Username = p.historyForm.username
		if p.historyForm.tokenMethod {
			settings.AuthMode = p.historyForm.authMode
		} else {
			settings.AuthMode = atuinmodel.AuthModeSession
		}
	}, "Could not save the account settings.")
	if p.historyForm.problem != "" {
		return
	}
	if _, err := service.Login(context.Background(), request); err != nil {
		p.historyForm.problem = historyActionProblem(err)
		return
	}
	p.historyForm.password = ""
	p.historyForm.token = ""
	p.historyForm.problem = ""
}

func (p *settingsPages) updateHistory(service HistorySyncService,
	change func(*domainpreferences.History), failure string) {
	if err := service.UpdateHistorySettings(change); err != nil {
		p.historyForm.problem = failure
		return
	}
	p.historyForm.problem = ""
}

func (p *settingsPages) clearHistoryCredentials() {
	p.historyForm.password = ""
	p.historyForm.token = ""
	p.historyForm.master = ""
	p.historyForm.askingKey = false
}

// historyStateText is the localized state sentence. It never contains a
// server message or an error string.
func historyStateText(state atuinmodel.SyncState) string {
	switch state {
	case atuinmodel.SyncDisabled:
		return "History sync is off."
	case atuinmodel.SyncNotConfigured:
		return "No Atuin account is configured."
	case atuinmodel.SyncLocked:
		return "Sync is paused while this device is locked."
	case atuinmodel.SyncIdle:
		return "Account ready."
	case atuinmodel.SyncSyncing:
		return "Syncing history…"
	case atuinmodel.SyncOffline:
		return "The Atuin server is unreachable; retrying."
	case atuinmodel.SyncAuthExpired:
		return "Sign in again to keep syncing."
	case atuinmodel.SyncKeyMismatch:
		return "The master key does not match this account."
	case atuinmodel.SyncIncompatibleServer:
		return "This Atuin server version is not supported."
	case atuinmodel.SyncError:
		return "Sync failed; retrying in the background."
	default:
		return "History sync is idle."
	}
}

func historyProblemText(problem atuinmodel.Problem) string {
	switch problem {
	case atuinmodel.ProblemNone, atuinmodel.ProblemDisabled:
		return ""
	case atuinmodel.ProblemNotConfigured:
		return "Sign in and save the master key to start syncing."
	case atuinmodel.ProblemLocked:
		return "The credential store is locked."
	case atuinmodel.ProblemAuthExpired:
		return "The session expired. Sign in again."
	case atuinmodel.ProblemKeyMismatch:
		return "The master key cannot decrypt this account's records."
	case atuinmodel.ProblemKeyMissing:
		return "Save the account's master key."
	case atuinmodel.ProblemKeyUnverified:
		return "Confirm the master key for this empty account."
	case atuinmodel.ProblemIncompatibleServer:
		return "The server is newer than this app supports."
	case atuinmodel.ProblemOffline:
		return "The server could not be reached."
	default:
		return "The last sync did not finish."
	}
}

// historyActionProblem maps only local sentinel errors to a sentence. Unknown
// errors become a generic action failure, never the underlying text.
func historyActionProblem(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, historysync.ErrInvalidSyncAddress):
		return "Enter an https server address (http is allowed only for localhost)."
	case errors.Is(err, historysync.ErrInvalidLogin):
		return "Enter a username and password, or a token."
	case errors.Is(err, historysync.ErrAuthFailed), errors.Is(err, historysync.ErrAuthExpired):
		return "The server rejected these credentials."
	case errors.Is(err, historysync.ErrKeyMismatch):
		return "The master key does not match this account."
	case errors.Is(err, historysync.ErrKeyValidationFailed):
		return "The server could not validate the master key."
	case errors.Is(err, historysync.ErrAccountChangeRequiresLogout):
		return "Log out before connecting to a different server."
	case errors.Is(err, historysync.ErrNotConfigured):
		return "The account is not configured yet."
	case errors.Is(err, historysync.ErrSecretsLocked), errors.Is(err, historysync.ErrAppLocked):
		return "The credential store is locked."
	case errors.Is(err, historysync.ErrSecretsUnavailable), errors.Is(err, historysync.ErrSecretUnreadable):
		return "Credentials are not available on this device."
	case errors.Is(err, historysync.ErrIncompatibleServer):
		return "This Atuin server version is not supported."
	case errors.Is(err, historysync.ErrOffline):
		return "The server could not be reached. The Session keeps running."
	case errors.Is(err, historysync.ErrRemoteUnavailable), errors.Is(err, historysync.ErrRemote):
		return "The Atuin server returned an error. The Session keeps running."
	default:
		return "That action could not be completed."
	}
}

func historyTimeText(at time.Time) string {
	if at.IsZero() {
		return "never"
	}
	return at.Local().Format("2006-01-02 15:04")
}
