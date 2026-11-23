package phoneshell

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// errHostGroupsReadOnly is returned by the group operations the Host editor
// never calls. The editor reads Host Groups for inherited defaults; creating,
// renaming, and deleting them stays on the Vault's own group pages, exactly as
// on the desktop.
var errHostGroupsReadOnly = errors.New("phoneshell: Host Groups are read-only in the Host editor")

// hostGroupReader adapts the Hosts list's group read to the shared state's
// Groups seam. The write half is never exercised by the Host editor, but the
// seam requires it, so it reports that the editor cannot change groups.
type hostGroupReader struct {
	store HostStore
}

func (g hostGroupReader) ListGroups(ctx context.Context) ([]domainvault.Group, error) {
	return g.store.ListGroups(ctx)
}

func (g hostGroupReader) CreateGroup(context.Context, domainvault.GroupDraft) (domainvault.Group, error) {
	return domainvault.Group{}, errHostGroupsReadOnly
}

func (g hostGroupReader) UpdateGroup(context.Context, string, domainvault.GroupDraft) (domainvault.Group, error) {
	return domainvault.Group{}, errHostGroupsReadOnly
}

func (g hostGroupReader) DeleteGroup(context.Context, string) error {
	return errHostGroupsReadOnly
}

func (g hostGroupReader) AssignHostsGroup(ctx context.Context, ids []domainmodel.HostID, groupID string) error {
	return g.store.AssignHostsGroup(ctx, ids, groupID)
}

// hostEditor is the phone's single-page Host editor. It binds the same
// vaultstate.Form, validation, and write path the desktop editor uses; only
// the layout and the two-action choice are phone-specific.
type hostEditor struct {
	state *vaultstate.State
	// pages is the Hosts section the editor acts on: the save path refreshes
	// its list and reports the outcome through its navigation callback.
	pages *hostPages

	// fromLink and unmatchedLink select the actions: a manual editor leads
	// with Save; an ssh:// link that matched no Host leads with Save and
	// connect, because opening the link's address is what the user asked for.
	fromLink      bool
	unmatchedLink bool

	authOpen     bool
	jumpOpen     bool
	advancedOpen bool

	// problem is the editor's inline message. problemAtAuth also expands the
	// Authentication group so an invalid authenticator is visible where it was
	// typed.
	problem       string
	problemAtAuth bool
}

// newHostEditor builds the shared editor state over the same application
// service the composition root passes to the deep-link creator. It returns nil
// when the build has no Host write surface, so the pages can say the editor is
// unavailable instead of showing a form that cannot save.
func newHostEditor(pages *hostPages, writer HostCreator, vault VaultStore) *hostEditor {
	if pages == nil || pages.store == nil || writer == nil {
		return nil
	}
	hosts := pages.store
	hostService, ok := writer.(vaultstate.Hosts)
	if !ok {
		return nil
	}
	options := make([]vaultstate.Option, 0, 6)
	if keys, ok := keySource(vault, writer); ok {
		options = append(options, vaultstate.WithKeys(keys))
	}
	if identities, ok := identitySource(vault, writer); ok {
		options = append(options, vaultstate.WithIdentities(identities))
	}
	if groups, ok := writer.(vaultstate.Groups); ok {
		options = append(options, vaultstate.WithGroups(groups))
	} else {
		options = append(options, vaultstate.WithGroups(hostGroupReader{store: hosts}))
	}
	if profiles, ok := proxySource(writer); ok {
		options = append(options, vaultstate.WithProxyProfiles(profiles))
	}
	if networks, ok := networkSource(writer); ok {
		options = append(options, vaultstate.WithNetworkProfiles(networks))
	}
	if credentials, ok := writer.(vaultstate.HostCredentials); ok {
		options = append(options, vaultstate.WithHostCredentials(credentials))
	}
	return &hostEditor{pages: pages, state: vaultstate.NewState(hostService, options...)}
}

func keySource(vault VaultStore, writer HostCreator) (vaultstate.Keys, bool) {
	if keys, ok := vault.(vaultstate.Keys); ok {
		return keys, true
	}
	keys, ok := writer.(vaultstate.Keys)
	return keys, ok
}

