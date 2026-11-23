package phoneshell

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// VaultSyncService is the phone Settings page's seam onto the shared Vault
// sync engine. The phone composition root implements it with the same
// appsync.Service the desktop builds, over this phone's Vault and settings
// documents; tests provide a fake. No method exposes a token, a provider
// credential, or the master password.
//
// The paged surface offers the providers a phone can configure on its own:
// WebDAV and S3-compatible storage take an endpoint and credentials typed on
// this page; Google Drive and OneDrive use the system browser and return
// through the app's registered custom URI scheme; and GitHub Gist uses the
// shared RFC 8628 device authorization so the user only enters a one-time code
// in a browser. A portable settings document written here can reach a desktop
// that is configured for the same account.
type VaultSyncService interface {
	Snapshot() appsync.Snapshot
	SetPassword(password string) error
	SetAutoSync(ctx context.Context, enabled bool) error
	ConnectWebDAV(ctx context.Context, password string, settings appsync.WebDAVSettings, confirmInsecure bool) error
	ConnectS3(ctx context.Context, password string, settings appsync.S3Settings, confirmInsecure bool) error
	// StartGitHubConnect switches the profile to GitHub Gist and starts the
	// shared device authorization on its own goroutine. The page follows
	// Snapshot for the one-time user code, the verification address, and the
	// outcome, so the frame that handled the tap never waits on the network.
	// The access token travels only to the system credential store.
	StartGitHubConnect(password string) error
	// StartBrowserConnect switches the profile to Google Drive or OneDrive
	// and starts the shared PKCE authorization in the system browser on its
	// own goroutine. The callback returns through the app's custom URI scheme,
	// so the page shows a waiting window and follows Snapshot for the outcome.
	// The access token travels only to the system credential store.
	StartBrowserConnect(password, provider string) error
	// CancelGitHubConnect aborts a device authorization that is in flight. The
	// engine returns to a retryable failed state with a problem the page can
	// explain.
	CancelGitHubConnect()
	// CancelBrowserConnect aborts a Drive/OneDrive sign-in that is in flight.
	// Cancelling, a mismatched state, and a failed token exchange all return
	// to the same retryable authorization state.
	CancelBrowserConnect()
	SyncNow()
	Disconnect(ctx context.Context) error
}

// vaultSyncDraft is the setup form's unsaved state. Credentials live only in
// these fields while the user types and are cleared after a successful
// connect; nothing here is ever rendered into the status box or a log line.
type vaultSyncDraft struct {
	loaded   bool
	provider string

	endpoint string
	username string
	password string

	region      string
	bucket      string
	prefix      string
	accessKeyID string
	secretKey   string

	master string

	// pendingInsecure holds the exact attempt the user must confirm for a
	// plain-HTTP endpoint. It is cleared by Cancel or by a successful connect,
	// and never persisted.
	pendingInsecure bool
	pendingWebDAV   appsync.WebDAVSettings
	pendingS3       appsync.S3Settings
	pendingMaster   string

	askingDisconnect bool
	problem          string
}

// vaultSync draws the status the shared engine reports and, while the profile
// is unconfigured, the WebDAV or S3 setup form. The page refreshes on its own
// timer so a background automatic sync becomes visible without the user
// leaving it.
func (p *settingsPages) vaultSync(c *ui.Context) {
	service := p.sync
	if service == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			"Vault sync is not available on this device.", nil)
		return
	}
	c.After(1500 * time.Millisecond)

	snapshot := service.Snapshot()
	p.vaultSyncStatus(c, snapshot)
	if p.syncForm.problem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.syncForm.problem, nil)
	}
	if snapshot.Connected {
		p.vaultSyncConnected(c, service, snapshot)
		return
	}
	p.vaultSyncSetup(c, service, snapshot)
}

