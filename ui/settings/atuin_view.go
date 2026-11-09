package settings

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// historySyncSettingsSection renders the device-local sync controls. The
// account identity lives in the Atuin account card, so this card keeps only
// the sync toggle, interval, and host preference. None of these fields
// travels in the portable settings document or the Vault.
func historySyncSettingsSection(c *ui.Context, state *PreferencesState, atuin *AtuinState,
	m locales.Messages, t *ui.Theme) {
	if state == nil {
		return
	}
	section(c, m.SettingsAtuinSectionTitle(), m.SettingsAtuinSectionDescription(), t, func() {
		settings := state.Settings()
		checkboxRow(c, state, m, domain.FieldHistorySyncEnabled, m.SettingsAtuinSyncEnabled(),
			settings.History.SyncEnabled, func(s *domain.Settings, value bool) {
				s.History.SyncEnabled = value
			})
		interval := float64(settings.History.SyncIntervalMinutes)
		fieldRow(c, state, m, t, domain.FieldHistorySyncInterval, m.SettingsAtuinSyncInterval(), func() {
			if ui.NumberInput(c, &interval, float64(domain.MinHistorySyncIntervalMinutes),
				float64(domain.MaxHistorySyncIntervalMinutes), 1).Width(110).Changed() {
				state.Update(func(s *domain.Settings) {
					s.History.SyncIntervalMinutes = int(interval)
				})
				c.Invalidate()
			}
		})
		checkboxRow(c, state, m, domain.FieldHistoryPreferHostAtuin,
			m.SettingsAtuinPreferHostAtuin(), settings.History.PreferHostAtuin,
			func(s *domain.Settings, value bool) { s.History.PreferHostAtuin = value })
		ui.Text(c, m.SettingsAtuinPreferHostAtuinHint()).FontSize(11).TextColor(t.TextMuted)
		if atuin != nil {
			atuin.SyncHistory(state.Settings().History)
		}
	})
}

// atuinIdentityFields is the one account's identity at the top of the
// account card. The sign-in button reads these same persisted fields, so the
// address and username a user sees here are the ones the service validates.
func atuinIdentityFields(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	if state == nil {
		return
	}
	fieldRow(c, state, m, t, domain.FieldHistorySyncAddress, m.SettingsAtuinAddress(), func() {
		if ui.TextInput(c, state.Field(domain.FieldHistorySyncAddress)).Width(320).Changed() {
			state.Update(func(s *domain.Settings) {
				s.History.SyncAddress = state.FieldValue(domain.FieldHistorySyncAddress)
			})
			c.Invalidate()
		}
	})
	ui.Text(c, m.SettingsAtuinAddressHint()).FontSize(11).TextColor(t.TextMuted)
	fieldRow(c, state, m, t, domain.FieldHistoryUsername, m.SettingsAtuinUsername(), func() {
		if ui.TextInput(c, state.Field(domain.FieldHistoryUsername)).Width(240).Changed() {
			state.Update(func(s *domain.Settings) {
				s.History.Username = state.FieldValue(domain.FieldHistoryUsername)
			})
			c.Invalidate()
		}
	})
}

// atuinAccountSection is the one-account panel: identity (server address and
// username), status, sign-in, master-key entry and validation, explicit
// warnings, and logout. It never displays a token or a master key.
func atuinAccountSection(c *ui.Context, state *AtuinState, prefs *PreferencesState,
	m locales.Messages, t *ui.Theme) {
	if state == nil {
		return
	}
	status := state.Status()
	section(c, m.SettingsAtuinAccountTitle(), m.SettingsAtuinAccountDescription(), t, func() {
		atuinIdentityFields(c, prefs, m, t)
		atuinStatusRow(c, state, m, t)
		if problem := state.Problem(); problem != AtuinProblemNone {
			ui.Text(c, atuinProblemText(m, problem)).FontSize(12).TextColor(uitheme.DangerText(t))
		}
		atuinDetailsControl(c, state, m, t)
		if status.KeyFingerprint != "" {
			ui.Text(c, m.SettingsAtuinKeyFingerprint(status.KeyFingerprint)).
				FontSize(12).TextColor(t.TextMuted)
		}

		atuinLoginControls(c, state, prefs, m, t)
		atuinKeyControls(c, state, m, t)
		atuinSessionControls(c, state, m, t)
	})
}