func identitySource(vault VaultStore, writer HostCreator) (vaultstate.Identities, bool) {
	if identities, ok := vault.(vaultstate.Identities); ok {
		return identities, true
	}
	identities, ok := writer.(vaultstate.Identities)
	return identities, ok
}

func proxySource(writer HostCreator) (vaultstate.ProxyProfiles, bool) {
	profiles, ok := writer.(vaultstate.ProxyProfiles)
	return profiles, ok
}

func networkSource(writer HostCreator) (vaultstate.NetworkProfiles, bool) {
	networks, ok := writer.(vaultstate.NetworkProfiles)
	return networks, ok
}

// beginCreate resets the shared editor to a new Host and prefills the address a
// link named. Reading the lists first gives the Authentication and Advanced
// groups their Identities, Keys, and profiles without a second form.
func (e *hostEditor) beginCreate(prefill *domainvault.Host, fromLink bool) {
	_ = e.state.Reload()
	e.state.BeginCreate()
	e.fromLink = fromLink
	e.unmatchedLink = fromLink
	e.authOpen = false
	e.jumpOpen = false
	e.advancedOpen = false
	e.problem = ""
	e.problemAtAuth = false
	if prefill != nil {
		prefillForm(e.state.Form(), *prefill)
	}
}

// beginEdit opens an existing record. It reports false when the shared state
// no longer lists the Host, so the caller can keep the detail page instead of
// showing an editor that cannot save.
func (e *hostEditor) beginEdit(host domainvault.Host, fromLink bool) bool {
	_ = e.state.Reload()
	if !e.state.BeginEdit(host.ID) {
		return false
	}
	e.fromLink = fromLink
	e.unmatchedLink = false
	e.authOpen = false
	e.jumpOpen = false
	e.advancedOpen = false
	e.problem = ""
	e.problemAtAuth = false
	return true
}

// prefillForm fills the basic connection fields from a Host or a link draft.
func prefillForm(form *vaultstate.Form, host domainvault.Host) {
	form.Label = host.Label
	form.Hostname = host.Hostname
	form.Port = ""
	if host.Port > 0 {
		form.Port = strconv.Itoa(host.Port)
	}
	form.Username = host.Username
	if host.Protocol != "" {
		form.Protocol = string(host.Protocol)
	}
}

// drawEditor renders the editor over the shared form. The Scroll holds the
// fields and the action bar stays below it, so Save is reachable however long
// the expanded groups are.
func (h *hostPages) drawEditor(c *ui.Context, cancel func()) {
	width := pageContentWidth(c)
	editor := h.editor
	if editor == nil {
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
				kit.EmptyHint(c, "This build cannot edit Hosts.")
				if kit.GhostButton(c, icons.X, "Back to Hosts").Clicked() && cancel != nil {
					cancel()
				}
			})
		})
		return
	}
	form := editor.state.Form()
	title := "New Host"
	if form.ID != "" {
		title = "Edit Host"
	}
	ui.Column(c).Fill().Gap(10).Children(func() {
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(12).Children(func() {
				kit.SectionBar(c, title, nil)
				if editor.unmatchedLink {
					kit.EmptyHint(c, "No saved Host matches this link. Save and connect to create it.")
				}
				if editor.problem != "" && !editor.problemAtAuth {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, editor.problem, nil)
				}
				editor.drawBasics(c)
				if domainvault.Protocol(form.Protocol).UsesSSHAuth() {
					editor.drawGroupHeader(c, "Authentication", &editor.authOpen, "Password, Identity, MFA")
					if editor.authOpen {
						editor.drawAuth(c)
					}
				}
				editor.drawGroupHeader(c, "Jump chain", &editor.jumpOpen, jumpChainSummary(editor))
				if editor.jumpOpen {
					editor.drawJumpChain(c)
				}
				editor.drawGroupHeader(c, "Advanced options", &editor.advancedOpen, "Group, tags, terminal")
				if editor.advancedOpen {
					editor.drawAdvanced(c)
				}
			})
		})
		ui.Column(c).FillWidth().Padding(12, 16).Gap(8).Children(func() {
			editor.drawActions(c, cancel)
		})
	})
}

