// Package vault renders the Vault surface: the Host list, the Host editor,
// and the delete confirmation. It resolves localized text; the state package
// holds behavior and never contains user-visible strings.
package vault

import (
	"net"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// View returns the Vault surface.
func View(s *vaultstate.State) func(*ui.Context) {
	return func(c *ui.Context) {
		// Test connection attempts complete off the UI thread.
		s.FlushTests()

		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Column(c).Fill().Grow(1).Gap(12).Children(func() {
			switch {
			case s.Editing():
				editor(c, s, m, t)
			case s.EditingKey():
				keyEditor(c, s, m, t)
			case s.EditingIdentity():
				identityEditor(c, s, m, t)
			default:
				list(c, s, m, t)
			}
		})
	}
}

func list(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Children(func() {
		ui.Text(c, m.VaultHostListTitle(s.HostCount())).FontSize(20).Bold()
		ui.Column(c).Grow(1)
		if ui.PrimaryButton(c, m.VaultHostNew()).Clicked() {
			s.BeginCreate()
			c.Invalidate()
		}
	})

	problem(c, s, t)
	secretBanner(c, s, m, t)

	if pending := s.PendingDelete(); pending != "" {
		message := m.VaultHostDeleteConfirmUnknown()
		for _, host := range s.Items() {
			if host.ID == pending {
				message = m.VaultHostDeleteConfirm(host.Label)
				break
			}
		}
		ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
			ui.Text(c, message).Grow(1).TextColor(t.Danger)
			if ui.Button(c, m.VaultHostCancel()).Clicked() {
				s.CancelDelete()
				c.Invalidate()
			}
			if ui.Button(c, m.VaultHostDelete()).Clicked() {
				_ = s.ConfirmDelete()
				c.Invalidate()
			}
		})
	}

	keyDeleteBanner(c, s, m, t)
	identityDeleteBanner(c, s, m, t)

	ui.Scroll(c).Grow(1).Children(func() {
		if s.HostCount() == 0 {
			ui.Text(c, m.VaultHostEmpty()).TextColor(t.TextMuted)
		}
		for _, host := range s.Items() {
			hostRow(c, s, host, m, t)
		}
		keySection(c, s, m, t)
		identitySection(c, s, m, t)
	})
}

func keySection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultKeyListTitle(s.KeyCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultKeyNew()).Clicked() {
			s.BeginCreateKey()
			c.Invalidate()
		}
	})
	if s.KeyCount() == 0 {
		ui.Text(c, m.VaultKeyEmpty()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, key := range s.Keys() {
		keyRow(c, s, key, m, t)
	}
}

func keyRow(c *ui.Context, s *vaultstate.State, key domainvault.Key, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, key.Label).Bold()
			ui.Text(c, keySummary(key, m)).FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditKey(key.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteKey(key.ID)
			c.Invalidate()
		}
	})
}

func keySummary(key domainvault.Key, m locales.Messages) string {
	parts := make([]string, 0, 4)
	if key.Type != "" {
		parts = append(parts, string(key.Type))
	}
	if key.HasPrivateKey {
		parts = append(parts, m.VaultKeyStored())
	}
	if key.HasPassphrase {
		parts = append(parts, m.VaultKeyPassphraseStored())
	}
	if key.Certificate != "" {
		parts = append(parts, m.VaultKeyCertificateStored())
	}
	return strings.Join(parts, " · ")
}

func keyDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteKey() == "" {
		return
	}
	label := ""
	for _, key := range s.Keys() {
		if key.ID == s.PendingDeleteKey() {
			label = key.Label
			break
		}
	}
	message := m.VaultDeleteConfirmUnknown()
	if label != "" {
		message = m.VaultDeleteConfirm(label)
	}
	ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, message).Grow(1).TextColor(t.Danger)
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteKey()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteKey()
			c.Invalidate()
		}
	})
}

