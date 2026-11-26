package phoneshell

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// keyDraftForm is the plaintext editor of one SSH Key. PrivateKey and
// Passphrase exist only while the form is open: they are handed to the shared
// Vault service, which encrypts them, and are never loaded back from a record.
type keyDraftForm struct {
	id    model.KeyID
	label string

	privateKey     string
	passphrase     string
	savePassphrase bool
	// clearPassphrase removes the stored passphrase on Update without
	// replacing the key material.
	clearPassphrase bool

	editing       bool
	hasPassphrase bool
}

// identityDraftForm is the plaintext editor of one Identity. Password is form
// input only; the shared Vault service encrypts it into the field-secret store.
type identityDraftForm struct {
	id       model.IdentityID
	label    string
	username string
	// auth is an index into identityAuthMethods.
	auth     int
	keyID    model.KeyID
	password string
	// clearPassword removes the stored password on Update.
	clearPassword bool

	editing     bool
	hasPassword bool
}

// identityAuthMethods maps the editor's choice to the domain's method. The
// default is the first entry, password authentication.
var identityAuthMethods = []domainvault.IdentityAuthMethod{
	domainvault.IdentityAuthPassword,
	domainvault.IdentityAuthKey,
	domainvault.IdentityAuthCertificate,
}

func identityAuthIndex(method domainvault.IdentityAuthMethod) int {
	for i, candidate := range identityAuthMethods {
		if candidate == method {
			return i
		}
	}
	return 0
}

// identityAuthAt reads the editor's choice defensively: a corrupt index falls
// back to password authentication rather than panicking the page.
func identityAuthAt(index int) domainvault.IdentityAuthMethod {
	if index < 0 || index >= len(identityAuthMethods) {
		return domainvault.IdentityAuthPassword
	}
	return identityAuthMethods[index]
}

// drawKeychain renders the Keychain section: the SSH Key and Identity lists,
// their editors, and the delete confirmations. A write failure stays on the
// editor with the typed values, so the user can correct them; a list failure
// keeps the rows already on screen.
func (v *vaultPages) drawKeychain(c *ui.Context) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			switch {
			case v.keyDraft != nil:
				v.drawKeyDraft(c)
			case v.identityDraft != nil:
				v.drawIdentityDraft(c)
			default:
				v.drawKeychainList(c)
			}
		})
	})
}

func (v *vaultPages) drawKeychainList(c *ui.Context) {
	if v.store == nil {
		// One message, not one per list: nothing on this page can work
		// without the shared Vault.
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, vaultUnavailable, nil)
		return
	}
	v.refreshBar(c, "Keychain")
	v.drawKeySection(c)
	v.drawIdentitySection(c)
}

// drawKeySection renders the SSH Key half of the Keychain and its Add action.
func (v *vaultPages) drawKeySection(c *ui.Context) {
	kit.SectionBar(c, "SSH keys", func() {
		if kit.GhostButton(c, icons.Plus, "Add key").Clicked() {
			v.beginCreateKey()
		}
	})
	if v.keysStatus == vaultLoading {
		kit.EmptyHint(c, "Loading the Vault…")
		return
	}
	if v.keysStatus == vaultFailed {
		// Retry reads the same shared store again; rows already loaded stay
		// visible below the message.
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.keysProblem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				_ = v.load(context.Background())
			}
		})
		if len(v.keys) == 0 {
			return
		}
	} else if v.keysProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.keysProblem, nil)
	}
	v.drawKeyRows(c)
}

func (v *vaultPages) drawKeyRows(c *ui.Context) {
	if len(v.keys) == 0 {
		kit.EmptyHint(c, "No SSH keys yet. Keys added on the desktop appear here.")
		return
	}
	for _, key := range v.keys {
		key := key
		row := kit.ListRow(c).Label("Edit key " + keyName(key))
		handled := false
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, keyName(key)).FontWeight(600).SingleLine()
				ui.Text(c, keyDetail(key)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.Trash, "Delete key "+keyName(key)).Clicked() {
				v.pendingKey = key.ID
				v.pendingIdentity = ""
				v.pendingKnownHost = ""
				handled = true
			}
			ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
		})
		if !handled && row.Clicked() {
			v.beginEditKey(key)
		}
	}
	if v.pendingKey != "" {
		name := v.pendingKeyName()
		cancelled, confirmed := kit.ConfirmBar(c,
			"Delete the key "+name+"?",
			func() {
				kit.EmptyHint(c, "Connections that use this key will fail until another key is chosen.")
			},
			"Cancel", "Delete")
		switch {
		case cancelled:
			v.pendingKey = ""
		case confirmed:
			v.deleteKey(context.Background(), v.pendingKey)
		}
	}
}