// drawBasics renders the fields that are always visible: where the Host is and
// how it is reached.
func (e *hostEditor) drawBasics(c *ui.Context) {
	form := e.state.Form()
	kit.FormRow(c, "Label", func() {
		ui.TextInput(c, &form.Label).Placeholder("A name you recognize").FillWidth()
	})
	kit.FormRow(c, "Hostname", func() {
		ui.TextInput(c, &form.Hostname).Placeholder("host.example.com").FillWidth()
	})
	kit.FormRow(c, "Port", func() {
		ui.TextInput(c, &form.Port).Placeholder(e.state.HostPortPlaceholder()).Width(120)
	})
	kit.FormRow(c, "Username", func() {
		ui.TextInput(c, &form.Username).Placeholder("user").FillWidth()
	})
	kit.FormRow(c, "Protocol", func() {
		protocols := []string{
			string(domainvault.ProtocolSSH),
			string(domainvault.ProtocolMosh),
			string(domainvault.ProtocolET),
			string(domainvault.ProtocolTelnet),
		}
		if ui.Select(c, &form.Protocol, protocols).Width(160).Changed() {
			// A hidden protocol-specific control must not keep a value Save
			// would persist; the shared state clears exactly the desktop's set.
			e.state.SetHostProtocol(form.Protocol)
			c.Invalidate()
		}
	})
}

// drawGroupHeader is one collapsible group's title. Its chevron says whether
// the group is open, and every part of the row toggles it.
func (e *hostEditor) drawGroupHeader(c *ui.Context, title string, open *bool, summary string) {
	row := kit.ListRow(c).Label(title)
	row.Children(func() {
		ui.Text(c, title).FontWeight(600)
		if summary != "" {
			ui.Text(c, summary).FontSize(12).TextColor(c.Theme().TextMuted).
				Grow(1).MinWidth(0).SingleLine()
		} else {
			ui.Box(c).Grow(1)
		}
		name := icons.ChevronRight
		if *open {
			name = icons.ChevronDown
		}
		kit.Icon(c, name).TextColor(c.Theme().TextMuted)
	})
	if row.Clicked() {
		*open = !*open
		c.Invalidate()
	}
}

// drawAuth renders the Authentication group. The stored password and
// authenticator are never loaded from the Vault; a blank field keeps them, so
// the hints say so instead of suggesting the form is empty.
func (e *hostEditor) drawAuth(c *ui.Context) {
	form := e.state.Form()
	if e.problemAtAuth && e.problem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, e.problem, nil)
	}
	passwordHint := "Leave blank to keep the stored password."
	if !form.HasPassword {
		passwordHint = "Stored encrypted in the Vault."
	}
	kit.FormRow(c, "Password", func() {
		ui.TextInput(c, &form.Password).Password().FillWidth()
	}).Description(passwordHint)

	totpHint := "An otpauth:// URI or a Base32 secret. Leave blank to keep the stored authenticator."
	kit.FormRow(c, "TOTP", func() {
		ui.TextInput(c, &form.TOTP).Password().
			Placeholder("otpauth:// URI or Base32 secret").FillWidth()
	}).Description(totpHint)
	if form.HasTOTP {
		if ui.Checkbox(c, &form.ClearTOTP, "Remove stored authenticator").Changed() {
			if form.ClearTOTP {
				form.TOTP = ""
			}
			c.Invalidate()
		}
	}

	kit.FormRow(c, "Identity", func() {
		ui.RadioGroup(c, func() {
			ui.Radio(c, &form.IdentityID, "", "None")
			for _, identity := range e.state.Identities() {
				ui.Radio(c, &form.IdentityID, string(identity.ID), identity.Label)
			}
		})
	}).Description("An Identity supplies its own username and, when it has one, its own password.")
	kit.FormRow(c, "SSH Key", func() {
		ui.RadioGroup(c, func() {
			ui.Radio(c, &form.KeyID, "", "None")
			for _, key := range e.state.Keys() {
				ui.Radio(c, &form.KeyID, string(key.ID), key.Label)
			}
		})
	}).Description("A key stored in the Vault's Keychain.")
	kit.FormRow(c, "Auth method", func() {
		ui.RadioGroup(c, func() {
			ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthAuto), "Automatic")
			ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthPassword), "Password")
			ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthKey), "Key")
			ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthCertificate), "Certificate")
		})
	})
	ui.Checkbox(c, &form.UseSSHAgent, "Use the system SSH agent")
	ui.Checkbox(c, &form.RequiresMFA, "Connection requires MFA")
}