func identitySection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultIdentityListTitle(s.IdentityCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultIdentityNew()).Clicked() {
			s.BeginCreateIdentity()
			c.Invalidate()
		}
	})
	if s.IdentityCount() == 0 {
		ui.Text(c, m.VaultIdentityEmpty()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, identity := range s.Identities() {
		identityRow(c, s, identity, m, t)
	}
}

func identityRow(c *ui.Context, s *vaultstate.State, identity domainvault.Identity, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, identity.Label).Bold()
			ui.Text(c, identitySummary(identity, m)).FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditIdentity(identity.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteIdentity(identity.ID)
			c.Invalidate()
		}
	})
}

func identitySummary(identity domainvault.Identity, m locales.Messages) string {
	parts := make([]string, 0, 3)
	if identity.Username != "" {
		parts = append(parts, identity.Username)
	}
	parts = append(parts, identityMethodLabel(identity.AuthMethod, m))
	if identity.HasPassword {
		parts = append(parts, m.VaultHostPasswordStored())
	}
	return strings.Join(parts, " · ")
}

func identityMethodLabel(method domainvault.IdentityAuthMethod, m locales.Messages) string {
	switch method {
	case domainvault.IdentityAuthKey:
		return m.VaultIdentityMethodKey()
	case domainvault.IdentityAuthCertificate:
		return m.VaultIdentityMethodCertificate()
	default:
		return m.VaultIdentityMethodPassword()
	}
}

func identityDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteIdentity() == "" {
		return
	}
	label := ""
	for _, identity := range s.Identities() {
		if identity.ID == s.PendingDeleteIdentity() {
			label = identity.Label
			break
		}
	}
	message := m.VaultDeleteConfirmUnknown()
	if label != "" {
		message = m.VaultDeleteConfirm(label)
	}
	ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, message).Grow(1).TextColor(t.Danger)
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteIdentity()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteIdentity()
			c.Invalidate()
		}
	})
}

// secretBanner offers the credential-protection choice when the OS store is
// unavailable, and unlocks a passphrase-wrapped store. Session-only mode is
// stated so the user knows credentials will not survive a restart.
func secretBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	switch s.SecretMode() {
	case appvault.SecretModeUnavailable:
		ui.Column(c).Fill().Padding(12).Gap(8).Background(t.Surface).Children(func() {
			ui.Text(c, m.VaultSecretSetupTitle()).Bold()
			ui.Text(c, m.VaultSecretSetupBody()).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, m.VaultSecretPassphrase(), func() {
				ui.TextInput(c, s.SecretPassphrase()).Password().Width(260)
			})
			ui.Field(c, m.VaultSecretConfirm(), func() {
				ui.TextInput(c, s.SecretConfirmation()).Password().Width(260)
			})
			secretProblem(c, s, t)
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, m.VaultSecretUsePassphrase()).Clicked() {
					s.SetSecretPassphrase()
					c.Invalidate()
				}
				if ui.Button(c, m.VaultSecretUseSession()).Clicked() {
					s.UseSessionSecrets()
					c.Invalidate()
				}
			})
		})
	case appvault.SecretModePassphraseLocked:
		ui.Column(c).Fill().Padding(12).Gap(8).Background(t.Surface).Children(func() {
			ui.Text(c, m.VaultSecretLockedTitle()).Bold()
			ui.Text(c, m.VaultSecretLockedBody()).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				ui.TextInput(c, s.SecretPassphrase()).Password().Width(260)
				if ui.PrimaryButton(c, m.VaultSecretUnlock()).Clicked() {
					s.UnlockSecrets()
					c.Invalidate()
				}
			})
			secretProblem(c, s, t)
		})
	case appvault.SecretModeSession:
		ui.Text(c, m.VaultSecretSessionNote()).FontSize(12).TextColor(t.TextMuted)
	}
}