func atuinStatusRow(c *ui.Context, state *AtuinState, m locales.Messages, t *ui.Theme) {
	status := state.Status()
	text := atuinStatusText(m, status)
	if status.ServerVersion != "" {
		text += " · " + m.SettingsAtuinServerVersion(status.ServerVersion)
	}
	ui.Text(c, text).FontSize(13).FontWeight(500)
	if status.AccountID != "" {
		ui.Text(c, m.SettingsAtuinAccountID(status.AccountID)).FontSize(11).TextColor(t.TextMuted)
	}
	if !status.LastSyncAt.IsZero() {
		ui.Text(c, m.SettingsAtuinLastSync(atuinSyncTime(status.LastSyncAt))).
			FontSize(11).TextColor(t.TextMuted)
	}
	if status.PendingUploads > 0 {
		ui.Text(c, m.SettingsAtuinPendingUploads(strconv.Itoa(status.PendingUploads))).
			FontSize(11).TextColor(t.TextMuted)
	}
	if !status.NextRetryAt.IsZero() {
		ui.Text(c, m.SettingsAtuinNextRetry(atuinSyncTime(status.NextRetryAt))).
			FontSize(11).TextColor(uitheme.WarningText(t))
	}
}

// atuinDetailsControl is the explicit details surface. It is collapsed by
// default and renders only the typed sanitized diagnostics model: a localized
// problem explanation, the sync state, and non-sensitive status fields. No raw
// error, server message or body, Status.Detail, URL, username, token, key, or
// command text has a path into this render.
func atuinDetailsControl(c *ui.Context, state *AtuinState, m locales.Messages, t *ui.Theme) {
	label := m.SettingsAtuinDetailsShow()
	if state.ShowDetails() {
		label = m.SettingsAtuinDetailsHide()
	}
	if ui.Button(c, label).Clicked() {
		state.ToggleDetails()
		c.Invalidate()
	}
	if !state.ShowDetails() {
		return
	}
	diagnostics := state.Diagnostics()
	ui.Column(c).FillWidth().Padding(10).Gap(4).Radius(6).Background(t.Surface).Children(func() {
		ui.Text(c, m.SettingsAtuinDetailsTitle()).FontSize(12).Bold()
		ui.Text(c, m.SettingsAtuinDetailsHint()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, m.SettingsAtuinDetailsState(atuinStatusText(m,
			historysync.Status{State: diagnostics.State}))).FontSize(11)
		if diagnostics.Problem != AtuinProblemNone {
			ui.Text(c, m.SettingsAtuinDetailsProblem(
				atuinProblemText(m, diagnostics.Problem))).FontSize(11)
		}
		if diagnostics.ServerVersion != "" {
			ui.Text(c, m.SettingsAtuinDetailsServer(diagnostics.ServerVersion)).FontSize(11)
		}
		if !diagnostics.LastSyncAt.IsZero() {
			ui.Text(c, m.SettingsAtuinDetailsLastSync(
				atuinSyncTime(diagnostics.LastSyncAt))).FontSize(11)
		}
		ui.Text(c, m.SettingsAtuinDetailsPending(
			strconv.Itoa(diagnostics.PendingUploads))).FontSize(11)
		if !diagnostics.NextRetryAt.IsZero() {
			ui.Text(c, m.SettingsAtuinDetailsNextRetry(
				atuinSyncTime(diagnostics.NextRetryAt))).FontSize(11)
		}
	})
}

// atuinSyncTime renders a sync timestamp in the user's local zone, matching
// the other device-local settings timestamps.
func atuinSyncTime(at time.Time) string {
	return at.Local().Format("2006-01-02 15:04")
}

