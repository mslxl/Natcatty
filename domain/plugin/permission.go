package plugin

import "fmt"

// Permission is a member of the closed, versioned plugin permission
// vocabulary. Unknown names are rejected at install; the set never grows by
// manifest declaration alone.
type Permission string

// Contribution permissions need no resource scope: they publish declarative
// descriptors the host renders.
const (
	PermissionCommands           Permission = "commands"
	PermissionMenus              Permission = "menus"
	PermissionKeybindings        Permission = "keybindings"
	PermissionViews              Permission = "views"
	PermissionSettingsContribute Permission = "settings.contribute"
	PermissionNotifications      Permission = "notifications"
	PermissionStorage            Permission = "storage"
	PermissionMCPTools           Permission = "mcp.tools"
	PermissionSystemPanel        Permission = "system.panel"
	PermissionEditorLanguage     Permission = "editor.language"
)

// UI and clipboard permissions are host-mediated but unscoped.
const (
	PermissionUINative       Permission = "ui.native"
	PermissionUIWebview      Permission = "ui.webview"
	PermissionClipboardRead  Permission = "clipboard.read"
	PermissionClipboardWrite Permission = "clipboard.write"
)

// Resource-scoped permissions always carry a structured Scope. There are no
// activation wildcards.
const (
	PermissionHostsRead          Permission = "hosts.read"
	PermissionHostsWrite         Permission = "hosts.write"
	PermissionSessionsRead       Permission = "sessions.read"
	PermissionSessionsControl    Permission = "sessions.control"
	PermissionTerminalObserve    Permission = "terminal.observe"
	PermissionTerminalWrite      Permission = "terminal.write"
	PermissionTerminalInfo       Permission = "terminal.info"
	PermissionVaultMetadata      Permission = "vault.metadata"
	PermissionVaultSecrets       Permission = "vault.secrets"
	PermissionFilesRead          Permission = "files.read"
	PermissionFilesWrite         Permission = "files.write"
	PermissionFilesRemoteRead    Permission = "files.remote.read"
	PermissionFilesRemoteWrite   Permission = "files.remote.write"
	PermissionNetworkDial        Permission = "network.dial"
	PermissionEventsSubscribe    Permission = "events.subscribe"
	PermissionSettingsRead       Permission = "settings.read"
	PermissionProviderConnection Permission = "provider.connection"
	PermissionProviderData       Permission = "provider.data"
	PermissionServicesInvoke     Permission = "services.invoke"
	PermissionServicesProvide    Permission = "services.provide"
	PermissionAuthInvoke         Permission = "auth.invoke"
	PermissionTrustVerify        Permission = "trust.verify"
)

// permissionDefinitions is the canonical vocabulary in stable display order.
var permissionDefinitions = []PermissionInfo{
	{Permission: PermissionCommands, Scope: ScopeNone},
	{Permission: PermissionMenus, Scope: ScopeNone},
	{Permission: PermissionKeybindings, Scope: ScopeNone},
	{Permission: PermissionViews, Scope: ScopeNone},
	{Permission: PermissionSettingsContribute, Scope: ScopeNone},
	{Permission: PermissionNotifications, Scope: ScopeNone},
	{Permission: PermissionStorage, Scope: ScopeNone},
	{Permission: PermissionMCPTools, Scope: ScopeNone},
	{Permission: PermissionSystemPanel, Scope: ScopeNone},
	{Permission: PermissionEditorLanguage, Scope: ScopeNone},
	{Permission: PermissionUINative, Scope: ScopeNone},
	{Permission: PermissionUIWebview, Scope: ScopeNone},
	{Permission: PermissionClipboardRead, Scope: ScopeNone},
	{Permission: PermissionClipboardWrite, Scope: ScopeNone},
	{Permission: PermissionHostsRead, Scope: ScopeHost},
	{Permission: PermissionHostsWrite, Scope: ScopeHost},
	{Permission: PermissionSessionsRead, Scope: ScopeSession},
	{Permission: PermissionSessionsControl, Scope: ScopeSession},
	{Permission: PermissionTerminalObserve, Scope: ScopeSession},
	{Permission: PermissionTerminalWrite, Scope: ScopeSession},
	{Permission: PermissionTerminalInfo, Scope: ScopeSession},
	{Permission: PermissionVaultMetadata, Scope: ScopeVault},
	{Permission: PermissionVaultSecrets, Scope: ScopeVault},
	{Permission: PermissionFilesRead, Scope: ScopeFiles},
	{Permission: PermissionFilesWrite, Scope: ScopeFiles},
	// Remote file grants are Host-scoped: the consent names the one Host whose
	// files the plugin may reach. The remote path never widens that grant.
	{Permission: PermissionFilesRemoteRead, Scope: ScopeHost},
	{Permission: PermissionFilesRemoteWrite, Scope: ScopeHost},
	{Permission: PermissionNetworkDial, Scope: ScopeNetwork},
	{Permission: PermissionEventsSubscribe, Scope: ScopeEvents},
	{Permission: PermissionSettingsRead, Scope: ScopeSettings},
	{Permission: PermissionProviderConnection, Scope: ScopeProvider},
	{Permission: PermissionProviderData, Scope: ScopeProvider},
	{Permission: PermissionServicesInvoke, Scope: ScopeServices},
	{Permission: PermissionServicesProvide, Scope: ScopeServices},
	{Permission: PermissionAuthInvoke, Scope: ScopeHost},
	{Permission: PermissionTrustVerify, Scope: ScopeHost},
}