func secretProblem(c *ui.Context, s *vaultstate.State, t *ui.Theme) {
	message := problemText(locales.In(c.Locale()), s.SecretProblem())
	if message == "" {
		return
	}
	ui.Text(c, message).TextColor(t.Danger)
}

func hostRow(c *ui.Context, s *vaultstate.State, host domainvault.Host, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, host.Label).Bold()
			ui.Text(c, hostSummary(s, host, m)).FontSize(12).TextColor(t.TextMuted)
			if problem := s.TestProblem(host.ID); problem != "" {
				if text := uiconnection.ProblemText(m, problem); text != "" {
					ui.Text(c, text).FontSize(12).TextColor(t.Danger)
				}
			} else if s.TestSucceeded(host.ID) {
				ui.Text(c, m.VaultHostTestSuccess()).FontSize(12).TextColor(t.TextMuted)
			}
		})

		if host.Protocol == domainvault.ProtocolSSH {
			testLabel := m.VaultHostTest()
			if s.Testing(host.ID) {
				testLabel = m.VaultHostTesting()
			}
			if ui.Button(c, testLabel).Clicked() {
				s.Test(host.ID)
				c.Invalidate()
			}
			if ui.PrimaryButton(c, m.VaultHostConnect()).Clicked() {
				s.Connect(host.ID)
				c.Invalidate()
			}
		}
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEdit(host.ID)
			c.Invalidate()
		}
		pin := m.VaultHostPin()
		if host.Pinned {
			pin = m.VaultHostUnpin()
		}
		if ui.Button(c, pin).Clicked() {
			_ = s.TogglePinned(host.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDelete(host.ID)
			c.Invalidate()
		}
	})
}

func hostSummary(s *vaultstate.State, host domainvault.Host, m locales.Messages) string {
	parts := make([]string, 0, 6)
	if host.Protocol == domainvault.ProtocolLocal {
		parts = append(parts, m.VaultHostLocal())
	} else {
		parts = append(parts, net.JoinHostPort(host.Hostname, strconv.Itoa(host.Port)))
	}
	if host.Username != "" {
		parts = append(parts, host.Username)
	}
	if host.Group != "" {
		parts = append(parts, host.Group)
	}
	parts = append(parts, authSummary(s, host, m))
	if len(host.Tags) > 0 {
		parts = append(parts, strings.Join(host.Tags, ", "))
	}
	if host.Pinned {
		parts = append(parts, "★")
	}
	return strings.Join(parts, " · ")
}

// authSummary reports the method a connection will use after the Identity,
// explicit Key, and Host method are resolved. The precedence itself comes from
// the domain so the list never claims a credential that will not be tried.
func authSummary(s *vaultstate.State, host domainvault.Host, m locales.Messages) string {
	identities := s.Identities()
	var identity *domainvault.Identity
	if host.IdentityID != "" {
		for i := range identities {
			if identities[i].ID == host.IdentityID {
				identity = &identities[i]
				break
			}
		}
	}
	selection := domainvault.ResolveHostAuth(host, identity)
	// The original uses the Identity password first and the Host password as
	// the fallback, so either makes a password attempt possible.
	hasPassword := host.HasPassword || (identity != nil && identity.HasPassword)

	switch selection.Method {
	case domainvault.HostAuthPassword:
		return m.VaultAuthSummaryPassword()
	case domainvault.HostAuthKey, domainvault.HostAuthCertificate:
		return keyAuthSummary(s.Keys(), string(selection.KeyID), selection.Method, m)
	default:
		// Automatic mode: report the first material that will be tried.
		if host.UseSSHAgent {
			return m.VaultAuthSummaryAgent()
		}
		if selection.KeyID != "" {
			return keyAuthSummary(s.Keys(), string(selection.KeyID), domainvault.HostAuthKey, m)
		}
		if hasPassword {
			return m.VaultAuthSummaryPassword()
		}
		return m.VaultAuthSummaryKeyboard()
	}
}