// vaultSyncStatus renders the engine's state, the actionable problem, the
// provider and the last successful sync. It says explicitly which kinds of
// setting travel, so a phone user can tell a synced theme from a device-local
// interface style.
func (p *settingsPages) vaultSyncStatus(c *ui.Context, snapshot appsync.Snapshot) {
	t := c.Theme()
	tone := t.TextMuted
	if snapshot.Status == appsync.StatusFailed ||
		snapshot.Status == appsync.StatusConflict ||
		(snapshot.Problem != "" && snapshot.Problem != appsync.ProblemNotConfigured) {
		tone = t.Danger
	}
	ui.Column(c).FillWidth().Gap(4).Padding(12, 14).Background(t.Surface).Radius(12).Children(func() {
		ui.Text(c, vaultSyncStateText(snapshot)).FontSize(14).FontWeight(600).TextColor(tone)
		if problem := vaultSyncProblemText(snapshot.Provider, snapshot.Problem); problem != "" {
			ui.Text(c, problem).FontSize(13).TextColor(tone)
		}
		if snapshot.Connected {
			ui.Text(c, "Provider: "+vaultSyncProviderText(snapshot.Provider)).
				FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, "Last sync: "+vaultSyncTimeText(snapshot.LastSyncedAt)).
				FontSize(12).TextColor(t.TextMuted)
		}
		ui.Text(c, "Portable settings and Vault records sync; interface style and app icon stay on this phone.").
			FontSize(12).TextColor(t.TextMuted)
	})
}

// vaultSyncSetup is the first-run form. The provider choice switches which
// endpoint fields are shown; the master password is shared because it encrypts
// the cloud copy for every provider. While GitHub is authorizing, the form is
// replaced by the device code and a Cancel action.
func (p *settingsPages) vaultSyncSetup(c *ui.Context, service VaultSyncService, snapshot appsync.Snapshot) {
	f := &p.syncForm
	if !f.loaded {
		f.provider = appsync.ProviderWebDAV
		f.loaded = true
	}
	if f.provider == appsync.ProviderGitHub && snapshot.Status == appsync.StatusAuthorizing {
		p.vaultSyncDeviceCode(c, service, snapshot)
		return
	}
	if isBrowserSyncProvider(f.provider) && snapshot.Status == appsync.StatusAuthorizing {
		p.vaultSyncBrowserWait(c, service)
		return
	}
	p.choices(c, "Sync provider", []choice{
		pick("GitHub Gist", f.provider == appsync.ProviderGitHub, func() {
			f.provider = appsync.ProviderGitHub
			f.pendingInsecure = false
			f.problem = ""
		}),
		pick("Google Drive", f.provider == appsync.ProviderGoogleDrive, func() {
			f.provider = appsync.ProviderGoogleDrive
			f.pendingInsecure = false
			f.problem = ""
		}),
		pick("OneDrive", f.provider == appsync.ProviderOneDrive, func() {
			f.provider = appsync.ProviderOneDrive
			f.pendingInsecure = false
			f.problem = ""
		}),
		pick("WebDAV", f.provider == appsync.ProviderWebDAV, func() {
			f.provider = appsync.ProviderWebDAV
			f.pendingInsecure = false
			f.problem = ""
		}),
		pick("S3", f.provider == appsync.ProviderS3, func() {
			f.provider = appsync.ProviderS3
			f.pendingInsecure = false
			f.problem = ""
		}),
	})
	// The warning sits above the form: it is the action the failed attempt
	// left the user, and a phone's short viewport would otherwise push it
	// below the fold.
	p.vaultSyncInsecurePrompt(c, service)
	switch f.provider {
	case appsync.ProviderGitHub:
		// GitHub has no endpoint fields. Selecting it and tapping Connect
		// starts the device code; the token is stored by the engine.
		kit.Notice(c, icons.Info, c.Theme().TextMuted,
			"Connect shows a one-time code to enter at github.com. The token stays in this phone's credential store.", nil)
	case appsync.ProviderGoogleDrive, appsync.ProviderOneDrive:
		// The browser opens outside the app; the callback comes back through
		// the registered URL type / intent-filter. No endpoint fields.
		kit.Notice(c, icons.Info, c.Theme().TextMuted,
			"Connect opens the system browser. Finish signing in there, then return to Natcatty; the token stays in this phone's credential store.", nil)
	case appsync.ProviderS3:
		p.vaultSyncS3Form(c)
	default:
		p.vaultSyncWebDAVForm(c)
	}
	kit.FormRow(c, "Master password", func() {
		ui.TextInput(c, &f.master).Password().FillWidth().
			Placeholder("Encrypts the cloud copy")
	})
	if kit.PrimaryButton(c, "", "Connect").Clicked() {
		p.connectVaultSync(service)
	}
}