// drawIdentitySection renders the Identity half of the Keychain and its Add
// action. It fails independently of the SSH Key list.
func (v *vaultPages) drawIdentitySection(c *ui.Context) {
	kit.SectionBar(c, "Identities", func() {
		if kit.GhostButton(c, icons.Plus, "Add identity").Clicked() {
			v.beginCreateIdentity()
		}
	})
	if v.identitiesStatus == vaultLoading {
		kit.EmptyHint(c, "Loading the Vault…")
		return
	}
	if v.identitiesStatus == vaultFailed {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.identityProblem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				_ = v.load(context.Background())
			}
		})
		if len(v.identities) == 0 {
			return
		}
	} else if v.identityProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.identityProblem, nil)
	}
	v.drawIdentityRows(c)
}

func (v *vaultPages) drawIdentityRows(c *ui.Context) {
	if len(v.identities) == 0 {
		kit.EmptyHint(c, "No Identities yet. Identities added on the desktop appear here.")
		return
	}
	for _, identity := range v.identities {
		identity := identity
		row := kit.ListRow(c).Label("Edit identity " + identityName(identity))
		handled := false
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, identityName(identity)).FontWeight(600).SingleLine()
				ui.Text(c, v.identityDetail(identity)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.Trash, "Delete identity "+identityName(identity)).Clicked() {
				v.pendingIdentity = identity.ID
				v.pendingKey = ""
				v.pendingKnownHost = ""
				handled = true
			}
			ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
		})
		if !handled && row.Clicked() {
			v.beginEditIdentity(identity)
		}
	}
	if v.pendingIdentity != "" {
		name := v.pendingIdentityName()
		cancelled, confirmed := kit.ConfirmBar(c,
			"Delete the identity "+name+"?",
			func() {
				kit.EmptyHint(c, "Hosts that use this Identity will fail until another is chosen.")
			},
			"Cancel", "Delete")
		switch {
		case cancelled:
			v.pendingIdentity = ""
		case confirmed:
			v.deleteIdentity(context.Background(), v.pendingIdentity)
		}
	}
}

// beginCreateKey opens an empty SSH Key editor.
func (v *vaultPages) beginCreateKey() {
	v.keyDraft = &keyDraftForm{savePassphrase: true}
	v.keyDraftProblem = ""
	v.identityDraft = nil
	v.pendingKey = ""
	v.pendingIdentity = ""
}

// beginEditKey opens one Key's non-secret fields. The material and passphrase
// are never loaded back from the record; leaving them blank keeps them.
func (v *vaultPages) beginEditKey(key domainvault.Key) {
	v.keyDraft = &keyDraftForm{
		id:             key.ID,
		label:          key.Label,
		editing:        true,
		hasPassphrase:  key.HasPassphrase,
		savePassphrase: true,
	}
	v.keyDraftProblem = ""
	v.identityDraft = nil
	v.pendingKey = ""
	v.pendingIdentity = ""
}

func (v *vaultPages) drawKeyDraft(c *ui.Context) {
	f := v.keyDraft
	title := "New SSH key"
	if f.editing {
		title = "Edit SSH key"
	}
	kit.SectionBar(c, title, nil)
	if v.keyDraftProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.keyDraftProblem, nil)
	}
	kit.FormRow(c, "Label", func() {
		ui.TextInput(c, &f.label).Placeholder("A name you recognize").FillWidth()
	})
	material := kit.FormRow(c, "Private key", func() {
		// The desktop uses a multi-line field for pasted key material, and a
		// phone has no masked multi-line input either. The value is form input
		// only and is never read back from the Vault.
		ui.TextArea(c, &f.privateKey).Height(140).Placeholder("Paste the private key")
	})
	if f.editing {
		material.Description("Leave blank to keep the stored key.")
	} else {
		material.Description("OpenSSH or PEM private key material.")
	}
	passphrase := kit.FormRow(c, "Passphrase", func() {
		ui.TextInput(c, &f.passphrase).Password().FillWidth().Placeholder("Optional")
	})
	if f.editing {
		passphrase.Description("Leave blank to keep the stored passphrase.")
	}
	kit.FormToggle(c, "Save passphrase", &f.savePassphrase, nil)
	if f.editing && f.hasPassphrase {
		kit.FormToggle(c, "Remove stored passphrase", &f.clearPassphrase, nil)
	}
	if kit.PrimaryButton(c, "", "Save key").Clicked() {
		v.submitKey()
	}
	if kit.SecondaryButton(c, "", "Cancel").Clicked() {
		v.cancelKeyDraft()
	}
}