// jumpChainSummary is the closed group's one-line description.
func jumpChainSummary(e *hostEditor) string {
	source, path, ids := e.state.HostJumpChainSource()
	if len(ids) == 0 {
		return "Direct"
	}
	labels := strings.Join(e.state.JumpHostLabels(ids), " → ")
	if source == vaultstate.InheritedValueSourceInherited {
		return labels + " (from " + path + ")"
	}
	return labels
}

// drawJumpChain shows the effective jump chain, as the desktop editor does.
// Editing the chain stays on the Host Group that owns it.
func (e *hostEditor) drawJumpChain(c *ui.Context) {
	source, path, ids := e.state.HostJumpChainSource()
	ui.Column(c).FillWidth().Padding(12).Gap(6).Background(c.Theme().Surface).Radius(12).
		Children(func() {
			value := "Direct connection"
			if len(ids) > 0 {
				value = strings.Join(e.state.JumpHostLabels(ids), " → ")
			}
			detailLine(c, "Chain", value)
			switch source {
			case vaultstate.InheritedValueSourceInherited:
				detailLine(c, "Source", "Inherited from "+path)
			case vaultstate.InheritedValueSourceRecord:
				detailLine(c, "Source", "This Host")
			default:
				detailLine(c, "Source", "Direct connection")
			}
		})
}

// drawAdvanced renders the remaining Host options. Protocol-specific controls
// appear only for the protocols that use them, and changing one clears what
// the hidden control owned.
func (e *hostEditor) drawAdvanced(c *ui.Context) {
	form := e.state.Form()
	kit.FormRow(c, "Group", func() {
		ui.TextInput(c, &form.Group).Placeholder("prod/web").FillWidth()
	}).Description("A slash-separated Host Group path.")
	kit.FormRow(c, "Tags", func() {
		ui.TextInput(c, &form.Tags).Placeholder("prod, web").FillWidth()
	}).Description("Comma-separated.")
	kit.FormRow(c, "Notes", func() {
		ui.TextArea(c, &form.Notes).Height(80)
	})
	kit.FormRow(c, "Startup command", func() {
		ui.TextArea(c, &form.StartupCommand).Height(100)
	}).Description("Typed once the Session is ready; replayed after every reconnect.")
	ui.Checkbox(c, &form.Pinned, "Pinned")

	protocol := domainvault.Protocol(form.Protocol)
	if protocol == domainvault.ProtocolSSH {
		ui.Text(c, "File transfer").FontWeight(600)
		kit.FormRow(c, "File protocol", func() {
			changed := ui.Select(c, &form.FileProtocol, []string{
				string(domainvault.FileProtocolAuto),
				string(domainvault.FileProtocolSFTP),
				string(domainvault.FileProtocolSCP),
			}).Width(160).Changed()
			if changed && form.FileProtocol == string(domainvault.FileProtocolSCP) {
				form.SFTPSudo = false
				c.Invalidate()
			}
		})
		ui.Checkbox(c, &form.SFTPSudo, "Elevate SFTP through sudo").
			Disabled(form.FileProtocol == string(domainvault.FileProtocolSCP))
		if ui.Checkbox(c, &form.X11Forwarding, "Enable X11 forwarding").Changed() {
			e.state.SetX11Forwarding(form.X11Forwarding)
			c.Invalidate()
		}
		if form.X11Forwarding {
			ui.Checkbox(c, &form.X11WarningAcknowledged,
				"I understand X11 forwarding exposes the remote client to this device")
		}
		if ui.Checkbox(c, &form.NetworkDevice, "Network device (vendor CLI)").Changed() {
			if form.NetworkDevice {
				form.AutoAttachTmux = false
			}
			c.Invalidate()
		}
		ui.Checkbox(c, &form.AutoAttachTmux, "Attach to a tmux session").
			Disabled(form.NetworkDevice)
		e.drawInputModes(c, form)
	}
	if protocol == domainvault.ProtocolMosh {
		kit.FormRow(c, "Mosh server path", func() {
			ui.TextInput(c, &form.MoshServerPath).Placeholder("mosh-server").FillWidth()
		})
	}
	if protocol == domainvault.ProtocolET {
		kit.FormRow(c, "ET port", func() {
			ui.TextInput(c, &form.ETPort).Placeholder("2022").Width(120)
		})
	}
}