// vaultSyncDeviceCode shows the one-time code and the address the user opens
// while the shared engine polls GitHub. Only the public user code and the
// verification address are rendered; the device code and any access token
// never reach the page.
func (p *settingsPages) vaultSyncDeviceCode(c *ui.Context, service VaultSyncService, snapshot appsync.Snapshot) {
	code := snapshot.DeviceCode
	if code.UserCode == "" {
		return
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(4).Padding(12, 14).Background(t.Surface).Radius(12).Children(func() {
		ui.Text(c, "Enter this code on GitHub").FontSize(13).TextColor(t.TextMuted)
		ui.Text(c, code.UserCode).FontSize(24).FontWeight(700).TextColor(t.Text)
		if code.VerificationURI != "" {
			ui.Text(c, "Open "+code.VerificationURI+" and enter the code above.").
				FontSize(12).TextColor(t.TextMuted)
		}
	})
	if kit.SecondaryButton(c, "", "Cancel sign-in").Clicked() {
		service.CancelGitHubConnect()
		p.syncForm.problem = ""
	}
}

// vaultSyncBrowserWait replaces the setup form while the system browser is
// open. The waiting window covers switching to the browser and back; the page
// polls Snapshot, so the outcome appears without the user tapping anything.
func (p *settingsPages) vaultSyncBrowserWait(c *ui.Context, service VaultSyncService) {
	t := c.Theme()
	name := vaultSyncProviderText(p.syncForm.provider)
	ui.Column(c).FillWidth().Gap(4).Padding(12, 14).Background(t.Surface).Radius(12).Children(func() {
		ui.Text(c, "Waiting for "+name+" sign-in…").FontSize(14).FontWeight(600).TextColor(t.Text)
		ui.Text(c, "Complete the sign-in in the browser, then return to Natcatty. The connection finishes here on its own.").
			FontSize(13).TextColor(t.TextMuted)
	})
	if kit.SecondaryButton(c, "", "Cancel sign-in").Clicked() {
		service.CancelBrowserConnect()
		p.syncForm.problem = ""
	}
}

func (p *settingsPages) vaultSyncWebDAVForm(c *ui.Context) {
	f := &p.syncForm
	kit.FormRow(c, "Endpoint", func() {
		ui.TextInput(c, &f.endpoint).FillWidth().
			Placeholder("https://server.example/dav/")
	})
	kit.FormRow(c, "Username", func() {
		ui.TextInput(c, &f.username).FillWidth().Placeholder("WebDAV username")
	})
	kit.FormRow(c, "Password", func() {
		ui.TextInput(c, &f.password).Password().FillWidth()
	})
}

func (p *settingsPages) vaultSyncS3Form(c *ui.Context) {
	f := &p.syncForm
	kit.FormRow(c, "Endpoint", func() {
		ui.TextInput(c, &f.endpoint).FillWidth().Placeholder("https://s3.example.com")
	})
	kit.FormRow(c, "Region", func() {
		ui.TextInput(c, &f.region).FillWidth().Placeholder("us-east-1")
	})
	kit.FormRow(c, "Bucket", func() {
		ui.TextInput(c, &f.bucket).FillWidth()
	})
	kit.FormRow(c, "Prefix", func() {
		ui.TextInput(c, &f.prefix).FillWidth().Placeholder("optional/prefix")
	})
	kit.FormRow(c, "Access key", func() {
		ui.TextInput(c, &f.accessKeyID).FillWidth()
	})
	kit.FormRow(c, "Secret key", func() {
		ui.TextInput(c, &f.secretKey).Password().FillWidth()
	})
}

// vaultSyncInsecurePrompt is the explicit confirmation a plain-HTTP endpoint
// needs. The retry reuses the exact settings captured by the failed attempt,
// so a field edit between the attempt and the confirmation cannot send a
// different credential than the one the user saw.
func (p *settingsPages) vaultSyncInsecurePrompt(c *ui.Context, service VaultSyncService) {
	f := &p.syncForm
	if !f.pendingInsecure {
		return
	}
	kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
		"This endpoint is not encrypted. The credential and Vault would travel in clear.",
		func() {
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				if kit.PrimaryButton(c, "", "Connect anyway").Clicked() {
					p.confirmInsecureVaultSync(service)
				}
				if kit.SecondaryButton(c, "", "Cancel").Clicked() {
					f.pendingInsecure = false
					f.problem = ""
				}
			})
		})
}