func keyAuthSummary(keys []domainvault.Key, keyID string, method domainvault.HostAuthMethod, m locales.Messages) string {
	label := keyID
	certified := false
	for _, key := range keys {
		if string(key.ID) == keyID {
			label = key.Label
			certified = key.Certificate != ""
			break
		}
	}
	if method == domainvault.HostAuthCertificate || certified {
		return m.VaultAuthSummaryCertificate(label)
	}
	return m.VaultAuthSummaryKey(label)
}

func editor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	form := s.Form()
	title := m.VaultHostFormCreateTitle()
	if form.ID != "" {
		title = m.VaultHostFormEditTitle()
	}

	ui.Text(c, title).FontSize(20).Bold()

	ui.Form(c, func() {
		ui.Field(c, m.VaultHostLabel(), func() {
			ui.TextInput(c, &form.Label)
		})
		ui.Field(c, m.VaultHostHostname(), func() {
			ui.TextInput(c, &form.Hostname).Placeholder("example.com")
		})
		ui.Row(c).Gap(8).Children(func() {
			ui.Field(c, m.VaultHostPort(), func() {
				ui.TextInput(c, &form.Port).Placeholder("22").Width(120)
			})
			ui.Field(c, m.VaultHostUsername(), func() {
				ui.TextInput(c, &form.Username).Placeholder("root").Width(220)
			})
			ui.Field(c, m.VaultHostProtocol(), func() {
				ui.Select(c, &form.Protocol, []string{
					string(domainvault.ProtocolSSH),
					string(domainvault.ProtocolTelnet),
					string(domainvault.ProtocolLocal),
				}).Width(140)
			})
		})
		ui.Field(c, m.VaultHostGroup(), func() {
			ui.TextInput(c, &form.Group).Placeholder("prod/web")
		}).Description(m.VaultHostGroupHint())
		ui.Field(c, m.VaultHostTags(), func() {
			ui.TextInput(c, &form.Tags).Placeholder("prod, web")
		}).Description(m.VaultHostTagsHint())
		ui.Field(c, m.VaultHostNotes(), func() {
			ui.TextArea(c, &form.Notes).Height(80)
		})
		passwordHint := m.VaultHostPasswordHint()
		if form.HasPassword {
			passwordHint = m.VaultHostPasswordKeep()
		}
		ui.Field(c, m.VaultHostPassword(), func() {
			// The input is plaintext only while the editor is open. It is
			// never loaded back from storage and never rendered again.
			ui.TextInput(c, &form.Password).Password()
		}).Description(passwordHint)

		ui.Text(c, m.VaultHostAuthSection()).Bold()
		ui.Field(c, m.VaultHostIdentity(), func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &form.IdentityID, "", m.VaultNone())
				for _, identity := range s.Identities() {
					ui.Radio(c, &form.IdentityID, string(identity.ID), identity.Label)
				}
			})
		}).Description(m.VaultHostIdentityHint())
		ui.Field(c, m.VaultHostKey(), func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &form.KeyID, "", m.VaultNone())
				for _, key := range s.Keys() {
					ui.Radio(c, &form.KeyID, string(key.ID), key.Label)
				}
			})
		}).Description(m.VaultHostKeyHint())
		ui.Field(c, m.VaultHostAuthMethod(), func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthAuto), m.VaultHostAuthAuto())
				ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthPassword), m.VaultHostAuthPassword())
				ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthKey), m.VaultHostAuthKey())
				ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthCertificate), m.VaultHostAuthCertificate())
			})
		})
		ui.Checkbox(c, &form.UseSSHAgent, m.VaultHostUseAgent())
		ui.Checkbox(c, &form.RequiresMFA, m.VaultHostRequiresMFA())
		ui.Checkbox(c, &form.Pinned, m.VaultHostPinned())
	})

	ui.Text(c, m.VaultHostCredentialsNote()).FontSize(12).TextColor(t.TextMuted)
	problem(c, s, t)

	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultHostSave()).Clicked() {
			_ = s.Save()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelEdit()
			c.Invalidate()
		}
	})
}

func keyEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	form := s.KeyForm()
	title := m.VaultKeyFormCreateTitle()
	if form.ID != "" {
		title = m.VaultKeyFormEditTitle()
	}

	ui.Text(c, title).FontSize(20).Bold()

	ui.Form(c, func() {
		ui.Field(c, m.VaultHostLabel(), func() {
			ui.TextInput(c, &form.Label)
		})
		materialHint := m.VaultKeyMaterialHint()
		if form.HasPrivateKey {
			materialHint = m.VaultKeyMaterialKeep()
		}
		ui.Field(c, m.VaultKeyMaterial(), func() {
			ui.TextArea(c, &form.PrivateKey).Height(120)
		}).Description(materialHint)
		passphraseHint := m.VaultKeyPassphraseHint()
		if form.HasPassphrase {
			passphraseHint = m.VaultKeyPassphraseKeep()
		}
		ui.Field(c, m.VaultKeyPassphrase(), func() {
			ui.TextInput(c, &form.Passphrase).Password()
		}).Description(passphraseHint)
		ui.Checkbox(c, &form.SavePassphrase, m.VaultKeySavePassphrase())
		if form.HasPassphrase {
			ui.Checkbox(c, &form.ClearPassphrase, m.VaultKeyClearPassphrase())
		}
		ui.Field(c, m.VaultKeyCertificate(), func() {
			ui.TextArea(c, &form.Certificate).Height(60)
		}).Description(m.VaultKeyCertificateHint())
		if form.HasCertificate {
			ui.Checkbox(c, &form.ClearCertificate, m.VaultKeyClearCertificate())
		}
	})

	ui.Text(c, m.VaultKeyNote()).FontSize(12).TextColor(t.TextMuted)
	problem(c, s, t)

	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultHostSave()).Clicked() {
			_ = s.SaveKey()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelKeyEdit()
			c.Invalidate()
		}
	})
}

func identityEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	form := s.IdentityForm()
	title := m.VaultIdentityFormCreateTitle()
	if form.ID != "" {
		title = m.VaultIdentityFormEditTitle()
	}

	ui.Text(c, title).FontSize(20).Bold()

	ui.Form(c, func() {
		ui.Field(c, m.VaultHostLabel(), func() {
			ui.TextInput(c, &form.Label)
		})
		ui.Field(c, m.VaultHostUsername(), func() {
			ui.TextInput(c, &form.Username).Placeholder("root")
		})
		ui.Field(c, m.VaultIdentityMethod(), func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &form.AuthMethod, string(domainvault.IdentityAuthPassword), m.VaultIdentityMethodPassword())
				ui.Radio(c, &form.AuthMethod, string(domainvault.IdentityAuthKey), m.VaultIdentityMethodKey())
				ui.Radio(c, &form.AuthMethod, string(domainvault.IdentityAuthCertificate), m.VaultIdentityMethodCertificate())
			})
		})
		if form.AuthMethod != string(domainvault.IdentityAuthPassword) {
			ui.Field(c, m.VaultIdentityKey(), func() {
				ui.RadioGroup(c, func() {
					for _, key := range s.Keys() {
						ui.Radio(c, &form.KeyID, string(key.ID), key.Label)
					}
				})
			}).Description(m.VaultIdentityKeyHint())
		}
		if form.AuthMethod == string(domainvault.IdentityAuthPassword) {
			passwordHint := m.VaultHostPasswordHint()
			if form.HasPassword {
				passwordHint = m.VaultHostPasswordKeep()
			}
			ui.Field(c, m.VaultHostPassword(), func() {
				ui.TextInput(c, &form.Password).Password()
			}).Description(passwordHint)
			if form.HasPassword {
				ui.Checkbox(c, &form.ClearPassword, m.VaultIdentityClearPassword())
			}
		}
	})

	ui.Text(c, m.VaultIdentityNote()).FontSize(12).TextColor(t.TextMuted)
	problem(c, s, t)

	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultHostSave()).Clicked() {
			_ = s.SaveIdentity()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelIdentityEdit()
			c.Invalidate()
		}
	})
}