// drawInputModes renders the SSH Enter/Backspace translation choices.
func (e *hostEditor) drawInputModes(c *ui.Context, form *vaultstate.Form) {
	ui.Text(c, "Input compatibility").FontWeight(600)
	newlineLabels := []string{"Default", "LF", "CRLF"}
	newlineValues := []string{"", "lf", "crlf"}
	newlineLabel := optionLabel(newlineLabels, newlineValues, form.NewlineMode)
	kit.FormRow(c, "Newline", func() {
		if ui.Select(c, &newlineLabel, newlineLabels).Width(160).Changed() {
			form.NewlineMode = optionValue(newlineValues, newlineLabels, newlineLabel)
			c.Invalidate()
		}
	})
	backspaceLabels := []string{"Default", "Ctrl-H"}
	backspaceValues := []string{"", "ctrl-h"}
	backspaceLabel := optionLabel(backspaceLabels, backspaceValues, form.BackspaceMode)
	kit.FormRow(c, "Backspace", func() {
		if ui.Select(c, &backspaceLabel, backspaceLabels).Width(160).Changed() {
			form.BackspaceMode = optionValue(backspaceValues, backspaceLabels, backspaceLabel)
			c.Invalidate()
		}
	})
	lineEditingLabels := []string{"Default", "Byte"}
	lineEditingValues := []string{"", "byte"}
	lineEditingLabel := optionLabel(lineEditingLabels, lineEditingValues, form.LineEditingMode)
	kit.FormRow(c, "Line editing", func() {
		if ui.Select(c, &lineEditingLabel, lineEditingLabels).Width(160).Changed() {
			form.LineEditingMode = optionValue(lineEditingValues, lineEditingLabels, lineEditingLabel)
			c.Invalidate()
		}
	})
}

// optionLabel returns the label for current, or the first label when current
// is not one of the options.
func optionLabel(labels, values []string, current string) string {
	for i, value := range values {
		if value == current {
			return labels[i]
		}
	}
	return labels[0]
}

// optionValue returns the value for label, or the first value when label is
// not one of the options.
func optionValue(values, labels []string, label string) string {
	for i, candidate := range labels {
		if candidate == label {
			return values[i]
		}
	}
	return values[0]
}

// drawActions is the one action bar. The primary action follows what opened
// the editor: manual editing saves, while an unmatched ssh:// link connects.
func (e *hostEditor) drawActions(c *ui.Context, cancel func()) {
	if e.problem != "" && e.problemAtAuth {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, e.problem, nil)
	}
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		if e.unmatchedLink {
			if kit.PrimaryButton(c, "", "Save and connect").Clicked() {
				e.save(true)
			}
			if kit.SecondaryButton(c, "", "Save").Clicked() {
				e.save(false)
			}
		} else {
			if kit.PrimaryButton(c, "", "Save").Clicked() {
				e.save(false)
			}
			if kit.SecondaryButton(c, "", "Save and connect").Clicked() {
				e.save(true)
			}
		}
		if kit.GhostButton(c, "", "Cancel").Clicked() && cancel != nil {
			cancel()
		}
	})
}

// save runs one write through the shared state. The outcome distinguishes a
// failed write, which keeps the draft, from a successful write whose list
// refresh failed, which closes the editor and never connects.
func (e *hostEditor) save(connect bool) {
	if e == nil || !e.state.Editing() {
		return
	}
	h := e.pages
	if h == nil {
		return
	}
	outcome := e.state.SaveWithOutcome()
	if !outcome.Saved {
		problem := e.state.Problem()
		e.problem = editorProblemText(problem)
		e.problemAtAuth = problem == vaultstate.ProblemTOTP
		if e.problemAtAuth {
			e.authOpen = true
		}
		return
	}
	e.problem = ""
	e.problemAtAuth = false
	if outcome.RefreshErr != nil {
		// The write is in the Vault; only this device's list is stale. Close
		// the editor, drop the typed credentials, re-read the list, and do not
		// connect: the user asked to save, not to dial a Host the page cannot
		// yet show.
		e.state.CancelEdit()
		_ = h.load(context.Background())
		if h.saveDone != nil {
			h.saveDone(domainvault.Host{}, false, true)
		}
		return
	}
	if err := h.load(context.Background()); err != nil {
		e.state.CancelEdit()
		_ = h.load(context.Background())
		if h.saveDone != nil {
			h.saveDone(domainvault.Host{}, false, true)
		}
		return
	}
	if h.saveDone != nil {
		h.saveDone(outcome.Host, connect, false)
	}
}