func atuinLoginControls(c *ui.Context, state *AtuinState, prefs *PreferencesState,
	m locales.Messages, t *ui.Theme) {
	status := state.Status()
	methods := []AtuinLoginMethod{AtuinLoginPassword, AtuinLoginToken}
	labels := []string{m.SettingsAtuinLoginPassword(), m.SettingsAtuinLoginToken()}
	method := labels[indexOfLoginMethods(methods, state.LoginMethod())]
	ui.Field(c, m.SettingsAtuinLoginMethod(), func() {
		if ui.Select(c, &method, labels).Width(240).Changed() {
			state.SetLoginMethod(methods[indexOf(labels, method)])
			c.Invalidate()
		}
	})
	if state.LoginMethod() == AtuinLoginToken {
		ui.Field(c, m.SettingsAtuinToken(), func() {
			ui.TextInput(c, state.Token()).Password().Width(320)
		})
		if prefs != nil {
			authModes := []string{string(atuinmodel.AuthModeSession), string(atuinmodel.AuthModeBearer)}
			authLabels := []string{m.SettingsAtuinAuthModeSession(), m.SettingsAtuinAuthModeBearer()}
			authMode := authLabels[indexOf(authModes, string(prefs.Settings().History.AuthMode))]
			fieldRow(c, prefs, m, t, domain.FieldHistoryAuthMode, m.SettingsAtuinAuthMode(), func() {
				if ui.Select(c, &authMode, authLabels).Width(200).Changed() {
					prefs.Update(func(settings *domain.Settings) {
						settings.History.AuthMode = atuinmodel.AuthMode(authModes[indexOf(authLabels, authMode)])
					})
					c.Invalidate()
				}
			})
			ui.Text(c, m.SettingsAtuinAuthModeHint()).FontSize(11).TextColor(t.TextMuted)
		}
	} else {
		ui.Field(c, m.SettingsAtuinPassword(), func() {
			ui.TextInput(c, state.Password()).Password().Width(320)
		})
	}
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.SettingsAtuinSignIn()).Clicked() {
			address, username := "", ""
			authMode := atuinmodel.AuthModeSession
			if prefs != nil {
				history := prefs.Settings().History
				address, username = history.SyncAddress, history.Username
				authMode = history.AuthMode
			}
			state.Login(address, username, authMode)
			c.Invalidate()
		}
		if status.Configured {
			if ui.Button(c, m.SettingsAtuinCheck()).Clicked() {
				state.Check()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsAtuinSyncNow()).Clicked() {
				state.SyncNow()
				c.Invalidate()
			}
		}
		if !status.Configured {
			ui.Text(c, m.SettingsAtuinNotSignedIn()).FontSize(11).TextColor(t.TextMuted)
		}
	})
}

func atuinKeyControls(c *ui.Context, state *AtuinState, m locales.Messages, t *ui.Theme) {
	status := state.Status()
	ui.Text(c, m.SettingsAtuinKeyTitle()).FontSize(15).Bold()
	ui.Text(c, m.SettingsAtuinKeyDescription()).FontSize(12).TextColor(t.TextMuted)
	ui.Field(c, m.SettingsAtuinMasterKey(), func() {
		ui.TextInput(c, state.MasterKey()).Password().Width(380)
	})
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c, m.SettingsAtuinSaveKey()).Clicked() {
			state.SaveMasterKey()
			c.Invalidate()
		}
		if status.KeyFingerprint != "" || status.KeyState != atuinmodel.KeyStateNone {
			if ui.Button(c, m.SettingsAtuinForgetKey()).Clicked() {
				state.AskForget()
				c.Invalidate()
			}
		}
	})

	if state.AskingKeyChange() {
		atuinWarning(c, m.SettingsAtuinKeyChangeTitle(), m.SettingsAtuinKeyChangeBody(), t, func() {
			if ui.PrimaryButton(c, m.SettingsAtuinReplaceKey()).Clicked() {
				state.ConfirmKeyChange()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsCancel()).Clicked() {
				state.CancelKeyChange()
				c.Invalidate()
			}
		})
	}
	if state.AskingForget() {
		atuinWarning(c, m.SettingsAtuinForgetKeyTitle(), m.SettingsAtuinForgetKeyBody(), t, func() {
			if ui.PrimaryButton(c, m.SettingsAtuinForgetKeyConfirm()).Clicked() {
				state.ConfirmForget()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsCancel()).Clicked() {
				state.CancelForget()
				c.Invalidate()
			}
		})
	}

	switch status.KeyState {
	case atuinmodel.KeyStateUnverified:
		if status.KeyConfirmed {
			ui.Text(c, m.SettingsAtuinKeyConfirmed()).FontSize(12).TextColor(uitheme.WarningText(t))
		} else {
			atuinWarning(c, m.SettingsAtuinKeyUnverifiedTitle(), m.SettingsAtuinKeyUnverifiedBody(),
				t, func() {
					if ui.Button(c, m.SettingsAtuinConfirmUnverified()).Clicked() {
						state.ConfirmUnverified()
						c.Invalidate()
					}
				})
		}
	case atuinmodel.KeyStateVerified:
		ui.Text(c, m.SettingsAtuinKeyVerified()).FontSize(12).TextColor(t.TextMuted)
	case atuinmodel.KeyStateMismatch:
		ui.Text(c, m.SettingsAtuinProblemKeyMismatch()).FontSize(12).TextColor(uitheme.DangerText(t))
	case atuinmodel.KeyStateMissing:
		atuinWarning(c, m.SettingsAtuinKeyMissingTitle(), m.SettingsAtuinKeyMissingBody(), t, nil)
	case atuinmodel.KeyStateNone:
		ui.Text(c, m.SettingsAtuinNoKey()).FontSize(12).TextColor(t.TextMuted)
	}
}