func (p *settingsPages) connectVaultSync(service VaultSyncService) {
	f := &p.syncForm
	if f.master == "" {
		f.problem = "Enter the master password."
		return
	}
	switch f.provider {
	case appsync.ProviderGitHub:
		// The shared engine keeps the password for this run and polls GitHub.
		// Snapshot reports the code and then the outcome.
		if err := service.StartGitHubConnect(f.master); err != nil {
			f.problem = vaultSyncActionProblem(err)
			return
		}
		f.problem = ""
		return
	case appsync.ProviderGoogleDrive, appsync.ProviderOneDrive:
		// The shared engine keeps the password for this run, opens the system
		// browser, and waits for the custom-scheme callback. Snapshot reports
		// the waiting state and then the outcome.
		if err := service.StartBrowserConnect(f.master, f.provider); err != nil {
			f.problem = vaultSyncActionProblem(err)
			return
		}
		f.problem = ""
		return
	}
	if err := service.SetPassword(f.master); err != nil {
		f.problem = vaultSyncActionProblem(err)
		return
	}
	var err error
	switch f.provider {
	case appsync.ProviderS3:
		settings := appsync.S3Settings{
			Endpoint:        f.endpoint,
			Region:          f.region,
			Bucket:          f.bucket,
			Prefix:          f.prefix,
			AccessKeyID:     f.accessKeyID,
			SecretAccessKey: f.secretKey,
		}
		err = service.ConnectS3(context.Background(), f.master, settings, false)
		if errors.Is(err, appsync.ErrInsecureEndpoint) {
			f.pendingS3 = settings
			f.pendingMaster = f.master
			f.pendingInsecure = true
		}
	default:
		settings := appsync.WebDAVSettings{
			Endpoint: f.endpoint,
			AuthType: domainsync.WebDAVAuthBasic,
			Username: f.username,
			Password: f.password,
		}
		err = service.ConnectWebDAV(context.Background(), f.master, settings, false)
		if errors.Is(err, appsync.ErrInsecureEndpoint) {
			f.pendingWebDAV = settings
			f.pendingMaster = f.master
			f.pendingInsecure = true
		}
	}
	if err != nil {
		f.problem = vaultSyncActionProblem(err)
		return
	}
	f.problem = ""
	f.password = ""
	f.secretKey = ""
	f.accessKeyID = ""
	f.pendingInsecure = false
}

func (p *settingsPages) confirmInsecureVaultSync(service VaultSyncService) {
	f := &p.syncForm
	var err error
	if f.provider == appsync.ProviderS3 {
		err = service.ConnectS3(context.Background(), f.pendingMaster, f.pendingS3, true)
	} else {
		err = service.ConnectWebDAV(context.Background(), f.pendingMaster, f.pendingWebDAV, true)
	}
	f.pendingInsecure = false
	if err != nil {
		f.problem = vaultSyncActionProblem(err)
		return
	}
	f.problem = ""
	f.password = ""
	f.secretKey = ""
	f.accessKeyID = ""
	f.pendingMaster = ""
}

