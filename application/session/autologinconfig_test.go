package session

import (
	"testing"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

func TestSerialAutoLoginOptionsNeedsEnabledAndContent(t *testing.T) {
	password := domainserial.AutoLogin{Enabled: true, PasswordSet: true}.Normalized()
	if got := SerialAutoLoginOptions(&password, "", false); got != nil {
		t.Fatalf("blank enabled profile = %+v, want nil", got)
	}
	if got := SerialAutoLoginOptions(nil, "secret", true); got != nil {
		t.Fatalf("nil profile = %+v, want nil", got)
	}
	disabled := domainserial.AutoLogin{Username: "admin", PasswordSet: true}
	if got := SerialAutoLoginOptions(&disabled, "secret", true); got != nil {
		t.Fatalf("disabled profile = %+v, want nil", got)
	}
}

func TestSerialAutoLoginOptionsCarriesARepliesOnlyProfile(t *testing.T) {
	profile := domainserial.AutoLogin{
		Enabled: true,
		Replies: []domainserial.AutoLoginReply{{Expect: "Site code:", Respond: "lab-1"}},
	}
	options := SerialAutoLoginOptions(&profile, "", false)
	if options == nil {
		t.Fatal("a replies-only profile did not arm the engine")
	}
	if len(options.Replies) != 1 || options.Replies[0].Expect != "Site code:" ||
		options.Replies[0].Respond != "lab-1" {
		t.Fatalf("replies = %+v, want the saved rule", options.Replies)
	}
	if options.Username != "" || options.PasswordSet {
		t.Fatalf("replies-only options = %+v, want no credentials", options)
	}
}

func TestSerialAutoLoginOptionsMapsTheProfileAndStoredPassword(t *testing.T) {
	profile := domainserial.AutoLogin{
		Enabled:     true,
		Username:    " admin ",
		MaxAttempts: 2,
		Replies: []domainserial.AutoLoginReply{
			{Expect: " Hostname: ", Respond: " console "},
			{Expect: "", Respond: "ignored"},
		},
	}
	options := SerialAutoLoginOptions(&profile, "secret", true)
	if options == nil {
		t.Fatal("a configured profile did not arm the engine")
	}
	if options.Username != "admin" || options.Password != "secret" || !options.PasswordSet ||
		options.MaxAttempts != 2 {
		t.Fatalf("options = %+v, want the normalized profile and password", options)
	}
	if len(options.Replies) != 1 || options.Replies[0].Expect != "Hostname:" ||
		options.Replies[0].Respond != "console" {
		t.Fatalf("replies = %+v, want only the non-empty rule", options.Replies)
	}
}
