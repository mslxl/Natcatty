package session

import (
	"github.com/mslxl/natcatty/application/session/autologin"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// SerialAutoLoginOptions turns a persisted serial profile into the expect
// engine's options. password is the value read from the encrypted field-secret
// store; passwordSet reports that the profile has a stored credential. The
// function returns nil when auto-login is off or the profile names nothing the
// detector could answer, so a restored workspace does not arm an idle engine.
func SerialAutoLoginOptions(profile *domainserial.AutoLogin, password string, passwordSet bool) *autologin.Options {
	if profile == nil || !profile.Enabled {
		return nil
	}
	normalized := profile.Normalized()
	options := &autologin.Options{
		Username:    normalized.Username,
		Password:    password,
		PasswordSet: passwordSet,
		MaxAttempts: normalized.MaxAttempts,
	}
	for _, reply := range normalized.Replies {
		options.Replies = append(options.Replies, autologin.Reply{
			Expect: reply.Expect, Respond: reply.Respond,
		})
	}
	if options.Username == "" && !options.PasswordSet && len(options.Replies) == 0 {
		return nil
	}
	return options
}
