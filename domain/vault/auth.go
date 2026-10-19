package vault

import "github.com/mslxl/natcatty/domain/model"

// AuthSelection is how a Host will authenticate after its Identity, explicit
// Key, and Host method are resolved. It is a pure summary: the application
// reads the referenced secrets separately.
type AuthSelection struct {
	Username    string
	Method      HostAuthMethod
	IdentityID  model.IdentityID
	KeyID       model.KeyID
	UseSSHAgent bool
	RequiresMFA bool
}

// ResolveHostAuth applies the original precedence: the selected Identity
// supplies the username and method, otherwise the Host does. Automatic mode
// stays automatic: a selected Key, certified or not, takes its place in the
// automatic order after the agent instead of replacing it. Password mode
// discards the selected Key.
//
// identity may be nil when the reference does not resolve; the selection keeps
// the dangling reference so the connection fails closed instead of silently
// substituting other credentials.
func ResolveHostAuth(host Host, identity *Identity) AuthSelection {
	selection := AuthSelection{
		Username:    host.Username,
		Method:      host.AuthMethod,
		IdentityID:  host.IdentityID,
		KeyID:       host.KeyID,
		UseSSHAgent: host.UseSSHAgent,
		RequiresMFA: host.RequiresMFA,
	}
	if selection.Method == "" {
		selection.Method = HostAuthAuto
	}

	if identity != nil {
		selection.IdentityID = identity.ID
		selection.Method = HostAuthMethod(identity.AuthMethod)
		if identity.Username != "" {
			selection.Username = identity.Username
		}
		if identity.KeyID != "" {
			selection.KeyID = identity.KeyID
		}
	}

	if selection.Method == HostAuthPassword {
		selection.KeyID = ""
	}
	return selection
}