// submitKey writes the draft through the shared Vault service. A failure keeps
// the editor and its typed values open with an actionable message; the list is
// reloaded only after the service accepted the write.
func (v *vaultPages) submitKey() {
	f := v.keyDraft
	if f == nil {
		return
	}
	if v.store == nil {
		v.keyDraftProblem = vaultUnavailable
		return
	}
	draft := domainvault.KeyDraft{
		Label:           f.label,
		PrivateKey:      f.privateKey,
		Passphrase:      f.passphrase,
		SavePassphrase:  f.savePassphrase,
		ClearPassphrase: f.clearPassphrase,
	}
	var err error
	if f.id == "" {
		_, err = v.store.CreateKey(context.Background(), draft)
	} else {
		_, err = v.store.UpdateKey(context.Background(), f.id, draft)
	}
	if err != nil {
		v.keyDraftProblem = "Could not save the key. Check the label and key material."
		return
	}
	v.cancelKeyDraft()
	_ = v.load(context.Background())
}

func (v *vaultPages) cancelKeyDraft() {
	v.keyDraft = nil
	v.keyDraftProblem = ""
}

// beginCreateIdentity opens an empty Identity editor with password auth, the
// domain's default.
func (v *vaultPages) beginCreateIdentity() {
	v.identityDraft = &identityDraftForm{}
	v.identityProblem = ""
	v.keyDraft = nil
	v.pendingKey = ""
	v.pendingIdentity = ""
}

// beginEditIdentity opens one Identity's non-secret fields. The password is
// never loaded back; leaving it blank keeps the stored credential.
func (v *vaultPages) beginEditIdentity(identity domainvault.Identity) {
	v.identityDraft = &identityDraftForm{
		id:          identity.ID,
		label:       identity.Label,
		username:    identity.Username,
		auth:        identityAuthIndex(identity.AuthMethod),
		keyID:       identity.KeyID,
		editing:     true,
		hasPassword: identity.HasPassword,
	}
	v.identityProblem = ""
	v.keyDraft = nil
	v.pendingKey = ""
	v.pendingIdentity = ""
}

func (v *vaultPages) drawIdentityDraft(c *ui.Context) {
	f := v.identityDraft
	title := "New Identity"
	if f.editing {
		title = "Edit Identity"
	}
	kit.SectionBar(c, title, nil)
	if v.identityProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.identityProblem, nil)
	}
	kit.FormRow(c, "Label", func() {
		ui.TextInput(c, &f.label).Placeholder("A name you recognize").FillWidth()
	})
	kit.FormRow(c, "Username", func() {
		ui.TextInput(c, &f.username).Placeholder("user").FillWidth()
	})
	kit.FormRow(c, "Authentication", func() {
		// The labels carry "auth" so the segmented control's text cannot be
		// confused with the Password field below it.
		ui.Segmented(c, &f.auth, "Password auth", "SSH key", "Certificate")
	})
	if identityAuthAt(f.auth) != domainvault.IdentityAuthPassword {
		v.drawIdentityKeyPicker(c, f)
	}
	password := kit.FormRow(c, "Password", func() {
		ui.TextInput(c, &f.password).Password().FillWidth().Placeholder("Optional")
	})
	if f.editing {
		password.Description("Leave blank to keep the stored password.")
	}
	if f.editing && f.hasPassword {
		kit.FormToggle(c, "Remove stored password", &f.clearPassword, nil)
	}
	if kit.PrimaryButton(c, "", "Save identity").Clicked() {
		v.submitIdentity()
	}
	if kit.SecondaryButton(c, "", "Cancel").Clicked() {
		v.cancelIdentityDraft()
	}
}