// problem renders the state's problem in the current language. Unknown ids
// render nothing, so the UI never shows an untranslated token.
func problem(c *ui.Context, s *vaultstate.State, t *ui.Theme) {
	message := problemText(locales.In(c.Locale()), s.Problem())
	if message == "" {
		return
	}
	ui.Text(c, message).TextColor(t.Danger)
}

func problemText(m locales.Messages, problem vaultstate.Problem) string {
	switch problem {
	case vaultstate.ProblemHostnameRequired:
		return m.VaultProblemHostnameRequired()
	case vaultstate.ProblemHostnameInvalid:
		return m.VaultProblemHostnameInvalid()
	case vaultstate.ProblemLabelRequired:
		return m.VaultProblemLabelRequired()
	case vaultstate.ProblemLabelInvalid:
		return m.VaultProblemLabelInvalid()
	case vaultstate.ProblemPortInvalid:
		return m.VaultProblemPortInvalid()
	case vaultstate.ProblemPortRange:
		return m.VaultProblemPortRange()
	case vaultstate.ProblemProtocol:
		return m.VaultProblemProtocol()
	case vaultstate.ProblemUsername:
		return m.VaultProblemUsername()
	case vaultstate.ProblemGroup:
		return m.VaultProblemGroup()
	case vaultstate.ProblemTags:
		return m.VaultProblemTags()
	case vaultstate.ProblemNotes:
		return m.VaultProblemNotes()
	case vaultstate.ProblemDuplicate:
		return m.VaultProblemDuplicate()
	case vaultstate.ProblemNotFound:
		return m.VaultProblemNotFound()
	case vaultstate.ProblemInternal:
		return m.VaultProblemInternal()
	case vaultstate.ProblemSecretField:
		return m.VaultProblemSecretField()
	case vaultstate.ProblemSecretsUnavailable:
		return m.VaultProblemSecretsUnavailable()
	case vaultstate.ProblemSecretsLocked:
		return m.VaultProblemSecretsLocked()
	case vaultstate.ProblemSecretWrongPassphrase:
		return m.VaultProblemSecretWrongPassphrase()
	case vaultstate.ProblemSecretPassphraseRequired:
		return m.VaultProblemSecretPassphraseRequired()
	case vaultstate.ProblemSecretPassphraseMismatch:
		return m.VaultProblemSecretPassphraseMismatch()
	case vaultstate.ProblemSecretUnreadable:
		return m.VaultProblemSecretUnreadable()
	case vaultstate.ProblemSecretSetupFailed:
		return m.VaultProblemSecretSetupFailed()
	case vaultstate.ProblemKeyRequired:
		return m.VaultProblemKeyRequired()
	case vaultstate.ProblemKeyMaterial:
		return m.VaultProblemKeyMaterial()
	case vaultstate.ProblemKeyPassphrase:
		return m.VaultProblemKeyPassphrase()
	case vaultstate.ProblemKeyCertificate:
		return m.VaultProblemKeyCertificate()
	case vaultstate.ProblemKeyUnsupported:
		return m.VaultProblemKeyUnsupported()
	case vaultstate.ProblemIdentityKeyRequired:
		return m.VaultProblemIdentityKeyRequired()
	case vaultstate.ProblemIdentityUsernameRequired:
		return m.VaultProblemIdentityUsernameRequired()
	case vaultstate.ProblemAuthMethod:
		return m.VaultProblemAuthMethod()
	case vaultstate.ProblemProxy:
		return m.VaultProblemProxy()
	case vaultstate.ProblemAlgorithms:
		return m.VaultProblemAlgorithms()
	}
	return ""
}