func atuinSessionControls(c *ui.Context, state *AtuinState, m locales.Messages, t *ui.Theme) {
	status := state.Status()
	if status.Configured {
		if ui.Button(c, m.SettingsAtuinLogout()).Clicked() {
			state.AskLogout()
			c.Invalidate()
		}
	}
	if state.AskingLogout() {
		atuinWarning(c, m.SettingsAtuinLogoutTitle(), m.SettingsAtuinLogoutBody(), t, func() {
			if ui.PrimaryButton(c, m.SettingsAtuinLogoutConfirm()).Clicked() {
				state.ConfirmLogout()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsCancel()).Clicked() {
				state.CancelLogout()
				c.Invalidate()
			}
		})
	}
}

func atuinWarning(c *ui.Context, title, body string, t *ui.Theme, actions func()) {
	ui.Column(c).FillWidth().Padding(12).Gap(6).Radius(6).Background(t.Surface).
		Border(1, t.Warning).Children(func() {
		ui.Text(c, title).FontSize(13).Bold().TextColor(uitheme.WarningText(t))
		ui.Text(c, body).FontSize(12).TextColor(t.TextMuted)
		if actions != nil {
			ui.Row(c).Gap(8).Children(actions)
		}
	})
}

func atuinStatusText(m locales.Messages, status historysync.Status) string {
	switch status.State {
	case atuinmodel.SyncDisabled:
		return m.SettingsAtuinStatusDisabled()
	case atuinmodel.SyncNotConfigured:
		return m.SettingsAtuinStatusNotConfigured()
	case atuinmodel.SyncLocked:
		return m.SettingsAtuinStatusLocked()
	case atuinmodel.SyncIdle:
		return m.SettingsAtuinStatusIdle()
	case atuinmodel.SyncSyncing:
		return m.SettingsAtuinStatusSyncing()
	case atuinmodel.SyncOffline:
		return m.SettingsAtuinStatusOffline()
	case atuinmodel.SyncAuthExpired:
		return m.SettingsAtuinStatusAuthExpired()
	case atuinmodel.SyncKeyMismatch:
		return m.SettingsAtuinStatusKeyMismatch()
	case atuinmodel.SyncIncompatibleServer:
		return m.SettingsAtuinStatusIncompatible()
	default:
		return m.SettingsAtuinStatusError()
	}
}

func atuinProblemText(m locales.Messages, problem AtuinProblem) string {
	switch problem {
	case AtuinProblemInvalidAddress:
		return m.SettingsAtuinProblemInvalidAddress()
	case AtuinProblemInvalidLogin:
		return m.SettingsAtuinProblemInvalidLogin()
	case AtuinProblemAuthFailed:
		return m.SettingsAtuinProblemAuthFailed()
	case AtuinProblemKeyInvalid:
		return m.SettingsAtuinProblemKeyInvalid()
	case AtuinProblemKeyMismatch:
		return m.SettingsAtuinProblemKeyMismatch()
	case AtuinProblemKeyUnverified:
		return m.SettingsAtuinProblemKeyUnverified()
	case AtuinProblemAccountChange:
		return m.SettingsAtuinProblemAccountChange()
	case AtuinProblemNotConfigured:
		return m.SettingsAtuinProblemNotConfigured()
	case AtuinProblemLocked:
		return m.SettingsAtuinProblemLocked()
	case AtuinProblemSecretsUnavailable:
		return m.SettingsAtuinProblemSecretsUnavailable()
	case AtuinProblemIncompatible:
		return m.SettingsAtuinProblemIncompatible()
	case AtuinProblemOffline:
		return m.SettingsAtuinProblemOffline()
	case AtuinProblemRemote:
		return m.SettingsAtuinProblemRemote()
	case AtuinProblemSave:
		return m.SettingsAtuinProblemSave()
	default:
		return ""
	}
}

func indexOfLoginMethods(values []AtuinLoginMethod, value AtuinLoginMethod) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}