// editorProblemText turns a shared Problem into the next step a phone user can
// take. The identifiers come from the shared state, so validation text cannot
// drift from the desktop's rules even though the wording is the phone's.
func editorProblemText(problem vaultstate.Problem) string {
	switch problem {
	case vaultstate.ProblemHostnameRequired:
		return "Enter the Host's address."
	case vaultstate.ProblemLabelRequired:
		return "Enter a name for the Host."
	case vaultstate.ProblemLabelInvalid:
		return "That name is not valid. Check the label and try again."
	case vaultstate.ProblemHostnameInvalid:
		return "That address is not valid. Check the hostname and try again."
	case vaultstate.ProblemPortInvalid, vaultstate.ProblemPortRange:
		return "Enter a port between 1 and 65535."
	case vaultstate.ProblemProtocol:
		return "Choose a supported protocol."
	case vaultstate.ProblemUsername:
		return "Check the username."
	case vaultstate.ProblemGroup:
		return "Check the Host Group path."
	case vaultstate.ProblemTags:
		return "Check the tags."
	case vaultstate.ProblemDuplicate:
		return "A Host with this endpoint already exists."
	case vaultstate.ProblemTOTP:
		return "The authenticator is not a valid TOTP secret or otpauth:// URI."
	case vaultstate.ProblemSecretsLocked:
		return "The credential store is locked. Unlock it and try again."
	case vaultstate.ProblemSecretsUnavailable:
		return "The credential store is not available on this device."
	case vaultstate.ProblemSecretField:
		return "The Vault could not store a credential. Try again."
	case vaultstate.ProblemFileProtocol:
		return "Choose a supported file protocol."
	case vaultstate.ProblemETPort:
		return "Enter an ET port between 1 and 65535."
	case vaultstate.ProblemX11Acknowledged:
		return "Acknowledge the X11 forwarding warning first."
	case vaultstate.ProblemX11:
		return "X11 forwarding requires SSH."
	case vaultstate.ProblemNotFound:
		return "This Host is no longer in the Vault."
	case vaultstate.ProblemInternal:
		return "The Vault could not save the Host. Try again."
	default:
		return "The Vault could not save the Host. Check the fields and try again."
	}
}

// copyProblemText turns a failed credential read into the reason the detail
// page shows.
func copyProblemText(problem vaultstate.Problem) string {
	switch problem {
	case vaultstate.ProblemNoPassword:
		return "No stored password to copy."
	case vaultstate.ProblemSecretsLocked:
		return "The credential store is locked."
	case vaultstate.ProblemSecretsUnavailable:
		return "The credential store is not available on this device."
	case vaultstate.ProblemNotFound:
		return "This Host is no longer in the Vault."
	default:
		return "Could not read the Host's credentials."
	}
}

// copyCredentials reads the effective login through the shared state, so the
// desktop's boundaries apply unchanged: surrounding whitespace is trimmed from
// the password, and a selected Identity's own (possibly empty) password does
// not fall back to the Host's.
func (h *hostPages) copyCredentials(id domainmodel.HostID) (string, bool) {
	if h.editor == nil {
		h.detailProblem = "This build cannot read Host credentials."
		return "", false
	}
	// Copy acts on the stored record, not on an open draft.
	_ = h.editor.state.Reload()
	text, ok := h.editor.state.CopyCredentials(id)
	if !ok {
		h.detailProblem = copyProblemText(h.editor.state.Problem())
		return "", false
	}
	h.detailProblem = ""
	return text, true
}

// editHostEntry is the detail page's Edit action.
func (h *hostPages) editHostEntry(host domainvault.Host) {
	if h.editHost != nil {
		h.editHost(host)
	}
}