// drawIdentityKeyPicker chooses the Key a key or certificate Identity
// authenticates with. Only the public label is shown.
func (v *vaultPages) drawIdentityKeyPicker(c *ui.Context, f *identityDraftForm) {
	kit.SectionBar(c, "SSH key", nil)
	if len(v.keys) == 0 {
		kit.EmptyHint(c, "No SSH keys yet. Add a key before choosing this method.")
		return
	}
	for _, key := range v.keys {
		key := key
		row := kit.ListRow(c).Label("Use key " + keyName(key))
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, keyName(key)).FontWeight(600).SingleLine()
				ui.Text(c, keyDetail(key)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if key.ID == f.keyID {
				ui.Text(c, "Selected").FontSize(12).TextColor(c.Theme().Accent)
			}
		})
		if row.Clicked() {
			f.keyID = key.ID
			c.Invalidate()
		}
	}
}

// submitIdentity writes the draft through the shared Vault service.
func (v *vaultPages) submitIdentity() {
	f := v.identityDraft
	if f == nil {
		return
	}
	if v.store == nil {
		v.identityProblem = vaultUnavailable
		return
	}
	draft := domainvault.IdentityDraft{
		Label:         f.label,
		Username:      f.username,
		AuthMethod:    identityAuthAt(f.auth),
		KeyID:         f.keyID,
		Password:      f.password,
		ClearPassword: f.clearPassword,
	}
	var err error
	if f.id == "" {
		_, err = v.store.CreateIdentity(context.Background(), draft)
	} else {
		_, err = v.store.UpdateIdentity(context.Background(), f.id, draft)
	}
	if err != nil {
		v.identityProblem = "Could not save the Identity. Check the label, username, and key."
		return
	}
	v.cancelIdentityDraft()
	_ = v.load(context.Background())
}

func (v *vaultPages) cancelIdentityDraft() {
	v.identityDraft = nil
	v.identityProblem = ""
}

// deleteIdentity removes the Identity from the shared store, then reloads the
// list. A failed write keeps the row and reports the problem in place.
func (v *vaultPages) deleteIdentity(ctx context.Context, id model.IdentityID) {
	v.pendingIdentity = ""
	if v.store == nil {
		v.identityProblem = vaultUnavailable
		return
	}
	if err := v.store.DeleteIdentity(ctx, id); err != nil {
		v.identityProblem = "Could not delete the Identity."
		return
	}
	_ = v.load(ctx)
}

func (v *vaultPages) pendingIdentityName() string {
	for _, identity := range v.identities {
		if identity.ID == v.pendingIdentity {
			return identityName(identity)
		}
	}
	return ""
}

// identityName is the label a user recognizes, or the ID for an unlabelled
// Identity.
func identityName(identity domainvault.Identity) string {
	if label := strings.TrimSpace(identity.Label); label != "" {
		return label
	}
	return string(identity.ID)
}

// identityDetail shows the username, the authentication method, and the Key it
// selects, all non-secret. A stored password is reported as a flag, never
// rendered.
func (v *vaultPages) identityDetail(identity domainvault.Identity) string {
	parts := make([]string, 0, 3)
	if username := strings.TrimSpace(identity.Username); username != "" {
		parts = append(parts, username)
	}
	switch identity.AuthMethod {
	case domainvault.IdentityAuthKey:
		parts = append(parts, "key: "+v.keyLabelByID(identity.KeyID))
	case domainvault.IdentityAuthCertificate:
		parts = append(parts, "certificate: "+v.keyLabelByID(identity.KeyID))
	default:
		if identity.HasPassword {
			parts = append(parts, "password saved")
		} else {
			parts = append(parts, "password")
		}
	}
	return strings.Join(parts, " · ")
}

// keyLabelByID resolves a Key reference to the label the list shows, falling
// back to the ID for a dangling reference.
func (v *vaultPages) keyLabelByID(id model.KeyID) string {
	for _, key := range v.keys {
		if key.ID == id {
			return keyName(key)
		}
	}
	if id == "" {
		return "not selected"
	}
	return string(id)
}

// vaultUnavailable is the one message every editor shows when no shared Vault
// was wired into this build.
const vaultUnavailable = "The Vault is not available on this device."