// vaultSyncConnected is the configured surface: the status above, an
// automatic-sync switch, a manual run, and a confirmed disconnect. The master
// password field reappears after a relaunch because the engine keeps it only
// in memory.
func (p *settingsPages) vaultSyncConnected(c *ui.Context, service VaultSyncService, snapshot appsync.Snapshot) {
	f := &p.syncForm
	if !snapshot.HasPassword {
		kit.FormRow(c, "Master password", func() {
			ui.TextInput(c, &f.master).Password().FillWidth().
				Placeholder("Unlock this device")
		})
		if kit.PrimaryButton(c, "", "Save password").Clicked() {
			if err := service.SetPassword(f.master); err != nil {
				f.problem = vaultSyncActionProblem(err)
			} else {
				f.master = ""
				f.problem = ""
			}
		}
	}
	p.toggle(c, "Sync automatically", snapshot.AutoSync, func(on bool) {
		if err := service.SetAutoSync(context.Background(), on); err != nil {
			f.problem = vaultSyncActionProblem(err)
			return
		}
		f.problem = ""
	})
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		if kit.PrimaryButton(c, "", "Sync now").Clicked() {
			service.SyncNow()
			f.problem = ""
		}
		if kit.SecondaryButton(c, "", "Disconnect").Clicked() {
			f.askingDisconnect = true
		}
	})
	if f.askingDisconnect {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"Disconnecting removes the provider credential from this phone. The Vault stays.",
			func() {
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					if kit.PrimaryButton(c, "", "Disconnect now").Clicked() {
						f.askingDisconnect = false
						if err := service.Disconnect(context.Background()); err != nil {
							f.problem = vaultSyncActionProblem(err)
							return
						}
						f.problem = ""
						f.master = ""
					}
					if kit.SecondaryButton(c, "", "Cancel").Clicked() {
						f.askingDisconnect = false
					}
				})
			})
	}
}

// vaultSyncStateText is the localized state sentence. It never embeds a
// provider message or an error string.
func vaultSyncStateText(snapshot appsync.Snapshot) string {
	if !snapshot.Connected {
		if snapshot.Status == appsync.StatusFailed {
			return "The provider connection did not finish."
		}
		return "Vault sync is not configured."
	}
	switch snapshot.Status {
	case appsync.StatusAuthorizing:
		return "Connecting the sync provider…"
	case appsync.StatusSyncing:
		return "Syncing the Vault…"
	case appsync.StatusSynced:
		return "The last sync finished."
	case appsync.StatusFailed:
		return "The last sync did not finish."
	case appsync.StatusNeedsPassword:
		return "Enter the master password to sync."
	case appsync.StatusConflict:
		return "Two devices changed the same records."
	default:
		return "Vault sync is ready."
	}
}

// isBrowserSyncProvider reports a provider whose sign-in leaves the app for
// the system browser. The page shows the waiting window instead of the form
// while one is authorizing.
func isBrowserSyncProvider(provider string) bool {
	switch appsync.NormalizeProvider(provider) {
	case appsync.ProviderGoogleDrive, appsync.ProviderOneDrive:
		return true
	default:
		return false
	}
}

func vaultSyncProviderText(provider string) string {
	switch appsync.NormalizeProvider(provider) {
	case appsync.ProviderS3:
		return "S3-compatible storage"
	case appsync.ProviderWebDAV:
		return "WebDAV"
	case appsync.ProviderGoogleDrive:
		return "Google Drive"
	case appsync.ProviderOneDrive:
		return "OneDrive"
	default:
		return "GitHub"
	}
}