// PermissionInfo describes one closed-vocabulary permission.
type PermissionInfo struct {
	Permission Permission
	// Scope is the kind of structured scope the permission requires. ScopeNone
	// means the grant covers the capability without further resource binding.
	Scope ScopeKind
}

// PermissionVocabulary returns the closed permission vocabulary in display
// order.
func PermissionVocabulary() []PermissionInfo {
	return append([]PermissionInfo(nil), permissionDefinitions...)
}

// PermissionByName looks up one vocabulary entry.
func PermissionByName(name string) (PermissionInfo, bool) {
	for _, info := range permissionDefinitions {
		if string(info.Permission) == name {
			return info, true
		}
	}
	return PermissionInfo{}, false
}

// IsKnownPermission reports whether name belongs to the closed vocabulary.
func IsKnownPermission(name string) bool {
	_, ok := PermissionByName(name)
	return ok
}

// ScopeKindFor returns the required scope kind for a permission, or false when
// the name is not in the vocabulary.
func ScopeKindFor(name string) (ScopeKind, bool) {
	info, ok := PermissionByName(name)
	if !ok {
		return "", false
	}
	return info.Scope, true
}

// IsResourceScoped reports whether the permission requires a structured scope.
func IsResourceScoped(name string) bool {
	kind, ok := ScopeKindFor(name)
	return ok && kind != ScopeNone
}

// ValidatePermissionRequests checks a manifest's required and optional lists:
// every name is known, no name appears twice, and each declared scope matches
// the permission's scope kind.
func ValidatePermissionRequests(required, optional []PermissionRequest) error {
	seen := make(map[Permission]bool, len(required)+len(optional))
	for i, request := range required {
		if err := validatePermissionRequest(request, fmt.Sprintf("permissions.required[%d]", i)); err != nil {
			return err
		}
		if seen[request.Permission] {
			return invalid("permissions", "permission %q is listed more than once", request.Permission)
		}
		seen[request.Permission] = true
	}
	for i, request := range optional {
		if err := validatePermissionRequest(request, fmt.Sprintf("permissions.optional[%d]", i)); err != nil {
			return err
		}
		if seen[request.Permission] {
			return invalid("permissions", "permission %q is listed more than once", request.Permission)
		}
		seen[request.Permission] = true
	}
	return nil
}

func validatePermissionRequest(request PermissionRequest, field string) error {
	info, ok := PermissionByName(string(request.Permission))
	if !ok {
		return invalid(field, "unknown permission %q", request.Permission)
	}
	if info.Scope == ScopeNone {
		if !request.Scope.IsZero() {
			return invalid(field+".scope", "permission %q takes no resource scope", request.Permission)
		}
		return nil
	}
	if request.Scope.IsZero() {
		return invalid(field+".scope", "permission %q requires a structured scope", request.Permission)
	}
	if request.Scope.Kind != info.Scope {
		return invalid(field+".scope.kind", "want %q for %q, got %q",
			info.Scope, request.Permission, request.Scope.Kind)
	}
	if request.Scope.Kind == ScopeSession && request.Scope.Session == "" &&
		request.Permission != PermissionSessionsRead {
		// Only the System panel snapshot service is Host-wide: it reads the
		// Host's collectors, not a Session. Every other session permission
		// remains bound to one Session.
		return invalid(field+".scope", "permission %q requires a Session ID",
			request.Permission)
	}
	if err := request.Scope.Validate(); err != nil {
		return invalid(field+".scope", "%v", err)
	}
	return nil
}