// vaultSyncProblemText is the actionable sentence for a failed connection or
// sync. Provider is used only to name the sign-in that did not finish, so a
// Google Drive state mismatch never reads as a GitHub problem.
func vaultSyncProblemText(provider string, problem appsync.Problem) string {
	switch problem {
	case appsync.ProblemNone:
		return ""
	case appsync.ProblemNotConfigured:
		if isBrowserSyncProvider(provider) {
			return vaultSyncProviderText(provider) + " sign-in is not configured on this build."
		}
		return "Configure a provider and master password to start syncing."
	case appsync.ProblemPasswordRequired:
		return "Enter the master password to sync."
	case appsync.ProblemWrongPassword:
		return "The master password does not decrypt the cloud copy."
	case appsync.ProblemNetwork:
		return "The provider could not be reached."
	case appsync.ProblemCredentials:
		return "The endpoint rejected the stored credentials."
	case appsync.ProblemUnauthorized:
		return "The provider rejected the credential."
	case appsync.ProblemForbidden:
		return "The provider refused access."
	case appsync.ProblemTLS:
		return "The endpoint certificate could not be verified."
	case appsync.ProblemInvalidEndpoint:
		return "Enter a valid endpoint."
	case appsync.ProblemInsecureEndpoint:
		return "The endpoint is not encrypted."
	case appsync.ProblemRedirect:
		return "The endpoint redirected somewhere unsafe."
	case appsync.ProblemWrongRegion:
		return "The bucket lives in a different region."
	case appsync.ProblemRateLimited:
		return "The provider is rate limiting this device."
	case appsync.ProblemAuthorization:
		// Cancel, a mismatched state, a timeout, and a rejected token exchange
		// are one retryable state; naming the provider tells the user which
		// sign-in to repeat.
		return vaultSyncProviderText(provider) + " sign-in did not finish. Try again."
	case appsync.ProblemCorrupt:
		return "The cloud copy is damaged."
	case appsync.ProblemConflict:
		return "Resolve the conflict on a desktop before syncing here."
	case appsync.ProblemEmptyVault:
		return "This Vault has nothing to publish yet."
	case appsync.ProblemLocal:
		return "The Vault could not be read on this phone."
	case appsync.ProblemAlreadyInProgress:
		return "A sync is already running."
	default:
		return "The last sync did not finish."
	}
}

// vaultSyncActionProblem maps only sentinel errors to a sentence. An adapter's
// raw text is never shown.
func vaultSyncActionProblem(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, appsync.ErrInsecureEndpoint):
		return "The endpoint is not encrypted; confirm before connecting."
	case errors.Is(err, appsync.ErrPasswordRequired):
		return "Enter the master password."
	case errors.Is(err, appsync.ErrInvalidEndpoint), errors.Is(err, appsync.ErrCredentialsRejected):
		return "Enter a valid endpoint and credentials."
	case errors.Is(err, appsync.ErrTLS):
		return "The endpoint certificate could not be verified."
	case errors.Is(err, appsync.ErrNetwork):
		return "The provider could not be reached."
	case errors.Is(err, appsync.ErrUnauthorized):
		return "The provider rejected the credential."
	case errors.Is(err, appsync.ErrAuthorizationDenied), errors.Is(err, appsync.ErrDeviceCodeExpired),
		errors.Is(err, appsync.ErrAuthorizationTimeout):
		return "GitHub sign-in did not finish. Try again."
	case errors.Is(err, appsync.ErrForbidden):
		return "The provider refused access."
	case errors.Is(err, appsync.ErrWrongRegion):
		return "The bucket lives in a different region."
	case errors.Is(err, appsync.ErrNotConfigured):
		return "Configure a provider first."
	case errors.Is(err, appsync.ErrAlreadyInProgress):
		return "A sync is already running."
	default:
		return "That action could not be completed."
	}
}

func vaultSyncTimeText(at int64) string {
	if at == 0 {
		return "never"
	}
	return time.UnixMilli(at).Local().Format("2006-01-02 15:04")
}
